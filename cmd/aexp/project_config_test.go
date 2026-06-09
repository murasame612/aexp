package main

import (
	"os"
	"path/filepath"
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
	if got := cfg.Sync.Excludes; len(got) != 1 || got[0] != "dataset/raw/" {
		t.Fatalf("unexpected sync excludes: %#v", got)
	}
}
