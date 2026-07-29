package release

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

func TestEvaluateAppendsBlockedReleasedAndFailedWithoutMutatingSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	project := &store.ProjectDefinition{
		ID: "project_release", Name: "Release Project", LocalRoot: root,
		GateCommand: "printf gate-blocked; exit 2",
	}
	if err := db.CreateProjectDefinition(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_release", Name: "release", Type: "local", Host: "localhost", RootDir: root}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveStorageTarget(ctx, &store.StorageTarget{ID: "storage_release", Name: "release-store", Kind: "local", ResourceID: "rsrc_release", RootPath: root, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	dataset := store.DatasetVersion{
		ID: "dataset_release_v1", DatasetID: "release-data", Version: "v1",
		StorageTargetID: "storage_release", StoragePath: "datasets/release/v1",
		ManifestSHA256: "sha256:dataset-manifest", State: store.DatasetStateVerified,
	}
	if err := db.SaveDatasetVersion(ctx, &dataset); err != nil {
		t.Fatal(err)
	}
	datasetsJSON, _ := json.Marshal([]store.RunDatasetInput{{
		ID: dataset.ID, DatasetID: dataset.DatasetID, Version: dataset.Version, ManifestSHA256: dataset.ManifestSHA256,
	}})
	run := &store.Run{
		ID: "run_release", ResourceID: "rsrc_release", ProjectID: project.ID,
		Status: store.RunStatusSucceeded, Kind: store.RunKindFormal, EvidenceGrade: store.RunEvidenceGradeFormal,
		DataFinalizationState: store.RunDataFinalizationCompleted, Command: "true",
		DatasetsJSON: string(datasetsJSON), SeedsJSON: `[41]`, ProjectConfigSHA256: "sha256:config",
		GitCommit: "abcdef123", SplitProtocol: "split-v1", EvaluationProtocol: "eval-v1",
	}
	if err := db.CreateRunWithBindings(ctx, run, store.RunBindings{Outputs: []store.RunOutputBinding{{
		SourcePattern: "metrics.json", LogicalURI: "aexp://project_release/runs/run_release/metrics.json",
		Role: "metrics", Required: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	outputs, err := db.ListRunOutputBindings(ctx, run.ID)
	if err != nil || len(outputs) != 1 {
		t.Fatalf("outputs=%#v err=%v", outputs, err)
	}
	now := time.Now()
	outputs[0].State = store.RunBindingPublished
	outputs[0].Revision = "sha256:metrics"
	outputs[0].PublishedAt = &now
	if err := db.UpdateRunOutputBinding(ctx, &outputs[0]); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunManifest(ctx, &store.RunManifest{
		RunID: run.ID, SchemaVersion: 1, State: store.RunManifestFinal,
		ManifestJSON: `{"status":"succeeded"}`, SHA256: "sha256:run-release",
		Completeness: store.RunManifestCompletenessCurrent, FinalizedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := db.CreateEvidenceSnapshot(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := snapshot.ManifestSHA256
	service := Service{Store: db}

	blocked, err := service.Evaluate(ctx, snapshot.ID)
	if err != nil || blocked.State != store.EvidenceReleaseBlocked || blocked.Sequence != 1 {
		t.Fatalf("blocked=%#v err=%v", blocked, err)
	}
	project.GateCommand = "printf gate-ok"
	if err := db.SaveProjectDefinition(ctx, project); err != nil {
		t.Fatal(err)
	}
	released, err := service.Evaluate(ctx, snapshot.ID)
	if err != nil || released.State != store.EvidenceReleaseReleased || released.Sequence != 2 {
		t.Fatalf("released=%#v err=%v", released, err)
	}
	project.AggregateCommand = "printf aggregate-failed; exit 3"
	if err := db.SaveProjectDefinition(ctx, project); err != nil {
		t.Fatal(err)
	}
	failed, err := service.Evaluate(ctx, snapshot.ID)
	if err != nil || failed.State != store.EvidenceReleaseFailed || failed.Sequence != 3 {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
	releases, err := db.ListEvidenceReleases(ctx, snapshot.ID)
	if err != nil || len(releases) != 3 || releases[0].Sequence != 3 {
		t.Fatalf("releases=%#v err=%v", releases, err)
	}
	after, err := db.GetEvidenceSnapshot(ctx, snapshot.ID)
	if err != nil || after == nil || after.ManifestSHA256 != originalHash {
		t.Fatalf("snapshot changed: %#v err=%v", after, err)
	}
}
