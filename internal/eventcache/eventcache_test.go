package eventcache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadMergeAndTail(t *testing.T) {
	t.Setenv(envCacheDir, t.TempDir())

	if _, err := Write("run_cache_1", []Line{
		{LineNo: 1, Content: `{"type":"metric","value":1}`},
		{LineNo: 2, Content: `{"type":"metric","value":2}`},
	}); err != nil {
		t.Fatalf("Write initial: %v", err)
	}
	if _, err := Write("run_cache_1", []Line{
		{LineNo: 2, Content: `{"type":"metric","value":20}`},
		{LineNo: 3, Content: `{"type":"metric","value":30}`},
	}); err != nil {
		t.Fatalf("Write merge: %v", err)
	}

	lines, path, err := Read("run_cache_1", 2)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if filepath.Base(path) != "run_cache_1.jsonl" {
		t.Fatalf("cache path = %q", path)
	}
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
	if lines[0].LineNo != 2 || lines[0].Content != `{"type":"metric","value":20}` {
		t.Fatalf("first tail line = %#v", lines[0])
	}
	if lines[1].LineNo != 3 || lines[1].Content != `{"type":"metric","value":30}` {
		t.Fatalf("second tail line = %#v", lines[1])
	}
}

func TestWriteReplacesWhenIncomingTailHasGap(t *testing.T) {
	t.Setenv(envCacheDir, t.TempDir())

	if _, err := Write("run_cache_gap", []Line{
		{LineNo: 1, Content: `old 1`},
		{LineNo: 2, Content: `old 2`},
	}); err != nil {
		t.Fatalf("Write initial: %v", err)
	}
	if _, err := Write("run_cache_gap", []Line{
		{LineNo: 10, Content: `tail 10`},
		{LineNo: 11, Content: `tail 11`},
	}); err != nil {
		t.Fatalf("Write tail gap: %v", err)
	}

	lines, _, err := Read("run_cache_gap", 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 2 || lines[0].Content != "tail 10" || lines[1].Content != "tail 11" {
		t.Fatalf("unexpected replacement lines: %#v", lines)
	}
}

func TestPathSanitizesRunID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envCacheDir, dir)

	path, err := Path("../run bad")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if got, want := filepath.Dir(path), dir; got != want {
		t.Fatalf("dir = %q, want %q", got, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Path should not create file, stat err = %v", err)
	}
}
