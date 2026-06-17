package main

import (
	"testing"

	"github.com/ziwu/aexp/internal/executor"
)

func TestDeriveRunEventSummary(t *testing.T) {
	lines := []executor.LogLine{
		{LineNo: 1, Content: `{"type":"param","name":"model","value":"yolov8s"}`},
		{LineNo: 2, Content: `{"type":"progress","name":"epoch","current":7,"total":100}`},
		{LineNo: 3, Content: `{"type":"metric","trial":"7","split":"val","name":"mAP50-95(B)","value":0.48,"epoch":7}`},
		{LineNo: 4, Content: `{"type":"metric","trial":"7","split":"val","name":"mAP50-95(B)","value":0.55,"epoch":8}`},
		{LineNo: 5, Content: `not json`},
	}
	events := parseRunEventLines(lines)
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4", len(events))
	}
	summary := deriveRunEventSummary(events)
	if got := summary.Params["model"]; got != "yolov8s" {
		t.Fatalf("model param = %#v", got)
	}
	progress := summary.Progress["epoch"]
	if progress.Current != 7 || progress.Total == nil || *progress.Total != 100 {
		t.Fatalf("progress = %#v", progress)
	}
	metric := summary.Metrics["trial:7/val/mAP50-95(B)"]
	if metric.Value != 0.55 || metric.Epoch == nil || *metric.Epoch != 8 {
		t.Fatalf("metric = %#v", metric)
	}
}

func TestAnalyzeEventQualityFlagsSuspiciousEvents(t *testing.T) {
	lines := []executor.LogLine{
		{LineNo: 1, Content: `{"type":"metric","name":"batch_size","value":16,"epoch":1}`},
		{LineNo: 2, Content: `{"type":"metric","name":"batch_size","value":16,"epoch":2}`},
		{LineNo: 3, Content: `{"type":"metric","name":"batch_size","value":16,"epoch":3}`},
		{LineNo: 4, Content: `{"type":"metric","name":"train/loss","value":0.5,"epoch":1,"series":"variant-a"}`},
		{LineNo: 5, Content: `{"type":"metric","name":"train/loss","value":0.4,"epoch":2,"series":"variant-a"}`},
		{LineNo: 6, Content: `{"type":"metric","name":"train/loss","value":0.6,"epoch":21,"series":"variant-b"}`},
		{LineNo: 7, Content: `{"type":"metric","name":"train/loss","value":0.5,"epoch":22,"series":"variant-b"}`},
		{LineNo: 8, Content: `{"type":"metric","name":"WaveletITransformer_dam_2h_raw_linear_mask_inputs_sl96_pl48/train_loss","value":0.3,"epoch":1}`},
	}
	snapshot := eventSnapshotFromLines("run_test", ".aexp/events/run_test.jsonl", "cache", lines, false)
	report := analyzeEventQuality(snapshot)
	for _, kind := range []string{"constant_as_metric", "missing_trial", "loss_axis_offset", "long_metric_name"} {
		if report.Summary[kind] == 0 {
			t.Fatalf("expected %s issue in %#v", kind, report.Issues)
		}
	}
}
