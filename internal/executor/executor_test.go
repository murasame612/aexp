package executor

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

func newExecutorTestStore(t *testing.T) *store.SQLite {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNormalizeUIEventsPath(t *testing.T) {
	if got := normalizeUIEventsPath("", "run_abc", true, "/workspace/.aexp/runs/run_abc"); got != ".aexp/events/run_abc.jsonl" {
		t.Fatalf("default ui events path = %q", got)
	}
	if got := normalizeUIEventsPath("", "run_abc", false, "/workspace/.aexp/runs/run_abc"); got != "/workspace/.aexp/runs/run_abc/events.jsonl" {
		t.Fatalf("run-dir ui events path = %q", got)
	}
	if got := normalizeUIEventsPath("off", "run_abc", true, "/workspace/.aexp/runs/run_abc"); got != "" {
		t.Fatalf("disabled ui events path = %q", got)
	}
	if got := normalizeUIEventsPath("events/train.jsonl", "run_abc", true, "/workspace/.aexp/runs/run_abc"); got != "events/train.jsonl" {
		t.Fatalf("explicit ui events path = %q", got)
	}
}

func TestWithResourceRemotePath(t *testing.T) {
	cmd := WithResourceRemotePath(&store.Resource{
		OSType:     "macos",
		RemotePath: "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
	}, "tmux -V")
	if !strings.HasPrefix(cmd, "export PATH='/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin':$PATH\n") {
		t.Fatalf("unexpected remote path wrapper: %q", cmd)
	}
	if !strings.HasSuffix(cmd, "tmux -V") {
		t.Fatalf("wrapped command missing original command: %q", cmd)
	}
	if got := EffectiveRemotePath(&store.Resource{OSType: "macos"}); !strings.Contains(got, "/opt/homebrew/bin") {
		t.Fatalf("macos default remote path missing homebrew bin: %q", got)
	}
	if got := WithResourceRemotePath(&store.Resource{OSType: "linux"}, "echo ok"); got != "echo ok" {
		t.Fatalf("linux without remote_path should not be wrapped: %q", got)
	}
}

func TestBuildCommandScriptInstallsAexpEventsHelper(t *testing.T) {
	req := SubmitRequest{
		Program:      "python",
		Args:         []string{"train.py"},
		Cwd:          "/workspace/project",
		UIEventsPath: ".aexp/events/run_abc.jsonl",
	}
	script := buildCommandScript(req, "", "", "", "/workspace", map[string]string{
		"AEXP_RUN_DIR":   "/workspace/.aexp/runs/run_abc",
		"AEXP_UI_EVENTS": ".aexp/events/run_abc.jsonl",
	}, nil)
	for _, want := range []string{
		`mkdir -p "$(dirname -- "$AEXP_UI_EVENTS")"`,
		`cat > "$AEXP_RUN_DIR/aexp_events.py" <<'PY'`,
		`export PYTHONPATH="$PWD:$AEXP_RUN_DIR${PYTHONPATH:+:$PYTHONPATH}"`,
		`export PYTHONUNBUFFERED="${PYTHONUNBUFFERED:-1}"`,
		"def metric(name, value, **fields):",
		"python 'train.py'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("command script missing %q\n%s", want, script)
		}
	}
}

func TestWrapperScriptStreamsStdoutBeforeNewline(t *testing.T) {
	bash, err := osexec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	runDir := t.TempDir()
	commandPath := filepath.Join(runDir, "command.sh")
	if err := os.WriteFile(commandPath, []byte("#!/usr/bin/env bash\nprintf 'progress 1\\r'\nsleep 0.4\nprintf 'progress 2\\r'\nsleep 0.1\nprintf '\\ndone\\n'\n"), 0o755); err != nil {
		t.Fatalf("write command.sh: %v", err)
	}
	wrapperPath := filepath.Join(runDir, "wrapper.sh")
	if err := os.WriteFile(wrapperPath, []byte(WrapperScript), 0o755); err != nil {
		t.Fatalf("write wrapper.sh: %v", err)
	}

	cmd := osexec.Command(bash, wrapperPath, runDir)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	stdoutPath := filepath.Join(runDir, "logs", "stdout.log")
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		data, _ := os.ReadFile(stdoutPath)
		if strings.Contains(string(data), "progress 1\r") {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("stdout.log did not receive carriage-return progress before newline; got %q", string(data))
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wrapper exited: %v", err)
		}
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("wrapper did not exit")
	}

	terminalPath := filepath.Join(runDir, "logs", "terminal.log")
	deadline = time.Now().Add(500 * time.Millisecond)
	for {
		terminal, err := os.ReadFile(terminalPath)
		if err != nil {
			t.Fatalf("read terminal.log: %v", err)
		}
		if strings.Contains(string(terminal), "[stdout] progress 1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal.log missing prefixed progress line:\n%s", string(terminal))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestResolveProjectProfileUsesCachedProfile(t *testing.T) {
	ctx := context.Background()
	db := newExecutorTestStore(t)
	res := &store.Resource{
		ID:      "rsrc_profile",
		Name:    "profile-resource",
		Type:    store.ResourceTypeSSH,
		Host:    "127.0.0.1",
		Port:    1,
		User:    "nobody",
		RootDir: "/workspace",
		Status:  store.ResourceStatusIdle,
	}
	if err := db.CreateResource(ctx, res); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := db.SaveProjectProfile(ctx, &store.ProjectProfile{
		ResourceID:   res.ID,
		ResourceName: res.Name,
		Cwd:          "/workspace/project",
		EnvStrategy:  ProjectEnvAuto,
		ResolvedEnv:  ProjectEnvVenv,
		Python:       "/workspace/project/.venv/bin/python",
		ResolvedCwd:  "/workspace/project",
		PythonOK:     true,
		TorchOK:      true,
		CUDA:         "ok",
		CUDAOK:       true,
		Logs:         []string{"logs/**/*.log"},
		Metrics:      []string{"runs/**/*.csv"},
	}); err != nil {
		t.Fatalf("SaveProjectProfile: %v", err)
	}

	exec := NewExecutor(NewSSHPool(1*time.Millisecond), db)
	profile, err := exec.ResolveProjectProfile(ctx, res, "/workspace/project", ProjectEnvAuto, "", false)
	if err != nil {
		t.Fatalf("ResolveProjectProfile: %v", err)
	}
	if profile.Python != "/workspace/project/.venv/bin/python" || profile.ResolvedEnv != ProjectEnvVenv {
		t.Fatalf("unexpected cached profile: %#v", profile)
	}
}

func TestResolveProjectProfileRefreshBypassesCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	db := newExecutorTestStore(t)
	res := &store.Resource{
		ID:      "rsrc_refresh",
		Name:    "refresh-resource",
		Type:    store.ResourceTypeSSH,
		Host:    "127.0.0.1",
		Port:    1,
		User:    "nobody",
		RootDir: "/workspace",
		Status:  store.ResourceStatusIdle,
	}
	if err := db.CreateResource(ctx, res); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := db.SaveProjectProfile(ctx, &store.ProjectProfile{
		ResourceID:   res.ID,
		ResourceName: res.Name,
		Cwd:          "/workspace/project",
		EnvStrategy:  ProjectEnvAuto,
		ResolvedEnv:  ProjectEnvVenv,
		Python:       "/workspace/project/.venv/bin/python",
		ResolvedCwd:  "/workspace/project",
		PythonOK:     true,
	}); err != nil {
		t.Fatalf("SaveProjectProfile: %v", err)
	}

	exec := NewExecutor(NewSSHPool(1*time.Millisecond), db)
	_, err := exec.ResolveProjectProfile(ctx, res, "/workspace/project", ProjectEnvAuto, "", true)
	if err == nil {
		t.Fatal("expected refresh to bypass cache and attempt remote detection")
	}
}

func TestUsableCachedProjectProfile(t *testing.T) {
	if usableCachedProjectProfile(nil) {
		t.Fatal("nil profile should not be usable")
	}
	if usableCachedProjectProfile(&store.ProjectProfile{ResolvedEnv: ProjectEnvVenv, ResolvedCwd: "/p"}) {
		t.Fatal("profile without python_ok should not be usable")
	}
	if !usableCachedProjectProfile(&store.ProjectProfile{ResolvedEnv: ProjectEnvVenv, ResolvedCwd: "/p", PythonOK: true}) {
		t.Fatal("valid profile should be usable")
	}
}

func TestCancelFinishedRunIsRejected(t *testing.T) {
	ctx := context.Background()
	db := newExecutorTestStore(t)
	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_cancel",
		Name:    "cancel-resource",
		Type:    store.ResourceTypeSSH,
		Host:    "127.0.0.1",
		Port:    22,
		User:    "nobody",
		RootDir: "/workspace",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_done",
		ResourceID: "rsrc_cancel",
		Status:     store.RunStatusSucceeded,
		Command:    "python train.py",
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	exec := NewExecutor(NewSSHPool(0), db)
	err := exec.Cancel(ctx, "run_done")
	if err == nil || !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("Cancel finished run error = %v, want already finished", err)
	}
	got, err := db.GetRun(ctx, "run_done")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != store.RunStatusSucceeded {
		t.Fatalf("status changed to %q", got.Status)
	}
}
