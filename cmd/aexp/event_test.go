package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEventMetricCommandWritesJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events", "run.jsonl")
	if err := runEventCommandForTest("metric", "train/loss", "0.23", "--epoch", "3", "--trial", "7", "--seed", "2021", "--field", "split=train", "--path", path); err != nil {
		t.Fatal(err)
	}
	event := readSingleEvent(t, path)
	if event["type"] != "metric" || event["name"] != "train/loss" || event["split"] != "train" {
		t.Fatalf("unexpected event identity: %#v", event)
	}
	if event["trial"] != "7" || event["seed"] != "2021" {
		t.Fatalf("unexpected sweep context: %#v", event)
	}
	if event["value"] != 0.23 || event["epoch"] != float64(3) {
		t.Fatalf("unexpected metric values: %#v", event)
	}
	if _, ok := event["time"].(float64); !ok {
		t.Fatalf("missing numeric time: %#v", event)
	}
}

func TestEventProgressCommandUsesEnvPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	t.Setenv("AEXP_UI_EVENTS", path)
	if err := runEventCommandForTest("progress", "epoch", "30", "--total", "100", "--series", "iTransformer/raw", "--stage", "train", "--label", "raw input epoch"); err != nil {
		t.Fatal(err)
	}
	event := readSingleEvent(t, path)
	if event["type"] != "progress" || event["name"] != "epoch" {
		t.Fatalf("unexpected progress event: %#v", event)
	}
	if event["series"] != "iTransformer/raw" || event["stage"] != "train" || event["label"] != "raw input epoch" {
		t.Fatalf("unexpected progress context: %#v", event)
	}
	if event["current"] != float64(30) || event["total"] != float64(100) {
		t.Fatalf("unexpected progress values: %#v", event)
	}
}

func TestEmitStructuredEventStrictRequiresPath(t *testing.T) {
	t.Setenv("AEXP_UI_EVENTS", "")
	err := emitStructuredEvent(eventOptions{strict: true}, map[string]interface{}{"type": "note", "text": "hello"})
	if err == nil {
		t.Fatal("expected strict mode to require an event path")
	}
}

func runEventCommandForTest(args ...string) error {
	cmd := eventCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	return cmd.Execute()
}

func readSingleEvent(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("decode event: %v\n%s", err, data)
	}
	return event
}
