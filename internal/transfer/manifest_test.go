package transfer

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ziwu/aexp/internal/filespace"
)

func TestNormalizeSelectionIsStableAndAddsParentDirectories(t *testing.T) {
	digestA := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	one, revisionOne, bytesOne, filesOne, err := NormalizeSelection([]ManifestEntry{
		{Path: "metrics/b.json", Type: "file", SHA256: digestB, Size: 7},
		{Path: "metrics/a.json", Type: "file", SHA256: digestA, Size: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	two, revisionTwo, bytesTwo, filesTwo, err := NormalizeSelection([]ManifestEntry{
		{Path: "metrics/a.json", SHA256: digestA, Size: 5},
		{Path: "metrics/b.json", SHA256: digestB, Size: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	if revisionOne != revisionTwo || bytesOne != 12 || bytesTwo != 12 || filesOne != 2 || filesTwo != 2 || len(one) != 3 || len(two) != 3 || one[0].Path != "metrics" || one[0].Type != "directory" {
		t.Fatalf("one=%#v two=%#v revisions=%s/%s totals=%d/%d files=%d/%d", one, two, revisionOne, revisionTwo, bytesOne, bytesTwo, filesOne, filesTwo)
	}
}

func TestNormalizeSelectionRejectsUnsafeOrAmbiguousEntries(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, entries := range [][]ManifestEntry{
		{{Path: "../secret", SHA256: digest}},
		{{Path: "/absolute", SHA256: digest}},
		{{Path: "missing-hash"}},
		{{Path: "node", SHA256: digest}, {Path: "node/child", SHA256: digest}},
		{{Path: "same", SHA256: digest}, {Path: "same", SHA256: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
	} {
		if _, _, _, _, err := NormalizeSelection(entries); err == nil {
			t.Fatalf("unsafe selection was accepted: %#v", entries)
		}
	}
}

func TestSelectionRevisionMatchesDestinationDirectoryHash(t *testing.T) {
	root := t.TempDir()
	payload := []byte("paper evidence")
	if err := os.MkdirAll(filepath.Join(root, "metrics"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "metrics", "seed.json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	_, selectionRevision, _, _, err := NormalizeSelection([]ManifestEntry{
		{Path: "metrics/seed.json", Type: "file", SHA256: fmt.Sprintf("sha256:%x", digest[:]), Size: int64(len(payload))},
		{Path: "empty", Type: "directory"},
	})
	if err != nil {
		t.Fatal(err)
	}
	destination, err := filespace.HashLocalPath(root, root)
	if err != nil || selectionRevision != destination.Revision {
		t.Fatalf("selection=%s destination=%#v err=%v", selectionRevision, destination, err)
	}
}
