package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerInitializeAndListTools(t *testing.T) {
	var out bytes.Buffer
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		"",
	}, "\n")

	err := NewServer("/bin/false").Serve(t.Context(), strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 responses, got %d: %s", len(lines), out.String())
	}

	var initResp map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &initResp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if initResp["error"] != nil {
		t.Fatalf("initialize returned error: %v", initResp["error"])
	}

	var listResp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	if len(listResp.Result.Tools) == 0 {
		t.Fatalf("expected tool definitions")
	}
	if listResp.Result.Tools[0].Name != "aexp_agent_card" {
		t.Fatalf("unexpected first tool %q", listResp.Result.Tools[0].Name)
	}
	var statusDescription string
	for _, tool := range listResp.Result.Tools {
		if tool.Name == "aexp_get_run_status" {
			statusDescription = tool.Description
			break
		}
	}
	if !strings.Contains(statusDescription, "Do not use for monitoring loops") ||
		!strings.Contains(statusDescription, "prefer aexp_get_run_snapshot") {
		t.Fatalf("status tool description should steer agents to snapshot monitoring, got %q", statusDescription)
	}
}

func TestExecToolInvokesAexpBinary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf '{"exit_code":0,"stdout":"ok\n","stderr":""}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_exec","arguments":{"resource":"mu","command":"pwd","cwd":"/workspace","timeout":12}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"exec", "--json", "--resource", "mu", "--timeout", "12", "--cwd", "/workspace", "--", "pwd"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("decode tool response: %v\n%s", err, out.String())
	}
	if resp.Result.IsError {
		t.Fatalf("tool returned error: %s", out.String())
	}
	if len(resp.Result.Content) != 1 || !strings.Contains(resp.Result.Content[0].Text, `"stdout":"ok`) {
		t.Fatalf("unexpected tool content: %#v", resp.Result.Content)
	}
}

func TestExecToolRejectsLongTimeout(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_exec","arguments":{"resource":"mu","command":"sleep 120","timeout":120}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer("/bin/false").Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("decode tool response: %v\n%s", err, out.String())
	}
	if !resp.Result.IsError {
		t.Fatalf("expected tool error, got %s", out.String())
	}
	if len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "aexp_submit_run") {
		t.Fatalf("expected submit_run guidance, got %#v", resp.Result.Content)
	}
}

func TestCLIArgValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "run list", args: []string{"run", "list", "--json"}},
		{name: "allow cancel", args: []string{"run", "cancel", "run_ABC"}},
		{name: "allow submit", args: []string{"run", "submit", "--resource", "mu", "--", "echo ok"}},
		{name: "allow resource update", args: []string{"resource", "update", "mu", "--remote-path", "/usr/bin:/bin"}},
		{name: "reject serve", args: []string{"serve", "--port", "8080"}, wantErr: true},
		{name: "reject mcp", args: []string{"mcp"}, wantErr: true},
		{name: "reject follow", args: []string{"run", "logs", "run_ABC", "--follow"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCLIArgs(tt.args)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMCPRunEventGuidanceUsesStatusPath(t *testing.T) {
	guidance := mcpRunEventGuidance("run_ABC", `{"ui_events":"events/custom.jsonl"}`)
	if got := guidance["ui_events"]; got != "events/custom.jsonl" {
		t.Fatalf("ui_events = %#v", got)
	}
	monitor, ok := guidance["monitor"].([]string)
	if !ok || len(monitor) == 0 || !strings.Contains(monitor[0], "run_ABC") {
		t.Fatalf("unexpected monitor guidance: %#v", guidance["monitor"])
	}
}

func TestMarkRunToolInvokesAexpBinary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf '{"id":"mark_123","run_id":"run_ABC","kind":"note"}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_mark_run","arguments":{"run_id":"run_ABC","kind":"note","title":"ok","reason":"checked"}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"run", "mark", "run_ABC", "--json", "--kind", "note", "--title", "ok", "--reason", "checked"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}
}

func TestRunLifecycleToolsInvokeAexpBinary(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want []string
	}{
		{name: "archive", tool: "aexp_archive_run", want: []string{"run", "archive", "run_ABC"}},
		{name: "restore", tool: "aexp_restore_run", want: []string{"run", "restore", "run_ABC"}},
		{name: "delete", tool: "aexp_delete_run", want: []string{"run", "delete", "run_ABC"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			argsFile := filepath.Join(dir, "args.txt")
			stub := filepath.Join(dir, "aexp-stub")
			script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf 'ok\n'
`
			if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
				t.Fatalf("write stub: %v", err)
			}
			t.Setenv("AEXP_STUB_ARGS", argsFile)

			input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tt.tool + `","arguments":{"run_id":"run_ABC"}}}` + "\n"
			var out bytes.Buffer
			if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
				t.Fatalf("Serve returned error: %v", err)
			}

			rawArgs, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatalf("read args: %v", err)
			}
			gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
			if strings.Join(gotArgs, "\x00") != strings.Join(tt.want, "\x00") {
				t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", tt.want, gotArgs)
			}
		})
	}
}

func TestRunStatusToolReturnsSnapshotGuidance(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf '{"id":"run_ABC","status":"running","ui_events":".aexp/events/run_ABC.jsonl"}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_get_run_status","arguments":{"run_id":"run_ABC"}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"run", "status", "run_ABC", "--short", "--json"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("decode tool response: %v\n%s", err, out.String())
	}
	if resp.Result.IsError {
		t.Fatalf("tool returned error: %s", out.String())
	}
	if len(resp.Result.Content) != 1 {
		t.Fatalf("unexpected content: %#v", resp.Result.Content)
	}
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(resp.Result.Content[0].Text), &body); err != nil {
		t.Fatalf("decode status body: %v\n%s", err, resp.Result.Content[0].Text)
	}
	monitoring, _ := body["monitoring"].(map[string]interface{})
	if monitoring["preferred_tool"] != "aexp_get_run_snapshot" {
		t.Fatalf("status response missing snapshot guidance: %#v", body)
	}
	if monitoring["avoid_for_monitoring"] != true {
		t.Fatalf("status response should discourage monitoring loops: %#v", monitoring)
	}
}

func TestResourceAddToolInvokesAexpBinary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf 'ok\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_resource_add","arguments":{"name":"mu","host":"1.2.3.4","root_dir":"/workspace","user":"root","port":2222,"remote_path":"/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"resource", "add", "--name", "mu", "--host", "1.2.3.4", "--root-dir", "/workspace", "--user", "root", "--remote-path", "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin", "--port", "2222"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}
}

func TestEventMetricToolInvokesAexpBinary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf 'ok\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_event_metric","arguments":{"name":"train/loss","value":0.25,"path":"/tmp/events.jsonl","epoch":3,"field":["split=train"]}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"event", "metric", "train/loss", "0.25", "--path", "/tmp/events.jsonl", "--field", "split=train", "--epoch", "3"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}
}

func TestRunSnapshotToolInvokesAexpBinary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf '{"ok":true}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_get_run_snapshot","arguments":{"run_id":"run_abc123","last":25,"refresh":true}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"run", "snapshot", "run_abc123", "--json", "--tail", "25", "--refresh"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}
}

func TestLatestRunMetricsAliasInvokesAexpBinary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf '{"ok":true}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_latest_run_metrics","arguments":{"run_id":"run_abc123","last":123}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"run", "metrics", "run_abc123", "--json", "--latest", "--tail", "123"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}
}
