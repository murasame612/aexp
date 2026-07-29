package freeze

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

func TestBuildPlanIsDeterministicAndUsesExplicitRoles(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc", Name: "gpu", Type: "ssh", Host: "gpu", RootDir: "/work", Status: store.ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveStorageTarget(ctx, &store.StorageTarget{ID: "storage_nas", Name: "nas", ResourceID: "rsrc", RootPath: "/work/storage"}); err != nil {
		t.Fatal(err)
	}
	dataset := &store.DatasetVersion{
		ID: "dataset_v3", DatasetID: "facade", Version: "v3", StorageTargetID: "storage_nas",
		StoragePath: "datasets/facade/v3", LogicalURI: "storage://nas/datasets/facade/v3",
		Revision: "sha256:data", ManifestSHA256: "sha256:data", State: store.DatasetStateRegistered,
	}
	if _, _, err := db.CreateDatasetVersionImmutable(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	datasets, _ := json.Marshal([]store.RunDatasetInput{{ID: "dataset_v3", DatasetID: "facade", Version: "v3", ManifestSHA256: "sha256:data"}})
	seeds, _ := json.Marshal([]int64{41, 42, 43})
	run := &store.Run{
		ID: "run_formal", ResourceID: "rsrc", Status: store.RunStatusSucceeded, Kind: store.RunKindFormal, EvidenceGrade: store.RunEvidenceGradeFormal,
		Command: "python train.py", GitCommit: "abcdef", ProjectConfigSHA256: "sha256:config",
		DatasetsJSON: string(datasets), SeedsJSON: string(seeds), SplitProtocol: "split-v1", EvaluationProtocol: "eval-v1",
	}
	if err := db.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.SaveRunManifest(ctx, &store.RunManifest{RunID: run.ID, SchemaVersion: 1, State: store.RunManifestFinal, ManifestJSON: "{}", SHA256: "sha256:run", Completeness: store.RunManifestCompletenessCurrent, FinalizedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveArtifacts(ctx, run.ID, []store.Artifact{{ID: "a2", RunID: run.ID, RelativePath: "predictions/seed42.csv", SourceURI: "ssh://rsrc/predictions/seed42.csv", SHA256: "sha256:pred", Size: 20}, {ID: "a1", RunID: run.ID, RelativePath: "results/per_seed.json", SourceURI: "ssh://rsrc/results/per_seed.json", SHA256: "sha256:metric", Size: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveArtifactCollection(ctx, &store.ArtifactCollection{RunID: run.ID, State: store.ArtifactCollectionIndexed, FileCount: 2, TotalBytes: 30}); err != nil {
		t.Fatal(err)
	}
	profile := Profile{Name: "paper", Storage: "nas", Rules: []RoleRule{{Role: "metrics", Patterns: []string{"results/**/per_seed.json"}, Required: true}, {Role: "predictions", Patterns: []string{"predictions/**/*.csv"}, Required: true}}, WorkspaceRoles: []string{"metrics", "predictions"}, AggregateCommand: "python aggregate.py", GateCommand: "python gate.py"}
	blocked, err := BuildPlan(ctx, db, run.ID, profile, "storage://nas/paper", "/paper")
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Eligible || !hasFreezeBlocker(blocked.Blockers, "dataset_not_verified") {
		t.Fatalf("registered dataset plan: %#v", blocked.Blockers)
	}
	verifiedDataset := *dataset
	verifiedDataset.State = store.DatasetStateVerified
	if _, _, err := db.CreateDatasetVersionImmutable(ctx, &verifiedDataset); err != nil {
		t.Fatal(err)
	}
	one, err := BuildPlan(ctx, db, run.ID, profile, "storage://nas/paper", "/paper")
	if err != nil {
		t.Fatal(err)
	}
	two, err := BuildPlan(ctx, db, run.ID, profile, "storage://nas/paper", "/paper")
	if err != nil {
		t.Fatal(err)
	}
	if !one.Eligible || one.PlanSHA256 != two.PlanSHA256 || one.FreezeID != two.FreezeID {
		t.Fatalf("unstable/ineligible plan: %#v", one)
	}
	if len(one.Files) != 2 || one.Files[0].Role != "metrics" || one.Files[1].Role != "predictions" {
		t.Fatalf("explicit roles not applied: %#v", one.Files)
	}
}

func hasFreezeBlocker(blockers []Blocker, code string) bool {
	for _, blocker := range blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func TestBuildPlanBlocksLegacyProvenanceAndMissingHashes(t *testing.T) {
	db, _ := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	defer db.Close()
	ctx := context.Background()
	_ = db.CreateResource(ctx, &store.Resource{ID: "r", Name: "r", Type: "ssh", Host: "r", RootDir: "/w"})
	_ = db.CreateRun(ctx, &store.Run{ID: "legacy", ResourceID: "r", Status: store.RunStatusSucceeded, Kind: store.RunKindFormal, EvidenceGrade: store.RunEvidenceGradeFormal, Command: "x"})
	now := time.Now()
	_ = db.SaveRunManifest(ctx, &store.RunManifest{RunID: "legacy", SchemaVersion: 1, State: store.RunManifestFinal, ManifestJSON: "{}", SHA256: "sha256:r", Completeness: store.RunManifestCompletenessCurrent, FinalizedAt: &now})
	_ = db.SaveArtifacts(ctx, "legacy", []store.Artifact{{ID: "a", RunID: "legacy", RelativePath: "metrics/a.json"}})
	_ = db.SaveArtifactCollection(ctx, &store.ArtifactCollection{RunID: "legacy", State: store.ArtifactCollectionIndexed})
	plan, err := BuildPlan(ctx, db, "legacy", Profile{Name: "paper", Storage: "nas", Rules: []RoleRule{{Role: "metrics", Patterns: []string{"metrics/**"}, Required: true}}, AggregateCommand: "x", GateCommand: "y"}, "storage://nas/paper", "/paper")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Eligible || len(plan.Blockers) < 4 {
		t.Fatalf("legacy plan should be blocked: %#v", plan.Blockers)
	}
}

func TestStoragePrefixRejectsTraversal(t *testing.T) {
	if got, err := storagePrefix("storage://nas/paper/project", "nas"); err != nil || got != "paper/project" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	for _, uri := range []string{"storage://other/x", "storage://nas/../x", "/tmp"} {
		if _, err := storagePrefix(uri, "nas"); err == nil {
			t.Fatalf("accepted %q", uri)
		}
	}
}
