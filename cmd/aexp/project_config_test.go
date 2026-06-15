package main

import (
	"context"
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
project:
  id: dam-imputation
  name: Dam Imputation
  promotion_default: no_proposal
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
	if cfg.Project.ID != "dam-imputation" || cfg.Project.Name != "Dam Imputation" || cfg.Project.PromotionDefault != "no_proposal" {
		t.Fatalf("unexpected project config: %#v", cfg.Project)
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
		"project:",
		"promotion_default: no_proposal",
		"resource: mu",
		"cwd: /remote/project",
		"sync:",
		"events helper: " + filepath.Join(resolvedDir, "aexp_events.py"),
		"target: /remote/project",
		"setup:",
		"command: python -m pip install -r requirements.txt",
		"check-results:",
		"kind: smoke",
		"no_gpu: true",
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

func TestProjectInitDoesNotPromoteAmbiguousMainPyToFormalTrain(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('cli dispatcher')\n"), 0644); err != nil {
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
	for _, want := range []string{
		"check-results:",
		"kind: smoke",
		"no_gpu: true",
		"# train:",
		"#   command: TODO replace with the real training command",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("ambiguous init output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "command: python main.py") {
		t.Fatalf("ambiguous main.py should not become a formal train command:\n%s", text)
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
	helperPath := filepath.Join(dir, "aexp_events.py")
	helper, err := os.ReadFile(helperPath)
	if err != nil {
		t.Fatalf("expected aexp_events.py: %v", err)
	}
	for _, want := range []string{
		"def metric(name, value, **fields):",
		`os.environ.get("AEXP_UI_EVENTS", "")`,
	} {
		if !strings.Contains(string(helper), want) {
			t.Fatalf("events helper missing %q:\n%s", want, helper)
		}
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

func TestProjectInitCanSkipEventsHelper(t *testing.T) {
	dir := t.TempDir()
	withWorkingDir(t, dir, func() {
		if out, err := runProjectInitForTest("--resource", "mu", "--cwd", "/remote/project", "--no-events-helper"); err != nil {
			t.Fatalf("project init failed: %v\n%s", err, out)
		}
	})
	if _, err := os.Stat(filepath.Join(dir, ".aexp.yaml")); err != nil {
		t.Fatalf("expected .aexp.yaml: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aexp_events.py")); !os.IsNotExist(err) {
		t.Fatalf("expected no aexp_events.py, stat err=%v", err)
	}
}

func TestTopLevelInitProjectDryRun(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("torch\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var text string
	withWorkingDir(t, dir, func() {
		out, err := runInitForTest("--project", "--resource", "mu", "--cwd", "/remote/project", "--dry-run")
		if err != nil {
			t.Fatalf("top-level init --project failed: %v\n%s", err, out)
		}
		text = out
	})
	for _, want := range []string{
		"Database created at " + filepath.Join(home, ".aexp", "aexp.db"),
		"Project config: run 'aexp project init' or 'aexp init --project'",
		"Creating project config...",
		"resource: mu",
		"cwd: /remote/project",
		"command: python -m pip install -r requirements.txt",
		"check-results:",
		"aexp project run train --dry-run",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("init --project output missing %q:\n%s", want, text)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".aexp.yaml")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write .aexp.yaml, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aexp_events.py")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write aexp_events.py, stat err=%v", err)
	}
}

func TestProjectCardCommandsUseProjectConfig(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(dir, ".aexp.yaml")
	if err := os.WriteFile(configPath, []byte(`
project:
  id: dam-imputation
  name: Dam Imputation
resource: mu
cwd: /remote/project
train:
  command: python train.py
  kind: formal
`), 0644); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(home, ".aexp", "aexp.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateResource(context.Background(), &store.Resource{
		ID:      "rsrc_project_card",
		Name:    "mu",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(context.Background(), &store.Run{
		ID:         "run_project_card",
		ResourceID: "rsrc_project_card",
		Name:       "train",
		Status:     store.RunStatusSucceeded,
		Kind:       store.RunKindFormal,
		Command:    "python train.py",
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	out, err := runProjectCardForTest(
		"--config", configPath,
		"run_project_card",
		"--question", "Does CAF beat IR?",
		"--verdict", "CAF improves mAP50-95.",
		"--level", "B",
		"--metric", "mAP50-95=0.606",
		"--important",
	)
	if err != nil {
		t.Fatalf("project card failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Saved project card for run_project_card in dam-imputation") {
		t.Fatalf("unexpected card output:\n%s", out)
	}

	out, err = runProjectRunsForTest("--config", configPath, "--important")
	if err != nil {
		t.Fatalf("project runs failed: %v\n%s", err, out)
	}
	for _, want := range []string{"run_project_...", "B", "yes", "Does CAF beat IR?", "CAF improves mAP50-95."} {
		if !strings.Contains(out, want) {
			t.Fatalf("project runs output missing %q:\n%s", want, out)
		}
	}

	out, err = runProjectDigestForTest("--config", configPath)
	if err != nil {
		t.Fatalf("project digest failed: %v\n%s", err, out)
	}
	for _, want := range []string{"# aexp project digest: dam-imputation", "- question: Does CAF beat IR?", "- metrics: mAP50-95=0.606"} {
		if !strings.Contains(out, want) {
			t.Fatalf("project digest output missing %q:\n%s", want, out)
		}
	}
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
	if !report.Events.Ready || !strings.Contains(report.Events.Helper, "injects aexp_events.py") {
		t.Fatalf("expected event readiness guidance: %#v", report.Events)
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

func TestRunMarksAcceptsPositionalRunID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, ".aexp", "aexp.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMark(context.Background(), &store.RunMark{
		ID:     "mark_1",
		RunID:  "run_target",
		Actor:  "agent",
		Kind:   "key_result",
		Title:  "target finding",
		Reason: "visible",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMark(context.Background(), &store.RunMark{
		ID:     "mark_2",
		RunID:  "run_other",
		Actor:  "agent",
		Kind:   "key_result",
		Title:  "other finding",
		Reason: "hidden",
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	out, err := runMarksForTest("run_target")
	if err != nil {
		t.Fatalf("run marks positional failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "target finding") {
		t.Fatalf("expected target finding in output:\n%s", out)
	}
	if strings.Contains(out, "other finding") {
		t.Fatalf("positional run id should filter other marks:\n%s", out)
	}

	out, err = runMarksForTest("run_target", "--run", "run_other")
	if err == nil {
		t.Fatalf("expected conflicting run ids to fail, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "specified twice") {
		t.Fatalf("expected conflict error, got %v out=%s", err, out)
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

func TestDetectResourceEnvFixSuggestsUpdateCommand(t *testing.T) {
	fix := detectResourceEnvFix(func(command string) (string, string, error) {
		return "remote_path|/opt/conda/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\nconda_base|/opt/conda\nconda_init|/opt/conda/etc/profile.d/conda.sh\nconda_env|base\n", "", nil
	}, "szu")
	for _, want := range []string{
		"aexp resource update 'szu'",
		"--remote-path '/opt/conda/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'",
		"--conda-base '/opt/conda'",
		"--conda-init '/opt/conda/etc/profile.d/conda.sh'",
		"--conda-env 'base'",
	} {
		if !strings.Contains(fix, want) {
			t.Fatalf("fix missing %q:\n%s", want, fix)
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
ui_events: .aexp/events/custom.jsonl
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
		"--ui-events",
		".aexp/events/custom.jsonl",
		"'aexp' 'run' 'submit'",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestProjectRunDryRunShowsDefaultUIEvents(t *testing.T) {
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

	out, err := runProjectRunForTest("--config", configPath, "train", "--dry-run")
	if err != nil {
		t.Fatalf("project run dry-run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ui_events: .aexp/events/<run_id>.jsonl (default)") {
		t.Fatalf("dry-run output missing default ui_events:\n%s", out)
	}
}

func TestProjectConfigWarnsUnknownRecipeKeys(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".aexp.yaml")
	if err := os.WriteFile(configPath, []byte(`
resource: mu
cwd: /remote/project
train:
  command: python train.py
  kind: formal
  metric_paths:
    - results.csv
  strange_list:
    - ignored.csv
  mystery: value
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadProjectFileConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Commands["train"].Metrics; len(got) != 1 || got[0] != "results.csv" {
		t.Fatalf("metric_paths alias not parsed: %#v", got)
	}
	for _, want := range []string{
		"unknown recipe list ignored: train.strange_list",
		"unknown recipe field ignored: train.mystery",
	} {
		if !containsString(cfg.Warnings, want) {
			t.Fatalf("warning missing %q: %#v", want, cfg.Warnings)
		}
	}
}

func TestResolveSyncExcludesUsesProfileIgnoreAndFlags(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".aexpignore"), []byte(`
# local scratch
dataset/raw/

*.tmp
`), 0644); err != nil {
		t.Fatal(err)
	}

	excludes, sources, err := resolveSyncExcludes(dir, "code", false, []string{"local-only/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		".git/",
		"node_modules/",
		".conda/",
		".venv/",
		".pytest_cache/",
		"__pycache__/",
		"dataset/",
		"outputs/",
		"outputs_remote/",
		"runs/",
		"runs/detect/",
		"*.csv",
		"*.parquet",
		"dataset/raw/",
		"*.tmp",
		"local-only/",
	} {
		if !containsString(excludes, want) {
			t.Fatalf("excludes missing %q: %#v", want, excludes)
		}
	}
	for _, want := range []string{"profile:code", filepath.Join(dir, ".aexpignore"), "flags"} {
		if !containsString(sources, want) {
			t.Fatalf("sources missing %q: %#v", want, sources)
		}
	}
}

func TestResolveSyncExcludesCodeDataKeepsDataDirs(t *testing.T) {
	excludes, _, err := resolveSyncExcludes(t.TempDir(), "code-data", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"dataset/", "outputs/", "runs/", "*.zip"} {
		if containsString(excludes, unwanted) {
			t.Fatalf("code-data should not exclude %q by default: %#v", unwanted, excludes)
		}
	}
	for _, want := range []string{".conda/", ".venv/", "node_modules/", ".git/"} {
		if !containsString(excludes, want) {
			t.Fatalf("code-data should still exclude env/cache dirs; missing %q in %#v", want, excludes)
		}
	}
}

func TestResolveSyncExcludesCodeProfileProtectsCommonLocalArtifacts(t *testing.T) {
	excludes, _, err := resolveSyncExcludes(t.TempDir(), "code", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		".conda/",
		"node_modules/",
		".git/",
		".codegraph/",
		"outputs/",
		"outputs_remote/",
		"dataset/",
		"runs/",
		"mlruns/",
		"lightning_logs/",
		"*.csv",
		"*.parquet",
		"*.npz",
		"*.safetensors",
	} {
		if !containsString(excludes, want) {
			t.Fatalf("code profile should exclude %q by default: %#v", want, excludes)
		}
	}
}

func TestResolveSyncExcludesNoDefaultOnlyKeepsExplicit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".aexpignore"), []byte("dataset/raw/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	excludes, sources, err := resolveSyncExcludes(dir, "code", true, []string{"keep-this-out/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(excludes) != 1 || excludes[0] != "keep-this-out/" {
		t.Fatalf("unexpected excludes with no defaults: %#v", excludes)
	}
	if len(sources) != 1 || sources[0] != "flags" {
		t.Fatalf("unexpected sources with no defaults: %#v", sources)
	}
}

func TestResolveSyncExcludesRejectsUnknownProfile(t *testing.T) {
	if _, _, err := resolveSyncExcludes(t.TempDir(), "mystery", false, nil); err == nil || !strings.Contains(err.Error(), "unknown sync profile") {
		t.Fatalf("expected unknown profile error, got %v", err)
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

func runInitForTest(args ...string) (string, error) {
	cmd := initCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	return captureStdout(func() error {
		return cmd.Execute()
	})
}

func runMarksForTest(args ...string) (string, error) {
	cmd := runMarksCmd()
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

func runProjectCardForTest(args ...string) (string, error) {
	cmd := projectCardCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	return captureStdout(func() error {
		return cmd.Execute()
	})
}

func runProjectRunsForTest(args ...string) (string, error) {
	cmd := projectRunsCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	return captureStdout(func() error {
		return cmd.Execute()
	})
}

func runProjectDigestForTest(args ...string) (string, error) {
	cmd := projectDigestCmd()
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
