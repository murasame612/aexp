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
	if len(listResp.Result.Tools) != 12 {
		t.Fatalf("default research profile should expose 12 intent-level tools, got %d: %#v", len(listResp.Result.Tools), listResp.Result.Tools)
	}
	if listResp.Result.Tools[0].Name != "aexp_agent_card" {
		t.Fatalf("unexpected first tool %q", listResp.Result.Tools[0].Name)
	}
	for _, name := range []string{
		"aexp_agent_card", "aexp_project_list", "aexp_get_project_research_context",
		"aexp_create_project_journal_entry", "aexp_get_project_journal_entry",
		"aexp_list_resources", "aexp_submit_run", "aexp_list_runs", "aexp_get_run_snapshot",
		"aexp_tail_run_logs", "aexp_create_evidence_proposal", "aexp_plan_evidence_graph_proposal",
	} {
		if !toolListed(listResp.Result.Tools, name) {
			t.Fatalf("default research tool %s should be listed", name)
		}
	}
	var contextDescription string
	for _, tool := range listResp.Result.Tools {
		if tool.Name == "aexp_get_project_research_context" {
			contextDescription = tool.Description
		}
	}
	for _, phrase := range []string{"project-research-context-v1", "next_reads", "omits", "aexp_list_runs"} {
		if !strings.Contains(contextDescription, phrase) {
			t.Fatalf("project context description missing %q: %q", phrase, contextDescription)
		}
	}

	t.Setenv("AEXP_MCP_TOOL_PROFILE", "advanced")
	advanced := toolDefinitions()
	if len(advanced) <= len(listResp.Result.Tools) {
		t.Fatalf("advanced profile should expose more tools, got %d", len(advanced))
	}
	if !definitionListed(advanced, "aexp_get_evidence_thread_map") || !definitionListed(advanced, "aexp_rebase_evidence_proposal") || !definitionListed(advanced, "aexp_cancel_run") {
		t.Fatalf("advanced profile is missing evidence/run drill-down tools")
	}
	for _, definition := range toolDefinitions() {
		if definition["name"] != "aexp_branch_from_outcome" {
			continue
		}
		schema, _ := definition["inputSchema"].(map[string]interface{})
		properties, _ := schema["properties"].(map[string]interface{})
		for _, forbidden := range []string{"patch_json", "action", "source_node_id", "target_node_id", "x", "y"} {
			if _, exists := properties[forbidden]; exists {
				t.Fatalf("typed branch tool exposes forbidden field %q: %#v", forbidden, properties)
			}
		}
		for _, required := range []string{"map_id", "outcome_node_id", "hypothesis_title", "branch_rationale"} {
			if _, exists := properties[required]; !exists {
				t.Fatalf("typed branch tool missing %q: %#v", required, properties)
			}
		}
	}
	for _, name := range []string{
		"aexp_get_evidence_thread_map", "aexp_rebase_evidence_proposal", "aexp_cancel_run",
		"aexp_mark_run", "aexp_list_run_marks",
		"aexp_project_card", "aexp_project_runs", "aexp_project_digest",
		"aexp_list_matrices", "aexp_create_matrix", "aexp_sync_dataset_push",
		"aexp_list_workspace_paths", "aexp_storage_stat", "aexp_storage_copy",
		"aexp_plan_transfer", "aexp_start_transfer", "aexp_get_transfer",
		"aexp_resource_add", "aexp_exec", "aexp_cli",
	} {
		if toolListed(listResp.Result.Tools, name) {
			t.Fatalf("legacy/admin tool %s must be hidden from the default research tool list", name)
		}
	}
	t.Setenv("AEXP_MCP_TOOL_PROFILE", "all")
	if len(toolDefinitions()) != len(toolRegistry()) {
		t.Fatalf("all profile must preserve compatibility discovery")
	}
}

func definitionListed(definitions []map[string]interface{}, name string) bool {
	for _, definition := range definitions {
		if definition["name"] == name {
			return true
		}
	}
	return false
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

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_submit_run","arguments":{"resource":"mu","project_id":"project-defect","name":"repair-env","kind":"setup","target_env":"defect-yolo","force":true,"force_reason":"preempt stale gpu lock for urgent repair","preempt_run":"run_OLD","preempt_save":false,"command":"python repair.py"}}}` + "\n"
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
		"--project", "project-defect",
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

func TestAssignRunProjectToolMapsConflictSafeCLI(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$AEXP_STUB_ARGS"
printf '{"run_id":"run_ABC","project_id":"project-b","changed":true}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_assign_run_project","arguments":{"run_id":"run_ABC","project_id":"project-b","expected_project_id":"project-a","actor":"agent-kimi","reason":"correct ownership"}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	want := []string{
		"run", "project", "set", "run_ABC",
		"--project", "project-b",
		"--json",
		"--expected-project", "project-a",
		"--actor", "agent-kimi",
		"--reason", "correct ownership",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args=%q want=%q", got, want)
	}
}

func TestRunBindingObjectsMapToJSONCLIFlags(t *testing.T) {
	cli, err := appendRunBindingFlags([]string{"run", "submit"}, map[string]interface{}{
		"inputs": []interface{}{map[string]interface{}{"from": "aexp://project/data/raw", "to": "data/raw", "revision": "sha256:abc"}, "legacy|target|sha256:def|copy"},
	}, "inputs", "--input", "--input-json")
	if err != nil {
		t.Fatal(err)
	}
	if len(cli) != 6 || cli[2] != "--input-json" || !strings.Contains(cli[3], `"from":"aexp://project/data/raw"`) || cli[4] != "--input" || cli[5] != "legacy|target|sha256:def|copy" {
		t.Fatalf("cli=%#v", cli)
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

func TestFileSpaceAndTransferToolsInvokeTypedCLI(t *testing.T) {
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
		t.Fatal(err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)
	tests := []struct {
		name string
		tool string
		args string
		want []string
	}{
		{name: "roots", tool: "aexp_list_workspace_paths", args: `{"workspace":"project"}`, want: []string{"fs", "roots", "--json", "--workspace", "project"}},
		{name: "storage stat", tool: "aexp_storage_stat", args: `{"uri":"storage://nas/data","resource":"nas-node"}`, want: []string{"storage", "stat", "storage://nas/data", "--json", "--on", "nas-node"}},
		{name: "storage list", tool: "aexp_storage_list", args: `{"uri":"storage://nas/data","limit":25,"cursor":"part-01"}`, want: []string{"storage", "ls", "storage://nas/data", "--json", "--limit", "25", "--cursor", "part-01"}},
		{name: "storage locations", tool: "aexp_storage_locations", args: `{"uri":"aexp://project/data"}`, want: []string{"storage", "locations", "aexp://project/data", "--json"}},
		{name: "storage copy", tool: "aexp_storage_copy", args: `{"source":"storage://nas/data","destination":"resource://gpu/cache"}`, want: []string{"storage", "copy", "storage://nas/data", "resource://gpu/cache", "--json"}},
		{name: "inspect", tool: "aexp_inspect_path", args: `{"uri":"aexp://project/data","resource":"gpu"}`, want: []string{"fs", "stat", "aexp://project/data", "--json", "--on", "gpu"}},
		{name: "list", tool: "aexp_list_path", args: `{"uri":"aexp://project/data","limit":25,"cursor":"part-01"}`, want: []string{"fs", "ls", "aexp://project/data", "--json", "--limit", "25", "--cursor", "part-01"}},
		{name: "plan", tool: "aexp_plan_transfer", args: `{"source":"aexp://project/data","destination":"resource://gpu/cache","source_revision":"sha256:abc","initiator":"nas","verification":"manifest"}`, want: []string{"transfer", "plan", "aexp://project/data", "resource://gpu/cache", "--json", "--source-revision", "sha256:abc", "--initiator", "nas", "--verify", "manifest"}},
		{name: "start", tool: "aexp_start_transfer", args: `{"source":"aexp://project/data","destination":"resource://gpu/cache","expected_plan_sha256":"sha256:plan"}`, want: []string{"transfer", "start", "aexp://project/data", "resource://gpu/cache", "--json", "--plan-sha256", "sha256:plan"}},
		{name: "status", tool: "aexp_get_transfer", args: `{"transfer_id":"transfer_abc"}`, want: []string{"transfer", "status", "transfer_abc", "--json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Remove(argsFile)
			input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tt.tool + `","arguments":` + tt.args + `}}` + "\n"
			var out bytes.Buffer
			if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatalf("read args: %v output=%s", err, out.String())
			}
			got := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Fatalf("want %#v got %#v output=%s", tt.want, got, out.String())
			}
		})
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

func TestCreateProjectJournalToolInvokesAexpBinary(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf '{"id":"journal_123","project_id":"project-a"}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_create_project_journal_entry","arguments":{"project_id":"project-a","actor":"agent","title":"Matched baseline","body_md":"## Result","next_action":"run seeds","run_ids":["run_A","run_B"]}}}` + "\n"
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
		"project", "journal", "create", "project-a", "--title", "Matched baseline", "--json",
		"--actor", "agent", "--body-md", "## Result", "--next-action", "run seeds",
		"--run", "run_A", "--run", "run_B",
	}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, gotArgs)
	}
}

func TestProjectResearchContextToolInvokesCompactCLI(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "aexp-stub")
	script := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg"
done > "$AEXP_STUB_ARGS"
printf '{"contract_version":"project-research-context-v1"}\n'
`
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("AEXP_STUB_ARGS", argsFile)
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_get_project_research_context","arguments":{"project_id":"project-a","map_limit":6,"thread_limit":2,"journal_limit":4,"run_limit":7}}}` + "\n"
	var out bytes.Buffer
	if err := NewServer(stub).Serve(t.Context(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	wantArgs := []string{"project", "context", "project-a", "--json", "--map-limit", "6", "--thread-limit", "2", "--journal-limit", "4", "--run-limit", "7"}
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

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_project_card","arguments":{"run_id":"run_ABC","config":"/tmp/.aexp.yaml","question":"Does CAF help?","verdict":"CAF improves mAP.","level":"B","metric":["mAP50-95=0.606"],"artifact":["runs/exp/summary.json"],"next_action":"rerun seeds","important":true,"promote":true,"reassign_project":true,"proposal_reason":"paper table","related_run":["run_DEF"]}}}` + "\n"
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
		"--reassign-project",
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
			name:  "get shared thread map",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_get_evidence_thread_map","arguments":{"map_id":"chain_ABC"}}}` + "\n",
			want:  []string{"evidence", "threads", "chain_ABC", "--json"},
		},
		{
			name:  "audit accepted map",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_audit_evidence_map","arguments":{"map_id":"chain_ABC"}}}` + "\n",
			want:  []string{"evidence", "audit", "chain_ABC", "--json"},
		},
		{
			name:  "archive secondary map",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_archive_evidence_map","arguments":{"map_id":"chain_ABC"}}}` + "\n",
			want:  []string{"evidence", "archive", "chain_ABC", "--json"},
		},
		{
			name:  "list project graphs",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_list_project_evidence_graphs","arguments":{"project_id":"project_ABC","status":"active","limit":12}}}` + "\n",
			want:  []string{"evidence", "list", "--project", "project_ABC", "--status", "active", "--limit", "12", "--json"},
		},
		{
			name:  "create topic graph",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_create_topic_evidence_graph","arguments":{"project_id":"project_ABC","title":"Data protocol","purpose":"Track protocol changes.","recipes":["formal-vis","formal-ir"],"keywords":["good810","paired"]}}}` + "\n",
			want:  []string{"evidence", "create", "Data protocol", "--project", "project_ABC", "--purpose", "Track protocol changes.", "--json", "--recipe", "formal-vis", "--recipe", "formal-ir", "--keyword", "good810", "--keyword", "paired"},
		},
		{
			name:  "submit Run-optional proposal",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_create_evidence_proposal","arguments":{"project_id":"project_ABC","target_map_id":"chain_ABC","summary":"Bootstrap context","actor":"agent","routing_reason":"This Topic owns the protocol question.","source_run_ids":[],"patch_json":"{\"chain_id\":\"chain_ABC\",\"nodes\":[],\"edges\":[]}"}}}` + "\n",
			want:  []string{"evidence", "proposal-submit", "project_ABC", "--json", "--summary", "Bootstrap context", "--patch-json", `{"chain_id":"chain_ABC","nodes":[],"edges":[]}`, "--target-map", "chain_ABC", "--actor", "agent", "--routing-reason", "This Topic owns the protocol question."},
		},
		{
			name:  "branch from accepted outcome",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_branch_from_outcome","arguments":{"map_id":"chain_ABC","outcome_node_id":"issue_1","hypothesis_title":"Matched data resolves the issue","hypothesis_body_md":"Hold the split fixed.","branch_rationale":"The accepted issue is testable.","experiment_design_title":"Run matched ablation","experiment_design_body_md":"Use five seeds.","summary":"Open matched-data branch","actor":"agent"}}}` + "\n",
			want:  []string{"evidence", "branch-from-outcome", "chain_ABC", "--json", "--outcome-node", "issue_1", "--hypothesis-title", "Matched data resolves the issue", "--branch-rationale", "The accepted issue is testable.", "--hypothesis-body-md", "Hold the split fixed.", "--experiment-design-title", "Run matched ablation", "--experiment-design-body-md", "Use five seeds.", "--summary", "Open matched-data branch", "--actor", "agent"},
		},
		{
			name:  "list independent proposals",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_list_evidence_proposals","arguments":{"project_id":"project_ABC","status":"pending","limit":20}}}` + "\n",
			want:  []string{"evidence", "proposal-list", "project_ABC", "--json", "--status", "pending", "--limit", "20"},
		},
		{
			name:  "get independent proposal",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_get_evidence_proposal","arguments":{"proposal_id":"proposal_ABC"}}}` + "\n",
			want:  []string{"evidence", "proposal-get", "proposal_ABC", "--json"},
		},
		{
			name:  "reroute independent proposal",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_reroute_evidence_proposal","arguments":{"proposal_id":"proposal_ABC","target_map_id":"chain_DEF","routing_reason":"The protocol Topic is the correct owner."}}}` + "\n",
			want:  []string{"evidence", "proposal-reroute", "proposal_ABC", "--target-map", "chain_DEF", "--json", "--routing-reason", "The protocol Topic is the correct owner."},
		},
		{
			name:  "plan Topic promotion",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_plan_evidence_promotion","arguments":{"source_map_id":"chain_TOPIC","source_node_ids":["claim_1"],"summary":"Project conclusion","node_type":"claim","actor":"agent"}}}` + "\n",
			want:  []string{"evidence", "promotion-plan", "chain_TOPIC", "--summary", "Project conclusion", "--json", "--source-node", "claim_1", "--node-type", "claim", "--actor", "agent"},
		},
		{
			name:  "create Topic promotion",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_create_evidence_promotion","arguments":{"source_map_id":"chain_TOPIC","source_node_ids":["claim_1"],"summary":"Project conclusion","node_type":"claim","actor":"agent","expected_plan_hash":"plan123"}}}` + "\n",
			want:  []string{"evidence", "promotion-create", "chain_TOPIC", "--summary", "Project conclusion", "--expected-plan-hash", "plan123", "--json", "--source-node", "claim_1", "--node-type", "claim", "--actor", "agent"},
		},
		{
			name:  "plan independent proposal",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_plan_evidence_graph_proposal","arguments":{"proposal_id":"proposal_ABC"}}}` + "\n",
			want:  []string{"evidence", "proposal-plan", "proposal_ABC", "--json"},
		},
		{
			name:  "plan evidence reorganization",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_plan_evidence_reorganization","arguments":{"map_id":"chain_ABC","patch_json":"{\"chain_id\":\"chain_ABC\",\"nodes\":[],\"edges\":[]}"}}}` + "\n",
			want:  []string{"evidence", "reorganization-plan", "chain_ABC", "--patch-json", `{"chain_id":"chain_ABC","nodes":[],"edges":[]}`, "--json"},
		},
		{
			name:  "create evidence reorganization proposal",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_create_evidence_reorganization_proposal","arguments":{"map_id":"chain_ABC","summary":"Clean one thread","patch_json":"{\"chain_id\":\"chain_ABC\",\"nodes\":[],\"edges\":[]}","expected_plan_hash":"plan123","actor":"agent","routing_reason":"Bounded cleanup","source_run_ids":["run_1"]}}}` + "\n",
			want:  []string{"evidence", "reorganization-create", "chain_ABC", "--summary", "Clean one thread", "--patch-json", `{"chain_id":"chain_ABC","nodes":[],"edges":[]}`, "--expected-plan-hash", "plan123", "--json", "--actor", "agent", "--routing-reason", "Bounded cleanup", "--source-run", "run_1"},
		},
		{
			name:  "rebase evidence proposal",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_rebase_evidence_proposal","arguments":{"proposal_id":"proposal_ABC","actor":"agent"}}}` + "\n",
			want:  []string{"evidence", "proposal-rebase", "proposal_ABC", "--json", "--actor", "agent"},
		},
		{
			name:  "review independent proposal",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_review_evidence_graph_proposal","arguments":{"proposal_id":"proposal_ABC","action":"accept","reviewer":"ziwu"}}}` + "\n",
			want:  []string{"evidence", "proposal-review", "proposal_ABC", "--json", "--action", "accept", "--reviewer", "ziwu"},
		},
		{
			name:  "propose routed graph",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_propose_evidence_graph","arguments":{"run_id":"run_123","graph_id":"chain_ABC","routing_reason":"Recipe formal-vis matches this topic.","base_revision":7,"patch_json":"{\"nodes\":[],\"edges\":[]}"}}}` + "\n",
			want:  []string{"evidence", "propose", "run_123", "--json", "--chain", "chain_ABC", "--patch-json", `{"nodes":[],"edges":[]}`, "--base-revision", "7", "--routing-reason", "Recipe formal-vis matches this topic."},
		},
		{
			name:  "propose graph patch",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_propose_evidence_graph_patch","arguments":{"run_id":"run_123","chain_id":"chain_ABC","routing_reason":"Legacy compatibility","base_revision":7,"patch_json":"{\"chain_id\":\"chain_ABC\",\"nodes\":[],\"edges\":[]}"}}}` + "\n",
			want:  []string{"evidence", "propose", "run_123", "--json", "--chain", "chain_ABC", "--patch-json", `{"chain_id":"chain_ABC","nodes":[],"edges":[]}`, "--base-revision", "7", "--routing-reason", "Legacy compatibility"},
		},
		{
			name:  "plan graph proposal",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_plan_evidence_graph_proposal","arguments":{"run_id":"run_123"}}}` + "\n",
			want:  []string{"evidence", "proposal-plan", "run_123", "--json"},
		},
		{
			name:  "review graph proposal",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"aexp_review_evidence_graph_proposal","arguments":{"run_id":"run_123","action":"accept","reviewer":"ziwu"}}}` + "\n",
			want:  []string{"evidence", "proposal-review", "run_123", "--json", "--action", "accept", "--reviewer", "ziwu"},
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

func TestCLIRejectsDirectEvidenceMutation(t *testing.T) {
	for _, args := range [][]string{
		{"evidence", "add-node", "chain_ABC", "--title", "Bypass"},
		{"evidence", "add-edge", "chain_ABC", "--from", "a", "--to", "b"},
		{"evidence", "save", "chain_ABC"},
	} {
		if err := validateCLIArgs(args); err == nil || !strings.Contains(err.Error(), "reviewed Evidence proposal") {
			t.Fatalf("validateCLIArgs(%#v) = %v, want proposal-only rejection", args, err)
		}
	}
	for _, args := range [][]string{
		{"evidence", "threads", "chain_ABC", "--json"},
		{"evidence", "proposal-plan", "proposal_ABC", "--json"},
		{"evidence", "audit", "chain_ABC", "--json"},
	} {
		if err := validateCLIArgs(args); err != nil {
			t.Fatalf("validateCLIArgs(%#v) = %v, want read/proposal command allowed", args, err)
		}
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
