package executor

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	gonanoid "github.com/matoous/go-nanoid/v2"

	"github.com/ziwu/aexp/internal/store"
)

// SubmitRequest contains the parameters for creating a new run.
type SubmitRequest struct {
	ResourceID      string            `json:"resource_id"`
	Name            string            `json:"name"`
	Command         string            `json:"command"`
	Cwd             string            `json:"cwd"`
	CondaEnv        string            `json:"conda_env"`
	LogPaths        []string          `json:"log_paths"`
	ArtifactPaths   []string          `json:"artifact_paths"`
	MetricPaths     []string          `json:"metric_paths"`
	EnvVars         map[string]string `json:"env_vars"`
	CreatedBy       string            `json:"created_by"`
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

// Submit creates and starts a new run on a resource.
func (e *Executor) Submit(ctx context.Context, req SubmitRequest) (*store.Run, error) {
	resource, err := e.store.GetResource(ctx, req.ResourceID)
	if err != nil {
		return nil, fmt.Errorf("get resource: %w", err)
	}
	if resource == nil {
		return nil, fmt.Errorf("resource %s not found", req.ResourceID)
	}

	// Check for existing active run on this resource
	activeRuns, err := e.store.ListRuns(ctx, store.RunFilter{
		ResourceID: req.ResourceID,
		Status:     store.RunStatusRunning,
	})
	if err != nil {
		return nil, fmt.Errorf("check active runs: %w", err)
	}
	if len(activeRuns) > 0 {
		return nil, fmt.Errorf("resource %s already has an active run (%s)", resource.Name, activeRuns[0].ID)
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

	// Build the full command
	fullCmd := buildCommand(req.Command, condaEnv, req.Cwd, resource.RootDir)

	// Create tmux command via wrapper
	tmuxCmd := fmt.Sprintf(
		`tmux new-session -d -s '%s' 'bash ~/.aexp/wrapper.sh %s %s'`,
		tmuxSession,
		remoteRunDir,
		shellEscape(fullCmd),
	)

	// Create run record
	run := &store.Run{
		ID:          runID,
		ResourceID:  req.ResourceID,
		Name:        req.Name,
		Status:      store.RunStatusStarting,
		Cwd:         req.Cwd,
		Command:     req.Command,
		CondaEnv:    condaEnv,
		TmuxSession: tmuxSession,
		RemoteRunDir: remoteRunDir,
		CreatedBy:   req.CreatedBy,
	}

	if err := e.store.CreateRun(ctx, run); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	// Ensure wrapper script is deployed
	if err := e.ensureWrapper(ctx, resource); err != nil {
		e.updateRunStatus(ctx, run, store.RunStatusFailed)
		return nil, fmt.Errorf("deploy wrapper: %w", err)
	}

	// Execute tmux command via SSH
	_, stderr, err := e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef, tmuxCmd)
	if err != nil {
		e.updateRunStatus(ctx, run, store.RunStatusFailed)
		return nil, fmt.Errorf("exec tmux: %w (stderr: %s)", err, stderr)
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
	out, _, _ := e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef,
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
	tmuxOut, _, tmuxErr := e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef,
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
	ch, err := e.pool.ExecStream(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef, cmd)
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

	out, _, err := e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef, cmd)
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
	_, _, err = e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef,
		fmt.Sprintf("tmux send-keys -t %s C-c", run.TmuxSession))
	if err != nil {
		// Try to kill the session directly
		e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef,
			fmt.Sprintf("tmux kill-session -t %s 2>/dev/null", run.TmuxSession))
	}

	// Wait a moment, then check
	time.Sleep(2 * time.Second)

	// Check if session is gone
	out, _, _ := e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef,
		fmt.Sprintf("tmux has-session -t %s 2>&1; echo $?", run.TmuxSession))

	if strings.TrimSpace(out) == "0" {
		// Still running, force kill
		e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef,
			fmt.Sprintf("tmux kill-session -t %s 2>/dev/null", run.TmuxSession))
	}

	// Update run
	run.Status = store.RunStatusCancelled
	now := sql.NullTime{Time: time.Now(), Valid: true}
	run.FinishedAt = now
	e.store.UpdateRun(ctx, run)

	// Write status to remote
	e.pool.Exec(ctx, resource.Host, resource.Port, resource.User, resource.AuthRef,
		fmt.Sprintf("echo 'cancelled' > %s/status", run.RemoteRunDir))

	e.checkResourceIdle(ctx, resource)

	return nil
}

// ensureWrapper deploys the wrapper script to a resource if not present.
func (e *Executor) ensureWrapper(ctx context.Context, r *store.Resource) error {
	// Check if wrapper exists
	checkCmd := "test -f ~/.aexp/wrapper.sh && echo exists"
	out, _, _ := e.pool.Exec(ctx, r.Host, r.Port, r.User, r.AuthRef, checkCmd)
	if strings.TrimSpace(out) == "exists" {
		return nil
	}

	// Deploy wrapper
	mkdirCmd := "mkdir -p ~/.aexp"
	if _, _, err := e.pool.Exec(ctx, r.Host, r.Port, r.User, r.AuthRef, mkdirCmd); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Write wrapper script via heredoc
	writeCmd := fmt.Sprintf("cat > ~/.aexp/wrapper.sh << 'WRAPPER_EOF'\n%s\nWRAPPER_EOF\nchmod +x ~/.aexp/wrapper.sh", WrapperScript)
	if _, stderr, err := e.pool.Exec(ctx, r.Host, r.Port, r.User, r.AuthRef, writeCmd); err != nil {
		return fmt.Errorf("write wrapper: %w (stderr: %s)", err, stderr)
	}

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

// buildCommand constructs the full shell command with conda activation.
func buildCommand(command, condaEnv, cwd, rootDir string) string {
	var parts []string

	// Source conda
	parts = append(parts, `source /opt/conda/etc/profile.d/conda.sh 2>/dev/null || true`)

	// Activate env
	if condaEnv != "" {
		parts = append(parts, fmt.Sprintf("conda activate %s 2>/dev/null || true", shellEscape(condaEnv)))
	}

	// cd to working directory
	if cwd != "" {
		resolved := cwd
		if !strings.HasPrefix(cwd, "/") {
			resolved = rootDir + "/" + cwd
		}
		parts = append(parts, fmt.Sprintf("cd %s", shellEscape(resolved)))
	}

	// The actual command
	parts = append(parts, command)

	return strings.Join(parts, " && ")
}

func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
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
