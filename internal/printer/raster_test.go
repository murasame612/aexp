package printer

import (
	"encoding/json"
	"image"
	"image/png"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

func TestBinaryDeviceKRemovesAntialiasingGray(t *testing.T) {
	bitmap := image.NewGray(image.Rect(0, 0, 4, 1))
	copy(bitmap.Pix, []byte{0, 127, 128, 255})
	got := binaryDeviceK(bitmap)
	want := []byte{255, 255, 0, 0}
	if string(got) != string(want) {
		t.Fatalf("DeviceK pixels = %v, want %v", got, want)
	}
}

func TestNativeRasterMediaMatchesExactPixelHeight(t *testing.T) {
	requireNativeCUPSRaster(t)
	_, document, err := renderReceiptCUPSRaster("AEXP RUN STARTED\n")
	if err != nil {
		t.Fatal(err)
	}
	heightText := strings.TrimSuffix(strings.TrimPrefix(document.Media, "Custom.72x"), "mm")
	heightMM, err := strconv.ParseFloat(heightText, 64)
	if err != nil {
		t.Fatalf("parse media %q: %v", document.Media, err)
	}
	want := float64(document.HeightDots) * 25.4 / printerDPI
	if math.Abs(heightMM-want) > 0.001 {
		t.Fatalf("media height %.3fmm does not match raster %.3fmm", heightMM, want)
	}
}

func TestTestReceiptHasDedicatedSemantics(t *testing.T) {
	_, receipt := BuildTestReceipt(time.Date(2026, 7, 24, 0, 33, 24, 0, time.Local))
	parsed := parseRasterReceipt(receipt)
	if parsed.Kind != "PRINTER" || parsed.Status != "TEST" || parsed.GPU != "CUPS" {
		t.Fatalf("test receipt parsed as an experiment: %#v", parsed)
	}
	if parsed.Warning != "NOT AN EXPERIMENT" {
		t.Fatalf("test warning = %q", parsed.Warning)
	}
	if path := strings.TrimSpace(os.Getenv("AEXP_PRINTER_TEST_PREVIEW")); path != "" {
		payload, _, err := renderReceiptPNG(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatalf("write test preview: %v", err)
		}
	}
}

func TestRenderReceiptPNGUsesCompactPOS80Layout(t *testing.T) {
	now := time.Date(2026, 7, 23, 22, 18, 40, 0, time.FixedZone("SGT", 8*60*60))
	datasets, _ := json.Marshal([]store.RunDatasetInput{{
		ID:        "dataset_clean_810",
		DatasetID: "facade-clean",
		Version:   "v3",
	}})
	seeds, _ := json.Marshal([]int64{41, 42, 43})
	run := &store.Run{
		ID:            "run_receipt_preview",
		Name:          "Clean-810 matched CAF ablation",
		Kind:          store.RunKindAblation,
		EvidenceGrade: store.RunEvidenceGradeFormal,
		Command:       "python train.py --dataset facade-clean@v3 --epochs 30 --batch-size 16 --lr 0.001 --seed 41 --semantic-gate --region-prior dino",
		DatasetsJSON:  string(datasets),
		SeedsJSON:     string(seeds),
		GPUIndex:      0,
	}
	_, receipt := BuildReceipt(run, "szumfy-rtx6000", store.PrintPhaseStart, store.PrinterRunEvent{
		RunID:     run.ID,
		Status:    store.RunStatusRunning,
		Kind:      store.RunKindAblation,
		StartedAt: &now,
	})
	parsed := parseRasterReceipt(receipt)
	for _, field := range parsed.Params {
		if strings.Trim(field.Key+field.Value, "- ") == "" {
			t.Fatalf("text divider leaked into raster fields: %#v", field)
		}
	}

	payload, document, err := renderReceiptPNG(receipt)
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != printerWidthDots {
		t.Fatalf("width = %d, want %d", config.Width, printerWidthDots)
	}
	if config.Height < 300 || config.Height > 1600 {
		t.Fatalf("receipt is not compact: height=%d", config.Height)
	}
	if document.Media == "Custom.72x80mm" || !strings.HasPrefix(document.Media, "Custom.72x") {
		t.Fatalf("media = %q", document.Media)
	}
	if path := strings.TrimSpace(os.Getenv("AEXP_PRINTER_PREVIEW")); path != "" {
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatalf("write preview: %v", err)
		}
	}
}

func TestStartReceiptBoundsPaperLength(t *testing.T) {
	args := make([]string, 0, 80)
	for index := range 20 {
		args = append(args, "--parameter-"+string(rune('a'+index)), strings.Repeat("value", 7))
	}
	run := &store.Run{ID: "run_long", Command: "python train.py " + strings.Join(args, " ")}
	_, receipt := BuildReceipt(run, "gpu", store.PrintPhaseStart, store.PrinterRunEvent{
		Status: store.RunStatusRunning,
		Kind:   store.RunKindPilot,
	})
	if !strings.Contains(receipt, "MORE - SEE RUN DETAIL") {
		t.Fatalf("parameter omission marker missing:\n%s", receipt)
	}
	if !strings.Contains(receipt, "[TRUNCATED - SEE RUN DETAIL]") {
		t.Fatalf("command truncation marker missing:\n%s", receipt)
	}
	_, document, err := renderReceiptPNG(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if document.HeightDots > 1800 {
		t.Fatalf("bounded receipt is still too tall: %d dots", document.HeightDots)
	}
}
