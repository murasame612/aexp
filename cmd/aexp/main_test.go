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

func TestSimplifiedResearchCommandSurface(t *testing.T) {
	asset := assetCmd()
	for _, name := range []string{"publish", "get", "list"} {
		child, _, err := asset.Find([]string{name})
		if err != nil || child == nil || child.Name() != name {
			t.Fatalf("asset command %q missing: child=%v err=%v", name, child, err)
		}
	}

	project := projectCmd()
	for _, name := range []string{"list", "get"} {
		child, _, err := project.Find([]string{name})
		if err != nil || child == nil || child.Name() != name || child.Hidden {
			t.Fatalf("canonical project command %q missing or hidden: child=%v err=%v", name, child, err)
		}
	}
	for _, name := range []string{"card", "digest", "runs", "sync"} {
		child, _, err := project.Find([]string{name})
		if err != nil || child == nil || !child.Hidden {
			t.Fatalf("legacy project command %q must remain callable but hidden: child=%v err=%v", name, child, err)
		}
	}

	run := runCmd()
	for _, name := range []string{"mark", "marks"} {
		child, _, err := run.Find([]string{name})
		if err != nil || child == nil || !child.Hidden {
			t.Fatalf("legacy run command %q must remain callable but hidden: child=%v err=%v", name, child, err)
		}
	}

	evidence := evidenceCmd()
	for _, name := range []string{"create", "add-node", "add-edge", "list"} {
		child, _, err := evidence.Find([]string{name})
		if err != nil || child == nil || !child.Hidden {
			t.Fatalf("direct evidence command %q must remain callable but hidden: child=%v err=%v", name, child, err)
		}
	}
}

func TestAgentCardUsesProjectJournalForResearchReasoning(t *testing.T) {
	steps, rules := agentCardContent()
	content := strings.Join(append(append([]string{}, steps...), rules...), "\n")
	for _, want := range []string{
		"project journal list",
		"project journal create",
		"Do not create new Run marks or Project cards",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("agent card missing %q:\n%s", want, content)
		}
	}
	for _, forbidden := range []string{
		"Record project card:",
		"Review project memory: aexp project digest",
		"aexp run mark <run_id>",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("agent card still advertises legacy reasoning path %q:\n%s", forbidden, content)
		}
	}
}

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

func TestReleaseAssetNameAndURLs(t *testing.T) {
	asset, err := releaseAssetName("darwin", "arm64")
	if err != nil {
		t.Fatalf("releaseAssetName error: %v", err)
	}
	if asset != "aexp_darwin_arm64.tar.gz" {
		t.Fatalf("asset = %q", asset)
	}
	download, sums := releaseAssetURLs("murasame612/aexp", "v0.2.0", asset)
	if download != "https://github.com/murasame612/aexp/releases/download/v0.2.0/aexp_darwin_arm64.tar.gz" {
		t.Fatalf("download URL = %q", download)
	}
	if sums != "https://github.com/murasame612/aexp/releases/download/v0.2.0/checksums.txt" {
		t.Fatalf("checksums URL = %q", sums)
	}
	latest, _ := releaseAssetURLs("murasame612/aexp", "latest", asset)
	if latest != "https://github.com/murasame612/aexp/releases/latest/download/aexp_darwin_arm64.tar.gz" {
		t.Fatalf("latest URL = %q", latest)
	}
}

func TestChecksumForAsset(t *testing.T) {
	checksums := "abc123  aexp_linux_amd64.tar.gz\nfeedface  aexp_darwin_arm64.tar.gz\n"
	if got := checksumForAsset(checksums, "aexp_darwin_arm64.tar.gz"); got != "feedface" {
		t.Fatalf("checksum = %q", got)
	}
	if got := checksumForAsset(checksums, "missing.tar.gz"); got != "" {
		t.Fatalf("missing checksum = %q", got)
	}
}

func TestReplaceBinaryCreatesBackupAndAliasPaths(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "aexp")
	candidate := filepath.Join(dir, "candidate")
	if err := os.WriteFile(target, []byte("old"), 0755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.WriteFile(candidate, []byte("new"), 0755); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	backup, err := replaceBinary(target, candidate)
	if err != nil {
		t.Fatalf("replaceBinary error: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("target content = %q", got)
	}
	if backup == "" {
		t.Fatal("expected backup path")
	}
	old, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(old) != "old" {
		t.Fatalf("backup content = %q", old)
	}
	paths := uninstallBinaryPaths(target)
	if !reflect.DeepEqual(paths, []string{target, filepath.Join(dir, "aexp-event")}) {
		t.Fatalf("uninstall paths = %#v", paths)
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

func TestParseDatasetRefAndRelativePathRejectTraversal(t *testing.T) {
	name, version, err := parseDatasetRef("facade@v3")
	if err != nil || name != "facade" || version != "v3" {
		t.Fatalf("parseDatasetRef = %q %q err=%v", name, version, err)
	}
	for _, ref := range []string{"facade", "facade@", "../facade@v3", "facade@v3/bad"} {
		if _, _, err := parseDatasetRef(ref); err == nil {
			t.Fatalf("parseDatasetRef(%q) accepted", ref)
		}
	}
	for _, path := range []string{"/absolute", "../escape", "datasets/../../escape", ""} {
		if _, err := cleanRelativeDataPath(path); err == nil {
			t.Fatalf("cleanRelativeDataPath(%q) accepted", path)
		}
	}
	if got, err := cleanRelativeDataPath("datasets/facade/v3"); err != nil || got != "datasets/facade/v3" {
		t.Fatalf("clean path=%q err=%v", got, err)
	}
}

func TestRunDatasetInputFromVersionRequiresVerifiedState(t *testing.T) {
	dataset := &store.DatasetVersion{
		ID: "dataset_facade_v3", DatasetID: "facade", Version: "v3",
		ManifestSHA256: "sha256:manifest", State: store.DatasetStateRegistered,
	}
	if _, err := runDatasetInputFromVersion(dataset); err == nil || !strings.Contains(err.Error(), "dataset ingest") {
		t.Fatalf("registered dataset error = %v", err)
	}
	dataset.State = store.DatasetStateVerified
	input, err := runDatasetInputFromVersion(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if input.ID != dataset.ID || input.DatasetID != dataset.DatasetID || input.Version != dataset.Version || input.ManifestSHA256 != dataset.ManifestSHA256 {
		t.Fatalf("input = %#v", input)
	}
}

func TestCwdEscapesRootUsesRemotePOSIXSemantics(t *testing.T) {
	tests := []struct {
		root, cwd string
		want      bool
	}{
		{root: "/", cwd: "/home/ziwu/project", want: false},
		{root: "/home/ziwu", cwd: "project", want: false},
		{root: "/home/ziwu", cwd: "/home/ziwu/project", want: false},
		{root: "/home/ziwu", cwd: "/home/ziwu-other/project", want: true},
		{root: "/home/ziwu", cwd: "../escape", want: true},
	}
	for _, tt := range tests {
		if got := cwdEscapesRoot(tt.root, tt.cwd); got != tt.want {
			t.Errorf("cwdEscapesRoot(%q, %q) = %v, want %v", tt.root, tt.cwd, got, tt.want)
		}
	}
}
