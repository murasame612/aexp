package filespace

import (
	"testing"

	"github.com/ziwu/aexp/internal/store"
)

func TestLogicalPathNormalizeRoundTrip(t *testing.T) {
	p, err := Parse("aexp://project-a/data//raw/./train%20set")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.String(), "aexp://project-a/data/raw/train%20set"; got != want {
		t.Fatalf("canonical URI = %q, want %q", got, want)
	}
	again, err := Parse(p.String())
	if err != nil || again != p {
		t.Fatalf("round trip = %#v, %v", again, err)
	}
}

func TestLogicalPathRejectsUnsafeInputs(t *testing.T) {
	for _, raw := range []string{
		"", " aexp://p/data", "aexp://p", "aexp:///data", "aexp://p/../secret",
		"aexp://p/%2e%2e/secret", "aexp://p/C:%5Csecret", "aexp://p/data?x=1",
		"aexp://user@p/data", "aexp://p:22/data", "aexp://p/data%0Acommand",
	} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) succeeded", raw)
		}
	}
}

func TestResolveRootAndPhysicalPath(t *testing.T) {
	p, err := Parse("aexp://project/data/raw/train.csv")
	if err != nil {
		t.Fatal(err)
	}
	roots := []store.LogicalRoot{
		{ID: "other", Workspace: "other", Prefix: "data", PhysicalRoot: "other/data"},
		{ID: "data", Workspace: "project", Prefix: "data", PhysicalRoot: "projects/project/data"},
	}
	root, suffix, err := ResolveRoot(p, roots)
	if err != nil {
		t.Fatal(err)
	}
	if root.ID != "data" || suffix != "raw/train.csv" {
		t.Fatalf("resolved root=%#v suffix=%q", root, suffix)
	}
	physical, err := PhysicalPath(root, suffix)
	if err != nil || physical != "projects/project/data/raw/train.csv" {
		t.Fatalf("physical=%q err=%v", physical, err)
	}
}

func TestResolveRootPrefersSpecificChildOverWorkspaceFallback(t *testing.T) {
	roots := []store.LogicalRoot{
		{ID: "workspace", Workspace: "project", Prefix: "", PhysicalRoot: "projects/project/outputs"},
		{ID: "datasets", Workspace: "project", Prefix: "datasets", PhysicalRoot: "projects/project/datasets"},
	}

	output, err := Parse("aexp://project/official-run-v1")
	if err != nil {
		t.Fatal(err)
	}
	root, suffix, err := ResolveRoot(output, roots)
	if err != nil || root.ID != "workspace" || suffix != "official-run-v1" {
		t.Fatalf("output root=%#v suffix=%q err=%v", root, suffix, err)
	}

	dataset, err := Parse("aexp://project/datasets/cohort-v1")
	if err != nil {
		t.Fatal(err)
	}
	root, suffix, err = ResolveRoot(dataset, roots)
	if err != nil || root.ID != "datasets" || suffix != "cohort-v1" {
		t.Fatalf("dataset root=%#v suffix=%q err=%v", root, suffix, err)
	}
}

func TestPhysicalPathRejectsEscape(t *testing.T) {
	root := store.LogicalRoot{PhysicalRoot: "projects/p/data"}
	for _, suffix := range []string{"../secret", "/absolute", "ok/../../secret", "bad\\path"} {
		if _, err := PhysicalPath(root, suffix); err == nil {
			t.Errorf("PhysicalPath(%q) succeeded", suffix)
		}
	}
}
