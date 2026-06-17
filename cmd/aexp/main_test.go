package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/store"
)

func TestExecViaLocalAPISendsRequest(t *testing.T) {
	var got executor.ExecRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/exec" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(executor.ExecResult{
			Stdout:   "ok\n",
			ExitCode: 0,
		})
	}))
	defer srv.Close()

	result, usedAPI, err := execViaLocalAPI(t.Context(), srv.URL+"/api/v1", executor.ExecRequest{
		ResourceID: "res_1",
		Command:    "hostname",
		Actor:      "cli",
		TimeoutSec: 1,
	})
	if err != nil {
		t.Fatalf("execViaLocalAPI error: %v", err)
	}
	if !usedAPI {
		t.Fatal("expected API path to be used")
	}
	if result == nil || result.Stdout != "ok\n" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got.Actor != "cli" {
		t.Fatalf("actor = %q, want cli", got.Actor)
	}
}

func TestBuildMCPInstallPlanAll(t *testing.T) {
	plan, err := buildMCPInstallPlan(mcpInstallOptions{
		Target:      "all",
		Name:        "aexp",
		Binary:      "/usr/local/bin/aexp",
		APIURL:      "http://127.0.0.1:8080/api/v1",
		ClaudeScope: "user",
	}, false)
	if err != nil {
		t.Fatalf("buildMCPInstallPlan error: %v", err)
	}
	got := make([][]string, 0, len(plan))
	for _, step := range plan {
		got = append(got, append([]string{step.Program}, step.Args...))
	}
	want := [][]string{
		{"codex", "mcp", "remove", "aexp"},
		{"codex", "mcp", "add", "aexp", "--env", "AEXP_API_URL=http://127.0.0.1:8080/api/v1", "--", "/usr/local/bin/aexp", "mcp"},
		{"claude", "mcp", "remove", "aexp"},
		{"claude", "mcp", "add", "--scope", "user", "aexp", "-e", "AEXP_API_URL=http://127.0.0.1:8080/api/v1", "--", "/usr/local/bin/aexp", "mcp"},
		{"hermes", "mcp", "remove", "aexp"},
		{"hermes", "mcp", "add", "aexp", "--command", "/usr/bin/env", "--args", "AEXP_API_URL=http://127.0.0.1:8080/api/v1", "/usr/local/bin/aexp", "mcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %#v, want %#v", got, want)
	}
	if !plan[len(plan)-1].Optional {
		t.Fatal("implicit --target all should tolerate missing Hermes Agent")
	}
}

func TestBuildMCPInstallPlanHermes(t *testing.T) {
	plan, err := buildMCPInstallPlan(mcpInstallOptions{
		Target: "hermes-agent",
		Name:   "aexp",
		Binary: "/opt/aexp/bin/aexp",
		APIURL: "http://127.0.0.1:8080/api/v1",
	}, false)
	if err != nil {
		t.Fatalf("buildMCPInstallPlan error: %v", err)
	}
	got := make([][]string, 0, len(plan))
	for _, step := range plan {
		got = append(got, append([]string{step.Program}, step.Args...))
	}
	want := [][]string{
		{"hermes", "mcp", "remove", "aexp"},
		{"hermes", "mcp", "add", "aexp", "--command", "/usr/bin/env", "--args", "AEXP_API_URL=http://127.0.0.1:8080/api/v1", "/opt/aexp/bin/aexp", "mcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %#v, want %#v", got, want)
	}
	if plan[1].Optional {
		t.Fatal("explicit hermes target should fail loudly when hermes is unavailable")
	}
}

func TestBuildMCPUninstallPlanClaudeAlias(t *testing.T) {
	plan, err := buildMCPInstallPlan(mcpInstallOptions{Target: "cc", Name: "aexp"}, true)
	if err != nil {
		t.Fatalf("buildMCPInstallPlan error: %v", err)
	}
	got := append([]string{plan[0].Program}, plan[0].Args...)
	want := []string{"claude", "mcp", "remove", "aexp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %#v, want %#v", got, want)
	}
	if !plan[0].Optional {
		t.Fatal("uninstall should tolerate missing MCP config")
	}
}

func TestExecViaLocalAPIFallsBackOnUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	result, usedAPI, err := execViaLocalAPI(t.Context(), srv.URL+"/api/v1", executor.ExecRequest{
		ResourceID: "res_1",
		Command:    "hostname",
		TimeoutSec: 1,
	})
	if err != nil {
		t.Fatalf("execViaLocalAPI error: %v", err)
	}
	if usedAPI {
		t.Fatal("expected unauthorized local API to fall back")
	}
	if result != nil {
		t.Fatalf("expected nil result on fallback, got %#v", result)
	}
}

func TestExecViaLocalAPIFallsBackWhenUnavailable(t *testing.T) {
	result, usedAPI, err := execViaLocalAPI(t.Context(), "http://127.0.0.1:1/api/v1", executor.ExecRequest{
		ResourceID: "res_1",
		Command:    "hostname",
		TimeoutSec: 1,
	})
	if err != nil {
		t.Fatalf("execViaLocalAPI error: %v", err)
	}
	if usedAPI {
		t.Fatal("expected unreachable local API to fall back")
	}
	if result != nil {
		t.Fatalf("expected nil result on fallback, got %#v", result)
	}
}

func TestExecViaLocalAPIReturnsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "EXEC_FAILED",
			"details": "remote command failed",
		})
	}))
	defer srv.Close()

	result, usedAPI, err := execViaLocalAPI(t.Context(), srv.URL+"/api/v1", executor.ExecRequest{
		ResourceID: "res_1",
		Command:    "hostname",
		TimeoutSec: 1,
	})
	if err == nil || err.Error() != "local aexp API exec failed: remote command failed" {
		t.Fatalf("execViaLocalAPI error = %v", err)
	}
	if !usedAPI {
		t.Fatal("expected API business error to stay on API path")
	}
	if result != nil {
		t.Fatalf("expected nil result on API error, got %#v", result)
	}
}

func TestLocalSSHArgsIncludesProxyCommand(t *testing.T) {
	args := localSSHArgs(&store.Resource{
		Host:         "gpu.example",
		Port:         2222,
		User:         "root",
		AuthRef:      "/tmp/key",
		ProxyCommand: "nc -X 5 -x proxy:3000 %h %p",
	})
	got := strings.Join(args, "\x00")
	for _, want := range []string{
		"ssh",
		"-p\x002222",
		"-i\x00/tmp/key",
		"-o\x00ProxyCommand=nc -X 5 -x proxy:3000 %h %p",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ssh args missing %q in %#v", want, args)
		}
	}
}

func TestBuildTarCreateArgsForDirectory(t *testing.T) {
	dir := t.TempDir()
	args, err := buildTarCreateArgs(dir, []string{".venv/", "runs/detect/"})
	if err != nil {
		t.Fatalf("buildTarCreateArgs: %v", err)
	}
	want := []string{"-czf", "-", "--exclude", ".venv/", "--exclude", "runs/detect/", "-C", dir, "."}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("tar args = %#v, want %#v", args, want)
	}
}

func TestBuildTarCreateArgsForFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data.jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	args, err := buildTarCreateArgs(file, nil)
	if err != nil {
		t.Fatalf("buildTarCreateArgs: %v", err)
	}
	want := []string{"-czf", "-", "-C", dir, "data.jsonl"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("tar args = %#v, want %#v", args, want)
	}
}
