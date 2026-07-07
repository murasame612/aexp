package executor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	osexec "os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	gonanoid "github.com/matoous/go-nanoid/v2"

	"github.com/ziwu/aexp/internal/eventcache"
	"github.com/ziwu/aexp/internal/store"
)

// SubmitRequest contains the parameters for creating a new run.
type SubmitRequest struct {
	ResourceID          string            `json:"resource_id"`
	Name                string            `json:"name"`
	Kind                string            `json:"kind"`      // smoke, pilot, formal, ablation
	GPUIndex            int               `json:"gpu_index"` // -2 = none, -1 = all, 0+ = specific GPU
	Force               bool              `json:"force"`     // skip GPU slot lock
	ForceReason         string            `json:"force_reason"`
	PreemptRunID        string            `json:"preempt_run_id"`
	PreemptSave         bool              `json:"preempt_save"`
	Command             string            `json:"command"`
	Program             string            `json:"program"` // structured: python, bash, etc.
	Args                []string          `json:"args"`    // structured args
	Cwd                 string            `json:"cwd"`
	CondaEnv            string            `json:"conda_env"`
	ProjectEnv          string            `json:"project_env"` // "", raw, auto
	TargetEnv           string            `json:"target_env"`  // intended runtime/app env for setup or repair runs
	LogPaths            []string          `json:"log_paths"`
	ArtifactPaths       []string          `json:"artifact_paths"`
	MetricPaths         []string          `json:"metric_paths"`
	UIEventsPath        string            `json:"ui_events_path"`
	EnvVars             map[string]string `json:"env_vars"`
	CreatedBy           string            `json:"created_by"`
	RefreshProjectEnv   bool              `json:"refresh_project_env"`
	AllowEphemeralPaths bool              `json:"allow_ephemeral_paths"`
	GitSourceDir        string            `json:"git_source_dir"`
	AllowDirtyGit       bool              `json:"allow_dirty_git"`
	RecordGitDiff       bool              `json:"record_git_diff"`
}

// SubmitOptions controls optional submit behavior.
type SubmitOptions struct {
	OnCreated func(*store.Run)
}

// Executor manages experiment runs on remote resources.
type Executor struct {
	pool  *SSHPool
	store store.Store
}

// NewExecutor creates a new executor.
func NewExecutor(pool *SSHPool, store store.Store) *Executor {
	return &Executor{pool: pool, store: store}
}

// Pool returns the underlying SSH pool.
func (e *Executor) Pool() *SSHPool {
	return e.pool
}

// exec is a helper that runs a command on a resource, using its proxy settings if configured.
func (e *Executor) exec(ctx context.Context, r *store.Resource, cmd string) (string, string, error) {
	return e.pool.Exec(ctx, r.Host, r.Port, r.User, r.AuthRef, WithResourceRemotePath(r, cmd), r.SocksProxy, r.ProxyCommand)
}

// execStream is a helper that streams a command's stdout from a resource.
func (e *Executor) execStream(ctx context.Context, r *store.Resource, cmd string) (<-chan string, error) {
	return e.pool.ExecStream(ctx, r.Host, r.Port, r.User, r.AuthRef, WithResourceRemotePath(r, cmd), r.SocksProxy, r.ProxyCommand)
}

const macOSDefaultRemotePath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// WithResourceRemotePath prefixes a remote shell command with the PATH aexp should
// use for non-interactive control-plane commands such as tmux/status/cancel.
func WithResourceRemotePath(r *store.Resource, cmd string) string {
	remotePath := EffectiveRemotePath(r)
	if remotePath == "" {
		return cmd
	}
	return "export PATH=" + shellQuote(remotePath) + ":$PATH\n" + cmd
}

// EffectiveRemotePath returns the configured PATH prefix for a resource. macOS
// hosts get Homebrew paths by default because SSH non-interactive shells often
// omit /opt/homebrew/bin.
func EffectiveRemotePath(r *store.Resource) string {
	if r == nil {
		return ""
	}
	if path := strings.TrimSpace(r.RemotePath); path != "" {
		return path
	}
	switch strings.ToLower(strings.TrimSpace(r.OSType)) {
	case "macos", "darwin":
		return macOSDefaultRemotePath
	default:
		return ""
	}
}

// Submit creates and starts a new run on a resource.
func (e *Executor) Submit(ctx context.Context, req SubmitRequest) (*store.Run, error) {
	return e.SubmitWithOptions(ctx, req, SubmitOptions{})
}

// SubmitWithOptions creates a run record, invokes OnCreated, then starts it remotely.
func (e *Executor) SubmitWithOptions(ctx context.Context, req SubmitRequest, opts SubmitOptions) (*store.Run, error) {
	resource, err := e.store.GetResource(ctx, req.ResourceID)
	if err != nil {
		return nil, fmt.Errorf("get resource: %w", err)
	}
	if resource == nil {
		return nil, fmt.Errorf("resource %s not found", req.ResourceID)
	}
	if req.ProjectEnv != "" && req.ProjectEnv != ProjectEnvRaw && req.ProjectEnv != ProjectEnvAuto {
		return nil, fmt.Errorf("project_env must be raw or auto")
	}

	// Path sandbox: cwd must be under root_dir
	if req.Cwd != "" {
		if err := validateCwd(resource.RootDir, req.Cwd); err != nil {
			return nil, cwdSandboxError(err, resource.RootDir)
		}
	}
	if !req.AllowEphemeralPaths {
		if err := validatePersistentRunPaths(resource.RootDir, req.Cwd); err != nil {
			return nil, err
		}
	}

	// Command allowlist check (only for free-form commands)
	if req.Program == "" && req.Command != "" {
		if err := validateCommand(req.Command); err != nil {
			return nil, fmt.Errorf("command rejected: %w", err)
		}
	}

	// Normalize GPU index
	gpuIndex := req.GPUIndex
	if gpuIndex < store.GPUIndexNone {
		gpuIndex = store.GPUIndexAll
	}
	req.ForceReason = strings.TrimSpace(req.ForceReason)
	req.PreemptRunID = strings.TrimSpace(req.PreemptRunID)
	if req.Force && req.ForceReason == "" {
		return nil, fmt.Errorf("--force requires --force-reason so the run record explains why the GPU lock was bypassed")
	}
	if req.PreemptRunID != "" {
		if req.ForceReason == "" {
			return nil, fmt.Errorf("--preempt-run requires --force-reason so the run record explains why another run was cancelled")
		}
		if err := e.preemptRunBeforeSubmit(ctx, req.PreemptRunID, resource.ID); err != nil {
			return nil, err
		}
	}

	// GPU slot lock (skip if --force, or if this run explicitly needs no GPU).
	if !req.Force && gpuIndex != store.GPUIndexNone {
		activeRuns, _, err := e.RefreshActiveRuns(ctx, req.ResourceID, 3*time.Second)
		if err != nil {
			return nil, fmt.Errorf("check active runs: %w", err)
		}

		for _, active := range activeRuns {
			if active.GPUIndex == store.GPUIndexNone {
				continue
			}
			if gpuIndex == store.GPUIndexAll {
				return nil, fmt.Errorf("resource %s already has an active run (%s) using GPU %d; use --force to override", resource.Name, active.ID, active.GPUIndex)
			}
			if active.GPUIndex == store.GPUIndexAll {
				return nil, fmt.Errorf("resource %s has run %s using all GPUs; use --force to override", resource.Name, active.ID)
			}
			if active.GPUIndex == gpuIndex {
				return nil, fmt.Errorf("GPU %d on resource %s is already in use by run %s; use --force to override", gpuIndex, resource.Name, active.ID)
			}
		}
	}

	kind := req.Kind
	if kind == "" {
		kind = store.RunKindFormal
	}

	// Generate run ID
	runID := genID("run_")
	tmuxSession := "aexp_" + runID
	remoteRunDir := resource.RootDir + "/.aexp/runs/" + runID

	shouldRecordGitDiff := req.RecordGitDiff && (req.AllowDirtyGit || !runKindRequiresCleanGit(kind))
	git, err := captureGitProvenance(ctx, req.GitSourceDir, runID, shouldRecordGitDiff)
	if err != nil {
		return nil, err
	}
	if git.RepoRoot != "" && git.Dirty && runKindRequiresCleanGit(kind) {
		if !req.AllowDirtyGit {
			return nil, fmt.Errorf("formal experiment requires a clean Git worktree at %s; commit/stash changes or rerun with --allow-dirty-git --record-git-diff", git.RepoRoot)
		}
		if !req.RecordGitDiff {
			return nil, fmt.Errorf("dirty formal experiment requires --record-git-diff so the run records a patch reference")
		}
	}

	// Determine conda env
	condaEnv := req.CondaEnv
	if condaEnv == "" {
		condaEnv = resource.CondaEnv
	}
	var projectProfile *store.ProjectProfile
	if req.ProjectEnv == ProjectEnvAuto {
		projectProfile, err = e.ResolveProjectProfile(ctx, resource, req.Cwd, req.ProjectEnv, req.CondaEnv, req.RefreshProjectEnv)
		if err != nil {
			return nil, fmt.Errorf("detect project: %w", err)
		}
		if len(req.LogPaths) == 0 {
			req.LogPaths = projectProfile.Logs
		}
		if len(req.MetricPaths) == 0 {
			req.MetricPaths = projectProfile.Metrics
		}
		if projectProfile.ResolvedEnv == ProjectEnvConda {
			condaEnv = projectProfile.EnvName
		} else {
			condaEnv = ""
		}
	}
	req.UIEventsPath = normalizeUIEventsPath(req.UIEventsPath, runID, req.Cwd != "" || projectProfile != nil, remoteRunDir)

	// Build env vars (GPU env must be injected before command)
	envVars := copyMap(req.EnvVars)
	if gpuIndex >= 0 {
		envVars["CUDA_VISIBLE_DEVICES"] = fmt.Sprintf("%d", gpuIndex)
	}
	envVars["AEXP_RUN_DIR"] = remoteRunDir
	if req.UIEventsPath != "" {
		envVars["AEXP_UI_EVENTS"] = req.UIEventsPath
	}

	// Build command.sh content
	commandScript := buildCommandScript(req, condaEnv, resource.CondaBase, resource.CondaInit, resource.RootDir, envVars, projectProfile)

	// Serialize for DB
	logPathsJSON, _ := json.Marshal(req.LogPaths)
	artifactPathsJSON, _ := json.Marshal(req.ArtifactPaths)
	metricPathsJSON, _ := json.Marshal(req.MetricPaths)
	envJSON, _ := json.Marshal(envVars)
	argsJSON, _ := json.Marshal(req.Args)

	// Create run record
	run := &store.Run{
		ID:                runID,
		ResourceID:        req.ResourceID,
		Name:              req.Name,
		Status:            store.RunStatusStarting,
		Kind:              kind,
		GPUIndex:          gpuIndex,
		Cwd:               req.Cwd,
		Command:           commandForDB(req),
		Program:           req.Program,
		ArgsJSON:          string(argsJSON),
		CondaEnv:          condaEnv,
		ProjectEnv:        req.ProjectEnv,
		TargetEnv:         strings.TrimSpace(req.TargetEnv),
		ForceReason:       req.ForceReason,
		PreemptRunID:      req.PreemptRunID,
		PreemptSave:       req.PreemptSave,
		GitRepoRoot:       git.RepoRoot,
		GitRemoteURL:      git.RemoteURL,
		GitBranch:         git.Branch,
		GitCommit:         git.Commit,
		GitDirty:          git.Dirty,
		GitStatus:         git.Status,
		GitDiffHash:       git.DiffHash,
		GitDiffPath:       git.DiffPath,
		GitAllowDirty:     req.AllowDirtyGit,
		EnvJSON:           string(envJSON),
		LogPathsJSON:      string(logPathsJSON),
		ArtifactPathsJSON: string(artifactPathsJSON),
		MetricPathsJSON:   string(metricPathsJSON),
		UIEventsPath:      req.UIEventsPath,
		TmuxSession:       tmuxSession,
		RemoteRunDir:      remoteRunDir,
		CreatedBy:         req.CreatedBy,
	}
	if projectProfile != nil {
		run.ResolvedEnv = projectProfile.ResolvedEnv
		run.ResolvedPython = projectProfile.Python
		run.ResolvedCwd = projectProfile.ResolvedCwd
	}

	if err := e.store.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Write agent event immediately (before attempting SSH)
	e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run", req, map[string]string{"run_id": run.ID, "status": "starting"})
	if opts.OnCreated != nil {
		opts.OnCreated(run)
	}

	// Ensure wrapper script is deployed
	if err := e.ensureWrapper(ctx, resource); err != nil {
		e.updateRunStatus(ctx, run, store.RunStatusFailed)
		e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run_failed", nil, map[string]string{"error": err.Error()})
		return nil, fmt.Errorf("deploy wrapper: %w", err)
	}

	// Create remote run dir and write command.sh
	setupCmd := fmt.Sprintf("mkdir -p %s/logs", remoteRunDir)
	if _, stderr, err := e.exec(ctx, resource, setupCmd); err != nil {
		e.updateRunStatus(ctx, run, store.RunStatusFailed)
		e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run_failed", nil, map[string]string{"error": err.Error(), "stderr": stderr})
		return nil, fmt.Errorf("create run dir: %w", err)
	}

	// Write command.sh to remote (base64 to avoid all quoting issues)
	encoded := base64Encode([]byte(commandScript))
	writeCmd := fmt.Sprintf("echo %s | base64 -d > %s/command.sh && chmod +x %s/command.sh",
		encoded, remoteRunDir, remoteRunDir)
	if _, stderr, err := e.exec(ctx, resource, writeCmd); err != nil {
		e.updateRunStatus(ctx, run, store.RunStatusFailed)
		e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run_failed", nil, map[string]string{"error": err.Error(), "stderr": stderr})
		return nil, fmt.Errorf("write command.sh: %w", err)
	}

	// Launch tmux session — no quoting issues, just pass run_dir
	tmuxCmd := fmt.Sprintf("tmux new-session -d -s %s 'bash ~/.aexp/wrapper.sh %s'",
		tmuxSession, remoteRunDir)

	if _, stderr, err := e.exec(ctx, resource, tmuxCmd); err != nil {
		e.updateRunStatus(ctx, run, store.RunStatusFailed)
		e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run_failed", nil, map[string]string{"error": err.Error(), "stderr": stderr})
		return nil, fmt.Errorf("launch tmux: %w (stderr: %s)", err, stderr)
	}

	// Update run to running
	now := sql.NullTime{Time: time.Now(), Valid: true}
	run.Status = store.RunStatusRunning
	run.StartedAt = now
	if err := e.store.UpdateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("update run: %w", err)
	}

	// Update resource status
	resource.Status = store.ResourceStatusBusy
	e.store.UpdateResource(ctx, resource)

	// Update agent event with success
	e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run", req, map[string]string{"run_id": run.ID, "status": "running"})

	return run, nil
}

type gitProvenance struct {
	RepoRoot  string
	RemoteURL string
	Branch    string
	Commit    string
	Dirty     bool
	Status    string
	DiffHash  string
	DiffPath  string
}

func captureGitProvenance(ctx context.Context, sourceDir, runID string, recordDiff bool) (gitProvenance, error) {
	var snap gitProvenance
	if strings.TrimSpace(sourceDir) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return snap, nil
		}
		sourceDir = wd
	}
	absDir, err := filepath.Abs(sourceDir)
	if err != nil {
		absDir = sourceDir
	}
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	root, err := gitOutput(gitCtx, absDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return snap, nil
	}
	snap.RepoRoot = strings.TrimSpace(root)
	if snap.RepoRoot == "" {
		return snap, nil
	}
	if branch, err := gitOutput(gitCtx, snap.RepoRoot, "branch", "--show-current"); err == nil {
		snap.Branch = strings.TrimSpace(branch)
	}
	if commit, err := gitOutput(gitCtx, snap.RepoRoot, "rev-parse", "HEAD"); err == nil {
		snap.Commit = strings.TrimSpace(commit)
	}
	if remote, err := gitOutput(gitCtx, snap.RepoRoot, "config", "--get", "remote.origin.url"); err == nil {
		snap.RemoteURL = sanitizeGitRemoteURL(strings.TrimSpace(remote))
	}
	status, err := gitOutput(gitCtx, snap.RepoRoot, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return snap, fmt.Errorf("inspect git status: %w", err)
	}
	snap.Status = strings.TrimRight(status, "\n")
	snap.Dirty = strings.TrimSpace(snap.Status) != ""
	if !snap.Dirty {
		return snap, nil
	}
	if !recordDiff {
		sum := sha256.Sum256([]byte(snap.Status))
		snap.DiffHash = hex.EncodeToString(sum[:])
		return snap, nil
	}
	diff, err := gitOutputBytes(gitCtx, snap.RepoRoot, "diff", "--binary", "HEAD", "--")
	if err != nil {
		return snap, fmt.Errorf("record git diff: %w", err)
	}
	header := fmt.Sprintf("repo: %s\ncommit: %s\nbranch: %s\nremote: %s\n\nstatus:\n%s\n\ntracked_diff:\n", snap.RepoRoot, snap.Commit, snap.Branch, snap.RemoteURL, snap.Status)
	patch := append([]byte(header), diff...)
	sum := sha256.Sum256(patch)
	snap.DiffHash = hex.EncodeToString(sum[:])
	path, err := writeGitDiffPatch(runID, patch)
	if err != nil {
		return snap, err
	}
	snap.DiffPath = path
	return snap, nil
}

func runKindRequiresCleanGit(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "", store.RunKindFormal, store.RunKindAblation:
		return true
	default:
		return false
	}
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitOutputBytes(ctx, dir, args...)
	return string(out), err
}

func gitOutputBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := osexec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func sanitizeGitRemoteURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

func writeGitDiffPatch(runID string, patch []byte) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home for git diff patch: %w", err)
	}
	dir := filepath.Join(home, ".aexp", "git-diffs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create git diff directory: %w", err)
	}
	path := filepath.Join(dir, runID+".patch")
	if err := os.WriteFile(path, patch, 0600); err != nil {
		return "", fmt.Errorf("write git diff patch: %w", err)
	}
	return path, nil
}

func (e *Executor) ResolveProjectProfile(ctx context.Context, resource *store.Resource, cwd, strategy, condaEnv string, refresh bool) (*store.ProjectProfile, error) {
	profileCwd := cwd
	if profileCwd == "" {
		profileCwd = resource.RootDir
	}
	if strategy == "" {
		strategy = ProjectEnvAuto
	}
	if strategy != ProjectEnvAuto {
		return e.DetectProject(ctx, resource, profileCwd, strategy, condaEnv)
	}
	if !refresh && e.store != nil {
		cached, cacheErr := e.store.GetProjectProfile(ctx, resource.ID, profileCwd)
		if cacheErr != nil {
			return nil, fmt.Errorf("load project profile cache: %w", cacheErr)
		}
		if usableCachedProjectProfile(cached) {
			return cached, nil
		}
	}
	profile, err := e.DetectProject(ctx, resource, profileCwd, strategy, condaEnv)
	if err != nil {
		return nil, err
	}
	if e.store != nil {
		if saveErr := e.store.SaveProjectProfile(ctx, profile); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save project profile: %v\n", saveErr)
		}
	}
	return profile, nil
}

func usableCachedProjectProfile(profile *store.ProjectProfile) bool {
	return profile != nil &&
		profile.PythonOK &&
		strings.TrimSpace(profile.ResolvedEnv) != "" &&
		strings.TrimSpace(profile.ResolvedCwd) != ""
}

// RefreshActiveRuns refreshes control-plane-checkable runs for a resource, or all resources if resourceID is empty.
func (e *Executor) RefreshActiveRuns(ctx context.Context, resourceID string, timeout time.Duration) ([]store.Run, map[string]bool, error) {
	runs, err := e.listActiveRuns(ctx, resourceID)
	if err != nil {
		return nil, nil, err
	}
	return e.RefreshRuns(ctx, runs, timeout)
}

// RefreshRuns refreshes any control-plane-checkable runs in the provided slice.
func (e *Executor) RefreshRuns(ctx context.Context, runs []store.Run, timeout time.Duration) ([]store.Run, map[string]bool, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cached := map[string]bool{}
	for i := range runs {
		if !store.IsRunRefreshableStatus(runs[i].Status) {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		refreshed, err := e.CheckRunStatus(checkCtx, runs[i].ID)
		cancel()
		if err != nil || refreshed == nil {
			cached[runs[i].ID] = true
			continue
		}
		runs[i] = *refreshed
	}
	return runs, cached, nil
}

func (e *Executor) listActiveRuns(ctx context.Context, resourceID string) ([]store.Run, error) {
	var out []store.Run
	for _, status := range []string{store.RunStatusStarting, store.RunStatusRunning, store.RunStatusSSHUnreachable} {
		runs, err := e.store.ListRuns(ctx, store.RunFilter{
			ResourceID: resourceID,
			Status:     status,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, runs...)
	}
	return out, nil
}

// CheckRunStatus checks the actual status of a run on the remote resource.
func (e *Executor) CheckRunStatus(ctx context.Context, runID string) (*store.Run, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("run %s not found", runID)
	}

	// Only check runs that may still represent a live process.
	if !store.IsRunRefreshableStatus(run.Status) {
		return run, nil
	}

	resource, err := e.store.GetResource(ctx, run.ResourceID)
	if err != nil || resource == nil {
		return run, nil
	}

	// Check if exit_code file exists
	exitCodeFile := run.RemoteRunDir + "/exit_code"
	quotedExitCodeFile := shellQuote(exitCodeFile)
	out, _, err := e.exec(ctx, resource,
		fmt.Sprintf("if [ -f %s ]; then cat %s; fi", quotedExitCodeFile, quotedExitCodeFile))
	if err != nil {
		e.recordRunStatusProbeFailure(ctx, resource, run, "resource control channel is unreachable: "+err.Error())
		return run, err
	}

	if strings.TrimSpace(out) != "" {
		// Run has finished
		code := 0
		fmt.Sscanf(strings.TrimSpace(out), "%d", &code)
		e.finishRun(ctx, resource, run, code)
		return run, nil
	}

	// Check if tmux session still exists
	tmuxOut, _, tmuxErr := e.exec(ctx, resource,
		fmt.Sprintf("tmux has-session -t %s >/dev/null 2>&1; printf '%%s\\n' \"$?\"", shellQuote(run.TmuxSession)))
	if tmuxErr != nil {
		e.recordRunStatusProbeFailure(ctx, resource, run, "tmux status check failed: "+tmuxErr.Error())
		return run, tmuxErr
	}
	tmuxStatus, ok := parseRemoteStatusCode(tmuxOut)
	if !ok {
		return run, fmt.Errorf("unexpected tmux status output for run %s: %q", run.ID, tmuxOut)
	}

	if tmuxStatus != 0 {
		if code, ok := e.wrapperExitCodeFromLogs(ctx, resource, run); ok {
			e.finishRun(ctx, resource, run, code)
			return run, nil
		}
		status := store.RunStatusLost
		reason := "tmux session is gone and no exit_code was written"
		if ok, err := e.remoteDirExists(ctx, resource, run.RemoteRunDir); err == nil && !ok {
			status = store.RunStatusContainerExpired
			reason = "remote run directory disappeared; the container or temporary mount likely expired"
		}
		if status == store.RunStatusLost {
			if snapshot, err := e.writeLastRunSnapshot(ctx, resource, run, status, reason); err == nil && snapshot.EventsCached {
				status = store.RunStatusLostButEventsCached
				reason += "; local structured event cache is available"
			}
		}
		e.markRunStatus(ctx, resource, run, status, reason)
	} else if run.Status == store.RunStatusSSHUnreachable {
		run.Status = store.RunStatusRunning
		e.store.UpdateRun(ctx, run)
		e.saveAgentEvent(ctx, run.ID, run.CreatedBy, "run_status_changed", map[string]string{"run_id": run.ID}, map[string]string{"status": run.Status, "reason": "remote control channel recovered and tmux session is alive"})
	}

	return run, nil
}

func (e *Executor) recordRunStatusProbeFailure(ctx context.Context, resource *store.Resource, run *store.Run, reason string) {
	_, _ = e.writeLastRunSnapshot(ctx, resource, run, run.Status, reason)
}

func (e *Executor) markRunStatus(ctx context.Context, resource *store.Resource, run *store.Run, status string, reason string) {
	if run.Status != status {
		run.Status = status
	}
	now := sql.NullTime{Time: time.Now(), Valid: true}
	if store.IsRunTerminalStatus(status) {
		run.FinishedAt = now
	}
	_, _ = e.writeLastRunSnapshot(ctx, resource, run, status, reason)
	e.store.UpdateRun(ctx, run)
	e.saveAgentEvent(ctx, run.ID, run.CreatedBy, "run_status_changed", map[string]string{"run_id": run.ID}, map[string]string{"status": status, "reason": reason})
	if store.IsRunTerminalStatus(status) && resource != nil {
		e.checkResourceIdle(ctx, resource)
	}
}

func (e *Executor) remoteDirExists(ctx context.Context, resource *store.Resource, dir string) (bool, error) {
	if strings.TrimSpace(dir) == "" {
		return false, nil
	}
	out, _, err := e.exec(ctx, resource, fmt.Sprintf("test -d %s; printf '%%s\\n' \"$?\"", shellQuote(dir)))
	if err != nil {
		return false, err
	}
	code, ok := parseRemoteStatusCode(out)
	if !ok {
		return false, fmt.Errorf("unexpected directory status output: %q", out)
	}
	return code == 0, nil
}

type LastRunSnapshot struct {
	RunID           string                 `json:"run_id"`
	Status          string                 `json:"status"`
	Reason          string                 `json:"reason,omitempty"`
	DetectedAt      string                 `json:"detected_at"`
	ResourceID      string                 `json:"resource_id,omitempty"`
	RemoteRunDir    string                 `json:"remote_run_dir,omitempty"`
	UIEventsPath    string                 `json:"ui_events_path,omitempty"`
	EventCachePath  string                 `json:"event_cache_path,omitempty"`
	EventsCached    bool                   `json:"events_cached"`
	EventCount      int                    `json:"event_count"`
	LastTrial       string                 `json:"last_trial,omitempty"`
	LastEpoch       *float64               `json:"last_epoch,omitempty"`
	LastStep        *float64               `json:"last_step,omitempty"`
	CompletedTrials int                    `json:"completed_trials,omitempty"`
	BestValMetric   *SnapshotMetric        `json:"best_val_metric,omitempty"`
	LatestMetrics   []SnapshotMetric       `json:"latest_metrics,omitempty"`
	StdoutTail      string                 `json:"stdout_tail,omitempty"`
	StderrTail      string                 `json:"stderr_tail,omitempty"`
	SummaryWritten  bool                   `json:"summary_written"`
	SnapshotPath    string                 `json:"snapshot_path,omitempty"`
	SnapshotWritten bool                   `json:"snapshot_written"`
	Extra           map[string]interface{} `json:"extra,omitempty"`
}

type SnapshotMetric struct {
	Name   string   `json:"name"`
	Series string   `json:"series,omitempty"`
	Value  float64  `json:"value"`
	Epoch  *float64 `json:"epoch,omitempty"`
	Step   *float64 `json:"step,omitempty"`
}

func (e *Executor) writeLastRunSnapshot(ctx context.Context, resource *store.Resource, run *store.Run, status string, reason string) (LastRunSnapshot, error) {
	snapshot := LastRunSnapshot{
		RunID:        run.ID,
		Status:       status,
		Reason:       reason,
		DetectedAt:   time.Now().Format(time.RFC3339),
		ResourceID:   run.ResourceID,
		RemoteRunDir: run.RemoteRunDir,
		UIEventsPath: run.UIEventsPath,
	}
	lines := []LogLine{}
	if strings.TrimSpace(run.UIEventsPath) != "" && resource != nil {
		if remoteLines, err := e.GetLogFileSnapshot(ctx, run.ID, run.UIEventsPath, 1000); err == nil {
			lines = remoteLines
			if cachePath, cacheErr := eventcache.Write(run.ID, eventCacheLinesFromLogLines(remoteLines)); cacheErr == nil {
				snapshot.EventCachePath = cachePath
			}
		}
	}
	if len(lines) == 0 {
		if cacheLines, cachePath, err := eventcache.Read(run.ID, 1000); err == nil {
			snapshot.EventCachePath = cachePath
			lines = logLinesFromEventCache(run.ID, run.UIEventsPath, cacheLines)
		}
	}
	applyEventEvidenceToSnapshot(&snapshot, lines)
	if resource != nil {
		snapshot.StdoutTail, snapshot.StderrTail = e.runLogTails(ctx, resource, run)
	}
	if path, err := eventcache.LastSnapshotPath(run.ID); err == nil {
		snapshot.SnapshotPath = path
		if data, marshalErr := json.MarshalIndent(snapshot, "", "  "); marshalErr == nil {
			if mkdirErr := os.MkdirAll(pathDir(path), 0755); mkdirErr == nil {
				if writeErr := os.WriteFile(path, append(data, '\n'), 0644); writeErr == nil {
					snapshot.SnapshotWritten = true
					data, _ = json.MarshalIndent(snapshot, "", "  ")
					_ = os.WriteFile(path, append(data, '\n'), 0644)
				}
			}
		}
	}
	return snapshot, nil
}

func eventCacheLinesFromLogLines(lines []LogLine) []eventcache.Line {
	out := make([]eventcache.Line, 0, len(lines))
	for _, line := range lines {
		out = append(out, eventcache.Line{LineNo: line.LineNo, Content: line.Content})
	}
	return out
}

func logLinesFromEventCache(runID, source string, lines []eventcache.Line) []LogLine {
	out := make([]LogLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, LogLine{RunID: runID, Source: source, LineNo: line.LineNo, Content: line.Content})
	}
	return out
}

func applyEventEvidenceToSnapshot(snapshot *LastRunSnapshot, lines []LogLine) {
	if len(lines) == 0 {
		return
	}
	snapshot.EventsCached = true
	snapshot.EventCount = len(lines)
	completedTrials := map[string]bool{}
	latestByKey := map[string]SnapshotMetric{}
	for _, line := range lines {
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(line.Content), &ev); err != nil {
			continue
		}
		if trial := eventStringField(ev, "trial"); trial != "" {
			snapshot.LastTrial = trial
			completedTrials[trial] = true
		}
		if epoch, ok := eventFloatField(ev, "epoch"); ok {
			snapshot.LastEpoch = &epoch
		}
		if step, ok := eventFloatField(ev, "step"); ok {
			snapshot.LastStep = &step
		}
		typ := strings.ToLower(eventStringField(ev, "type"))
		name := firstEventString(ev, "name", "metric", "key", "label")
		value, valueOK := eventFloatField(ev, "value")
		if valueOK && (typ == "metric" || typ == "metrics" || typ == "eval" || typ == "scalar" || name != "") {
			metric := SnapshotMetric{
				Name:   name,
				Series: eventSeriesLabel(ev),
				Value:  value,
			}
			if epoch, ok := eventFloatField(ev, "epoch"); ok {
				metric.Epoch = &epoch
			}
			if step, ok := eventFloatField(ev, "step"); ok {
				metric.Step = &step
			}
			key := metric.Name + "\x00" + metric.Series
			latestByKey[key] = metric
			if looksLikeValidationMetric(metric.Name) && (snapshot.BestValMetric == nil || metric.Value < snapshot.BestValMetric.Value) {
				m := metric
				snapshot.BestValMetric = &m
			}
		}
		if strings.Contains(strings.ToLower(typ), "summary") || eventStringField(ev, "summary") != "" {
			snapshot.SummaryWritten = true
		}
	}
	snapshot.CompletedTrials = len(completedTrials)
	for _, metric := range latestByKey {
		snapshot.LatestMetrics = append(snapshot.LatestMetrics, metric)
	}
	if len(snapshot.LatestMetrics) > 20 {
		snapshot.LatestMetrics = snapshot.LatestMetrics[len(snapshot.LatestMetrics)-20:]
	}
}

func (e *Executor) runLogTails(ctx context.Context, resource *store.Resource, run *store.Run) (string, string) {
	if strings.TrimSpace(run.RemoteRunDir) == "" {
		return "", ""
	}
	stdoutPath := path.Join(run.RemoteRunDir, "logs", "stdout.log")
	stderrPath := path.Join(run.RemoteRunDir, "logs", "stderr.log")
	stdout, _, _ := e.exec(ctx, resource, fmt.Sprintf("tail -n 80 %s 2>/dev/null || true", shellQuote(stdoutPath)))
	stderr, _, _ := e.exec(ctx, resource, fmt.Sprintf("tail -n 80 %s 2>/dev/null || true", shellQuote(stderrPath)))
	return strings.TrimRight(stdout, "\n"), strings.TrimRight(stderr, "\n")
}

func pathDir(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx <= 0 {
		return "."
	}
	return p[:idx]
}

func eventStringField(ev map[string]interface{}, key string) string {
	switch v := ev[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func firstEventString(ev map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := eventStringField(ev, key); value != "" {
			return value
		}
	}
	return ""
}

func eventFloatField(ev map[string]interface{}, key string) (float64, bool) {
	switch v := ev[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func eventSeriesLabel(ev map[string]interface{}) string {
	parts := make([]string, 0, 6)
	for _, key := range []string{"series", "variant", "split", "stage", "trial", "seed"} {
		value := eventStringField(ev, key)
		if value == "" {
			continue
		}
		if key == "series" {
			parts = append(parts, value)
		} else {
			parts = append(parts, key+":"+value)
		}
	}
	return strings.Join(parts, " / ")
}

func looksLikeValidationMetric(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(name, "val") || strings.Contains(name, "valid") || strings.Contains(name, "validation")
}

func parseRemoteStatusCode(out string) (int, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var code int
		if _, err := fmt.Sscanf(line, "%d", &code); err == nil {
			return code, true
		}
		return 0, false
	}
	return 0, false
}

func (e *Executor) finishRun(ctx context.Context, resource *store.Resource, run *store.Run, code int) {
	run.ExitCode = sql.NullInt64{Int64: int64(code), Valid: true}
	now := sql.NullTime{Time: time.Now(), Valid: true}
	run.FinishedAt = now
	if code == 0 {
		run.Status = store.RunStatusSucceeded
		run.FailureKind = ""
		run.FailureReason = ""
	} else {
		run.Status = store.RunStatusFailed
		stdoutTail, stderrTail := "", ""
		if resource != nil {
			stdoutTail, stderrTail = e.runLogTails(ctx, resource, run)
		}
		run.FailureKind, run.FailureReason = classifyRunFailure(code, stdoutTail, stderrTail)
	}
	e.store.UpdateRun(ctx, run)
	e.checkResourceIdle(ctx, resource)
}

func classifyRunFailure(exitCode int, stdoutTail, stderrTail string) (string, string) {
	text := strings.TrimSpace(stderrTail + "\n" + stdoutTail)
	lower := strings.ToLower(text)
	switch {
	case exitCode == 137 || strings.Contains(lower, "killed") && strings.Contains(lower, "137"):
		return store.RunFailureKilled137, "process was killed with exit code 137, usually memory pressure, container eviction, or a hard kill"
	case strings.Contains(lower, "no space left on device") || strings.Contains(lower, "disk quota exceeded"):
		return store.RunFailureDiskFull, "disk is full or quota was exceeded"
	case strings.Contains(lower, "cuda out of memory") || strings.Contains(lower, "cublas_status_alloc_failed") || strings.Contains(lower, "device-side assert"):
		return store.RunFailureGPUBusy, "GPU memory/runtime failure; inspect GPU occupancy and batch size"
	case strings.Contains(lower, "modulenotfounderror") || strings.Contains(lower, "no module named"):
		return store.RunFailureDependencyError, firstFailureLine(text, "missing Python dependency")
	case strings.Contains(lower, "/opt/conda/lib/python") && strings.Contains(lower, "site-packages") && strings.Contains(lower, "conda"):
		return store.RunFailureEnvMismatch, "Python packages appear to be loaded from an unexpected conda environment"
	case strings.Contains(lower, "importerror") || strings.Contains(lower, "cannot import name") || strings.Contains(lower, ".so.") || strings.Contains(lower, "libxcb.so"):
		return store.RunFailureImportError, firstFailureLine(text, "import/runtime library error")
	case strings.Contains(lower, "filenotfounderror") || strings.Contains(lower, "no such file or directory") || strings.Contains(lower, "not found:") || strings.Contains(lower, "cannot stat"):
		return store.RunFailureDataMissing, firstFailureLine(text, "required data or file path is missing")
	case strings.Contains(lower, "connection reset") || strings.Contains(lower, "broken pipe") || strings.Contains(lower, "network is unreachable") || strings.Contains(lower, "connection timed out"):
		return store.RunFailureNetworkReset, firstFailureLine(text, "network/control channel failed during run")
	case strings.TrimSpace(text) != "":
		return store.RunFailureUnknown, firstFailureLine(text, "run exited non-zero; inspect stderr/stdout")
	default:
		return store.RunFailureUnknown, fmt.Sprintf("run exited with code %d but no log tail was available", exitCode)
	}
}

func firstFailureLine(text, fallback string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 240 {
			return line[:237] + "..."
		}
		return line
	}
	return fallback
}

func (e *Executor) wrapperExitCodeFromLogs(ctx context.Context, resource *store.Resource, run *store.Run) (int, bool) {
	if strings.TrimSpace(run.RemoteRunDir) == "" {
		return 0, false
	}
	terminalLog := path.Join(run.RemoteRunDir, "logs", "terminal.log")
	stdoutLog := path.Join(run.RemoteRunDir, "logs", "stdout.log")
	cmd := fmt.Sprintf("tail -n 50 %s %s 2>/dev/null", shellQuote(terminalLog), shellQuote(stdoutLog))
	out, _, err := e.exec(ctx, resource, cmd)
	if err != nil {
		return 0, false
	}
	return parseWrapperExitCode(out)
}

func parseWrapperExitCode(text string) (int, bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		idx := strings.LastIndex(line, "with exit code ")
		if idx < 0 {
			continue
		}
		codeText := strings.TrimSpace(line[idx+len("with exit code "):])
		var code int
		if _, err := fmt.Sscanf(codeText, "%d", &code); err == nil {
			return code, true
		}
	}
	return 0, false
}

// TailLogs returns a channel that streams log lines from a run.
func (e *Executor) TailLogs(ctx context.Context, runID string, source string, lastN int) (<-chan LogLine, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("run %s not found", runID)
	}

	resource, err := e.store.GetResource(ctx, run.ResourceID)
	if err != nil || resource == nil {
		return nil, fmt.Errorf("resource not found")
	}

	logFile := fmt.Sprintf("%s/logs/%s.log", run.RemoteRunDir, source)
	if source == "" {
		logFile = run.RemoteRunDir + "/logs/stdout.log"
	}

	if lastN < 0 {
		lastN = 0
	} else if lastN == 0 {
		lastN = 200
	}

	cmd := fmt.Sprintf("tail -F -n %d %s", lastN, shellQuote(logFile))
	ch, err := e.execStream(ctx, resource, cmd)
	if err != nil {
		return nil, err
	}

	logCh := make(chan LogLine, 64)
	go func() {
		defer close(logCh)
		lineNo := 0
		for line := range ch {
			if line == "" {
				continue
			}
			lineNo++
			select {
			case logCh <- LogLine{RunID: runID, Source: source, LineNo: lineNo, Content: line}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return logCh, nil
}

// TailLogFile streams a project log file from a run. Relative paths resolve from run.Cwd.
func (e *Executor) TailLogFile(ctx context.Context, runID string, logPath string, lastN int) (<-chan LogLine, error) {
	run, resource, logFile, label, err := e.resolveRunLogFile(ctx, runID, logPath)
	if err != nil {
		return nil, err
	}
	if lastN < 0 {
		lastN = 0
	} else if lastN == 0 {
		lastN = 200
	}

	cmd := fmt.Sprintf("tail -F -n %d %s", lastN, shellQuote(logFile))
	ch, err := e.execStream(ctx, resource, cmd)
	if err != nil {
		return nil, err
	}

	logCh := make(chan LogLine, 64)
	go func() {
		defer close(logCh)
		lineNo := 0
		for line := range ch {
			if line == "" {
				continue
			}
			lineNo++
			select {
			case logCh <- LogLine{RunID: run.ID, Source: label, LineNo: lineNo, Content: line}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return logCh, nil
}

// GetLogSnapshot returns the last N lines of a log file as a one-shot read.
func (e *Executor) GetLogSnapshot(ctx context.Context, runID string, source string, lastN int) ([]LogLine, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("run %s not found", runID)
	}

	resource, err := e.store.GetResource(ctx, run.ResourceID)
	if err != nil || resource == nil {
		return nil, fmt.Errorf("resource not found")
	}

	if source == "" {
		source = "stdout"
	}
	if lastN <= 0 {
		lastN = 200
	}

	logFile := fmt.Sprintf("%s/logs/%s.log", run.RemoteRunDir, source)
	cmd := fmt.Sprintf(`LOG_FILE=%s
if [ ! -f "$LOG_FILE" ]; then exit 0; fi
total=$(tr '\r' '\n' < "$LOG_FILE" 2>/dev/null | wc -l | tr -d ' ')
printf '\001AEXP_TOTAL_LINES\t%%s\n' "$total"
tr '\r' '\n' < "$LOG_FILE" 2>/dev/null | tail -n %d`, shellQuote(logFile), lastN)

	out, _, err := e.exec(ctx, resource, cmd)
	if err != nil {
		return nil, err
	}

	totalLines := 0
	contentLines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(contentLines) > 0 && strings.HasPrefix(contentLines[0], "\x01AEXP_TOTAL_LINES\t") {
		fmt.Sscanf(strings.TrimPrefix(contentLines[0], "\x01AEXP_TOTAL_LINES\t"), "%d", &totalLines)
		contentLines = contentLines[1:]
	}
	startLine := 1
	if totalLines > len(contentLines) {
		startLine = totalLines - len(contentLines) + 1
	}

	var lines []LogLine
	for i, content := range contentLines {
		if content == "" {
			continue
		}
		lines = append(lines, LogLine{
			RunID:   runID,
			Source:  source,
			LineNo:  startLine + i,
			Content: content,
		})
	}
	return lines, nil
}

// GetLogFileSnapshot returns the last N lines from a project log file.
func (e *Executor) GetLogFileSnapshot(ctx context.Context, runID string, logPath string, lastN int) ([]LogLine, error) {
	run, resource, logFile, label, err := e.resolveRunLogFile(ctx, runID, logPath)
	if err != nil {
		return nil, err
	}
	if lastN <= 0 {
		lastN = 200
	}

	cmd := fmt.Sprintf(`LOG_FILE=%s
if [ ! -f "$LOG_FILE" ]; then printf '\001AEXP_LOG_MISSING\t%%s\n' "$LOG_FILE"; exit 0; fi
total=$(tr '\r' '\n' < "$LOG_FILE" 2>/dev/null | wc -l | tr -d ' ')
printf '\001AEXP_TOTAL_LINES\t%%s\n' "$total"
tr '\r' '\n' < "$LOG_FILE" 2>/dev/null | tail -n %d`, shellQuote(logFile), lastN)

	out, _, err := e.exec(ctx, resource, cmd)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(out, "\x01AEXP_LOG_MISSING\t") {
		missing := strings.TrimSpace(strings.TrimPrefix(out, "\x01AEXP_LOG_MISSING\t"))
		return nil, fmt.Errorf("log file not found: %s", missing)
	}
	return parseLogSnapshot(run.ID, label, out)
}

func (e *Executor) resolveRunLogFile(ctx context.Context, runID string, logPath string) (*store.Run, *store.Resource, string, string, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return nil, nil, "", "", err
	}
	if run == nil {
		return nil, nil, "", "", fmt.Errorf("run %s not found", runID)
	}
	resource, err := e.store.GetResource(ctx, run.ResourceID)
	if err != nil || resource == nil {
		return nil, nil, "", "", fmt.Errorf("resource not found")
	}
	cleanPath := strings.TrimSpace(logPath)
	if cleanPath == "" || strings.ContainsAny(cleanPath, "\x00\r\n") {
		return nil, nil, "", "", fmt.Errorf("invalid log path")
	}
	var remotePath string
	if strings.HasPrefix(cleanPath, "/") {
		remotePath = path.Clean(cleanPath)
	} else {
		base := strings.TrimSpace(run.Cwd)
		if strings.TrimSpace(run.ResolvedCwd) != "" {
			base = strings.TrimSpace(run.ResolvedCwd)
		}
		if base == "" {
			base = run.RemoteRunDir
		}
		remotePath = path.Join(base, cleanPath)
	}
	root := path.Clean(resource.RootDir)
	if root != "." && remotePath != root && !strings.HasPrefix(remotePath, strings.TrimRight(root, "/")+"/") {
		return nil, nil, "", "", fmt.Errorf("log path %s is outside resource root %s", remotePath, root)
	}
	return run, resource, remotePath, cleanPath, nil
}

func parseLogSnapshot(runID string, source string, out string) ([]LogLine, error) {
	totalLines := 0
	contentLines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(contentLines) > 0 && strings.HasPrefix(contentLines[0], "\x01AEXP_TOTAL_LINES\t") {
		fmt.Sscanf(strings.TrimPrefix(contentLines[0], "\x01AEXP_TOTAL_LINES\t"), "%d", &totalLines)
		contentLines = contentLines[1:]
	}
	startLine := 1
	if totalLines > len(contentLines) {
		startLine = totalLines - len(contentLines) + 1
	}

	var lines []LogLine
	for i, content := range contentLines {
		if content == "" {
			continue
		}
		lines = append(lines, LogLine{
			RunID:   runID,
			Source:  source,
			LineNo:  startLine + i,
			Content: content,
		})
	}
	return lines, nil
}

func (e *Executor) preemptRunBeforeSubmit(ctx context.Context, runID, resourceID string) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("load preempt target %s: %w", runID, err)
	}
	if run == nil {
		return fmt.Errorf("preempt target run %s not found", runID)
	}
	if run.ResourceID != resourceID {
		return fmt.Errorf("preempt target run %s is on resource %s, not requested resource %s", runID, run.ResourceID, resourceID)
	}
	if !store.IsRunRefreshableStatus(run.Status) {
		return fmt.Errorf("preempt target run %s is not active (status: %s)", runID, run.Status)
	}
	if err := e.Cancel(ctx, runID); err != nil {
		return fmt.Errorf("preempt target run %s: %w", runID, err)
	}
	return nil
}

// Cancel stops a running run.
func (e *Executor) Cancel(ctx context.Context, runID string) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("run %s not found", runID)
	}
	if store.IsRunRefreshableStatus(run.Status) {
		if refreshed, refreshErr := e.CheckRunStatus(ctx, runID); refreshErr == nil && refreshed != nil {
			run = refreshed
		}
	}
	if run.Status != store.RunStatusRunning && run.Status != store.RunStatusStarting {
		if isFinishedRunStatus(run.Status) {
			return fmt.Errorf("run %s already finished (status: %s)", runID, run.Status)
		}
		return fmt.Errorf("run %s is not running (status: %s)", runID, run.Status)
	}

	resource, err := e.store.GetResource(ctx, run.ResourceID)
	if err != nil || resource == nil {
		return fmt.Errorf("resource not found")
	}

	// Send Ctrl+C to tmux session
	_, _, err = e.exec(ctx, resource,
		fmt.Sprintf("tmux send-keys -t %s C-c", run.TmuxSession))
	if err != nil {
		// Try to kill the session directly
		e.exec(ctx, resource,
			fmt.Sprintf("tmux kill-session -t %s 2>/dev/null", run.TmuxSession))
	}

	// Wait a moment, then check
	time.Sleep(2 * time.Second)

	// Check if session is gone
	out, _, _ := e.exec(ctx, resource,
		fmt.Sprintf("tmux has-session -t %s 2>&1; echo $?", run.TmuxSession))

	if strings.TrimSpace(out) == "0" {
		// Still running, force kill
		e.exec(ctx, resource,
			fmt.Sprintf("tmux kill-session -t %s 2>/dev/null", run.TmuxSession))
	}

	// Update run
	run.Status = store.RunStatusCancelled
	now := sql.NullTime{Time: time.Now(), Valid: true}
	run.FinishedAt = now
	e.store.UpdateRun(ctx, run)

	// Write status to remote
	e.exec(ctx, resource,
		fmt.Sprintf("echo 'cancelled' > %s/status", run.RemoteRunDir))

	e.checkResourceIdle(ctx, resource)

	// Log agent event
	e.store.SaveAgentEvent(ctx, &store.AgentEvent{
		RunID:      runID,
		Actor:      run.CreatedBy,
		ToolName:   "stop_run",
		InputJSON:  fmt.Sprintf(`{"run_id":"%s"}`, runID),
		OutputJSON: `{"status":"cancelled"}`,
	})

	return nil
}

func isFinishedRunStatus(status string) bool {
	return store.IsRunTerminalStatus(status)
}

// ExecRequest is a one-shot remote command (not a Run).
type ExecRequest struct {
	ResourceID        string `json:"resource_id"`
	Command           string `json:"command"`
	Cwd               string `json:"cwd"`
	ProjectEnv        string `json:"project_env"` // "", raw, auto
	CondaEnv          string `json:"conda_env"`   // optional override for project_env=auto
	TimeoutSec        int    `json:"timeout_sec"` // 0 = default 30s
	Actor             string `json:"actor"`
	RefreshProjectEnv bool   `json:"refresh_project_env"`
}

// ExecResult is the response from a one-shot exec.
type ExecResult struct {
	Stdout     string    `json:"stdout"`
	Stderr     string    `json:"stderr"`
	ExitCode   int       `json:"exit_code"`
	Duration   string    `json:"duration"` // human-readable, e.g. "1.2s"
	EventID    string    `json:"event_id,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

const (
	defaultExecTimeout = 30 * time.Second
	maxExecTimeout     = 5 * time.Minute
	maxExecOutputBytes = 1 << 20 // 1 MiB
)

// Exec runs a one-shot command on a resource. It does NOT create a Run.
// Used for operational/inspection commands by agents.
func (e *Executor) Exec(ctx context.Context, req ExecRequest) (*ExecResult, error) {
	resource, err := e.store.GetResource(ctx, req.ResourceID)
	if err != nil {
		return nil, fmt.Errorf("get resource: %w", err)
	}
	if resource == nil {
		return nil, fmt.Errorf("resource %s not found", req.ResourceID)
	}

	// Command safety check
	if err := validateCommand(req.Command); err != nil {
		return nil, fmt.Errorf("command rejected: %w", err)
	}

	// Cwd sandbox
	if req.Cwd != "" {
		if err := validateCwd(resource.RootDir, req.Cwd); err != nil {
			return nil, cwdSandboxError(err, resource.RootDir)
		}
	}

	// Timeout
	timeout := defaultExecTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
		if timeout > maxExecTimeout {
			timeout = maxExecTimeout
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build command with optional cwd and project environment.
	cmd := req.Command
	if req.ProjectEnv == ProjectEnvAuto {
		profile, err := e.ResolveProjectProfile(ctx, resource, req.Cwd, req.ProjectEnv, req.CondaEnv, req.RefreshProjectEnv)
		if err != nil {
			return nil, fmt.Errorf("detect project: %w", err)
		}
		cmd = buildProjectWrappedCommand(resource, profile, req.Command)
	} else if req.Cwd != "" {
		resolved := req.Cwd
		if !strings.HasPrefix(req.Cwd, "/") {
			resolved = resource.RootDir + "/" + req.Cwd
		}
		cmd = fmt.Sprintf("cd %s && %s", shellQuote(resolved), req.Command)
	}

	// Execute
	start := time.Now()
	stdout, stderr, err := e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef, WithResourceRemotePath(resource, cmd), resource.SocksProxy, resource.ProxyCommand)
	duration := time.Since(start)

	// Truncate output if too large
	if len(stdout) > maxExecOutputBytes {
		stdout = stdout[:maxExecOutputBytes] + "\n... [truncated]"
	}
	if len(stderr) > maxExecOutputBytes {
		stderr = stderr[:maxExecOutputBytes] + "\n... [truncated]"
	}

	// Determine exit code
	exitCode := 0
	if err != nil {
		exitCode = 1
		// Try to extract exit code from error
		if exitErr, ok := err.(interface{ ExitStatus() int }); ok {
			exitCode = exitErr.ExitStatus()
		}
	}

	// Audit: record exec event
	startedAt := start
	finishedAt := time.Now()
	eventID := genID("exec_")

	stdoutTail := truncateToLastBytes(stdout, 2048)
	stderrTail := truncateToLastBytes(stderr, 2048)

	event := &store.ExecEvent{
		ID:         eventID,
		ResourceID: req.ResourceID,
		Actor:      req.Actor,
		Command:    req.Command,
		Cwd:        req.Cwd,
		ExitCode:   sql.NullInt64{Int64: int64(exitCode), Valid: true},
		StartedAt:  startedAt,
		FinishedAt: sql.NullTime{Time: finishedAt, Valid: true},
		DurationMs: duration.Milliseconds(),
		StdoutTail: stdoutTail,
		StderrTail: stderrTail,
	}
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer persistCancel()
	if saveErr := e.store.SaveExecEvent(persistCtx, event); saveErr != nil {
		fmt.Fprintf(os.Stderr, "warning: remote command completed, but local exec audit persistence failed: %v\n", saveErr)
	}

	// Also keep legacy agent_events audit (cross-reference via event_id)
	inputJSON := fmt.Sprintf(`{"resource":"%s","command":%s}`, resource.Name, mustJSON(req.Command))
	outputJSON := fmt.Sprintf(`{"exit_code":%d,"duration_ms":%d,"event_id":"%s"}`, exitCode, duration.Milliseconds(), eventID)
	if saveErr := e.store.SaveAgentEvent(persistCtx, &store.AgentEvent{
		RunID:      "",
		Actor:      req.Actor,
		ToolName:   "exec",
		InputJSON:  inputJSON,
		OutputJSON: outputJSON,
	}); saveErr != nil {
		fmt.Fprintf(os.Stderr, "warning: remote command completed, but legacy local agent audit persistence failed: %v\n", saveErr)
	}

	return &ExecResult{
		Stdout:     stdout,
		Stderr:     stderr,
		ExitCode:   exitCode,
		Duration:   duration.Round(time.Millisecond).String(),
		EventID:    eventID,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}, nil
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// truncateToLastBytes returns the last n bytes of s, trimming any incomplete
// leading UTF-8 rune so the result is always valid UTF-8.
func truncateToLastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for i := 0; i < len(s); i++ {
		if utf8.RuneStart(s[i]) {
			return s[i:]
		}
	}
	return s
}

// DetectLongRunningCmd checks if a command looks like it would run for
// a long time (training, etc.) and returns a reason string if so.
func DetectLongRunningCmd(command string) (bool, string) {
	type pattern struct {
		substr string
		reason string
	}
	patterns := []pattern{
		{"torchrun ", "torchrun (multi-GPU training)"},
		{"torchrun\n", "torchrun (multi-GPU training)"},
		{"accelerate launch", "accelerate launch (HuggingFace training)"},
		{"deepspeed ", "deepspeed (distributed training)"},
		{"deepspeed\n", "deepspeed (distributed training)"},
		{"mpirun ", "mpirun (MPI job)"},
		{"mpirun\n", "mpirun (MPI job)"},
		{"nohup ", "nohup (background process)"},
		{"python train", "python training script"},
		{"python3 train", "python3 training script"},
		{"python finetune", "python finetuning script"},
		{"python3 finetune", "python3 finetuning script"},
		{"python pretrain", "python pretraining script"},
		{"python3 pretrain", "python3 pretraining script"},
		{"bash train.sh", "training shell script"},
		{"bash run.sh", "run shell script"},
		{"./train", "./train binary"},
		{"./run.sh", "./run.sh script"},
	}

	lower := strings.ToLower(strings.TrimSpace(command))
	lower = strings.NewReplacer("'", "", "\"", "").Replace(lower)
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p.substr)) {
			return true, p.reason
		}
	}
	return false, ""
}

func (e *Executor) ensureWrapper(ctx context.Context, r *store.Resource) error {
	// Check if wrapper exists AND has the correct version
	checkCmd := "cat ~/.aexp/wrapper.version 2>/dev/null || echo none"
	out, _, _ := e.exec(ctx, r, checkCmd)
	if strings.TrimSpace(out) == WrapperHash {
		return nil // up to date
	}

	// Deploy wrapper
	mkdirCmd := "mkdir -p ~/.aexp"
	if _, _, err := e.exec(ctx, r, mkdirCmd); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Write wrapper script via base64 (avoids heredoc quoting issues)
	encoded := base64Encode([]byte(WrapperScript))
	writeCmd := fmt.Sprintf("echo %s | base64 -d > ~/.aexp/wrapper.sh && chmod +x ~/.aexp/wrapper.sh", encoded)
	if _, stderr, err := e.exec(ctx, r, writeCmd); err != nil {
		return fmt.Errorf("write wrapper: %w (stderr: %s)", err, stderr)
	}

	// Write version tag
	versionCmd := fmt.Sprintf("printf %%s %s > ~/.aexp/wrapper.version", shellQuote(WrapperHash))
	e.exec(ctx, r, versionCmd)

	return nil
}

func (e *Executor) updateRunStatus(ctx context.Context, run *store.Run, status string) {
	run.Status = status
	now := sql.NullTime{Time: time.Now(), Valid: true}
	run.FinishedAt = now
	if err := e.store.UpdateRun(ctx, run); err != nil && ctx.Err() != nil {
		persistCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = e.store.UpdateRun(persistCtx, run)
	}
}

func (e *Executor) checkResourceIdle(ctx context.Context, r *store.Resource) {
	runs, _, _ := e.RefreshActiveRuns(ctx, r.ID, 2*time.Second)
	active := 0
	for _, run := range runs {
		if store.IsRunRefreshableStatus(run.Status) {
			active++
		}
	}
	if active == 0 {
		r.Status = store.ResourceStatusIdle
		e.store.UpdateResource(ctx, r)
	}
}

// validateCwd ensures cwd is within the resource's root_dir.
func validateCwd(rootDir, cwd string) error {
	if cwd == "" {
		return nil
	}

	// Resolve to absolute path
	resolved := cwd
	if !strings.HasPrefix(cwd, "/") {
		resolved = rootDir + "/" + cwd
	}

	// Clean both paths to handle ../ etc.
	cleanRoot := cleanPath(rootDir)
	cleanResolved := cleanPath(resolved)

	if !strings.HasPrefix(cleanResolved, cleanRoot+"/") && cleanResolved != cleanRoot {
		return fmt.Errorf("cwd %q escapes root_dir %q", cwd, rootDir)
	}
	return nil
}

func validatePersistentRunPaths(rootDir, cwd string) error {
	if reason := ephemeralRemotePathReason(rootDir); reason != "" {
		return fmt.Errorf("persistent path check failed: resource root_dir %q %s; set root_dir to a durable workspace/bucket path or pass allow_ephemeral_paths only for disposable smoke/setup work", rootDir, reason)
	}
	effectiveCwd := cwd
	if strings.TrimSpace(effectiveCwd) == "" {
		effectiveCwd = rootDir
	} else if !strings.HasPrefix(effectiveCwd, "/") && strings.TrimSpace(rootDir) != "" {
		effectiveCwd = strings.TrimRight(rootDir, "/") + "/" + effectiveCwd
	}
	if reason := ephemeralRemotePathReason(effectiveCwd); reason != "" {
		return fmt.Errorf("persistent path check failed: cwd %q %s; choose a durable project directory under the resource root_dir", effectiveCwd, reason)
	}
	return nil
}

func ephemeralRemotePathReason(p string) string {
	clean := cleanPath(strings.TrimSpace(p))
	if clean == "" || clean == "/" {
		return ""
	}
	for _, prefix := range []string{"/tmp", "/var/tmp", "/run", "/dev/shm"} {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return "looks like an ephemeral mount"
		}
	}
	if strings.HasPrefix(clean, "/mnt/") && strings.Contains(strings.ToLower(clean), "tmp") {
		return "looks like an ephemeral /mnt temporary mount"
	}
	return ""
}

// validateCommand blocks obviously dangerous commands.
func validateCommand(command string) error {
	blocked := []string{
		"rm -rf /",
		"rm -rf /*",
		"mkfs.",
		"dd if=/dev/",
		"> /dev/sd",
		"shutdown",
		"reboot",
		"init 0",
		"init 6",
	}
	cmdLower := strings.ToLower(strings.TrimSpace(command))
	for _, b := range blocked {
		if strings.Contains(cmdLower, strings.ToLower(b)) {
			return fmt.Errorf("command contains blocked pattern: %s", b)
		}
	}
	return nil
}

func cwdSandboxError(err error, rootDir string) error {
	return fmt.Errorf(`cwd sandbox violation: %w
This resource root_dir is %s.
Either use a cwd under %s, update the resource root_dir, or put an explicit cd inside the remote command.`, err, rootDir, rootDir)
}

func cleanPath(p string) string {
	// Simple path clean that removes trailing slashes and resolves ..
	if p == "" {
		return p
	}
	parts := strings.Split(p, "/")
	var cleaned []string
	for _, part := range parts {
		if part == ".." && len(cleaned) > 0 {
			cleaned = cleaned[:len(cleaned)-1]
		} else if part != "." && part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return "/" + strings.Join(cleaned, "/")
}

// buildCommandScript generates the content of command.sh for a run.
func buildCommandScript(req SubmitRequest, condaEnv, condaBase, condaInit, rootDir string, envVars map[string]string, projectProfile *store.ProjectProfile) string {
	var lines []string

	lines = append(lines, "#!/usr/bin/env bash")
	lines = append(lines, "set -e")
	lines = append(lines, `export PYTHONUNBUFFERED="${PYTHONUNBUFFERED:-1}"`)

	// Export environment variables
	for k, v := range envVars {
		lines = append(lines, fmt.Sprintf("export %s=%s", k, shellQuote(v)))
	}

	if projectProfile != nil {
		lines = append(lines, fmt.Sprintf("cd %s", shellQuote(projectProfile.ResolvedCwd)))
		if projectProfile.ResolvedEnv == ProjectEnvConda {
			lines = append(lines, condaSetupLines(condaBase, condaInit)...)
		} else {
			lines = append(lines, projectEnvPrelude(&store.Resource{CondaBase: condaBase, CondaInit: condaInit}, projectProfile)...)
		}
	} else if condaEnv != "" {
		lines = append(lines, condaSetupLines(condaBase, condaInit)...)
	}

	// cd to working directory
	if projectProfile == nil && req.Cwd != "" {
		resolved := req.Cwd
		if !strings.HasPrefix(req.Cwd, "/") {
			resolved = rootDir + "/" + req.Cwd
		}
		lines = append(lines, fmt.Sprintf("cd %s", shellQuote(resolved)))
	}
	if req.UIEventsPath != "" {
		lines = append(lines, `mkdir -p "$(dirname -- "$AEXP_UI_EVENTS")"`)
		lines = append(lines, `: > "$AEXP_UI_EVENTS"`)
		lines = append(lines, `cat > "$AEXP_RUN_DIR/aexp_events.py" <<'PY'`)
		lines = append(lines, AexpEventsPythonHelper())
		lines = append(lines, `PY`)
		lines = append(lines, `if [ ! -e "$PWD/aexp_events.py" ]; then cat > "$PWD/aexp_events.py" <<'PY'`)
		lines = append(lines, AexpEventsPythonShim())
		lines = append(lines, `PY`)
		lines = append(lines, `fi`)
		lines = append(lines, `export PYTHONPATH="$PWD:$AEXP_RUN_DIR${PYTHONPATH:+:$PYTHONPATH}"`)
	}

	// The actual command
	commandLine := runCommandLine(req)
	if projectProfile != nil && projectProfile.ResolvedEnv == ProjectEnvUV {
		if req.Program != "" {
			commandLine = "uv run " + commandLine
		} else {
			commandLine = "uv run bash -lc " + shellQuote(commandLine)
		}
	}
	if projectProfile != nil && projectProfile.ResolvedEnv == ProjectEnvConda && projectProfile.EnvName != "" {
		commandLine = condaRunCommand(projectProfile.EnvName, commandLine)
	} else if projectProfile == nil && condaEnv != "" {
		commandLine = condaRunCommand(condaEnv, commandLine)
	}
	lines = append(lines, commandLine)

	return strings.Join(lines, "\n") + "\n"
}

func condaSetupLines(condaBase, condaInit string) []string {
	lines := make([]string, 0, 4)
	for _, path := range condaInitCandidates(condaBase, condaInit) {
		lines = append(lines, fmt.Sprintf("if [ -f %s ]; then source %s; fi", shellPath(path), shellPath(path)))
	}
	lines = append(lines, `if ! command -v conda >/dev/null 2>&1; then echo "[aexp] conda not found; set resource conda_base/conda_init" >&2; exit 127; fi`)
	return lines
}

func condaRunCommand(env string, commandLine string) string {
	flag := "-n"
	if isCondaPrefix(env) {
		flag = "-p"
	}
	return "conda run --no-capture-output " + flag + " " + shellQuote(env) + " " + commandLine
}

func runCommandLine(req SubmitRequest) string {
	if req.Program != "" {
		// Structured mode: properly escape each arg
		parts := []string{req.Program}
		for _, arg := range req.Args {
			parts = append(parts, shellQuote(arg))
		}
		return strings.Join(parts, " ")
	}
	// Free-form command: pass through as-is
	return req.Command
}

func isCondaPrefix(env string) bool {
	env = strings.TrimSpace(env)
	return strings.HasPrefix(env, "/") || strings.HasPrefix(env, "~") || strings.Contains(env, "/") || strings.Contains(env, "\\")
}

func condaInitCandidates(condaBase, condaInit string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	add(condaInit)
	if condaBase != "" {
		add(strings.TrimRight(condaBase, "/") + "/etc/profile.d/conda.sh")
	}
	add("$HOME/miniforge3/etc/profile.d/conda.sh")
	add("$HOME/miniconda3/etc/profile.d/conda.sh")
	add("$HOME/anaconda3/etc/profile.d/conda.sh")
	add("/opt/conda/etc/profile.d/conda.sh")
	return out
}

func shellPath(path string) string {
	if strings.HasPrefix(path, "$HOME/") {
		return `"` + path + `"`
	}
	return shellQuote(path)
}

// commandForDB returns a human-readable command string for the DB record.
func commandForDB(req SubmitRequest) string {
	if req.Program != "" {
		parts := []string{req.Program}
		parts = append(parts, req.Args...)
		return strings.Join(parts, " ")
	}
	return req.Command
}

// shellQuote safely quotes a string for bash using single quotes.
// Unlike shellEscape, this is for use inside a script file, not nested tmux commands.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// shellEscape is kept for backward compatibility with tmux session names etc.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func normalizeUIEventsPath(value string, runID string, hasWorkingDir bool, remoteRunDir string) string {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "none", "off", "false", "disable", "disabled":
		return ""
	case "":
		if !hasWorkingDir {
			return path.Join(remoteRunDir, "events.jsonl")
		}
		return path.Join(".aexp", "events", runID+".jsonl")
	default:
		return value
	}
}

// AexpEventsPythonShim makes `import aexp_events` survive commands that reset
// PYTHONPATH to "." after aexp has injected the run helper directory.
func AexpEventsPythonShim() string {
	return `import os
import runpy

_helper = os.path.join(os.environ.get("AEXP_RUN_DIR", ""), "aexp_events.py")
if not _helper or not os.path.exists(_helper):
    raise ImportError("aexp_events helper not found; AEXP_RUN_DIR is missing or stale")

globals().update(runpy.run_path(_helper))
`
}

func AexpEventsPythonHelper() string {
	return `"""aexp structured event helper.

Use this helper inside training/evaluation code while an aexp run is executing.
Do not reconstruct loss, metric, progress, or parameter events after a run; use
run marks for post-hoc interpretation notes instead.

Contract:
- Metric/progress names are short semantic names: "train/loss", "val/loss",
  "val/observed_mse", "epoch", "trial".
- Put experiment identity in fields such as trial, variant, series, split, stage,
  seed, and fold.
- For sweeps, epoch is local to that trial. Do not use a global trial counter as
  epoch, or loss curves from later trials will appear shifted halfway across the
  chart.
"""

import json
import os
import time
from pathlib import Path

_WARNED_EVENT_QUALITY = set()
_FIRST_EPOCH_BY_CURVE = {}


def _event_path():
    path = os.environ.get("AEXP_UI_EVENTS", "")
    if not path:
        return None
    return Path(path)


def emit(event=None, **fields):
    data = {}
    if isinstance(event, dict):
        data.update(event)
    elif event is not None:
        data["type"] = str(event)
    data.update(fields)
    warnings = _event_warnings(data)
    _normalize_event(data)
    warnings.extend(_event_warnings(data, include_axis=True))
    data.setdefault("time", time.time())
    path = _event_path()
    if path is None:
        return data
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as f:
        f.write(json.dumps(data, ensure_ascii=False, separators=(",", ":")) + "\n")
        for warning in _dedupe_warnings(warnings):
            warning.setdefault("time", data.get("time", time.time()))
            f.write(json.dumps(warning, ensure_ascii=False, separators=(",", ":")) + "\n")
    return data


def metric(name, value, **fields):
    """Record a numeric metric during training/evaluation."""
    return emit(type="metric", name=name, value=value, **fields)


def progress(name, current=None, total=None, **fields):
    """Record live progress such as local epoch, trial, fold, or batch."""
    return emit(type="progress", name=name, current=current, total=total, **fields)


def training_epoch(epoch, total=None, **fields):
    """Record the current training epoch.

    Use this from training loops instead of manually forcing epoch progress to
    100% when a trainer exits early. If a sweep is running, pass trial/variant
    fields so the UI keeps each trial's local epoch separate.
    """
    fields.setdefault("stage", "train")
    return progress("epoch", current=epoch, total=total, status="running", **fields)


def training_done(epoch=None, total=None, best_epoch=None, early_stopped=False, **fields):
    """Record the training-loop terminal state without lying about epoch count.

    For early stopping, keep current at the actual last epoch and set
    early_stopped=True. The UI will show "early stopped" instead of treating the
    run as unfinished or pretending all configured epochs were consumed.
    """
    fields.setdefault("stage", "train")
    if best_epoch is not None:
        fields["best_epoch"] = best_epoch
    status = "early_stopped" if early_stopped else "completed"
    current = total if epoch is None and not early_stopped else epoch
    return progress("epoch", current=current, total=total, status=status, **fields)


def param(name, value, **fields):
    """Record a parameter/config value for the current run or trial."""
    return emit(type="param", name=name, value=value, **fields)


def note(text, **fields):
    """Record a short runtime note. Use run marks for post-hoc analysis."""
    return emit(type="note", text=text, **fields)


def _normalize_event(data):
    typ = str(data.get("type", "")).lower()
    if typ not in {"metric", "metrics", "eval", "scalar", "progress"}:
        return data
    name = str(data.get("name", "")).strip()
    if not name:
        return data
    if data.get("series"):
        if typ != "progress":
            data["name"] = _normalize_metric_leaf(name)
        return data
    parts = [part.strip() for part in name.split("/") if part.strip()]
    if len(parts) < 2:
        return data
    leaf = parts[-1]
    context = "/".join(parts[:-1])
    if len(context) <= 20 and "_" not in leaf:
        return data
    data["series"] = context
    data["name"] = leaf if typ == "progress" else _normalize_metric_leaf(leaf)
    return data


def _normalize_metric_leaf(name):
    lower = str(name).lower()
    for split in ("train", "val", "valid", "validation", "test", "eval"):
        prefix = split + "_"
        if lower.startswith(prefix):
            out_split = "val" if split in {"valid", "validation"} else split
            return out_split + "/" + name[len(prefix):]
    return name


def _event_warnings(data, include_axis=False):
    typ = str(data.get("type", "")).lower()
    if typ in {"warning", "note", "log", "message"}:
        return []
    name = str(data.get("name") or data.get("metric") or data.get("key") or data.get("label") or "").strip()
    if not name:
        return []
    series = _event_series(data)
    warnings = []
    if _long_metric_identity(name, series):
        warnings.append(_event_warning(
            "long_metric_name",
            "metric/progress identity looks too long; keep name semantic and put experiment identity in series/trial/variant fields",
            name,
            series,
            {"name_len": len(name), "series_len": len(series)},
        ))
    if typ in {"metric", "metrics", "eval", "scalar"} and _looks_like_param_name(name):
        warnings.append(_event_warning(
            "constant_as_metric",
            "this looks like a parameter/config value emitted as a metric; use param(name, value) instead",
            name,
            series,
        ))
    if include_axis:
        warning = _epoch_offset_warning(data, name, series, typ)
        if warning:
            warnings.append(warning)
    return warnings


def _event_warning(issue, message, name, series="", detail=None):
    return {
        "type": "warning",
        "kind": "event_quality",
        "issue": issue,
        "severity": "warning",
        "message": message,
        "name": name,
        "series": series,
        **({"detail": detail} if detail else {}),
    }


def _dedupe_warnings(warnings):
    out = []
    for warning in warnings:
        key = (warning.get("issue"), warning.get("name"), warning.get("series"))
        if key in _WARNED_EVENT_QUALITY:
            continue
        _WARNED_EVENT_QUALITY.add(key)
        out.append(warning)
    return out


def _event_series(data):
    parts = []
    for key in ("series", "run", "variant", "trial", "seed", "fold", "split", "stage"):
        value = str(data.get(key, "")).strip()
        if not value:
            continue
        if key in {"trial", "seed", "fold"}:
            value = key + ":" + value
        if value not in parts:
            parts.append(value)
    return "/".join(parts)


def _long_metric_identity(name, series=""):
    if len(name) > 64 or len(series) > 96:
        return True
    if "/" in name:
        parts = [part for part in name.split("/") if part]
        if len(parts) > 2 and len("/".join(parts[:-1])) > 20:
            return True
    return "/home/" in name or "/Users/" in name or name.count("_") >= 6


def _looks_like_param_name(name):
    lower = str(name).strip().lower()
    if lower in {
        "batch_size", "epochs", "seed", "gpu", "num_workers", "model",
        "input_mode", "target_dim", "target_prefix", "seq_len", "pred_len",
        "patience", "max_trials", "trial_count", "selection_metric", "python",
    }:
        return True
    return lower.endswith(("_dir", "_path", "_csv", "_root", "_file"))


def _epoch_offset_warning(data, name, series, typ):
    if typ not in {"metric", "metrics", "eval", "scalar"} or "loss" not in str(name).lower():
        return None
    if data.get("trial"):
        return None
    try:
        epoch = float(data.get("epoch"))
    except (TypeError, ValueError):
        return None
    key = (str(name), str(series))
    if key in _FIRST_EPOCH_BY_CURVE:
        return None
    _FIRST_EPOCH_BY_CURVE[key] = epoch
    if epoch <= 5:
        return None
    return _event_warning(
        "epoch_offset_suspect",
        "first observed loss epoch is already high and no trial field is set; check for a global trial/epoch counter",
        name,
        series,
        {"first_epoch": epoch},
    )
`
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return make(map[string]string)
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func (e *Executor) saveAgentEvent(ctx context.Context, runID, actor, toolName string, input interface{}, output interface{}) {
	inputJSON := "{}"
	outputJSON := "{}"
	if input != nil {
		if b, err := json.Marshal(input); err == nil {
			inputJSON = string(b)
		}
	}
	if output != nil {
		if b, err := json.Marshal(output); err == nil {
			outputJSON = string(b)
		}
	}
	e.store.SaveAgentEvent(ctx, &store.AgentEvent{
		RunID:      runID,
		Actor:      actor,
		ToolName:   toolName,
		InputJSON:  inputJSON,
		OutputJSON: outputJSON,
	})
}

// LogLine is a helper type for streaming.
type LogLine struct {
	RunID   string `json:"run_id"`
	Source  string `json:"source"`
	LineNo  int    `json:"line_no"`
	Content string `json:"content"`
}

func genID(prefix string) string {
	id, _ := gonanoid.Generate("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", 12)
	return prefix + id
}
