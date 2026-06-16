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
