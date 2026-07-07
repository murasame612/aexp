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
	if !toolListed(listResp.Result.Tools, "aexp_project_card") ||
		!toolListed(listResp.Result.Tools, "aexp_project_runs") ||
		!toolListed(listResp.Result.Tools, "aexp_project_digest") {
		t.Fatalf("project card tools should be listed: %#v", listResp.Result.Tools)
	}
	if !toolListed(listResp.Result.Tools, "aexp_get_evidence_chain") ||
		!toolListed(listResp.Result.Tools, "aexp_add_evidence_node") ||
		!toolListed(listResp.Result.Tools, "aexp_add_evidence_edge") {
		t.Fatalf("evidence chain tools should be listed: %#v", listResp.Result.Tools)
	}
	if !toolListed(listResp.Result.Tools, "aexp_list_matrices") ||
		!toolListed(listResp.Result.Tools, "aexp_create_matrix") ||
		!toolListed(listResp.Result.Tools, "aexp_get_matrix") ||
		!toolListed(listResp.Result.Tools, "aexp_set_matrix_cell") {
		t.Fatalf("matrix tools should be listed: %#v", listResp.Result.Tools)
	}
	if !toolListed(listResp.Result.Tools, "aexp_sync_dataset_push") {
		t.Fatalf("dataset sync tool should be listed: %#v", listResp.Result.Tools)
	}
}

func toolListed(tools []struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
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

func TestSubmitRunToolPassesSafetyMetadata(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
printf 'CALL\n' >> "$AEXP_STUB_ARGS"
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$AEXP_STUB_ARGS"
done
if [ "$1" = "run" ] && [ "$2" = "submit" ]; then
  printf 'created run_ABC\n'
else
  printf '{"id":"run_ABC","status":"running"}\n'
fi
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_submit_run","arguments":{"resource":"mu","name":"repair-env","kind":"setup","target_env":"defect-yolo","force":true,"force_reason":"preempt stale gpu lock for urgent repair","preempt_run":"run_OLD","preempt_save":false,"command":"python repair.py"}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(rawArgs)), "\nCALL\n")
	if len(calls) == 0 {
		t.Fatalf("expected recorded calls, got %q", string(rawArgs))
	}
	gotArgs := strings.Split(strings.TrimPrefix(calls[0], "CALL\n"), "\n")
	wantArgs := []string{
		"run", "submit", "--resource", "mu",
		"--name", "repair-env",
		"--kind", "setup",
		"--target-env", "defect-yolo",
		"--force",
		"--force-reason", "preempt stale gpu lock for urgent repair",
		"--preempt-run", "run_OLD",
		"--preempt-save=false",
		"--shell",
		"--", "python repair.py",
	}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v\noutput %s", wantArgs, gotArgs, out.String())
	}
}

func TestSyncDatasetPushToolInvokesDataSafeCLI(t *testing.T) {
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

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_sync_dataset_push","arguments":{"resource":"mu","source":"dataset","target":"/data/dataset","dry_run":true,"delete":true,"verify":false,"exclude":["*.tmp"],"sync_timeout":600,"retries":3}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{
		"sync", "dataset", "push", "--resource", "mu",
		"--dry-run",
		"--delete",
		"--no-verify",
		"--exclude", "*.tmp",
		"--timeout", "600",
		"--retries", "3",
		"dataset", "/data/dataset",
	}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v\noutput %s", wantArgs, gotArgs, out.String())
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
	rules, ok := guidance["rules"].([]string)
	if !ok || len(rules) == 0 || !strings.Contains(strings.Join(rules, "\n"), "trial") {
		t.Fatalf("expected trial-aware event rules: %#v", guidance["rules"])
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

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_mark_run","arguments":{"run_id":"run_ABC","kind":"note","title":"ok","statement":"short","body_md":"## Body","reason":"checked","attachment":["/tmp/plot.png|Plot"]}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"run", "mark", "run_ABC", "--json", "--kind", "note", "--title", "ok", "--statement", "short", "--body-md", "## Body", "--reason", "checked", "--attach", "/tmp/plot.png|Plot"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}
}

func TestProjectCardToolInvokesAexpBinary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf '{"run_id":"run_ABC","project_id":"dam-imputation"}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_project_card","arguments":{"run_id":"run_ABC","config":"/tmp/.aexp.yaml","question":"Does CAF help?","verdict":"CAF improves mAP.","level":"B","metric":["mAP50-95=0.606"],"artifact":["runs/exp/summary.json"],"next_action":"rerun seeds","important":true,"promote":true,"proposal_reason":"paper table","related_run":["run_DEF"]}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{
		"project", "card", "run_ABC", "--json",
		"--config", "/tmp/.aexp.yaml",
		"--question", "Does CAF help?",
		"--verdict", "CAF improves mAP.",
		"--level", "B",
		"--metric", "mAP50-95=0.606",
		"--artifact", "runs/exp/summary.json",
		"--next-action", "rerun seeds",
		"--important",
		"--promote",
		"--proposal-reason", "paper table",
		"--related-run", "run_DEF",
	}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}
}

func TestProjectDigestToolInvokesAexpBinary(t *testing.T) {
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

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_project_digest","arguments":{"config":"/tmp/.aexp.yaml","important":true,"json":true,"limit":5}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"project", "digest", "--config", "/tmp/.aexp.yaml", "--important", "--json", "--limit", "5"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}
}

func TestEvidenceChainToolsInvokeAexpBinary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "get",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_get_evidence_chain","arguments":{"chain_id":"chain_ABC"}}}` + "\n",
			want:  []string{"evidence", "show", "chain_ABC", "--json"},
		},
		{
			name:  "add node",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_add_evidence_node","arguments":{"chain_id":"chain_ABC","type":"note","title":"Next check","body":"Compare failed seeds","run_id":"run_123","width":300,"height":180}}}` + "\n",
			want:  []string{"evidence", "add-node", "chain_ABC", "--json", "--type", "note", "--title", "Next check", "--body", "Compare failed seeds", "--run-id", "run_123", "--width", "300", "--height", "180"},
		},
		{
			name:  "add edge",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_add_evidence_edge","arguments":{"chain_id":"chain_ABC","from_node_id":"node_a","to_node_id":"node_b","type":"supports","label":"supports","rationale":"metric improved"}}}` + "\n",
			want:  []string{"evidence", "add-edge", "chain_ABC", "--json", "--from", "node_a", "--to", "node_b", "--type", "supports", "--label", "supports", "--rationale", "metric improved"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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

			var out bytes.Buffer
			if err := NewServer(stub).Serve(t.Context(), strings.NewReader(tc.input), &out); err != nil {
				t.Fatalf("Serve returned error: %v", err)
			}
			rawArgs, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatalf("read args: %v", err)
			}
			gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
			if strings.Join(gotArgs, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", tc.want, gotArgs)
			}
		})
	}
}

func TestMatrixToolsInvokeAexpBinary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "get",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_get_matrix","arguments":{"matrix_id":"matrix_ABC"}}}` + "\n",
			want:  []string{"matrix", "show", "matrix_ABC", "--json"},
		},
		{
			name:  "create",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_create_matrix","arguments":{"title":"Dam ablation","description":"Compare trials","columns":["run_id","val_loss","conclusion"]}}}` + "\n",
			want:  []string{"matrix", "create", "Dam ablation", "--json", "--description", "Compare trials", "--column", "run_id", "--column", "val_loss", "--column", "conclusion"},
		},
		{
			name:  "set cell",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_set_matrix_cell","arguments":{"matrix_id":"matrix_ABC","row":"trial022 seed2021","column":"val_loss","value":"0.071","run_id":"run_123","project_card_id":"card_123"}}}` + "\n",
			want:  []string{"matrix", "set", "matrix_ABC", "--json", "--row", "trial022 seed2021", "--column", "val_loss", "--value", "0.071", "--run-id", "run_123", "--project-card-id", "card_123"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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

			var out bytes.Buffer
			if err := NewServer(stub).Serve(t.Context(), strings.NewReader(tc.input), &out); err != nil {
				t.Fatalf("Serve returned error: %v", err)
			}
			rawArgs, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatalf("read args: %v", err)
			}
			gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
			if strings.Join(gotArgs, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", tc.want, gotArgs)
			}
		})
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

func TestManualEventToolsAreNotAgentFacing(t *testing.T) {
	var out bytes.Buffer
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"
	if err := NewServer("/bin/false").Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	var listResp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &listResp); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	for _, name := range []string{"aexp_event_metric", "aexp_event_progress", "aexp_event_param", "aexp_event_note"} {
		if toolListed(listResp.Result.Tools, name) {
			t.Fatalf("%s should not be exposed to agents; training telemetry belongs in aexp_events.py instrumentation", name)
		}
	}
}

func TestCLIRejectsManualEventEmission(t *testing.T) {
	var out bytes.Buffer
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_cli","arguments":{"args":["event","metric","train/loss","0.25"]}}}` + "\n"
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
	if !resp.Result.IsError || len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "instrument the training/eval script") {
		t.Fatalf("manual event CLI should be rejected with instrumentation guidance: %s", out.String())
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

func TestCheckRunEventsToolInvokesEventQualityCLI(t *testing.T) {
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

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_check_run_events","arguments":{"run_id":"run_abc123","last":50}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"run", "event-quality", "run_abc123", "--json", "--tail", "50", "--max-issues", "200"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}
}
