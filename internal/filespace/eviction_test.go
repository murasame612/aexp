package filespace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

type evictionRemote struct {
	entries map[string]RemoteEntry
	hashes  map[string]HashResult
	removed []string
}

func (f *evictionRemote) Stat(_ context.Context, location RemoteLocation) (RemoteEntry, error) {
	entry, ok := f.entries[location.PhysicalPath]
	if !ok {
		return RemoteEntry{Path: location.PhysicalPath, Exists: false}, nil
	}
	return entry, nil
}

func (f *evictionRemote) List(context.Context, RemoteLocation, string, int) (ListResult, error) {
	return ListResult{}, errors.New("not implemented")
}

func (f *evictionRemote) Hash(_ context.Context, location RemoteLocation) (HashResult, error) {
	result, ok := f.hashes[location.PhysicalPath]
	if !ok {
		return HashResult{}, errors.New("hash not found")
	}
	return result, nil
}

func (f *evictionRemote) RemoveVerified(_ context.Context, location RemoteLocation, expected string) error {
	if f.hashes[location.PhysicalPath].Revision != expected {
		return errors.New("revision changed")
	}
	f.removed = append(f.removed, location.PhysicalPath)
	delete(f.entries, location.PhysicalPath)
	delete(f.hashes, location.PhysicalPath)
	return nil
}

func TestEvictionRequiresAnotherLiveMatchingAuthoritativePlacement(t *testing.T) {
	ctx := context.Background()
	revision := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nasPath := "/vol/one/aexp/projects/project/data/raw"
	cachePath := "/scratch/cache/raw"
	remote := &evictionRemote{entries: map[string]RemoteEntry{
		nasPath: {Path: nasPath, Exists: true, Type: "directory"}, cachePath: {Path: cachePath, Exists: true, Type: "directory"},
	}, hashes: map[string]HashResult{
		nasPath: {Revision: revision, TotalBytes: 40}, cachePath: {Revision: revision, TotalBytes: 40},
	}}
	service, db := newFileSpaceService(t, remote)
	if err := db.CreateResource(ctx, &store.Resource{ID: "gpu", Name: "gpu", Type: store.ResourceTypeSSH, Host: "gpu", RootDir: "/scratch"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cache := &store.PathPlacement{ID: "cache", LogicalURI: "aexp://project/data/raw", ResourceID: "gpu", PhysicalPath: cachePath, Role: store.PlacementRoleCache, DesiredState: store.PlacementDesiredPresent, ObservedState: store.PlacementObservedPresent, Revision: revision, CheckedAt: &now, ObservedAt: &now}
	if err := db.SavePathPlacement(ctx, cache); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PlanEviction(ctx, cache.LogicalURI, "gpu"); err == nil {
		t.Fatal("unique verified copy was accepted for eviction")
	}
	authoritative := &store.PathPlacement{ID: "authoritative", LogicalURI: cache.LogicalURI, ResourceID: "nas", StorageTargetID: "storage", PhysicalPath: nasPath, Role: store.PlacementRoleAuthoritative, DesiredState: store.PlacementDesiredPresent, ObservedState: store.PlacementObservedPresent, Revision: revision, CheckedAt: &now, ObservedAt: &now}
	if err := db.SavePathPlacement(ctx, authoritative); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanEviction(ctx, cache.LogicalURI, "gpu")
	if err != nil || plan.SourcePhysicalPath != cachePath || plan.AuthoritativePlacementID != authoritative.ID || plan.PlanSHA256 == "" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if _, err := service.Evict(ctx, cache.LogicalURI, "gpu", "sha256:stale"); err == nil || len(remote.removed) != 0 {
		t.Fatalf("stale plan removed payload: err=%v removed=%v", err, remote.removed)
	}
	executed, err := service.Evict(ctx, cache.LogicalURI, "gpu", plan.PlanSHA256)
	if err != nil || executed.PlanSHA256 != plan.PlanSHA256 || len(remote.removed) != 1 || remote.removed[0] != cachePath {
		t.Fatalf("executed=%#v err=%v removed=%v", executed, err, remote.removed)
	}
	stored, err := db.GetPathPlacement(ctx, cache.ID)
	if err != nil || stored.ObservedState != store.PlacementObservedMissing || stored.ObservationSource != "managed_evict" {
		t.Fatalf("placement=%#v err=%v", stored, err)
	}
}

func TestEvictionRejectsAuthoritativeRevisionMismatch(t *testing.T) {
	ctx := context.Background()
	remote := &evictionRemote{entries: map[string]RemoteEntry{}, hashes: map[string]HashResult{}}
	service, db := newFileSpaceService(t, remote)
	if err := db.CreateResource(ctx, &store.Resource{ID: "gpu", Name: "gpu", Type: store.ResourceTypeSSH, Host: "gpu", RootDir: "/scratch"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sourcePath, nasPath := "/scratch/cache/raw", "/vol/one/aexp/projects/project/data/raw"
	remote.entries[sourcePath], remote.entries[nasPath] = RemoteEntry{Exists: true}, RemoteEntry{Exists: true}
	remote.hashes[sourcePath] = HashResult{Revision: "sha256:source"}
	remote.hashes[nasPath] = HashResult{Revision: "sha256:other"}
	for _, placement := range []*store.PathPlacement{
		{ID: "cache", LogicalURI: "aexp://project/data/raw", ResourceID: "gpu", PhysicalPath: sourcePath, Role: store.PlacementRoleCache, ObservedState: store.PlacementObservedPresent, Revision: "sha256:source", CheckedAt: &now},
		{ID: "auth", LogicalURI: "aexp://project/data/raw", ResourceID: "nas", StorageTargetID: "storage", PhysicalPath: nasPath, Role: store.PlacementRoleAuthoritative, ObservedState: store.PlacementObservedPresent, Revision: "sha256:source", CheckedAt: &now},
	} {
		if err := db.SavePathPlacement(ctx, placement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.PlanEviction(ctx, "aexp://project/data/raw", "gpu"); err == nil {
		t.Fatal("stale authoritative registry revision was trusted without live verification")
	}
}
