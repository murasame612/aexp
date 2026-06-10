package executor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	gonanoid "github.com/matoous/go-nanoid/v2"

	"github.com/ziwu/aexp/internal/store"
)

// SubmitRequest contains the parameters for creating a new run.
type SubmitRequest struct {
	ResourceID        string            `json:"resource_id"`
	Name              string            `json:"name"`
	Kind              string            `json:"kind"`      // smoke, pilot, formal, ablation
	GPUIndex          int               `json:"gpu_index"` // -2 = none, -1 = all, 0+ = specific GPU
	Force             bool              `json:"force"`     // skip GPU slot lock
	Command           string            `json:"command"`
	Program           string            `json:"program"` // structured: python, bash, etc.
	Args              []string          `json:"args"`    // structured args
	Cwd               string            `json:"cwd"`
	CondaEnv          string            `json:"conda_env"`
	ProjectEnv        string            `json:"project_env"` // "", raw, auto
	LogPaths          []string          `json:"log_paths"`
	ArtifactPaths     []string          `json:"artifact_paths"`
	MetricPaths       []string          `json:"metric_paths"`
	UIEventsPath      string            `json:"ui_events_path"`
	EnvVars           map[string]string `json:"env_vars"`
	CreatedBy         string            `json:"created_by"`
	RefreshProjectEnv bool              `json:"refresh_project_env"`
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
	return e.pool.Exec(ctx, r.Host, r.Port, r.User, r.AuthRef, cmd, r.SocksProxy, r.ProxyCommand)
}

// execStream is a helper that streams a command's stdout from a resource.
func (e *Executor) execStream(ctx context.Context, r *store.Resource, cmd string) (<-chan string, error) {
	return e.pool.ExecStream(ctx, r.Host, r.Port, r.User, r.AuthRef, cmd, r.SocksProxy, r.ProxyCommand)
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

	// Generate run ID
	runID := genID("run_")
	tmuxSession := "aexp_" + runID
	remoteRunDir := resource.RootDir + "/.aexp/runs/" + runID

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

	kind := req.Kind
	if kind == "" {
		kind = store.RunKindFormal
	}

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

// RefreshActiveRuns refreshes starting/running runs for a resource, or all resources if resourceID is empty.
func (e *Executor) RefreshActiveRuns(ctx context.Context, resourceID string, timeout time.Duration) ([]store.Run, map[string]bool, error) {
	runs, err := e.listActiveRuns(ctx, resourceID)
	if err != nil {
		return nil, nil, err
	}
	return e.RefreshRuns(ctx, runs, timeout)
}

// RefreshRuns refreshes any starting/running runs in the provided slice.
func (e *Executor) RefreshRuns(ctx context.Context, runs []store.Run, timeout time.Duration) ([]store.Run, map[string]bool, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cached := map[string]bool{}
	for i := range runs {
		if runs[i].Status != store.RunStatusRunning && runs[i].Status != store.RunStatusStarting {
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
	for _, status := range []string{store.RunStatusStarting, store.RunStatusRunning} {
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

	// Only check running/starting runs
	if run.Status != store.RunStatusRunning && run.Status != store.RunStatusStarting {
		return run, nil
	}

	resource, err := e.store.GetResource(ctx, run.ResourceID)
	if err != nil || resource == nil {
		return run, nil
	}

	// Check if exit_code file exists
	exitCodeFile := run.RemoteRunDir + "/exit_code"
	out, _, _ := e.exec(ctx, resource,
		fmt.Sprintf("cat %s 2>/dev/null", exitCodeFile))

	if strings.TrimSpace(out) != "" {
		// Run has finished
		code := 0
		fmt.Sscanf(strings.TrimSpace(out), "%d", &code)
		run.ExitCode = sql.NullInt64{Int64: int64(code), Valid: true}
		now := sql.NullTime{Time: time.Now(), Valid: true}
		run.FinishedAt = now

		if code == 0 {
			run.Status = store.RunStatusSucceeded
		} else {
			run.Status = store.RunStatusFailed
		}
		e.store.UpdateRun(ctx, run)
		e.checkResourceIdle(ctx, resource)
		return run, nil
	}

	// Check if tmux session still exists
	tmuxOut, _, tmuxErr := e.exec(ctx, resource,
		fmt.Sprintf("tmux has-session -t %s 2>&1; echo $?", run.TmuxSession))

	if strings.TrimSpace(tmuxOut) != "0" {
		// tmux session gone but no exit_code → lost
		run.Status = store.RunStatusLost
		now := sql.NullTime{Time: time.Now(), Valid: true}
		run.FinishedAt = now
		e.store.UpdateRun(ctx, run)
		e.checkResourceIdle(ctx, resource)
	}

	_ = tmuxErr
	return run, nil
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

	cmd := fmt.Sprintf("tail -f -n %d %s", lastN, shellQuote(logFile))
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

// Cancel stops a running run.
func (e *Executor) Cancel(ctx context.Context, runID string) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("run %s not found", runID)
	}
	if run.Status == store.RunStatusRunning || run.Status == store.RunStatusStarting {
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
	return status == store.RunStatusSucceeded ||
		status == store.RunStatusFailed ||
		status == store.RunStatusCancelled ||
		status == store.RunStatusLost
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
	stdout, stderr, err := e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef, cmd, resource.SocksProxy, resource.ProxyCommand)
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
		if run.Status == store.RunStatusRunning || run.Status == store.RunStatusStarting {
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

	// Export environment variables
	for k, v := range envVars {
		lines = append(lines, fmt.Sprintf("export %s=%s", k, shellQuote(v)))
	}

	if projectProfile != nil {
		lines = append(lines, fmt.Sprintf("cd %s", shellQuote(projectProfile.ResolvedCwd)))
		lines = append(lines, projectEnvPrelude(&store.Resource{CondaBase: condaBase, CondaInit: condaInit}, projectProfile)...)
	} else if condaEnv != "" {
		// Activate conda environment. If an env is requested, activation must succeed.
		for _, path := range condaInitCandidates(condaBase, condaInit) {
			lines = append(lines, fmt.Sprintf("if [ -f %s ]; then source %s; fi", shellPath(path), shellPath(path)))
		}
		lines = append(lines, `if ! command -v conda >/dev/null 2>&1; then echo "[aexp] conda not found; set resource conda_base/conda_init" >&2; exit 127; fi`)
		lines = append(lines, fmt.Sprintf("conda activate %s", shellQuote(condaEnv)))
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
	lines = append(lines, commandLine)

	return strings.Join(lines, "\n") + "\n"
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

func AexpEventsPythonHelper() string {
	return `import json
import os
import time
from pathlib import Path


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
    data.setdefault("time", time.time())
    path = _event_path()
    if path is None:
        return data
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as f:
        f.write(json.dumps(data, ensure_ascii=False, separators=(",", ":")) + "\n")
    return data


def metric(name, value, **fields):
    return emit(type="metric", name=name, value=value, **fields)


def progress(name, current=None, total=None, **fields):
    return emit(type="progress", name=name, current=current, total=total, **fields)


def param(name, value, **fields):
    return emit(type="param", name=name, value=value, **fields)


def note(text, **fields):
    return emit(type="note", text=text, **fields)
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
