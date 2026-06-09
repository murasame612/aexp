package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziwu/aexp/internal/store"
)

func TestLoadProjectFileConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".aexp.yaml")
	if err := os.WriteFile(path, []byte(`
resource: mu
cwd: /home/ziwu/project
env: auto
conda_env: defect-yolo
default_gpu: 0
logs:
  - logs/**/*.log
metrics:
  - runs/**/*.csv
train:
  command: python train.py --epochs 10
  kind: formal
setup:
  command: python -m pip install -r requirements.txt
  kind: setup
eval:
  command: |
    mkdir -p logs
    python eval.py \
      --config configs/eval.yaml
quick: python quick.py
sync:
  source: ./
  target: /home/ziwu/project
  profile: code-data
  exclude:
    - dataset/raw/
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadProjectFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Resource != "mu" || cfg.Cwd != "/home/ziwu/project" || cfg.Env != "auto" || cfg.CondaEnv != "defect-yolo" {
		t.Fatalf("unexpected top-level config: %#v", cfg)
	}
	if cfg.DefaultGPU == nil || *cfg.DefaultGPU != 0 {
		t.Fatalf("unexpected default gpu: %#v", cfg.DefaultGPU)
	}
	if got := cfg.Commands["train"].Command; got != "python train.py --epochs 10" {
		t.Fatalf("unexpected train command: %q", got)
	}
	if got := cfg.Commands["setup"].Kind; got != store.RunKindSetup {
		t.Fatalf("unexpected setup kind: %q", got)
	}
	if got := cfg.Commands["eval"].Command; got != "mkdir -p logs\npython eval.py \\\n  --config configs/eval.yaml" {
		t.Fatalf("unexpected multiline command: %q", got)
	}
	if got := cfg.Commands["quick"].Command; got != "python quick.py" {
		t.Fatalf("unexpected shorthand command: %q", got)
	}
	if got := cfg.Sync.Excludes; len(got) != 1 || got[0] != "dataset/raw/" {
		t.Fatalf("unexpected sync excludes: %#v", got)
	}
}

func TestProjectInitDryRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "configs", "experiments"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("torch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "train_fusion.sh"), []byte("#!/usr/bin/env bash\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "experiments", "fusion.yaml"), []byte("epochs: 10\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var text string
	withWorkingDir(t, dir, func() {
		out, err := runProjectInitForTest("--resource", "mu", "--cwd", "/remote/project", "--dry-run")
		if err != nil {
			t.Fatalf("project init dry-run failed: %v\n%s", err, out)
		}
		text = out
	})
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"target: " + filepath.Join(resolvedDir, ".aexp.yaml"),
		"resource: mu",
		"cwd: /remote/project",
		"sync:",
		"target: /remote/project",
		"setup:",
		"command: python -m pip install -r requirements.txt",
		"train:",
		"command: bash scripts/train_fusion.sh configs/experiments/fusion.yaml",
		"aexp event metric train/loss 0.23 --epoch 1",
		"aexp project doctor",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, text)
		}
	}
}

func TestProjectInitWritesAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	withWorkingDir(t, dir, func() {
		if out, err := runProjectInitForTest("--resource", "mu", "--cwd", "/remote/project"); err != nil {
			t.Fatalf("project init failed: %v\n%s", err, out)
		}
	})
	if _, err := os.Stat(filepath.Join(dir, ".aexp.yaml")); err != nil {
		t.Fatalf("expected .aexp.yaml: %v", err)
	}
	withWorkingDir(t, dir, func() {
		out, err := runProjectInitForTest("--resource", "mu", "--cwd", "/remote/project")
		if err == nil {
			t.Fatalf("expected overwrite refusal, got success:\n%s", out)
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("expected overwrite warning, got err=%v out=%s", err, out)
		}
	})
}

func TestProjectDoctorConfigRecommendations(t *testing.T) {
	cfg := &projectFileConfig{
		Path:     filepath.Join(t.TempDir(), ".aexp.yaml"),
		Resource: "mu",
		Cwd:      "/remote/project",
		Commands: map[string]projectFileCommand{
			"setup": {Command: "python -m pip install -r requirements.txt", Kind: store.RunKindSetup},
			"train": {Command: "python train.py", Kind: store.RunKindFormal},
		},
		Sync: projectFileSync{
			Source:  "./",
			Target:  "/remote/project",
			Profile: "code",
		},
	}
	report := doctorReport{
		Checks: []doctorCheck{
			{Name: "cwd exists", OK: true, Severity: "ok"},
		},
	}

	applyProjectDoctorConfigRecommendations(&report, cfg, "")

	if report.ProjectConfig != cfg.Path {
		t.Fatalf("project config = %q, want %q", report.ProjectConfig, cfg.Path)
	}
	if report.RecommendedSubmitCommand != "aexp project run 'train'" {
		t.Fatalf("recommended submit = %q", report.RecommendedSubmitCommand)
	}
	for _, want := range []string{
		"aexp project sync --dry-run",
		"aexp project run setup --dry-run",
		"aexp project run 'train' --dry-run",
		"aexp project run 'train'",
	} {
		if !containsString(report.Recommended, want) {
			t.Fatalf("recommended missing %q: %#v", want, report.Recommended)
		}
	}
	var setup, train doctorRecipe
	for _, recipe := range report.Recipes {
		switch recipe.Name {
		case "setup":
			setup = recipe
		case "train":
			train = recipe
		}
	}
	if setup.Kind != store.RunKindSetup || setup.Evidence != "tooling only, not experiment evidence" {
		t.Fatalf("unexpected setup recipe: %#v", setup)
	}
	if train.Kind != store.RunKindFormal || !train.Selected || train.Evidence != "experiment evidence" {
		t.Fatalf("unexpected train recipe: %#v", train)
	}
}

func TestProjectDoctorConfigFixesMissingRemoteCWD(t *testing.T) {
	cfg := &projectFileConfig{
		Path:     filepath.Join(t.TempDir(), ".aexp.yaml"),
		Resource: "mu",
		Cwd:      "/remote/project",
		Commands: map[string]projectFileCommand{
			"train": {Command: "python train.py", Kind: store.RunKindFormal},
		},
		Sync: projectFileSync{Target: "/remote/project"},
	}
	report := doctorReport{
		Checks: []doctorCheck{
			{Name: "cwd exists", OK: false, Severity: "fail"},
		},
	}

	applyProjectDoctorConfigRecommendations(&report, cfg, "train")

	for _, want := range []string{
		"cwd missing on remote; use: aexp project sync --dry-run",
		"cwd missing on remote; then: aexp project sync",
	} {
		if !containsString(report.RecommendedFixes, want) {
			t.Fatalf("fixes missing %q: %#v", want, report.RecommendedFixes)
		}
	}
}

func TestProjectRunDryRunShowsRefreshEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".aexp.yaml")
	if err := os.WriteFile(configPath, []byte(`
resource: mu
cwd: /remote/project
env: auto
train:
  command: python train.py
  kind: formal
`), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := runProjectRunForTest("--config", configPath, "train", "--dry-run", "--refresh-env")
	if err != nil {
		t.Fatalf("project run dry-run failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"expanded submit command:",
		"--refresh-env",
		"'aexp' 'run' 'submit'",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

func runProjectInitForTest(args ...string) (string, error) {
	cmd := projectInitCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	return captureStdout(func() error {
		return cmd.Execute()
	})
}

func runProjectRunForTest(args ...string) (string, error) {
	cmd := projectRunCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	return captureStdout(func() error {
		return cmd.Execute()
	})
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(old); err != nil {
			t.Fatal(err)
		}
	}()
	fn()
}

func captureStdout(fn func() error) (string, error) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w
	runErr := fn()
	closeErr := w.Close()
	os.Stdout = old
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		return "", readErr
	}
	if closeErr != nil && runErr == nil {
		runErr = closeErr
	}
	return string(out), runErr
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
