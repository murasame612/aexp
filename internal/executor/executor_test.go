package executor

import (
	"strings"
	"testing"
)

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
		`export PYTHONPATH="$AEXP_RUN_DIR${PYTHONPATH:+:$PYTHONPATH}"`,
		"def metric(name, value, **fields):",
		"python 'train.py'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("command script missing %q\n%s", want, script)
		}
	}
}
