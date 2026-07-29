package transfer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
)

type physicalPlannerRemote struct {
	entry filespace.RemoteEntry
	hash  filespace.HashResult
	err   error
}

type pathAwarePlannerRemote struct {
	sourceEntry filespace.RemoteEntry
	sourceHash  filespace.HashResult
	sourceErr   error
}

func (f pathAwarePlannerRemote) Stat(_ context.Context, location filespace.RemoteLocation) (filespace.RemoteEntry, error) {
	if location.PhysicalPath == "/vol/data/aexp/datasets/raw" {
		return f.sourceEntry, f.sourceErr
	}
	return filespace.RemoteEntry{Exists: false}, nil
}

func (f pathAwarePlannerRemote) Hash(_ context.Context, location filespace.RemoteLocation) (filespace.HashResult, error) {
	if location.PhysicalPath == "/vol/data/aexp/datasets/raw" {
		return f.sourceHash, f.sourceErr
	}
	return filespace.HashResult{}, errors.New("unexpected destination hash")
}

func (f pathAwarePlannerRemote) List(context.Context, filespace.RemoteLocation, string, int) (filespace.ListResult, error) {
	return filespace.ListResult{}, errors.New("not implemented")
}

func (f physicalPlannerRemote) Stat(context.Context, filespace.RemoteLocation) (filespace.RemoteEntry, error) {
	return f.entry, f.err
}
func (f physicalPlannerRemote) Hash(context.Context, filespace.RemoteLocation) (filespace.HashResult, error) {
	return f.hash, f.err
}
func (f physicalPlannerRemote) List(context.Context, filespace.RemoteLocation, string, int) (filespace.ListResult, error) {
	return filespace.ListResult{}, errors.New("not implemented")
}

func newPlannerFixture(t *testing.T) (*Planner, *store.SQLite, time.Time) {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, resource := range []store.Resource{
		{ID: "nas_id", Name: "nas", Type: store.ResourceTypeSSH, Host: "nas", RootDir: "/vol/data"},
		{ID: "gpu_id", Name: "gpu", Type: store.ResourceTypeSSH, Host: "gpu", RootDir: "/scratch"},
	} {
		if err := db.CreateResource(ctx, &resource); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	target := &store.StorageTarget{
		ID: "storage_id", Name: "nas-store", ResourceID: "nas_id", RootPath: "/vol/data/aexp",
		Health: &store.StorageTargetHealth{CheckedAt: now, DataPlane: []store.StorageDataPlaneHealth{{
			ResourceID: "gpu_id", CheckedAt: now, SelectedInitiator: store.StorageInitiatorNAS,
			NASInitiated:     store.StorageConnectionHealth{Status: store.StorageStatusHealthy, SSHReachable: true, Rsync: true},
			ComputeInitiated: store.StorageConnectionHealth{Status: store.StorageStatusHealthy, SSHReachable: true, Rsync: true},
		}}},
	}
	if err := db.SaveStorageTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLogicalRoot(ctx, &store.LogicalRoot{ID: "data_root", Workspace: "project", Prefix: "data", StorageTargetID: target.ID, PhysicalRoot: "projects/project/data"}); err != nil {
		t.Fatal(err)
	}
	checked := now.Add(-time.Minute)
	if err := db.SavePathPlacement(ctx, &store.PathPlacement{
		ID: "source_placement", LogicalURI: "aexp://project/data/raw", ResourceID: "nas_id", StorageTargetID: target.ID,
		PhysicalPath: "/vol/data/aexp/projects/project/data/raw", Role: store.PlacementRoleAuthoritative,
		DesiredState: store.PlacementDesiredPresent, ObservedState: store.PlacementObservedPresent,
		Revision: "sha256:source", ManifestSHA256: "sha256:source", BytesPresent: 1234,
		ObservationSource: "remote_hash", ObservedAt: &checked, CheckedAt: &checked,
	}); err != nil {
		t.Fatal(err)
	}
	files := filespace.NewService(db, nil)
	planner := NewPlanner(db, files)
	planner.Now = func() time.Time { return now }
	return planner, db, now
}

func TestPlannerSelectsNASRouteAndIsSideEffectFree(t *testing.T) {
	planner, db, _ := newPlannerFixture(t)
	request := PlanRequest{Source: "aexp://project/data/raw", Destination: "resource://gpu/cache/raw", Verification: "manifest"}
	one, err := planner.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if one.Initiator != "nas" || one.CommandResourceID != "nas_id" || one.LocalDataPath || len(one.Fallback) != 1 || one.Fallback[0].Initiator != "compute" {
		t.Fatalf("route=%#v", one)
	}
	if one.Source.Revision != "sha256:source" || one.TotalBytes != 1234 || len(one.Blockers) != 0 {
		t.Fatalf("source/blockers=%#v", one)
	}
	if one.StagingPath != "/scratch/cache/.incoming-{transfer_id}" {
		t.Fatalf("staging=%q", one.StagingPath)
	}
	two, err := planner.Build(context.Background(), request)
	if err != nil || two.PlanSHA256 != one.PlanSHA256 {
		t.Fatalf("stable hash one=%s two=%s err=%v", one.PlanSHA256, two.PlanSHA256, err)
	}
	storedPlan, err := db.GetTransferPlan(context.Background(), one.PlanSHA256)
	if err != nil || storedPlan != nil {
		t.Fatalf("plan had a database side effect: %#v err=%v", storedPlan, err)
	}
	jobs, err := db.ListTransferJobs(context.Background(), "", 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("plan created transfer jobs: %#v err=%v", jobs, err)
	}
}

func TestPlannerProbesPhysicalDestinationAndBlocksRevisionConflict(t *testing.T) {
	planner, _, _ := newPlannerFixture(t)
	planner.Files.Remote = physicalPlannerRemote{
		entry: filespace.RemoteEntry{Exists: true, Type: "directory"},
		hash:  filespace.HashResult{Revision: "sha256:different", TotalBytes: 99},
	}
	request := PlanRequest{Source: "aexp://project/data/raw", Destination: "resource://gpu/cache/raw"}
	plan, err := planner.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, blocker := range plan.Blockers {
		found = found || blocker.Code == "destination_revision_conflict"
	}
	if !found || plan.Destination.ObservedState != store.PlacementObservedPresent || plan.Destination.Revision != "sha256:different" {
		t.Fatalf("plan=%#v", plan)
	}
}

func TestPlannerDiscoversPhysicalSourceRevision(t *testing.T) {
	planner, _, _ := newPlannerFixture(t)
	planner.Files.Remote = pathAwarePlannerRemote{
		sourceEntry: filespace.RemoteEntry{Exists: true, Type: "directory"},
		sourceHash:  filespace.HashResult{Revision: "sha256:discovered", ManifestSHA256: "sha256:discovered", TotalBytes: 2048, FileCount: 3},
	}
	plan, err := planner.Build(context.Background(), PlanRequest{Source: "storage://nas-store/datasets/raw", Destination: "resource://gpu/cache/raw"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 0 || plan.Source.Revision != "sha256:discovered" || plan.TotalBytes != 2048 || plan.Source.ObservedState != store.PlacementObservedPresent {
		t.Fatalf("plan=%#v", plan)
	}
}

func TestPlannerReturnsStructuredMissingPhysicalSourceBlockers(t *testing.T) {
	planner, _, _ := newPlannerFixture(t)
	planner.Files.Remote = pathAwarePlannerRemote{sourceEntry: filespace.RemoteEntry{Exists: false}}
	plan, err := planner.Build(context.Background(), PlanRequest{Source: "storage://nas-store/datasets/raw", Destination: "resource://gpu/cache/raw"})
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]bool{"source_not_present": false, "source_revision_unavailable": false}
	for _, blocker := range plan.Blockers {
		if _, ok := wants[blocker.Code]; ok {
			wants[blocker.Code] = true
		}
	}
	for code, found := range wants {
		if !found {
			t.Fatalf("missing %s in %#v", code, plan.Blockers)
		}
	}
}

func TestPlannerMatchingPhysicalDestinationIsSatisfiedAndStable(t *testing.T) {
	planner, _, _ := newPlannerFixture(t)
	planner.Files.Remote = physicalPlannerRemote{
		entry: filespace.RemoteEntry{Exists: true, Type: "directory"},
		hash:  filespace.HashResult{Revision: "sha256:source", TotalBytes: 1234},
	}
	request := PlanRequest{Source: "aexp://project/data/raw", Destination: "resource://gpu/cache/raw"}
	one, err := planner.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	two, err := planner.Build(context.Background(), request)
	if err != nil || !one.AlreadySatisfied || one.PlanSHA256 != two.PlanSHA256 || one.Destination.LogicalURI != request.Source || one.Destination.Role != store.PlacementRoleCache {
		t.Fatalf("one=%#v two=%#v err=%v", one, two, err)
	}
}

func TestPlannerReturnsStructuredBlockersForStaleSourceAndRoute(t *testing.T) {
	planner, _, now := newPlannerFixture(t)
	planner.Now = func() time.Time { return now.Add(20 * time.Minute) }
	plan, err := planner.Build(context.Background(), PlanRequest{Source: "aexp://project/data/raw", Destination: "resource://gpu/cache/raw"})
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]bool{"source_observation_stale": false, "route_health_stale": false}
	for _, blocker := range plan.Blockers {
		if _, ok := wants[blocker.Code]; ok {
			wants[blocker.Code] = true
		}
	}
	for code, found := range wants {
		if !found {
			t.Fatalf("missing blocker %s in %#v", code, plan.Blockers)
		}
	}
}

func TestPlannerRejectsPhysicalPathEscape(t *testing.T) {
	planner, _, _ := newPlannerFixture(t)
	for _, destination := range []string{"resource://gpu/../secret", "storage://nas-store/%2e%2e/secret", "resource://gpu/bad%0Apath"} {
		if _, err := planner.Build(context.Background(), PlanRequest{Source: "aexp://project/data/raw", Destination: destination}); err == nil {
			t.Errorf("destination %q was accepted", destination)
		}
	}
}

func TestPlannerExplicitMacRouteIsVisible(t *testing.T) {
	planner, _, _ := newPlannerFixture(t)
	plan, err := planner.Build(context.Background(), PlanRequest{Source: "aexp://project/data/raw", Destination: "resource://gpu/cache/raw", Initiator: "mac"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Initiator != "mac" || !plan.LocalDataPath || plan.CommandResourceID != "local" {
		t.Fatalf("mac route not transparent: %#v", plan)
	}
}

func TestPlannerSelectionPinsStablePayloadRevision(t *testing.T) {
	planner, _, _ := newPlannerFixture(t)
	digestA := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	request := PlanRequest{
		Source: "resource://gpu/run/results", Destination: "aexp://project/data/frozen", Initiator: "compute",
		Selection: []ManifestEntry{{Path: "seed-2.json", SHA256: digestB, Size: 20}, {Path: "seed-1.json", SHA256: digestA, Size: 10}},
	}
	one, err := planner.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Selection[0], request.Selection[1] = request.Selection[1], request.Selection[0]
	two, err := planner.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if one.PlanSHA256 != two.PlanSHA256 || one.Source.Revision == "" || one.Source.Revision != two.Source.Revision || one.TotalBytes != 30 || one.FileCount != 2 || len(one.Selection) != 2 {
		t.Fatalf("one=%#v two=%#v", one, two)
	}
}
