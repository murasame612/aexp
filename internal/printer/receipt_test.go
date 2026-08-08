package printer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ziwu/aexp/internal/store"
)

func TestSmokeReceiptCannotBeMistakenForAResult(t *testing.T) {
	now := time.Now()
	run := &store.Run{ID: "run_smoke", Name: "plumbing", Kind: store.RunKindSmoke, EvidenceGrade: store.RunEvidenceGradeSmoke, DataFinalizationState: "skipped"}
	_, receipt := BuildReceipt(run, "gpu", store.PrintPhaseEnd, store.PrinterRunEvent{RunID: run.ID, Status: store.RunStatusSucceeded, Kind: store.RunKindSmoke, StartedAt: &now, FinishedAt: &now})
	if !strings.Contains(receipt, "PLUMBING ONLY - NOT A RESULT") {
		t.Fatalf("smoke warning missing:\n%s", receipt)
	}
}

func TestStartReceiptPrintsCommandAndHyperparameters(t *testing.T) {
	now := time.Now()
	datasets, _ := json.Marshal([]store.RunDatasetInput{{ID: "dataset_opaque", DatasetID: "dam-data", Version: "v3"}})
	seeds, _ := json.Marshal([]int64{41, 42, 43})
	run := &store.Run{
		ID:            "run_formal",
		Name:          "ablation",
		Kind:          store.RunKindAblation,
		EvidenceGrade: store.RunEvidenceGradeFormal,
		Command:       "EPOCHS=30 python train.py --batch-size 16 --lr=0.001 --no-revin",
		DatasetsJSON:  string(datasets),
		SeedsJSON:     string(seeds),
	}
	_, receipt := BuildReceipt(run, "gpu", store.PrintPhaseStart, store.PrinterRunEvent{Status: store.RunStatusRunning, Kind: store.RunKindAblation, StartedAt: &now})
	for _, expected := range []string{
		"HYPERPARAMETERS",
		"HYPERPARAM     VALUE",
		"epochs         30",
		"batch_size     16",
		"lr             0.001",
		"no_revin       true",
		"seeds          41,42,43",
		"datasets       dam-data@v3",
		"COMMAND",
		"python train.py",
	} {
		if !strings.Contains(receipt, expected) {
			t.Fatalf("start receipt missing %q:\n%s", expected, receipt)
		}
	}
	for _, line := range strings.Split(receipt, "\n") {
		if utf8.RuneCountInString(line) > receiptWidth {
			t.Fatalf("receipt line exceeds %d columns: %q", receiptWidth, line)
		}
	}
}

func TestStartReceiptRedactsSensitiveValues(t *testing.T) {
	run := &store.Run{
		ID:      "run_secret",
		Command: "PASSWORD=hunter2 python train.py --api-token top-secret --epochs=3",
	}
	_, receipt := BuildReceipt(run, "gpu", store.PrintPhaseStart, store.PrinterRunEvent{Status: store.RunStatusRunning, Kind: store.RunKindFormal})
	if strings.Contains(receipt, "hunter2") || strings.Contains(receipt, "top-secret") {
		t.Fatalf("secret leaked into receipt:\n%s", receipt)
	}
	if !strings.Contains(receipt, "<redacted>") {
		t.Fatalf("receipt should show that a value was redacted:\n%s", receipt)
	}
}

func TestEndReceiptDoesNotRepeatCommand(t *testing.T) {
	run := &store.Run{ID: "run_end", Command: "python train.py --epochs 3"}
	_, receipt := BuildReceipt(run, "gpu", store.PrintPhaseEnd, store.PrinterRunEvent{Status: store.RunStatusSucceeded, Kind: store.RunKindFormal})
	if strings.Contains(receipt, "COMMAND") || strings.Contains(receipt, "HYPERPARAMETERS") {
		t.Fatalf("end receipt repeated start-only detail:\n%s", receipt)
	}
}

func TestStartReceiptKeepsCommandSeedWhenProvenanceIsMissing(t *testing.T) {
	run := &store.Run{ID: "run_legacy", Command: "python train.py --seed 7"}
	_, receipt := BuildReceipt(run, "gpu", store.PrintPhaseStart, store.PrinterRunEvent{Status: store.RunStatusRunning, Kind: store.RunKindPilot})
	if !strings.Contains(receipt, "seed           7") {
		t.Fatalf("legacy command seed disappeared:\n%s", receipt)
	}
}

func TestReceiptRemovesControlCharacters(t *testing.T) {
	run := &store.Run{ID: "run_bad\x1b[2J", Name: "bad\x00name", EvidenceGrade: "formal"}
	_, receipt := BuildReceipt(run, "gpu", store.PrintPhaseEnd, store.PrinterRunEvent{Status: store.RunStatusFailed, Kind: store.RunKindFormal})
	if strings.ContainsRune(receipt, '\x1b') || strings.ContainsRune(receipt, '\x00') {
		t.Fatalf("control character survived: %q", receipt)
	}
}

func TestReceiptTimeIsNeverTruncated(t *testing.T) {
	now := time.Date(2026, 7, 24, 0, 33, 24, 0, time.FixedZone("SGT", 8*60*60))
	_, receipt := BuildTestReceipt(now)
	want := now.In(time.Local).Format("2006-01-02 15:04:05")
	if !strings.Contains(receipt, "time:      "+want+"\n") {
		t.Fatalf("full timestamp missing:\n%s", receipt)
	}
}
