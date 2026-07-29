package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziwu/aexp/internal/executor"
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
target_env: train-env
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
  target_env: defect-yolo
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
	if cfg.Resource != "mu" || cfg.Cwd != "/home/ziwu/project" || cfg.Env != "auto" || cfg.CondaEnv != "defect-yolo" || cfg.TargetEnv != "train-env" {
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
	if got := cfg.Commands["setup"].TargetEnv; got != "defect-yolo" {
		t.Fatalf("unexpected setup target env: %q", got)
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

func TestLoadProjectFileConfigResourceProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".aexp.yaml")
	if err := os.WriteFile(path, []byte(`
resource: szu
cwd: /shared/project
env: auto
conda_env: shared-conda
default_gpu: 0
resource_profiles:
  mu:
    resource: mu
    cwd: /home/murasame/pythonproject/dam-displacement-imputation
    project_env: auto
    conda_env: dam-mu
    target_env: dam-runtime
    default_gpu: 1
    ui_events: .aexp/events/mu.jsonl
    env_vars:
      DATA_ROOT: /home/murasame/data/dam
      OUTPUT_ROOT: /home/murasame/outputs/dam
  szu:
    cwd: /workspace/dam
    conda_env: dam-szu
train:
  command: python train.py
  kind: formal
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadProjectFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	mu, ok := cfg.ResourceProfiles["mu"]
	if !ok {
		t.Fatalf("missing mu profile: %#v", cfg.ResourceProfiles)
	}
	if mu.Resource != "mu" || mu.Cwd != "/home/murasame/pythonproject/dam-displacement-imputation" || mu.CondaEnv != "dam-mu" || mu.TargetEnv != "dam-runtime" || mu.Env != "auto" {
		t.Fatalf("unexpected mu profile: %#v", mu)
	}
	if mu.DefaultGPU == nil || *mu.DefaultGPU != 1 {
		t.Fatalf("unexpected mu gpu: %#v", mu.DefaultGPU)
	}
	if mu.EnvVars["DATA_ROOT"] != "/home/murasame/data/dam" || mu.EnvVars["OUTPUT_ROOT"] != "/home/murasame/outputs/dam" {
		t.Fatalf("unexpected mu env vars: %#v", mu.EnvVars)
	}
	szu, ok := cfg.ResourceProfiles["szu"]
	if !ok || szu.Cwd != "/workspace/dam" || szu.CondaEnv != "dam-szu" {
		t.Fatalf("unexpected szu profile: %#v", szu)
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
		"metric(\"train/loss\", loss, epoch=epoch, trial=trial_id, variant=\"sl192_pl96\")",
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
	otherConfigPath := filepath.Join(dir, "other.aexp.yaml")
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
	if err := os.WriteFile(otherConfigPath, []byte(`
project:
  id: other-project
  name: Other Project
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
	for _, project := range []*store.ProjectDefinition{
		{ID: "dam-imputation", Name: "Dam Imputation"},
		{ID: "other-project", Name: "Other Project"},
	} {
		if err := db.CreateProjectDefinition(context.Background(), project); err != nil {
			t.Fatal(err)
		}
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

	if _, err := runProjectCardForTest("--config", otherConfigPath, "run_project_card", "--verdict", "Updated from another cwd."); err != nil {
		t.Fatalf("cross-project ordinary update failed: %v", err)
	}
	db, err = store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	card, err := db.GetProjectRunCard(context.Background(), "run_project_card")
	if err != nil {
		t.Fatal(err)
	}
	if card.ProjectID != "dam-imputation" {
		t.Fatalf("ordinary update drifted card ownership: %#v", card)
	}
	db.Close()

	if _, err := runProjectCardForTest("--config", otherConfigPath, "run_project_card", "--reassign-project"); err != nil {
		t.Fatalf("explicit reassign failed: %v", err)
	}
	db, err = store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	card, err = db.GetProjectRunCard(context.Background(), "run_project_card")
	if err != nil {
		t.Fatal(err)
	}
	if card.ProjectID != "other-project" {
		t.Fatalf("explicit reassign did not change ownership: %#v", card)
	}
	db.Close()

	out, err = runProjectRunsForTest("--config", otherConfigPath, "--important")
	if err != nil {
		t.Fatalf("reassigned project runs failed: %v\n%s", err, out)
	}
	for _, want := range []string{"run_project_...", "B", "yes", "Does CAF beat IR?", "Updated from another cwd."} {
		if !strings.Contains(out, want) {
			t.Fatalf("reassigned project runs output missing %q:\n%s", want, out)
		}
	}

	out, err = runProjectDigestForTest("--config", otherConfigPath)
	if err != nil {
		t.Fatalf("reassigned project digest failed: %v\n%s", err, out)
	}
	for _, want := range []string{"# aexp project digest: other-project", "- question: Does CAF beat IR?", "- verdict: Updated from another cwd."} {
		if !strings.Contains(out, want) {
			t.Fatalf("reassigned project digest output missing %q:\n%s", want, out)
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
	ctx := context.Background()
	if err := db.CreateResource(ctx, &store.Resource{ID: "marks_resource", Name: "marks-resource", Type: store.ResourceTypeSSH, Host: "localhost", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run_target", "run_other"} {
		if err := db.CreateRun(ctx, &store.Run{ID: runID, ResourceID: "marks_resource", Status: store.RunStatusSucceeded, Command: "true"}); err != nil {
			t.Fatal(err)
		}
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

func TestRunMarkCopiesAttachmentsAndAddsMarkdownRefs(t *testing.T) {
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
	ctx := context.Background()
	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_attach",
		Name:    "local",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_attach",
		ResourceID: "rsrc_attach",
		Name:       "attach-run",
		Status:     store.RunStatusSucceeded,
		Command:    "python train.py",
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	source := filepath.Join(t.TempDir(), "plot.png")
	if err := os.WriteFile(source, []byte("fake-png"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := runMarkForTest(
		"run_attach",
		"--title", "Plot note",
		"--statement", "Plot explains the failure mode.",
		"--body-md", "## Result\n\nSee attached plot.",
		"--attach", source+"|Prediction plot",
	)
	if err != nil {
		t.Fatalf("run mark failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "aexp-attachment://att_") {
		t.Fatalf("expected attachment URI in output:\n%s", out)
	}

	db, err = store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	marks, err := db.ListRunMarks(ctx, store.RunMarkFilter{RunID: "run_attach", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 1 {
		t.Fatalf("marks = %#v, want one", marks)
	}
	mark := marks[0]
	if mark.Statement != "Plot explains the failure mode." {
		t.Fatalf("statement = %q", mark.Statement)
	}
	if len(mark.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want one", mark.Attachments)
	}
	attachment := mark.Attachments[0]
	if attachment.Caption != "Prediction plot" || attachment.Mime != "image/png" {
		t.Fatalf("attachment metadata = %#v", attachment)
	}
	if !strings.Contains(mark.BodyMD, "![Prediction plot](aexp-attachment://"+attachment.ID+")") {
		t.Fatalf("body md missing attachment ref:\n%s", mark.BodyMD)
	}
	data, err := os.ReadFile(attachment.LocalPath)
	if err != nil {
		t.Fatalf("copied attachment not readable: %v", err)
	}
	if string(data) != "fake-png" {
		t.Fatalf("copied attachment = %q", string(data))
	}
	if !strings.HasPrefix(attachment.LocalPath, filepath.Join(home, ".aexp", "attachments", "run_marks")) {
		t.Fatalf("attachment path = %q, want under temporary HOME", attachment.LocalPath)
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

func TestProjectRunDryRunUsesResourceProfile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".aexp.yaml")
	if err := os.WriteFile(configPath, []byte(`
resource: szu
cwd: /workspace/wrong-for-mu
env: raw
conda_env: shared-conda
default_gpu: 0
resource_profiles:
  mu:
    cwd: /home/murasame/pythonproject/dam-displacement-imputation
    project_env: auto
    conda_env: dam-mu
    default_gpu: 1
    env_vars:
      DATA_ROOT: /home/murasame/data/dam
      OUTPUT_ROOT: /home/murasame/outputs/dam
train:
  command: python train.py
  kind: formal
`), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := runProjectRunForTest("--config", configPath, "--resource", "mu", "train", "--dry-run")
	if err != nil {
		t.Fatalf("project run dry-run failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"resource: mu",
		"cwd:      /home/murasame/pythonproject/dam-displacement-imputation",
		"env:      auto",
		"conda:    dam-mu",
		"gpu:      1",
		"env_vars:",
		"  AEXP_RESOURCE_PROFILE=mu",
		"  DATA_ROOT=/home/murasame/data/dam",
		"  OUTPUT_ROOT=/home/murasame/outputs/dam",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("resource profile dry-run output missing %q:\n%s", want, out)
		}
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

func TestLoadProjectFileConfigParsesCommandInputsAndFreezeProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".aexp.yaml")
	content := `project:
  id: paper-project
commands:
  train:
    command: python train.py
    kind: formal
    inputs:
      datasets:
        - facade@v3
      seeds:
        - 41
        - 42
freeze_profiles:
  paper:
    storage: nas
    storage_prefix: paper-evidence/project
    required:
      metrics:
        - results/**/per_seed.json
      predictions:
        - predictions/**/*.csv
    optional:
      masks:
        - masks/**/*
    workspace_roles:
      - metrics
      - predictions
    aggregate:
      command: python scripts/paper/build.py
      outputs:
        - derived/tables/**/*.csv
    release_gate:
      command: python scripts/paper/gate.py
      report: release-gate.json
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := cfg.Commands["train"]
	if entry.Command != "python train.py" || len(entry.Datasets) != 1 || len(entry.Seeds) != 2 {
		t.Fatalf("command inputs: %#v", entry)
	}
	for _, warning := range cfg.Warnings {
		if strings.Contains(warning, "train.datasets") || strings.Contains(warning, "train.seeds") {
			t.Fatalf("structured provenance produced a false ignored-field warning: %q", warning)
		}
	}
	profile := cfg.FreezeProfiles["paper"]
	if profile.Storage != "nas" || len(profile.Rules) != 3 || len(profile.WorkspaceRoles) != 2 || len(profile.AggregateOutputs) != 1 || profile.GateCommand == "" {
		t.Fatalf("freeze profile: %#v", profile)
	}
}

func TestProjectRunPlanIncludesReplayableProvenanceFlags(t *testing.T) {
	req := executor.SubmitRequest{
		Kind:                store.RunKindFormal,
		Cwd:                 "/remote/project",
		ProjectEnv:          executor.ProjectEnvAuto,
		GPUIndex:            0,
		ProjectConfigSHA256: "sha256:config",
		Datasets: []store.RunDatasetInput{{
			DatasetID:      "private-facade-good810-context2x",
			Version:        "v1",
			ManifestSHA256: "sha256:dataset",
		}},
		Seeds:              []int64{41, 42},
		SplitProtocol:      "good810-group-aware-v1",
		EvaluationProtocol: "locked-test-v1",
		Inputs: []store.RunInputBinding{{
			LogicalURI: "storage://nas/datasets/good810/pair-gt.ndjson",
			TargetPath: "dataset/pair-gt.ndjson",
			Revision:   "sha256:input",
			Mode:       "copy",
		}},
		Outputs: []store.RunOutputBinding{{
			SourcePattern: "output/aexp/{run_id}/report.json",
			LogicalURI:    "storage://nas/runs/{run_id}/report.json",
			Role:          "metrics",
			Required:      true,
		}},
		Program: "bash",
		Args:    []string{"-lc", "python train.py"},
	}
	out, err := captureStdout(func() error {
		printProjectRunPlan("/project/.aexp.yaml", "train", "gpu", req)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--dataset' 'private-facade-good810-context2x@v1",
		"--seed' '41",
		"--seed' '42",
		"--config-sha256' 'sha256:config",
		"--split-protocol' 'good810-group-aware-v1",
		"--evaluation-protocol' 'locked-test-v1",
		"--input-json",
		`"revision":"sha256:input"`,
		"--output-json",
		`"required":true`,
		"datasets:",
		"seeds:",
		"managed_inputs:",
		"managed_outputs:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestLoadProjectFileConfigParsesStructuredRunBindings(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".aexp.yaml")
	content := `project:
  id: paper-project
commands:
  train:
    command: python train.py
    kind: formal
    split_protocol: split-v3
    evaluation_protocol: eval-v2
    inputs:
      datasets: [facade@v3]
      seeds: [41, 42]
      files:
        - from: aexp://paper-project/data/facade-v3
          to: data/facade
          revision: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
          mode: copy
    outputs:
      - from: results/per_seed.json
        to: aexp://paper-project/runs/{run_id}/per_seed.json
        role: metrics
        required: true
      - from: checkpoints/best.pt
        to: aexp://paper-project/runs/{run_id}/best.pt
        role: checkpoint
        required: false
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectFileConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	entry := cfg.Commands["train"]
	if len(entry.InputBindings) != 1 || entry.InputBindings[0].LogicalURI != "aexp://paper-project/data/facade-v3" || entry.InputBindings[0].TargetPath != "data/facade" || entry.InputBindings[0].Mode != "copy" {
		t.Fatalf("inputs=%#v", entry.InputBindings)
	}
	if len(entry.OutputBindings) != 2 || !entry.OutputBindings[0].Required || entry.OutputBindings[0].Role != "metrics" || entry.OutputBindings[1].Required {
		t.Fatalf("outputs=%#v", entry.OutputBindings)
	}
	if entry.SplitProtocol != "split-v3" || entry.EvaluationProtocol != "eval-v2" || len(entry.Seeds) != 2 || len(entry.Datasets) != 1 {
		t.Fatalf("entry=%#v", entry)
	}
	for _, warning := range cfg.Warnings {
		if strings.Contains(warning, ".from") || strings.Contains(warning, ".to") || strings.Contains(warning, ".files") {
			t.Fatalf("structured binding produced parser warning: %q", warning)
		}
	}
}

func TestParseRunBindingJSONUsesAgentFriendlyFields(t *testing.T) {
	input, err := parseRunInputJSON(`{"from":"aexp://project/data/raw","to":"data/raw","revision":"sha256:abc","mode":"copy"}`)
	if err != nil || input.LogicalURI != "aexp://project/data/raw" || input.TargetPath != "data/raw" || input.Revision != "sha256:abc" {
		t.Fatalf("input=%#v err=%v", input, err)
	}
	output, err := parseRunOutputJSON(`{"from":"results/**","to":"aexp://project/runs/{run_id}/results","role":"metrics","required":true}`)
	if err != nil || output.SourcePattern != "results/**" || output.LogicalURI == "" || output.Role != "metrics" || !output.Required {
		t.Fatalf("output=%#v err=%v", output, err)
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

func TestDatasetSyncExtraArgs(t *testing.T) {
	withVerify := datasetSyncExtraArgs(true)
	for _, want := range []string{"--partial", "--partial-dir=.aexp-rsync-partial", "--checksum"} {
		if !containsString(withVerify, want) {
			t.Fatalf("verified dataset sync args missing %q: %#v", want, withVerify)
		}
	}
	withoutVerify := datasetSyncExtraArgs(false)
	if containsString(withoutVerify, "--checksum") {
		t.Fatalf("no-verify dataset sync should not include checksum: %#v", withoutVerify)
	}
	if containsString(withVerify, "--info=progress2") || containsString(withoutVerify, "--info=progress2") {
		t.Fatalf("legacy dataset sync must remain compatible with macOS rsync 2.6.9: with=%#v without=%#v", withVerify, withoutVerify)
	}
}

func TestLocalDatasetStats(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte("1234"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "b.bin"), []byte("123456"), 0644); err != nil {
		t.Fatal(err)
	}
	stats, err := localDatasetStats(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 2 || stats.Bytes != 10 {
		t.Fatalf("dataset stats = %#v, want 2 files / 10 bytes", stats)
	}
	if _, err := localDatasetStats(filepath.Join(dir, "a.bin")); err == nil || !strings.Contains(err.Error(), "must be a directory") {
		t.Fatalf("expected file source rejection, got %v", err)
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

func runMarkForTest(args ...string) (string, error) {
	cmd := runMarkCmd()
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
