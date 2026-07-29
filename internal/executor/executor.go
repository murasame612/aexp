package executor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	osexec "os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	gonanoid "github.com/matoous/go-nanoid/v2"

	"github.com/ziwu/aexp/internal/eventcache"
	"github.com/ziwu/aexp/internal/store"
)

// SubmitRequest contains the parameters for creating a new run.
type SubmitRequest struct {
	ResourceID          string                   `json:"resource_id"`
	ProjectID           string                   `json:"project_id"`
	TargetID            string                   `json:"target_id"`
	RecipeName          string                   `json:"recipe_name"`
	ProjectConfigSHA256 string                   `json:"project_config_sha256"`
	Datasets            []store.RunDatasetInput  `json:"datasets"`
	Seeds               []int64                  `json:"seeds"`
	SplitProtocol       string                   `json:"split_protocol"`
	EvaluationProtocol  string                   `json:"evaluation_protocol"`
	Inputs              []store.RunInputBinding  `json:"inputs"`
	Outputs             []store.RunOutputBinding `json:"outputs"`
	Name                string                   `json:"name"`
	Kind                string                   `json:"kind"` // smoke, pilot, formal, ablation
	TaskRole            string                   `json:"task_role"`
	EvidenceGrade       string                   `json:"evidence_grade"`
	ExperimentRole      string                   `json:"experiment_role"`
	GPUIndex            int                      `json:"gpu_index"` // -2 = none, -1 = all, 0+ = specific GPU
	Force               bool                     `json:"force"`     // skip GPU slot lock
	ForceReason         string                   `json:"force_reason"`
	PreemptRunID        string                   `json:"preempt_run_id"`
	PreemptSave         bool                     `json:"preempt_save"`
	Command             string                   `json:"command"`
	Program             string                   `json:"program"` // structured: python, bash, etc.
	Args                []string                 `json:"args"`    // structured args
	Cwd                 string                   `json:"cwd"`
	CondaEnv            string                   `json:"conda_env"`
	ProjectEnv          string                   `json:"project_env"` // "", raw, auto
	TargetEnv           string                   `json:"target_env"`  // intended runtime/app env for setup or repair runs
	LogPaths            []string                 `json:"log_paths"`
	ArtifactPaths       []string                 `json:"artifact_paths"`
	MetricPaths         []string                 `json:"metric_paths"`
	UIEventsPath        string                   `json:"ui_events_path"`
	EnvVars             map[string]string        `json:"env_vars"`
	CreatedBy           string                   `json:"created_by"`
	RefreshProjectEnv   bool                     `json:"refresh_project_env"`
	AllowEphemeralPaths bool                     `json:"allow_ephemeral_paths"`
	GitSourceDir        string                   `json:"git_source_dir"`
	AllowDirtyGit       bool                     `json:"allow_dirty_git"`
	RecordGitDiff       bool                     `json:"record_git_diff"`
	reservedRunID       string
}

// SubmitOptions controls optional submit behavior.
type SubmitOptions struct {
	OnCreated func(*store.Run)
}

// Executor manages experiment runs on remote resources.
type Executor struct {
	pool          *SSHPool
	runner        remoteCommandRunner
	store         store.Store
	runIO         RunIO
	launchMu      sync.Mutex
	launchCancels map[string]context.CancelFunc
	cancelGrace   time.Duration
}

// RunIO keeps managed data transport behind the same TransferJob boundary as
// Dataset and Freeze. Executor owns lifecycle sequencing; the adapter owns data.
type RunIO interface {
	EnsureInputs(ctx context.Context, run *store.Run, resource *store.Resource) error
	FinalizeOutputs(ctx context.Context, run *store.Run, resource *store.Resource) error
}

type RunPreflightBlocker struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type RunPreflightBlockedError struct {
	Blockers []RunPreflightBlocker `json:"blockers"`
}

func (e *RunPreflightBlockedError) Error() string {
	parts := make([]string, 0, len(e.Blockers))
	for _, blocker := range e.Blockers {
		parts = append(parts, blocker.Code+": "+blocker.Message)
	}
	return "run preflight blocked: " + strings.Join(parts, "; ")
}

type remoteCommandRunner interface {
	Exec(ctx context.Context, host string, port int, user string, keyPath string, cmd string, socksProxy string, proxyCommand string) (string, string, error)
	ExecStream(ctx context.Context, host string, port int, user string, keyPath string, cmd string, socksProxy string, proxyCommand string) (<-chan string, error)
}

// NewExecutor creates a new executor.
func NewExecutor(pool *SSHPool, store store.Store) *Executor {
	return &Executor{pool: pool, runner: pool, store: store, launchCancels: make(map[string]context.CancelFunc), cancelGrace: 2 * time.Second}
}

func (e *Executor) SetRunIO(runIO RunIO) { e.runIO = runIO }

// Pool returns the underlying SSH pool.
func (e *Executor) Pool() *SSHPool {
	return e.pool
}

// exec is a helper that runs a command on a resource, using its proxy settings if configured.
func (e *Executor) exec(ctx context.Context, r *store.Resource, cmd string) (string, string, error) {
	if e.runner == nil {
		return "", "", fmt.Errorf("remote command runner is unavailable")
	}
	return e.runner.Exec(ctx, r.Host, r.Port, r.User, r.AuthRef, WithResourceRemotePath(r, cmd), r.SocksProxy, r.ProxyCommand)
}

// execStream is a helper that streams a command's stdout from a resource.
func (e *Executor) execStream(ctx context.Context, r *store.Resource, cmd string) (<-chan string, error) {
	if e.runner == nil {
		return nil, fmt.Errorf("remote command runner is unavailable")
	}
	return e.runner.ExecStream(ctx, r.Host, r.Port, r.User, r.AuthRef, WithResourceRemotePath(r, cmd), r.SocksProxy, r.ProxyCommand)
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

// SubmitAsync validates and persists a queued run before any remote probe or
// environment detection. The detached launch keeps progressing after the HTTP
// request that created it has returned, so all clients can observe the run
// immediately through the durable run-change ledger.
func (e *Executor) SubmitAsync(ctx context.Context, req SubmitRequest, opts SubmitOptions) (*store.Run, error) {
	run, err := e.prepareSubmissionReservation(ctx, req)
	if err != nil {
		return nil, err
	}
	requestJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode queued launch: %w", err)
	}
	job := &store.RunLaunchJob{RunID: run.ID, RequestJSON: string(requestJSON), State: store.RunLaunchQueued}
	if err := e.store.CreateRunWithLaunchJob(ctx, run, job, bindingsForRequest(req, run.ID)); err != nil {
		return nil, fmt.Errorf("persist queued run and launch: %w", err)
	}
	if opts.OnCreated != nil {
		opts.OnCreated(run)
	}
	e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run", req, map[string]string{"run_id": run.ID, "status": store.RunStatusQueued})
	go e.launchPersistedSubmission(run.ID)
	return run, nil
}

// ResumePendingSubmissions reclaims launch jobs interrupted by a control-plane
// restart. Run creation and the launch intent are both durable, while the
// actual SSH work remains bounded and idempotent by tmux session identity.
func (e *Executor) ResumePendingSubmissions(ctx context.Context) error {
	if err := e.store.RequeueInterruptedRunLaunchJobs(ctx); err != nil {
		return err
	}
	jobs, err := e.store.ListPendingRunLaunchJobs(ctx)
	if err != nil {
		return err
	}
	if err := e.reconcileOrphanedLocalRuns(ctx, jobs); err != nil {
		return err
	}
	for i := range jobs {
		go e.launchPersistedSubmission(jobs[i].RunID)
	}
	return nil
}

func (e *Executor) reconcileOrphanedLocalRuns(ctx context.Context, jobs []store.RunLaunchJob) error {
	pending := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		pending[job.RunID] = true
	}
	for _, status := range []string{store.RunStatusCreated, store.RunStatusQueued, store.RunStatusPreflighting} {
		runs, err := e.store.ListRuns(ctx, store.RunFilter{Status: status})
		if err != nil {
			return err
		}
		for i := range runs {
			run := &runs[i]
			if pending[run.ID] {
				continue
			}
			reason := strings.TrimSpace(run.FailureReason)
			if reason == "" {
				reason = "run has no pending launch job and never reached a remote lifecycle state"
			}
			failureKind := strings.TrimSpace(run.FailureKind)
			if failureKind == "" {
				failureKind = store.RunFailureLaunchOrphaned
			}
			if err := e.markLocalLaunchFailure(ctx, run, failureKind, reason); err != nil {
				return err
			}
			e.saveAgentEvent(ctx, run.ID, run.CreatedBy, "run_launch_orphaned", nil, map[string]string{"error": reason})
		}
	}
	return nil
}

func (e *Executor) launchPersistedSubmission(runID string) {
	claimCtx, claimCancel := context.WithTimeout(context.Background(), 5*time.Second)
	job, claimed, err := e.store.ClaimRunLaunchJob(claimCtx, runID)
	claimCancel()
	if err != nil || !claimed || job == nil {
		return
	}
	runCtx, runCancel := context.WithTimeout(context.Background(), 5*time.Second)
	run, runErr := e.store.GetRun(runCtx, runID)
	runCancel()
	if runErr != nil || run == nil {
		if runErr == nil {
			runErr = fmt.Errorf("queued run %s no longer exists", runID)
		}
		e.completeLaunchJob(runID, store.RunLaunchFailed, runErr.Error())
		return
	}
	// A reclaimed job may have crossed the remote launch boundary before the
	// control plane stopped. Lifecycle state is authoritative: never relaunch a
	// run that is already observable remotely or terminal.
	if store.IsRunRefreshableStatus(run.Status) {
		e.completeLaunchJob(runID, store.RunLaunchSucceeded, "")
		return
	}
	if store.IsRunTerminalStatus(run.Status) {
		state, message := store.RunLaunchFailed, "run became terminal before launch completed: "+run.Status
		if run.Status == store.RunStatusSucceeded {
			state, message = store.RunLaunchSucceeded, ""
		}
		e.completeLaunchJob(runID, state, message)
		return
	}
	if run.Status != store.RunStatusQueued && run.Status != store.RunStatusPreflighting {
		e.completeLaunchJob(runID, store.RunLaunchFailed, "run is not launchable from lifecycle state: "+run.Status)
		return
	}
	var req SubmitRequest
	if err := json.Unmarshal([]byte(job.RequestJSON), &req); err != nil {
		e.recordLaunchFailure(runID, "", fmt.Errorf("decode queued launch: %w", err))
		e.completeLaunchJob(runID, store.RunLaunchFailed, err.Error())
		return
	}
	req.reservedRunID = runID
	launchCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	e.launchMu.Lock()
	e.launchCancels[runID] = cancel
	e.launchMu.Unlock()
	_, launchErr := e.SubmitWithOptions(launchCtx, req, SubmitOptions{})
	cancel()
	e.launchMu.Lock()
	delete(e.launchCancels, runID)
	e.launchMu.Unlock()
	state := store.RunLaunchSucceeded
	lastError := ""
	if launchErr != nil {
		state = store.RunLaunchFailed
		var blocked *RunPreflightBlockedError
		if errors.As(launchErr, &blocked) {
			state = store.RunLaunchBlocked
		}
		lastError = launchErr.Error()
		e.recordLaunchFailure(runID, req.CreatedBy, launchErr)
	}
	e.completeLaunchJob(runID, state, lastError)
}

func (e *Executor) completeLaunchJob(runID, state, lastError string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.store.CompleteRunLaunchJob(ctx, runID, state, lastError)
}

// SubmitVisibleWithOptions is the synchronous CLI/MCP submission path. It
// persists and announces the queued run first, then performs preflight and
// remote launch in the foreground so the calling process cannot exit halfway
// through an in-memory goroutine.
func (e *Executor) SubmitVisibleWithOptions(ctx context.Context, req SubmitRequest, opts SubmitOptions) (*store.Run, error) {
	run, err := e.reserveSubmission(ctx, req)
	if err != nil {
		return nil, err
	}
	if opts.OnCreated != nil {
		opts.OnCreated(run)
	}
	e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run", req, map[string]string{"run_id": run.ID, "status": store.RunStatusQueued})
	req.reservedRunID = run.ID
	launched, err := e.SubmitWithOptions(ctx, req, SubmitOptions{})
	if err != nil {
		e.recordLaunchFailure(run.ID, req.CreatedBy, err)
	}
	return launched, err
}

func (e *Executor) recordLaunchFailure(runID, createdBy string, launchErr error) {
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	persisted, getErr := e.store.GetRun(persistCtx, runID)
	if getErr == nil && persisted != nil {
		persisted.StatusCheckError = launchErr.Error()
		persisted.StatusSource = store.RunStatusSourceLocalCache
		var blocked *RunPreflightBlockedError
		if errors.As(launchErr, &blocked) {
			_ = e.markLocalLaunchFailure(persistCtx, persisted, store.RunFailurePreflightBlocked, launchErr.Error())
			e.saveAgentEvent(persistCtx, runID, createdBy, "run_preflight_blocked", nil, map[string]string{"error": launchErr.Error()})
			return
		}
		persisted.FailureKind = store.RunFailureUnknown
		persisted.FailureReason = launchErr.Error()
		if !store.IsRunTerminalStatus(persisted.Status) {
			e.updateRunStatus(persistCtx, persisted, store.RunStatusFailed)
		} else {
			updated, err := e.store.UpdateRunFailureMetadata(
				persistCtx, runID, persisted.Status, persisted.FailureKind,
				persisted.FailureReason, persisted.StatusSource, persisted.StatusCheckError,
			)
			if err != nil {
				warnRunStatusWrite(runID, persisted.Status, err)
			} else if !updated {
				warnRunStatusWrite(runID, persisted.Status, fmt.Errorf("terminal status changed before failure metadata was persisted"))
			}
		}
	}
	e.saveAgentEvent(persistCtx, runID, createdBy, "create_run_failed", nil, map[string]string{"error": launchErr.Error()})
}

func (e *Executor) markLocalLaunchFailure(ctx context.Context, run *store.Run, failureKind, reason string) error {
	expectedStatus := run.Status
	run.Status = store.RunStatusFailed
	run.StatusSource = store.RunStatusSourceLocalCache
	run.StatusCheckError = reason
	run.FailureKind = failureKind
	run.FailureReason = reason
	run.FinishedAt = sql.NullTime{Time: time.Now(), Valid: true}
	updated, err := e.store.UpdateRunIfStatus(ctx, run, expectedStatus)
	if err != nil {
		warnRunStatusWrite(run.ID, run.Status, err)
		return err
	}
	if !updated {
		err := fmt.Errorf("run status changed from %s before launch failure was persisted", expectedStatus)
		warnRunStatusWrite(run.ID, run.Status, err)
		return err
	}
	return nil
}

func (e *Executor) reserveSubmission(ctx context.Context, req SubmitRequest) (*store.Run, error) {
	run, err := e.prepareSubmissionReservation(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := e.store.CreateRunWithBindings(ctx, run, bindingsForRequest(req, run.ID)); err != nil {
		return nil, fmt.Errorf("create queued run: %w", err)
	}
	return run, nil
}

func (e *Executor) prepareSubmissionReservation(ctx context.Context, req SubmitRequest) (*store.Run, error) {
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
	if req.Program == "" && req.Command != "" {
		if err := validateCommand(req.Command); err != nil {
			return nil, fmt.Errorf("command rejected: %w", err)
		}
	}
	if req.Force && strings.TrimSpace(req.ForceReason) == "" {
		return nil, fmt.Errorf("--force requires --force-reason so the run record explains why the GPU lock was bypassed")
	}
	kind, taskRole, evidenceGrade, experimentRole, err := store.NormalizeRunSemantics(req.Kind, req.TaskRole, req.EvidenceGrade, req.ExperimentRole)
	if err != nil {
		return nil, err
	}
	gpuIndex := req.GPUIndex
	if gpuIndex < store.GPUIndexNone {
		gpuIndex = store.GPUIndexAll
	}
	runID := genID("run_")
	argsJSON, _ := json.Marshal(req.Args)
	datasetsJSON, _ := json.Marshal(req.Datasets)
	seedsJSON, _ := json.Marshal(req.Seeds)
	run := &store.Run{
		ID: runID, ResourceID: req.ResourceID, ProjectID: strings.TrimSpace(req.ProjectID), TargetID: strings.TrimSpace(req.TargetID),
		RecipeName: strings.TrimSpace(req.RecipeName), ProjectConfigSHA256: strings.TrimSpace(req.ProjectConfigSHA256),
		DatasetsJSON: string(datasetsJSON), SeedsJSON: string(seedsJSON), SplitProtocol: strings.TrimSpace(req.SplitProtocol), EvaluationProtocol: strings.TrimSpace(req.EvaluationProtocol), DataFinalizationState: dataFinalizationInitialState(req.Outputs), Name: req.Name, Status: store.RunStatusQueued,
		Kind: kind, TaskRole: taskRole, EvidenceGrade: evidenceGrade, ExperimentRole: experimentRole,
		GPUIndex: gpuIndex, Cwd: req.Cwd, Command: commandForDB(req), Program: req.Program, ArgsJSON: string(argsJSON),
		ProjectEnv: req.ProjectEnv, TargetEnv: strings.TrimSpace(req.TargetEnv), ForceReason: strings.TrimSpace(req.ForceReason),
		PreemptRunID: strings.TrimSpace(req.PreemptRunID), PreemptSave: req.PreemptSave,
		TmuxSession: "aexp_" + runID, RemoteRunDir: resource.RootDir + "/.aexp/runs/" + runID, CreatedBy: req.CreatedBy,
	}
	return run, nil
}

func bindingsForRequest(req SubmitRequest, runID string) store.RunBindings {
	bindings := store.RunBindings{
		Inputs:  append([]store.RunInputBinding(nil), req.Inputs...),
		Outputs: append([]store.RunOutputBinding(nil), req.Outputs...),
	}
	for index := range bindings.Inputs {
		bindings.Inputs[index].ID = fmt.Sprintf("%s_input_%d", runID, index)
		bindings.Inputs[index].RunID = runID
		bindings.Inputs[index].Ordinal = index
	}
	for index := range bindings.Outputs {
		bindings.Outputs[index].ID = fmt.Sprintf("%s_output_%d", runID, index)
		bindings.Outputs[index].RunID = runID
		bindings.Outputs[index].Ordinal = index
		bindings.Outputs[index].SourcePattern = strings.ReplaceAll(bindings.Outputs[index].SourcePattern, "{run_id}", runID)
		bindings.Outputs[index].LogicalURI = strings.ReplaceAll(bindings.Outputs[index].LogicalURI, "{run_id}", runID)
	}
	return bindings
}

func dataFinalizationInitialState(outputs []store.RunOutputBinding) string {
	if len(outputs) == 0 {
		return store.RunDataFinalizationSkipped
	}
	return store.RunDataFinalizationPending
}

func formalPreflightBlockers(ctx context.Context, registry store.Store, req SubmitRequest, kind string, git gitProvenance) ([]RunPreflightBlocker, error) {
	if kind != store.RunKindFormal && kind != store.RunKindAblation {
		return nil, nil
	}
	blockers := make([]RunPreflightBlocker, 0)
	if strings.TrimSpace(req.ProjectID) == "" {
		blockers = append(blockers, RunPreflightBlocker{Code: "project_missing", Field: "project_id", Message: "formal evidence requires a registered Project"})
	} else {
		project, err := registry.GetProjectDefinition(ctx, req.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("load project %s: %w", req.ProjectID, err)
		}
		if project == nil {
			blockers = append(blockers, RunPreflightBlocker{Code: "project_not_registered", Field: "project_id", Message: fmt.Sprintf("Project %s is not registered", req.ProjectID)})
		}
	}
	provenanceBlockers, err := store.CheckRunProvenance(ctx, registry, store.RunProvenance{
		Datasets:            req.Datasets,
		Seeds:               req.Seeds,
		ProjectConfigSHA256: req.ProjectConfigSHA256,
		GitCommit:           git.Commit,
		GitDirty:            git.Dirty,
		GitDiffHash:         git.DiffHash,
		SplitProtocol:       req.SplitProtocol,
		EvaluationProtocol:  req.EvaluationProtocol,
	})
	if err != nil {
		return nil, err
	}
	for _, blocker := range provenanceBlockers {
		blockers = append(blockers, RunPreflightBlocker{
			Code: blocker.Code, Field: blocker.Field, Message: blocker.Message,
		})
	}
	for index, input := range req.Inputs {
		if strings.TrimSpace(input.Revision) == "" {
			blockers = append(blockers, RunPreflightBlocker{Code: "input_revision_missing", Field: "inputs", Message: fmt.Sprintf("input %d (%s) requires a pinned revision", index, input.LogicalURI)})
		}
	}
	return blockers, nil
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
	reserved := strings.TrimSpace(req.reservedRunID) != ""
	if reserved {
		queued, getErr := e.store.GetRun(ctx, req.reservedRunID)
		if getErr != nil {
			return nil, fmt.Errorf("load queued run %s: %w", req.reservedRunID, getErr)
		}
		if queued == nil {
			return nil, fmt.Errorf("load queued run %s: not found", req.reservedRunID)
		}
		expectedStatus := queued.Status
		queued.Status = store.RunStatusPreflighting
		queued.StatusCheckError = ""
		updated, err := e.store.UpdateRunIfStatus(ctx, queued, expectedStatus)
		if err != nil {
			warnRunStatusWrite(queued.ID, queued.Status, err)
			return nil, fmt.Errorf("mark run preflighting: %w", err)
		}
		if !updated {
			err := fmt.Errorf("run status changed from %s before preflight", expectedStatus)
			warnRunStatusWrite(queued.ID, queued.Status, err)
			return nil, fmt.Errorf("mark run preflighting: %w", err)
		}
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

	kind, taskRole, evidenceGrade, experimentRole, err := store.NormalizeRunSemantics(req.Kind, req.TaskRole, req.EvidenceGrade, req.ExperimentRole)
	if err != nil {
		return nil, err
	}

	// Generate run ID
	runID := req.reservedRunID
	if runID == "" {
		runID = genID("run_")
	}
	tmuxSession := "aexp_" + runID
	remoteRunDir := resource.RootDir + "/.aexp/runs/" + runID

	shouldRecordGitDiff := req.RecordGitDiff && (req.AllowDirtyGit || evidenceGrade != store.RunEvidenceGradeFormal)
	git, err := captureGitProvenance(ctx, req.GitSourceDir, runID, shouldRecordGitDiff)
	if err != nil {
		return nil, err
	}
	if git.RepoRoot != "" && git.Dirty && evidenceGrade == store.RunEvidenceGradeFormal {
		if !req.AllowDirtyGit {
			return nil, &RunPreflightBlockedError{Blockers: []RunPreflightBlocker{{Code: "git_dirty", Message: fmt.Sprintf("formal experiment requires a clean Git worktree at %s", git.RepoRoot)}}}
		}
		if !req.RecordGitDiff {
			return nil, &RunPreflightBlockedError{Blockers: []RunPreflightBlocker{{Code: "git_diff_missing", Message: "dirty formal experiment requires a recorded Git diff"}}}
		}
	}
	blockers, err := formalPreflightBlockers(ctx, e.store, req, kind, git)
	if err != nil {
		return nil, fmt.Errorf("check formal provenance: %w", err)
	}
	if len(blockers) > 0 {
		return nil, &RunPreflightBlockedError{Blockers: blockers}
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
	req.LogPaths = replaceRunIDTokens(req.LogPaths, runID)
	req.MetricPaths = replaceRunIDTokens(req.MetricPaths, runID)
	req.ArtifactPaths = replaceRunIDTokens(req.ArtifactPaths, runID)

	// Build env vars (GPU env must be injected before command)
	envVars := copyMap(req.EnvVars)
	if gpuIndex >= 0 {
		envVars["CUDA_VISIBLE_DEVICES"] = fmt.Sprintf("%d", gpuIndex)
	}
	outputBase := strings.TrimSpace(req.Cwd)
	if projectProfile != nil && strings.TrimSpace(projectProfile.ResolvedCwd) != "" {
		outputBase = strings.TrimSpace(projectProfile.ResolvedCwd)
	}
	if outputBase == "" {
		outputBase = "."
	}
	outputDir := path.Join(outputBase, "output", "aexp", runID)
	envVars["AEXP_RUN_ID"] = runID
	envVars["AEXP_RUN_DIR"] = remoteRunDir
	envVars["AEXP_OUTPUT_DIR"] = outputDir
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
	datasetsJSON, _ := json.Marshal(req.Datasets)
	seedsJSON, _ := json.Marshal(req.Seeds)

	// Create run record
	initialLaunchStatus := store.RunStatusStarting
	if reserved {
		initialLaunchStatus = store.RunStatusPreflighting
	}
	run := &store.Run{
		ID:                    runID,
		ResourceID:            req.ResourceID,
		ProjectID:             strings.TrimSpace(req.ProjectID),
		TargetID:              strings.TrimSpace(req.TargetID),
		RecipeName:            strings.TrimSpace(req.RecipeName),
		ProjectConfigSHA256:   strings.TrimSpace(req.ProjectConfigSHA256),
		DatasetsJSON:          string(datasetsJSON),
		SeedsJSON:             string(seedsJSON),
		SplitProtocol:         strings.TrimSpace(req.SplitProtocol),
		EvaluationProtocol:    strings.TrimSpace(req.EvaluationProtocol),
		DataFinalizationState: dataFinalizationInitialState(req.Outputs),
		Name:                  req.Name,
		Status:                initialLaunchStatus,
		Kind:                  kind,
		TaskRole:              taskRole,
		EvidenceGrade:         evidenceGrade,
		ExperimentRole:        experimentRole,
		GPUIndex:              gpuIndex,
		Cwd:                   req.Cwd,
		Command:               commandForDB(req),
		Program:               req.Program,
		ArgsJSON:              string(argsJSON),
		CondaEnv:              condaEnv,
		ProjectEnv:            req.ProjectEnv,
		TargetEnv:             strings.TrimSpace(req.TargetEnv),
		ForceReason:           req.ForceReason,
		PreemptRunID:          req.PreemptRunID,
		PreemptSave:           req.PreemptSave,
		GitRepoRoot:           git.RepoRoot,
		GitRemoteURL:          git.RemoteURL,
		GitBranch:             git.Branch,
		GitCommit:             git.Commit,
		GitDirty:              git.Dirty,
		GitStatus:             git.Status,
		GitDiffHash:           git.DiffHash,
		GitDiffPath:           git.DiffPath,
		GitAllowDirty:         req.AllowDirtyGit,
		EnvJSON:               string(envJSON),
		LogPathsJSON:          string(logPathsJSON),
		ArtifactPathsJSON:     string(artifactPathsJSON),
		MetricPathsJSON:       string(metricPathsJSON),
		UIEventsPath:          req.UIEventsPath,
		TmuxSession:           tmuxSession,
		RemoteRunDir:          remoteRunDir,
		CreatedBy:             req.CreatedBy,
	}
	if projectProfile != nil {
		run.ResolvedEnv = projectProfile.ResolvedEnv
		run.ResolvedPython = projectProfile.Python
		run.ResolvedCwd = projectProfile.ResolvedCwd
	}

	if reserved {
		updated, err := e.store.UpdateRunIfStatus(ctx, run, store.RunStatusPreflighting)
		if err != nil {
			warnRunStatusWrite(run.ID, run.Status, err)
			return nil, fmt.Errorf("update preflighted run: %w", err)
		}
		if !updated {
			err := fmt.Errorf("run status changed before preflight metadata was persisted")
			warnRunStatusWrite(run.ID, run.Status, err)
			return nil, fmt.Errorf("update preflighted run: %w", err)
		}
	} else if err := e.store.CreateRunWithBindings(ctx, run, bindingsForRequest(req, run.ID)); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	if len(req.Inputs) > 0 {
		if e.runIO == nil {
			return nil, &RunPreflightBlockedError{Blockers: []RunPreflightBlocker{{Code: "run_io_unavailable", Message: "managed run input service is unavailable"}}}
		}
		if err := e.runIO.EnsureInputs(ctx, run, resource); err != nil {
			var blocked *RunPreflightBlockedError
			if errors.As(err, &blocked) {
				return nil, err
			}
			return nil, &RunPreflightBlockedError{Blockers: []RunPreflightBlocker{{Code: "input_ensure_failed", Message: err.Error()}}}
		}
	}
	manifest, err := e.persistRunManifest(ctx, run, resource, store.RunManifestDraft, nil)
	if err != nil {
		return nil, fmt.Errorf("create run manifest: %w", err)
	}
	if err := e.store.SaveArtifactCollection(ctx, &store.ArtifactCollection{RunID: run.ID, State: store.ArtifactCollectionDeclared}); err != nil {
		return nil, fmt.Errorf("create artifact collection: %w", err)
	}

	// Write agent event immediately (before attempting SSH)
	e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run", req, map[string]string{"run_id": run.ID, "status": "starting"})
	if !reserved && opts.OnCreated != nil {
		opts.OnCreated(run)
	}

	// Ensure wrapper script is deployed
	if err := e.ensureWrapper(ctx, resource); err != nil {
		e.failSubmission(ctx, run, fmt.Errorf("deploy wrapper: %w", err))
		e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run_failed", nil, map[string]string{"error": err.Error()})
		return nil, fmt.Errorf("deploy wrapper: %w", err)
	}

	// Create remote run dir and write command.sh
	setupCmd := fmt.Sprintf("mkdir -p %s %s", shellQuote(path.Join(remoteRunDir, "logs")), shellQuote(outputDir))
	if _, stderr, err := e.exec(ctx, resource, setupCmd); err != nil {
		e.failSubmission(ctx, run, fmt.Errorf("create run dir: %w%s", err, stderrSuffix(stderr)))
		e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run_failed", nil, map[string]string{"error": err.Error(), "stderr": stderr})
		return nil, fmt.Errorf("create run dir: %w", err)
	}
	manifestEncoded := base64Encode([]byte(manifest.ManifestJSON))
	if _, stderr, err := e.exec(ctx, resource, fmt.Sprintf("printf '%%s' %s | base64 -d > %s/manifest.json", shellQuote(manifestEncoded), shellQuote(remoteRunDir))); err != nil {
		e.failSubmission(ctx, run, fmt.Errorf("write remote run manifest: %w%s", err, stderrSuffix(stderr)))
		return nil, fmt.Errorf("write remote run manifest: %w%s", err, stderrSuffix(stderr))
	}

	// Write command.sh to remote (base64 to avoid all quoting issues)
	encoded := base64Encode([]byte(commandScript))
	writeCmd := fmt.Sprintf("echo %s | base64 -d > %s/command.sh && chmod +x %s/command.sh",
		encoded, remoteRunDir, remoteRunDir)
	if _, stderr, err := e.exec(ctx, resource, writeCmd); err != nil {
		e.failSubmission(ctx, run, fmt.Errorf("write command.sh: %w", err))
		e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run_failed", nil, map[string]string{"error": err.Error(), "stderr": stderr})
		return nil, fmt.Errorf("write command.sh: %w", err)
	}

	// Launch tmux session — no quoting issues, just pass run_dir
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expectedStatus := run.Status
	run.Status = store.RunStatusStarting
	updated, err := e.store.UpdateRunIfStatus(ctx, run, expectedStatus)
	if err != nil {
		warnRunStatusWrite(run.ID, run.Status, err)
		return nil, fmt.Errorf("mark run starting: %w", err)
	}
	if !updated {
		err := fmt.Errorf("run status changed from %s before remote start", expectedStatus)
		warnRunStatusWrite(run.ID, run.Status, err)
		return nil, fmt.Errorf("mark run starting: %w", err)
	}
	tmuxCmd := fmt.Sprintf("if [ -f %s/exit_code ] || tmux has-session -t %s >/dev/null 2>&1; then :; else tmux new-session -d -s %s 'bash ~/.aexp/wrapper.sh %s'; fi",
		remoteRunDir, tmuxSession, tmuxSession, remoteRunDir)

	if _, stderr, err := e.exec(ctx, resource, tmuxCmd); err != nil {
		e.failSubmission(ctx, run, fmt.Errorf("launch tmux: %w", err))
		e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run_failed", nil, map[string]string{"error": err.Error(), "stderr": stderr})
		return nil, fmt.Errorf("launch tmux: %w (stderr: %s)", err, stderr)
	}

	// Update run to running
	now := sql.NullTime{Time: time.Now(), Valid: true}
	expectedStatus = run.Status
	run.Status = store.RunStatusRunning
	run.StartedAt = now
	updated, err = e.store.UpdateRunIfStatus(ctx, run, expectedStatus)
	if err != nil {
		warnRunStatusWrite(run.ID, run.Status, err)
		return nil, fmt.Errorf("update run: %w", err)
	}
	if !updated {
		err := fmt.Errorf("run status changed from %s before running was persisted", expectedStatus)
		warnRunStatusWrite(run.ID, run.Status, err)
		return nil, fmt.Errorf("update run: %w", err)
	}

	// Update resource status
	resource.Status = store.ResourceStatusBusy
	e.store.UpdateResource(ctx, resource)

	// Update agent event with success
	e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run", req, map[string]string{"run_id": run.ID, "status": "running"})

	return run, nil
}

func replaceRunIDTokens(values []string, runID string) []string {
	if len(values) == 0 {
		return values
	}
	out := append([]string(nil), values...)
	for index := range out {
		out[index] = strings.ReplaceAll(out[index], "{run_id}", runID)
	}
	return out
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
	return e.RefreshRunsWithConcurrency(ctx, runs, timeout, 4)
}

type RunProbeFailure struct {
	ResourceID string   `json:"resource_id"`
	RunIDs     []string `json:"run_ids"`
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Retryable  bool     `json:"retryable"`
}

type RunRefreshError struct {
	Failures []RunProbeFailure `json:"failures"`
}

type indexedRun struct {
	index int
	run   store.Run
}

func (e *RunRefreshError) Error() string {
	if e == nil || len(e.Failures) == 0 {
		return "run refresh failed"
	}
	return fmt.Sprintf("run refresh failed for %d resource probe(s): %s", len(e.Failures), e.Failures[0].Message)
}

func newRunProbeFailure(resourceID string, runs []store.Run, message string) RunProbeFailure {
	runIDs := make([]string, 0, len(runs))
	for i := range runs {
		if store.IsRunRefreshableStatus(runs[i].Status) {
			runIDs = append(runIDs, runs[i].ID)
		}
	}
	observationErr := store.RunObservationErrorFrom(message)
	failure := RunProbeFailure{ResourceID: resourceID, RunIDs: runIDs, Code: "remote_unreachable", Message: message, Retryable: true}
	if observationErr != nil {
		failure.Code = observationErr.Code
		failure.Retryable = observationErr.Retryable
	}
	return failure
}

// RefreshRunsWithConcurrency refreshes runs serially within each resource and
// bounds the number of resources probed in parallel.
func (e *Executor) RefreshRunsWithConcurrency(ctx context.Context, runs []store.Run, timeout time.Duration, concurrency int) ([]store.Run, map[string]bool, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	groups := make(map[string][]indexedRun)
	for i := range runs {
		if store.IsRunRefreshableStatus(runs[i].Status) {
			groups[runs[i].ResourceID] = append(groups[runs[i].ResourceID], indexedRun{index: i, run: runs[i]})
		}
	}

	cached := map[string]bool{}
	var cachedMu sync.Mutex
	var failureMu sync.Mutex
	failures := make([]RunProbeFailure, 0)
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for _, group := range groups {
		group := group
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				cachedMu.Lock()
				for _, item := range group {
					cached[item.run.ID] = true
					run := item.run
					_ = e.recordRunStatusProbeFailure(ctx, nil, &run, "run probe scheduling failed: "+ctx.Err().Error())
				}
				cachedMu.Unlock()
				failureMu.Lock()
				failures = append(failures, newRunProbeFailure(group[0].run.ResourceID, groupRunsFromIndexed(group), ctx.Err().Error()))
				failureMu.Unlock()
				return
			}
			checkCtx, cancel := context.WithTimeout(ctx, timeout)
			groupRuns := make([]store.Run, 0, len(group))
			for _, item := range group {
				groupRuns = append(groupRuns, item.run)
			}
			refreshedByID, cachedIDs, refreshErr := e.refreshResourceRunGroup(checkCtx, groupRuns)
			cancel()
			if refreshErr != nil {
				failureMu.Lock()
				failures = append(failures, refreshErr.Failures...)
				failureMu.Unlock()
			}
			for _, item := range group {
				refreshed, ok := refreshedByID[item.run.ID]
				if !ok {
					cachedMu.Lock()
					cached[item.run.ID] = true
					cachedMu.Unlock()
					continue
				}
				runs[item.index] = refreshed
				if cachedIDs[item.run.ID] {
					cachedMu.Lock()
					cached[item.run.ID] = true
					cachedMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if len(failures) > 0 {
		return runs, cached, &RunRefreshError{Failures: failures}
	}
	return runs, cached, nil
}

func groupRunsFromIndexed(group []indexedRun) []store.Run {
	runs := make([]store.Run, 0, len(group))
	for _, item := range group {
		runs = append(runs, item.run)
	}
	return runs
}

func (e *Executor) refreshResourceRunGroup(ctx context.Context, runs []store.Run) (map[string]store.Run, map[string]bool, *RunRefreshError) {
	refreshed := make(map[string]store.Run, len(runs))
	cached := make(map[string]bool)
	if len(runs) == 0 {
		return refreshed, cached, nil
	}
	resource, err := e.store.GetResource(ctx, runs[0].ResourceID)
	if err != nil || resource == nil {
		message := "resource not found"
		if err != nil {
			message = "load resource for run probe: " + err.Error()
		}
		failureMessage := message
		for i := range runs {
			if store.IsRunRefreshableStatus(runs[i].Status) {
				if recorded := e.recordProbeFailureReason(ctx, nil, &runs[i], message); recorded != message {
					failureMessage = recorded
				}
			}
			refreshed[runs[i].ID], cached[runs[i].ID] = runs[i], true
		}
		return refreshed, cached, &RunRefreshError{Failures: []RunProbeFailure{newRunProbeFailure(runs[0].ResourceID, runs, failureMessage)}}
	}
	for i := range runs {
		if !store.IsRunRefreshableStatus(runs[i].Status) {
			refreshed[runs[i].ID] = runs[i]
		}
	}
	script := buildResourceRunProbeScript(runs)
	if script == "" {
		return refreshed, cached, nil
	}
	out, _, probeErr := e.exec(ctx, resource, script)
	if probeErr != nil {
		message := "resource batch control probe failed: " + probeErr.Error()
		failureMessage := message
		for i := range runs {
			run := &runs[i]
			if store.IsRunRefreshableStatus(run.Status) {
				if recorded := e.recordProbeFailureReason(ctx, resource, run, message); recorded != message {
					failureMessage = recorded
				}
				cached[run.ID] = true
			}
			refreshed[run.ID] = *run
		}
		return refreshed, cached, &RunRefreshError{Failures: []RunProbeFailure{newRunProbeFailure(resource.ID, runs, failureMessage)}}
	}
	type result struct{ state, value string }
	results := map[string]result{}
	failures := make([]RunProbeFailure, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) == 3 {
			results[parts[0]] = result{state: parts[1], value: parts[2]}
		}
	}
	for i := range runs {
		run := &runs[i]
		if !store.IsRunRefreshableStatus(run.Status) {
			continue
		}
		expectedStatus := run.Status
		probe, ok := results[run.ID]
		if !ok {
			message := "resource batch probe returned no result for run"
			message = e.recordProbeFailureReason(ctx, resource, run, message)
			failures = append(failures, newRunProbeFailure(resource.ID, []store.Run{*run}, message))
			cached[run.ID] = true
			refreshed[run.ID] = *run
			continue
		}
		switch probe.state {
		case "exit":
			code, parseErr := strconv.Atoi(strings.TrimSpace(probe.value))
			if parseErr != nil {
				message := "invalid remote exit code: " + probe.value
				message = e.recordProbeFailureReason(ctx, resource, run, message)
				failures = append(failures, newRunProbeFailure(resource.ID, []store.Run{*run}, message))
				cached[run.ID] = true
			} else {
				markRunStatusObserved(run, store.RunStatusSourceRemoteExitCode)
				e.finishRun(ctx, resource, run, code, expectedStatus)
			}
		case "live":
			previousStatus := run.Status
			markRunStatusObserved(run, store.RunStatusSourceRemoteTmux)
			if run.Status == store.RunStatusSSHUnreachable {
				run.Status = store.RunStatusRunning
			}
			if e.persistRunProbe(ctx, run, expectedStatus) && previousStatus == store.RunStatusSSHUnreachable {
				e.saveAgentEvent(ctx, run.ID, run.CreatedBy, "run_status_changed", map[string]string{"run_id": run.ID}, map[string]string{"status": run.Status, "reason": "remote batch probe confirmed the tmux session is alive"})
			}
		case "gone":
			markRunStatusObserved(run, store.RunStatusSourceRemoteTmux)
			e.resolveGoneRun(ctx, resource, run, expectedStatus)
		default:
			message := "unexpected resource batch probe state: " + probe.state
			message = e.recordProbeFailureReason(ctx, resource, run, message)
			failures = append(failures, newRunProbeFailure(resource.ID, []store.Run{*run}, message))
			cached[run.ID] = true
		}
		refreshed[run.ID] = *run
	}
	if len(failures) > 0 {
		return refreshed, cached, &RunRefreshError{Failures: failures}
	}
	return refreshed, cached, nil
}

func buildResourceRunProbeScript(runs []store.Run) string {
	var script strings.Builder
	for i := range runs {
		run := &runs[i]
		if !store.IsRunRefreshableStatus(run.Status) {
			continue
		}
		exitFile := shellQuote(path.Join(run.RemoteRunDir, "exit_code"))
		fmt.Fprintf(&script, "if [ -f %s ]; then c=$(cat %s 2>/dev/null); printf '%%s\\texit\\t%%s\\n' %s \"$c\"; elif tmux has-session -t %s >/dev/null 2>&1; then printf '%%s\\tlive\\t0\\n' %s; else printf '%%s\\tgone\\t1\\n' %s; fi\n", exitFile, exitFile, shellQuote(run.ID), shellQuote(run.TmuxSession), shellQuote(run.ID), shellQuote(run.ID))
	}
	return script.String()
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
	expectedStatus := run.Status

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
		markRunStatusObserved(run, store.RunStatusSourceRemoteExitCode)
		e.finishRun(ctx, resource, run, code, expectedStatus)
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
		markRunStatusObserved(run, store.RunStatusSourceRemoteTmux)
		e.resolveGoneRun(ctx, resource, run, expectedStatus)
	} else {
		previousStatus := run.Status
		markRunStatusObserved(run, store.RunStatusSourceRemoteTmux)
		if run.Status == store.RunStatusSSHUnreachable {
			run.Status = store.RunStatusRunning
		}
		store.RefreshRunStatusFreshness(run, time.Now())
		persisted := e.persistRunProbe(ctx, run, expectedStatus)
		if persisted && previousStatus == store.RunStatusSSHUnreachable {
			e.saveAgentEvent(ctx, run.ID, run.CreatedBy, "run_status_changed", map[string]string{"run_id": run.ID}, map[string]string{"status": run.Status, "reason": "remote control channel recovered and tmux session is alive"})
		}
	}

	return run, nil
}

func (e *Executor) resolveGoneRun(ctx context.Context, resource *store.Resource, run *store.Run, expectedStatus string) {
	if code, ok := e.wrapperExitCodeFromLogs(ctx, resource, run); ok {
		run.StatusSource = store.RunStatusSourceRemoteProbe
		e.finishRun(ctx, resource, run, code, expectedStatus)
		return
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
	e.markRunStatus(ctx, resource, run, status, reason, expectedStatus)
}

func (e *Executor) recordRunStatusProbeFailure(ctx context.Context, resource *store.Resource, run *store.Run, reason string) error {
	now := time.Now()
	run.StatusSource = store.RunStatusSourceLocalCache
	run.StatusCheckedAt = &now
	run.StatusCheckError = reason
	store.RefreshRunStatusFreshness(run, now)
	persist := func(persistCtx context.Context) (bool, error) {
		return e.store.UpdateRunStatusObservation(persistCtx, run.ID, run.Status, store.RunStatusObservation{Source: run.StatusSource, CheckedAt: now, Error: reason})
	}
	persisted, err := persist(ctx)
	if err != nil && ctx.Err() != nil {
		persistCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		persisted, err = persist(persistCtx)
	}
	if err != nil {
		return err
	}
	if !persisted {
		return fmt.Errorf("run %s lifecycle changed before probe failure could be recorded", run.ID)
	}
	if resource != nil {
		_, _ = e.writeLastRunSnapshot(ctx, resource, run, run.Status, reason)
	}
	return nil
}

func (e *Executor) recordProbeFailureReason(ctx context.Context, resource *store.Resource, run *store.Run, reason string) string {
	if err := e.recordRunStatusProbeFailure(ctx, resource, run, reason); err != nil {
		return reason + "; persist observation: " + err.Error()
	}
	return reason
}

func markRunStatusObserved(run *store.Run, source string) {
	now := time.Now()
	run.StatusSource = source
	run.StatusObservedAt = &now
	run.StatusCheckedAt = &now
	run.StatusCheckError = ""
	store.RefreshRunStatusFreshness(run, now)
}

func (e *Executor) markRunStatus(ctx context.Context, resource *store.Resource, run *store.Run, status string, reason string, expectedStatus string) {
	if run.Status != status {
		run.Status = status
	}
	now := sql.NullTime{Time: time.Now(), Valid: true}
	if store.IsRunTerminalStatus(status) {
		run.FinishedAt = now
	}
	_, _ = e.writeLastRunSnapshot(ctx, resource, run, status, reason)
	if !e.persistRunProbe(ctx, run, expectedStatus) {
		return
	}
	e.saveAgentEvent(ctx, run.ID, run.CreatedBy, "run_status_changed", map[string]string{"run_id": run.ID}, map[string]string{"status": status, "reason": reason})
	if store.IsRunTerminalStatus(status) && resource != nil {
		e.checkResourceIdle(ctx, resource)
		go e.finalizeRunEvidence(run.ID)
	}
}

func (e *Executor) persistRunProbe(ctx context.Context, run *store.Run, expectedStatus string) bool {
	updated, err := e.store.UpdateRunIfStatus(ctx, run, expectedStatus)
	if err == nil && updated {
		return true
	}
	if err != nil {
		warnRunStatusWrite(run.ID, run.Status, err)
	} else {
		warnRunStatusWrite(run.ID, run.Status, fmt.Errorf("expected current status %s", expectedStatus))
	}
	if latest, getErr := e.store.GetRun(ctx, run.ID); getErr == nil && latest != nil {
		*run = *latest
	}
	return false
}

func warnRunStatusWrite(runID, targetStatus string, err error) {
	slog.Warn("persist run status failed", "run_id", runID, "target_status", targetStatus, "error", err)
}

const runManifestSchemaVersion = 2

func (e *Executor) persistRunManifest(ctx context.Context, run *store.Run, resource *store.Resource, state string, artifacts []store.Artifact) (*store.RunManifest, error) {
	inputs, _ := e.store.ListRunInputBindings(ctx, run.ID)
	outputs, _ := e.store.ListRunOutputBindings(ctx, run.ID)
	env := map[string]string{}
	_ = json.Unmarshal([]byte(run.EnvJSON), &env)
	for key, value := range env {
		upper := strings.ToUpper(key)
		if strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL") || strings.HasSuffix(upper, "_KEY") {
			env[key] = "[redacted]"
		} else {
			env[key] = value
		}
	}
	payload := map[string]interface{}{
		"schema_version": runManifestSchemaVersion,
		"run": map[string]interface{}{
			"id": run.ID, "project_id": run.ProjectID, "target_id": run.TargetID, "recipe_name": run.RecipeName,
			"status": run.Status, "task_role": run.TaskRole, "evidence_grade": run.EvidenceGrade, "experiment_role": run.ExperimentRole,
			"created_at": run.CreatedAt, "started_at": run.StartedAt, "finished_at": run.FinishedAt, "exit_code": run.ExitCode,
			"failure_kind": run.FailureKind, "failure_reason": run.FailureReason,
			"data_finalization_state": run.DataFinalizationState, "data_finalization_error": run.DataFinalizationError, "data_finalization_updated_at": run.DataFinalizationUpdatedAt,
		},
		"execution": map[string]interface{}{
			"command": run.Command, "program": run.Program, "args_json": run.ArgsJSON, "cwd": run.Cwd,
			"resolved_cwd": run.ResolvedCwd, "resolved_env": run.ResolvedEnv, "resolved_python": run.ResolvedPython,
			"environment": env,
		},
		"git": map[string]interface{}{
			"repo_root": run.GitRepoRoot, "remote_url": run.GitRemoteURL, "branch": run.GitBranch, "commit": run.GitCommit,
			"dirty": run.GitDirty, "status": run.GitStatus, "diff_hash": run.GitDiffHash, "diff_path": run.GitDiffPath,
		},
		"declared":   map[string]interface{}{"logs_json": run.LogPathsJSON, "metrics_json": run.MetricPathsJSON, "artifacts_json": run.ArtifactPathsJSON, "ui_events_path": run.UIEventsPath},
		"provenance": map[string]interface{}{"project_config_sha256": run.ProjectConfigSHA256, "datasets_json": run.DatasetsJSON, "seeds_json": run.SeedsJSON, "split_protocol": run.SplitProtocol, "evaluation_protocol": run.EvaluationProtocol},
		"inputs":     inputs,
		"outputs":    outputs,
		"artifacts":  artifacts,
	}
	if resource != nil {
		payload["resource"] = map[string]interface{}{"id": resource.ID, "name": resource.Name, "type": resource.Type, "os_type": resource.OSType, "gpu_indices": resource.GPUIndices}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	manifest := &store.RunManifest{
		RunID: run.ID, SchemaVersion: runManifestSchemaVersion, State: state, ManifestJSON: string(raw), SHA256: "sha256:" + hex.EncodeToString(digest[:]), Completeness: store.RunManifestCompletenessCurrent,
	}
	if existing, _ := e.store.GetRunManifest(ctx, run.ID); existing != nil {
		manifest.CreatedAt = existing.CreatedAt
	}
	if state == store.RunManifestFinal {
		now := time.Now()
		manifest.FinalizedAt = &now
	}
	if err := e.store.SaveRunManifest(ctx, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

type remoteArtifactInventory struct {
	Files  []remoteArtifactFile `json:"files"`
	Errors []string             `json:"errors"`
}

type remoteArtifactFile struct {
	Path     string  `json:"path"`
	Relative string  `json:"relative_path"`
	Size     int64   `json:"size"`
	MTime    float64 `json:"mtime"`
	SHA256   string  `json:"sha256"`
	Mime     string  `json:"mime"`
}

// CollectArtifacts discovers declared artifact globs on the remote target and
// indexes metadata/checksums locally. It never downloads artifact contents.
func (e *Executor) CollectArtifacts(ctx context.Context, runID string) (*store.ArtifactCollection, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil || run == nil {
		return nil, firstError(err, fmt.Errorf("run %s not found", runID))
	}
	collection := &store.ArtifactCollection{RunID: run.ID, State: store.ArtifactCollectionDiscovering}
	now := time.Now()
	collection.StartedAt = &now
	_ = e.store.SaveArtifactCollection(ctx, collection)
	fail := func(err error) (*store.ArtifactCollection, error) {
		finished := time.Now()
		collection.State, collection.Error, collection.FinishedAt = store.ArtifactCollectionFailed, err.Error(), &finished
		_ = e.store.SaveArtifactCollection(context.Background(), collection)
		return collection, err
	}
	resource, err := e.store.GetResource(ctx, run.ResourceID)
	if err != nil || resource == nil {
		return fail(firstError(err, fmt.Errorf("resource %s not found", run.ResourceID)))
	}
	patterns := []string{}
	if strings.TrimSpace(run.ArtifactPathsJSON) != "" {
		if err := json.Unmarshal([]byte(run.ArtifactPathsJSON), &patterns); err != nil {
			return fail(fmt.Errorf("invalid artifact paths: %w", err))
		}
	}
	if outputs, listErr := e.store.ListRunOutputBindings(ctx, run.ID); listErr == nil {
		seen := make(map[string]bool, len(patterns))
		for _, pattern := range patterns {
			seen[pattern] = true
		}
		for _, output := range outputs {
			if pattern := strings.TrimSpace(output.SourcePattern); pattern != "" && !seen[pattern] {
				patterns = append(patterns, pattern)
				seen[pattern] = true
			}
		}
	}
	if len(patterns) == 0 {
		_ = e.store.SaveArtifacts(ctx, run.ID, nil)
		finished := time.Now()
		collection.State, collection.FinishedAt = store.ArtifactCollectionIndexed, &finished
		_ = e.store.SaveArtifactCollection(ctx, collection)
		return collection, nil
	}
	root := firstNonEmpty(run.ResolvedCwd, run.Cwd, run.RemoteRunDir)
	patternsRaw, _ := json.Marshal(patterns)
	python := `import base64,glob,hashlib,json,mimetypes,os,sys
root=os.path.realpath(base64.b64decode(sys.argv[1]).decode())
patterns=json.loads(base64.b64decode(sys.argv[2]).decode())
files=[]; errors=[]; seen=set(); max_files=100000
for pattern in patterns:
  candidate=pattern if os.path.isabs(pattern) else os.path.join(root,pattern)
  for value in glob.glob(candidate,recursive=True):
    try:
      real=os.path.realpath(value)
      if os.path.commonpath([root,real]) != root: errors.append('outside root: '+value); continue
      if real in seen or not os.path.isfile(real): continue
      seen.add(real)
      if len(files)>=max_files: errors.append('artifact limit reached'); break
      stat=os.stat(real); digest=''
      h=hashlib.sha256()
      with open(real,'rb') as stream:
        for chunk in iter(lambda: stream.read(1048576),b''): h.update(chunk)
      digest='sha256:'+h.hexdigest()
      files.append({'path':real,'relative_path':os.path.relpath(real,root),'size':stat.st_size,'mtime':stat.st_mtime,'sha256':digest,'mime':mimetypes.guess_type(real)[0] or ''})
    except Exception as exc: errors.append(value+': '+str(exc))
print(json.dumps({'files':files,'errors':errors},separators=(',',':')))`
	cmd := fmt.Sprintf("PY=$(command -v python3 || command -v python) && test -n \"$PY\" && \"$PY\" -c %s %s %s", shellQuote(python), shellQuote(base64.StdEncoding.EncodeToString([]byte(root))), shellQuote(base64.StdEncoding.EncodeToString(patternsRaw)))
	out, stderr, err := e.exec(ctx, resource, cmd)
	if err != nil {
		return fail(fmt.Errorf("artifact discovery failed: %w%s", err, stderrSuffix(stderr)))
	}
	var inventory remoteArtifactInventory
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &inventory); err != nil {
		return fail(fmt.Errorf("decode artifact inventory: %w", err))
	}
	discovered := time.Now()
	artifacts := make([]store.Artifact, 0, len(inventory.Files))
	for _, file := range inventory.Files {
		digest := sha256.Sum256([]byte(run.ID + "\x00" + file.Relative))
		artifact := store.Artifact{ID: "artifact_" + hex.EncodeToString(digest[:8]), RunID: run.ID, Path: file.Path, RelativePath: file.Relative, SourceURI: "ssh://" + resource.ID + "/" + strings.TrimPrefix(file.Path, "/"), Type: "file", Role: inferArtifactRole(file.Relative), Mime: file.Mime, Size: file.Size, SHA256: file.SHA256, CollectionState: store.ArtifactCollectionIndexed, DiscoveredAt: discovered, ModifiedAt: time.Unix(0, int64(file.MTime*float64(time.Second)))}
		artifacts = append(artifacts, artifact)
		collection.TotalBytes += file.Size
	}
	if err := e.store.SaveArtifacts(ctx, run.ID, artifacts); err != nil {
		return fail(err)
	}
	collection.FileCount = len(artifacts)
	collection.State = store.ArtifactCollectionIndexed
	if len(inventory.Errors) > 0 {
		collection.State = store.ArtifactCollectionPartial
		collection.Error = strings.Join(inventory.Errors, "; ")
	}
	finished := time.Now()
	collection.FinishedAt = &finished
	if err := e.store.SaveArtifactCollection(ctx, collection); err != nil {
		return collection, err
	}
	return collection, nil
}

func inferArtifactRole(relative string) string {
	value := strings.ToLower(relative)
	switch {
	case strings.Contains(value, "checkpoint") || strings.HasSuffix(value, ".ckpt") || strings.HasSuffix(value, ".pt") || strings.HasSuffix(value, ".pth"):
		return "checkpoint"
	case strings.Contains(value, "metric") || strings.HasSuffix(value, ".csv"):
		return "metric"
	case strings.HasSuffix(value, ".png") || strings.HasSuffix(value, ".jpg") || strings.HasSuffix(value, ".jpeg") || strings.HasSuffix(value, ".svg"):
		return "plot"
	case strings.HasSuffix(value, ".md") || strings.HasSuffix(value, ".pdf") || strings.Contains(value, "report"):
		return "report"
	default:
		return "other"
	}
}

func firstError(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func (e *Executor) finalizeRunEvidence(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	_, _ = e.CollectArtifacts(ctx, runID)
	run, err := e.store.GetRun(ctx, runID)
	if err != nil || run == nil {
		return
	}
	resource, _ := e.store.GetResource(ctx, run.ResourceID)
	if run.Status == store.RunStatusSucceeded && e.runIO != nil {
		_ = e.runIO.FinalizeOutputs(ctx, run, resource)
		run, _ = e.store.GetRun(ctx, runID)
		if run == nil {
			return
		}
	}
	artifacts, _ := e.store.ListArtifacts(ctx, run.ID)
	manifest, err := e.persistRunManifest(ctx, run, resource, store.RunManifestFinal, artifacts)
	if err != nil {
		return
	}
	if resource != nil && run.RemoteRunDir != "" {
		encoded := base64Encode([]byte(manifest.ManifestJSON))
		_, _, _ = e.exec(ctx, resource, fmt.Sprintf("printf '%%s' %s | base64 -d > %s/manifest.json", shellQuote(encoded), shellQuote(run.RemoteRunDir)))
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
			if cachePath, cacheErr := eventcache.WriteSnapshot(run.ID, eventCacheLinesFromLogLines(remoteLines)); cacheErr == nil {
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

func (e *Executor) finishRun(ctx context.Context, resource *store.Resource, run *store.Run, code int, expectedStatus string) {
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
	if !e.persistRunProbe(ctx, run, expectedStatus) {
		return
	}
	e.checkResourceIdle(ctx, resource)
	go e.finalizeRunEvidence(run.ID)
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
	return e.tailLogs(ctx, runID, source, lastN, 0, false)
}

// TailLogsAfter streams from the first logical line after an absolute cursor.
func (e *Executor) TailLogsAfter(ctx context.Context, runID string, source string, afterLine int) (<-chan LogLine, error) {
	return e.tailLogs(ctx, runID, source, 0, afterLine, true)
}

func (e *Executor) tailLogs(ctx context.Context, runID string, source string, lastN, afterLine int, useCursor bool) (<-chan LogLine, error) {
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
	if useCursor {
		cmd = fmt.Sprintf("tail -F -n +%d %s", afterLine+1, shellQuote(logFile))
	}
	ch, err := e.execStream(ctx, resource, cmd)
	if err != nil {
		return nil, err
	}

	logCh := make(chan LogLine, 64)
	go func() {
		defer close(logCh)
		lineNo := afterLine
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
	return e.tailLogFile(ctx, runID, logPath, lastN, 0, false)
}

// TailLogFileAfter streams a project log file after an absolute line cursor.
func (e *Executor) TailLogFileAfter(ctx context.Context, runID string, logPath string, afterLine int) (<-chan LogLine, error) {
	return e.tailLogFile(ctx, runID, logPath, 0, afterLine, true)
}

func (e *Executor) tailLogFile(ctx context.Context, runID string, logPath string, lastN, afterLine int, useCursor bool) (<-chan LogLine, error) {
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
	if useCursor {
		cmd = fmt.Sprintf("tail -F -n +%d %s", afterLine+1, shellQuote(logFile))
	}
	ch, err := e.execStream(ctx, resource, cmd)
	if err != nil {
		return nil, err
	}

	logCh := make(chan LogLine, 64)
	go func() {
		defer close(logCh)
		lineNo := afterLine
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

// GetLogSnapshotAfter returns the next bounded page after an absolute logical
// line cursor. Unlike a tail snapshot this can fill gaps older than the UI's
// in-memory window. reset is true when the file was truncated below afterLine.
func (e *Executor) GetLogSnapshotAfter(ctx context.Context, runID, source string, afterLine, limit int) ([]LogLine, int, bool, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil || run == nil {
		return nil, 0, false, firstError(err, fmt.Errorf("run %s not found", runID))
	}
	resource, err := e.store.GetResource(ctx, run.ResourceID)
	if err != nil || resource == nil {
		return nil, 0, false, firstError(err, fmt.Errorf("resource not found"))
	}
	if source == "" {
		source = "stdout"
	}
	logFile := path.Join(run.RemoteRunDir, "logs", source+".log")
	return e.getRemoteLogSnapshotAfter(ctx, resource, run.ID, source, logFile, afterLine, limit, false)
}

func (e *Executor) GetLogFileSnapshotAfter(ctx context.Context, runID, logPath string, afterLine, limit int) ([]LogLine, int, bool, error) {
	run, resource, logFile, label, err := e.resolveRunLogFile(ctx, runID, logPath)
	if err != nil {
		return nil, 0, false, err
	}
	return e.getRemoteLogSnapshotAfter(ctx, resource, run.ID, label, logFile, afterLine, limit, true)
}

func (e *Executor) getRemoteLogSnapshotAfter(ctx context.Context, resource *store.Resource, runID, source, logFile string, afterLine, limit int, missingIsError bool) ([]LogLine, int, bool, error) {
	if afterLine < 0 {
		afterLine = 0
	}
	if limit <= 0 {
		limit = 500
	}
	missing := "exit 0"
	if missingIsError {
		missing = `printf '\001AEXP_LOG_MISSING\t%s\n' "$LOG_FILE"; exit 0`
	}
	cmd := fmt.Sprintf(`LOG_FILE=%s
if [ ! -f "$LOG_FILE" ]; then %s; fi
total=$(tr '\r' '\n' < "$LOG_FILE" 2>/dev/null | wc -l | tr -d ' ')
printf '\001AEXP_TOTAL_LINES\t%%s\n' "$total"
if [ "$total" -lt %d ]; then start=1; else start=%d; fi
printf '\001AEXP_START_LINE\t%%s\n' "$start"
tr '\r' '\n' < "$LOG_FILE" 2>/dev/null | sed -n "${start},$ p" | head -n %d`, shellQuote(logFile), missing, afterLine, afterLine+1, limit)
	out, _, err := e.exec(ctx, resource, cmd)
	if err != nil {
		return nil, 0, false, err
	}
	if strings.HasPrefix(out, "\x01AEXP_LOG_MISSING\t") {
		return nil, 0, false, fmt.Errorf("log file not found: %s", strings.TrimSpace(strings.TrimPrefix(out, "\x01AEXP_LOG_MISSING\t")))
	}
	lines, total, err := parseLogSnapshotWithTotal(runID, source, out)
	return lines, total, total < afterLine, err
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
	lines, _, err := parseLogSnapshotWithTotal(runID, source, out)
	return lines, err
}

func parseLogSnapshotWithTotal(runID string, source string, out string) ([]LogLine, int, error) {
	totalLines := 0
	explicitStart := 0
	contentLines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(contentLines) > 0 && strings.HasPrefix(contentLines[0], "\x01AEXP_TOTAL_LINES\t") {
		fmt.Sscanf(strings.TrimPrefix(contentLines[0], "\x01AEXP_TOTAL_LINES\t"), "%d", &totalLines)
		contentLines = contentLines[1:]
	}
	if len(contentLines) > 0 && strings.HasPrefix(contentLines[0], "\x01AEXP_START_LINE\t") {
		fmt.Sscanf(strings.TrimPrefix(contentLines[0], "\x01AEXP_START_LINE\t"), "%d", &explicitStart)
		contentLines = contentLines[1:]
	}
	startLine := 1
	if explicitStart > 0 {
		startLine = explicitStart
	} else if totalLines > len(contentLines) {
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
	return lines, totalLines, nil
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
	e.launchMu.Lock()
	launchCancel := e.launchCancels[runID]
	e.launchMu.Unlock()
	if run.Status == store.RunStatusCreated || run.Status == store.RunStatusQueued || run.Status == store.RunStatusPreflighting || (run.Status == store.RunStatusStarting && launchCancel != nil) {
		expectedStatus := run.Status
		cancel := launchCancel
		if cancel != nil {
			cancel()
		}
		run.Status = store.RunStatusCancelled
		run.StatusCheckError = "launch cancelled before remote process became authoritative"
		run.FinishedAt = sql.NullTime{Time: time.Now(), Valid: true}
		updated, err := e.store.UpdateRunIfStatus(ctx, run, expectedStatus)
		if err != nil {
			return err
		}
		if !updated {
			current, reloadErr := e.store.GetRun(ctx, runID)
			if reloadErr != nil {
				return reloadErr
			}
			if current != nil && isFinishedRunStatus(current.Status) {
				go e.finalizeRunEvidence(current.ID)
				return fmt.Errorf("run %s already finished (status: %s)", runID, current.Status)
			}
			return fmt.Errorf("run %s changed before cancellation", runID)
		}
		_ = e.store.CompleteRunLaunchJob(ctx, runID, store.RunLaunchFailed, "cancelled")
		e.saveAgentEvent(ctx, runID, run.CreatedBy, "stop_run", map[string]string{"run_id": runID}, map[string]string{"status": store.RunStatusCancelled, "stage": "preflight"})
		return nil
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
	grace := e.cancelGrace
	if grace <= 0 {
		grace = 2 * time.Second
	}
	time.Sleep(grace)

	// Check if session is gone
	out, _, _ := e.exec(ctx, resource,
		fmt.Sprintf("tmux has-session -t %s 2>&1; echo $?", run.TmuxSession))

	if strings.TrimSpace(out) == "0" {
		// Still running, force kill
		e.exec(ctx, resource,
			fmt.Sprintf("tmux kill-session -t %s 2>/dev/null", run.TmuxSession))
	}

	// The remote command may have completed naturally while cancellation was in
	// flight. Only the lifecycle state observed before SSH is allowed to become
	// cancelled; a concurrent terminal state wins.
	expectedStatus := run.Status
	run.Status = store.RunStatusCancelled
	now := sql.NullTime{Time: time.Now(), Valid: true}
	run.FinishedAt = now
	updated, err := e.store.UpdateRunIfStatus(ctx, run, expectedStatus)
	if err != nil {
		return err
	}
	if !updated {
		current, reloadErr := e.store.GetRun(ctx, runID)
		if reloadErr != nil {
			return reloadErr
		}
		if current != nil && isFinishedRunStatus(current.Status) {
			go e.finalizeRunEvidence(current.ID)
			return fmt.Errorf("run %s already finished (status: %s)", runID, current.Status)
		}
		return fmt.Errorf("run %s changed before cancellation", runID)
	}

	// Write status to remote
	e.exec(ctx, resource,
		fmt.Sprintf("echo 'cancelled' > %s/status", run.RemoteRunDir))

	e.checkResourceIdle(ctx, resource)
	go e.finalizeRunEvidence(run.ID)

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
	expectedStatus := run.Status
	run.Status = status
	now := sql.NullTime{Time: time.Now(), Valid: true}
	run.FinishedAt = now
	updated, err := e.store.UpdateRunIfStatus(ctx, run, expectedStatus)
	if err != nil && ctx.Err() != nil {
		persistCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		updated, err = e.store.UpdateRunIfStatus(persistCtx, run, expectedStatus)
		cancel()
	}
	if err != nil {
		warnRunStatusWrite(run.ID, status, err)
		return
	}
	if !updated {
		warnRunStatusWrite(run.ID, status, fmt.Errorf("expected current status %s", expectedStatus))
		return
	}
	if store.IsRunTerminalStatus(status) {
		go e.finalizeRunEvidence(run.ID)
	}
}

func (e *Executor) failSubmission(ctx context.Context, run *store.Run, launchErr error) {
	if run == nil || launchErr == nil {
		return
	}
	now := time.Now()
	run.StatusSource = store.RunStatusSourceLocalCache
	run.StatusCheckedAt = &now
	run.StatusCheckError = launchErr.Error()
	run.FailureKind = store.RunFailureUnknown
	run.FailureReason = launchErr.Error()
	e.updateRunStatus(ctx, run, store.RunStatusFailed)
}

func (e *Executor) checkResourceIdle(ctx context.Context, r *store.Resource) {
	runs, _ := e.listActiveRuns(ctx, r.ID)
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

	if cleanRoot == "/" && strings.HasPrefix(cleanResolved, "/") {
		return nil
	}
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
Do not reconstruct loss, metric, progress, or parameter events after a run.
Runtime notes are telemetry; post-run interpretation belongs in the Project journal.

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
import sys
import time
from pathlib import Path

_WARNED_EVENT_QUALITY = set()
_WARNED_EVENT_FAILURES = set()
_FIRST_EPOCH_BY_CURVE = {}


def _warn_once(issue, detail=""):
    if issue in _WARNED_EVENT_FAILURES:
        return
    _WARNED_EVENT_FAILURES.add(issue)
    try:
        suffix = (": " + str(detail)) if detail else ""
        sys.stderr.write("[aexp-events] " + issue + suffix + "\n")
        sys.stderr.flush()
    except Exception:
        pass


def _event_path():
    path = os.environ.get("AEXP_UI_EVENTS", "")
    if not path:
        _warn_once("AEXP_UI_EVENTS is not set; structured events are disabled")
        return None
    return Path(path)


def _env_rank():
    """Return the best available global rank and local rank."""
    rank = None
    local_rank = None
    for key in ("RANK", "SLURM_PROCID", "OMPI_COMM_WORLD_RANK", "PMI_RANK"):
        value = os.environ.get(key)
        if value not in (None, ""):
            try:
                rank = int(value)
            except Exception:
                rank = value
            break
    value = os.environ.get("LOCAL_RANK")
    if value not in (None, ""):
        try:
            local_rank = int(value)
        except Exception:
            local_rank = value
    if rank is None:
        rank = local_rank
    return rank, local_rank


def _all_ranks_enabled():
    return os.environ.get("AEXP_EVENTS_ALL_RANKS", "").strip().lower() in (
        "1", "true", "yes", "on"
    )


def _coerce(value, seen=None):
    """Convert common scientific Python values to JSON-safe values."""
    if value is None or isinstance(value, (bool, int, float, str)):
        return value
    if seen is None:
        seen = set()
    marker = id(value)
    if marker in seen:
        return "<recursive>"
    seen.add(marker)
    try:
        if isinstance(value, dict):
            out = {}
            for key, item in value.items():
                try:
                    safe_key = str(key)
                except Exception:
                    safe_key = "<unprintable-key>"
                out[safe_key] = _coerce(item, seen)
            return out
        if isinstance(value, (list, tuple, set, frozenset)):
            return [_coerce(item, seen) for item in value]
        item = getattr(value, "item", None)
        if callable(item):
            converted = item()
            if converted is not value:
                return _coerce(converted, seen)
        tolist = getattr(value, "tolist", None)
        if callable(tolist):
            converted = tolist()
            if converted is not value:
                return _coerce(converted, seen)
        try:
            return repr(value)
        except Exception:
            return "<unserializable>"
    except Exception:
        return "<unserializable>"
    finally:
        seen.discard(marker)


def emit(event=None, **fields):
    data = {}
    try:
        if isinstance(event, dict):
            data.update(event)
        elif event is not None:
            data["type"] = str(event)
        data.update(fields)
        data = _coerce(data)
        rank, local_rank = _env_rank()
        all_ranks = _all_ranks_enabled()
        if rank not in (None, 0, "0") and not all_ranks:
            return data
        if all_ranks:
            if rank is not None:
                data.setdefault("rank", rank)
            if local_rank is not None:
                data.setdefault("local_rank", local_rank)
        warnings = _event_warnings(data)
        # Event identities are caller-owned provenance. Keep names byte-for-byte
        # stable and only suggest better structure through quality warnings.
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
                f.write(json.dumps(_coerce(warning), ensure_ascii=False, separators=(",", ":")) + "\n")
        return data
    except Exception as exc:
        _warn_once("failed to emit structured event", exc)
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
    """Record a short runtime telemetry note, not post-run research reasoning."""
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
