package executor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gonanoid "github.com/matoous/go-nanoid/v2"

	"github.com/ziwu/aexp/internal/store"
)

// SubmitRequest contains the parameters for creating a new run.
type SubmitRequest struct {
	ResourceID    string            `json:"resource_id"`
	Name          string            `json:"name"`
	Kind          string            `json:"kind"`     // smoke, pilot, formal, ablation
	GPUIndex      int               `json:"gpu_index"` // -1 = all, 0+ = specific GPU
	Command       string            `json:"command"`
	Program       string            `json:"program"` // structured: python, bash, etc.
	Args          []string          `json:"args"`    // structured args
	Cwd           string            `json:"cwd"`
	CondaEnv      string            `json:"conda_env"`
	LogPaths      []string          `json:"log_paths"`
	ArtifactPaths []string          `json:"artifact_paths"`
	MetricPaths   []string          `json:"metric_paths"`
	EnvVars       map[string]string `json:"env_vars"`
	CreatedBy     string            `json:"created_by"`
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
	resource, err := e.store.GetResource(ctx, req.ResourceID)
	if err != nil {
		return nil, fmt.Errorf("get resource: %w", err)
	}
	if resource == nil {
		return nil, fmt.Errorf("resource %s not found", req.ResourceID)
	}

	// Path sandbox: cwd must be under root_dir
	if req.Cwd != "" {
		if err := validateCwd(resource.RootDir, req.Cwd); err != nil {
			return nil, fmt.Errorf("path sandbox violation: %w", err)
		}
	}

	// Command allowlist check (only for free-form commands)
	if req.Program == "" && req.Command != "" {
		if err := validateCommand(req.Command); err != nil {
			return nil, fmt.Errorf("command rejected: %w", err)
		}
	}

	// GPU slot lock
	activeRuns, err := e.store.ListRuns(ctx, store.RunFilter{
		ResourceID: req.ResourceID,
		Status:     store.RunStatusRunning,
	})
	if err != nil {
		return nil, fmt.Errorf("check active runs: %w", err)
	}

	gpuIndex := req.GPUIndex
	if gpuIndex < -1 {
		gpuIndex = -1
	}

	for _, active := range activeRuns {
		if gpuIndex == -1 {
			return nil, fmt.Errorf("resource %s already has an active run (%s) using GPU %d", resource.Name, active.ID, active.GPUIndex)
		}
		if active.GPUIndex == -1 {
			return nil, fmt.Errorf("resource %s has run %s using all GPUs", resource.Name, active.ID)
		}
		if active.GPUIndex == gpuIndex {
			return nil, fmt.Errorf("GPU %d on resource %s is already in use by run %s", gpuIndex, resource.Name, active.ID)
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

	// Build env vars (GPU env must be injected before command)
	envVars := copyMap(req.EnvVars)
	if gpuIndex >= 0 {
		envVars["CUDA_VISIBLE_DEVICES"] = fmt.Sprintf("%d", gpuIndex)
	}

	// Build command.sh content
	commandScript := buildCommandScript(req, condaEnv, resource.RootDir, envVars)

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
		EnvJSON:           string(envJSON),
		LogPathsJSON:      string(logPathsJSON),
		ArtifactPathsJSON: string(artifactPathsJSON),
		MetricPathsJSON:   string(metricPathsJSON),
		TmuxSession:       tmuxSession,
		RemoteRunDir:      remoteRunDir,
		CreatedBy:         req.CreatedBy,
	}

	if err := e.store.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Write agent event immediately (before attempting SSH)
	e.saveAgentEvent(ctx, run.ID, req.CreatedBy, "create_run", req, map[string]string{"run_id": run.ID, "status": "starting"})

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

	if lastN <= 0 {
		lastN = 200
	}

	cmd := fmt.Sprintf("tail -f -n %d %s", lastN, logFile)
	ch, err := e.execStream(ctx, resource, cmd)
	if err != nil {
		return nil, err
	}

	logCh := make(chan LogLine, 64)
	go func() {
		defer close(logCh)
		lineNo := 0
		for line := range ch {
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
	cmd := fmt.Sprintf("tail -n %d %s 2>/dev/null", lastN, logFile)

	out, _, err := e.exec(ctx, resource, cmd)
	if err != nil {
		return nil, err
	}

	var lines []LogLine
	for i, content := range strings.Split(out, "\n") {
		if content == "" {
			continue
		}
		lines = append(lines, LogLine{
			RunID:   runID,
			Source:  source,
			LineNo:  lastN - len(strings.Split(out, "\n")) + i + 1,
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
	if run.Status != store.RunStatusRunning && run.Status != store.RunStatusStarting {
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
		RunID:    runID,
		Actor:    run.CreatedBy,
		ToolName: "stop_run",
		InputJSON:  fmt.Sprintf(`{"run_id":"%s"}`, runID),
		OutputJSON: `{"status":"cancelled"}`,
	})

	return nil
}

// ensureWrapper deploys or upgrades the wrapper script on a resource.
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
	e.store.UpdateRun(ctx, run)
}

func (e *Executor) checkResourceIdle(ctx context.Context, r *store.Resource) {
	runs, _ := e.store.ListRuns(ctx, store.RunFilter{
		ResourceID: r.ID,
		Status:     store.RunStatusRunning,
	})
	if len(runs) == 0 {
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
func buildCommandScript(req SubmitRequest, condaEnv, rootDir string, envVars map[string]string) string {
	var lines []string

	lines = append(lines, "#!/usr/bin/env bash")
	lines = append(lines, "set -e")

	// Export environment variables
	for k, v := range envVars {
		lines = append(lines, fmt.Sprintf("export %s=%s", k, shellQuote(v)))
	}

	// Source conda
	lines = append(lines, `source /opt/conda/etc/profile.d/conda.sh 2>/dev/null || true`)

	// Activate env
	if condaEnv != "" {
		lines = append(lines, fmt.Sprintf("conda activate %s 2>/dev/null || true", shellQuote(condaEnv)))
	}

	// cd to working directory
	if req.Cwd != "" {
		resolved := req.Cwd
		if !strings.HasPrefix(req.Cwd, "/") {
			resolved = rootDir + "/" + req.Cwd
		}
		lines = append(lines, fmt.Sprintf("cd %s", shellQuote(resolved)))
	}

	// The actual command
	if req.Program != "" {
		// Structured mode: properly escape each arg
		parts := []string{req.Program}
		for _, arg := range req.Args {
			parts = append(parts, shellQuote(arg))
		}
		lines = append(lines, strings.Join(parts, " "))
	} else {
		// Free-form command: pass through as-is
		lines = append(lines, req.Command)
	}

	return strings.Join(lines, "\n") + "\n"
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
	RunID   string
	Source  string
	LineNo  int
	Content string
}

func genID(prefix string) string {
	id, _ := gonanoid.Generate("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", 12)
	return prefix + id
}
