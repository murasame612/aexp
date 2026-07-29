package eventcache

import (
	"os"
	"testing"
)

func TestWriteSnapshotDropsPreviousGenerationTail(t *testing.T) {
	t.Setenv(envCacheDir, t.TempDir())
	if _, err := WriteSnapshot("run_truncated", []Line{
		{LineNo: 1, Content: "old-one"},
		{LineNo: 2, Content: "old-two"},
		{LineNo: 3, Content: "old-tail"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSnapshot("run_truncated", []Line{
		{LineNo: 1, Content: "new-one"},
		{LineNo: 2, Content: "new-two"},
	}); err != nil {
		t.Fatal(err)
	}
	lines, _, err := Read("run_truncated", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Content != "new-one" || lines[1].Content != "new-two" {
		t.Fatalf("cache retained an old generation: %#v", lines)
	}
}

func TestWriteSnapshotCanRepresentEmptyGeneration(t *testing.T) {
	t.Setenv(envCacheDir, t.TempDir())
	if _, err := WriteSnapshot("run_empty", []Line{{LineNo: 1, Content: "old"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSnapshot("run_empty", nil); err != nil {
		t.Fatal(err)
	}
	lines, _, err := Read("run_empty", 0)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("empty generation retained old lines: %#v", lines)
	}
}
