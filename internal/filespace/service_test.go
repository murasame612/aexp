package filespace

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

type fakeRemoteFS struct {
	entry   RemoteEntry
	statErr error
	hash    HashResult
	hashErr error
	list    ListResult
	paths   []string
}

func (f *fakeRemoteFS) Stat(_ context.Context, location RemoteLocation) (RemoteEntry, error) {
	f.paths = append(f.paths, location.PhysicalPath)
	return f.entry, f.statErr
}

func (f *fakeRemoteFS) List(_ context.Context, location RemoteLocation, _ string, _ int) (ListResult, error) {
	f.paths = append(f.paths, location.PhysicalPath)
	return f.list, nil
}

func (f *fakeRemoteFS) Hash(_ context.Context, location RemoteLocation) (HashResult, error) {
	f.paths = append(f.paths, location.PhysicalPath)
	return f.hash, f.hashErr
}

func newFileSpaceService(t *testing.T, remote RemoteFS) (*Service, *store.SQLite) {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := db.CreateResource(ctx, &store.Resource{ID: "nas", Name: "nas", Type: store.ResourceTypeSSH, Host: "nas", RootDir: "/vol/one"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveStorageTarget(ctx, &store.StorageTarget{ID: "storage", Name: "storage", ResourceID: "nas", RootPath: "/vol/one/aexp"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLogicalRoot(ctx, &store.LogicalRoot{ID: "data", Workspace: "project", Prefix: "data", StorageTargetID: "storage", PhysicalRoot: "projects/project/data"}); err != nil {
		t.Fatal(err)
	}
	return NewService(db, remote), db
}

func TestServiceResolveAndInspectPersistsTruthfulObservation(t *testing.T) {
	remote := &fakeRemoteFS{entry: RemoteEntry{Exists: true, Type: "directory", Size: 4096}}
	service, db := newFileSpaceService(t, remote)
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	service.Now = func() time.Time { return now }
	resolved, err := service.Resolve(context.Background(), "aexp://project/data/raw")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolved.DefaultPlacement.PhysicalPath, "/vol/one/aexp/projects/project/data/raw"; got != want {
		t.Fatalf("physical path=%q want=%q", got, want)
	}
	if len(resolved.Placements) != 0 {
		t.Fatalf("resolve must not create registry rows: %#v", resolved.Placements)
	}
	result, err := service.Inspect(context.Background(), resolved.LogicalURI, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Placement.ObservedState != store.PlacementObservedPresent || result.Placement.Freshness != "fresh" {
		t.Fatalf("inspect=%#v", result)
	}
	placements, err := db.ListPathPlacements(context.Background(), resolved.LogicalURI)
	if err != nil || len(placements) != 1 || placements[0].CheckedAt == nil {
		t.Fatalf("persisted placements=%#v err=%v", placements, err)
	}
	service.Now = func() time.Time { return now.Add(10 * time.Minute) }
	located, err := service.Locate(context.Background(), resolved.LogicalURI)
	if err != nil || len(located) != 1 || located[0].Freshness != "stale" {
		t.Fatalf("stale locate=%#v err=%v", located, err)
	}
}

func TestServiceSeparatesMissingUnreachableAndUnknown(t *testing.T) {
	tests := []struct {
		name  string
		entry RemoteEntry
		err   error
		want  string
	}{
		{name: "missing", entry: RemoteEntry{Exists: false}, want: store.PlacementObservedMissing},
		{name: "unreachable", err: &RemoteError{Kind: RemoteErrorUnreachable, Detail: "dial timeout", Err: errors.New("timeout")}, want: store.PlacementObservedUnreachable},
		{name: "command", err: &RemoteError{Kind: RemoteErrorCommand, Detail: "python missing", Err: errors.New("exit 127")}, want: store.PlacementObservedUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, _ := newFileSpaceService(t, &fakeRemoteFS{entry: tt.entry, statErr: tt.err})
			result, err := service.Inspect(context.Background(), "aexp://project/data/raw", "")
			if err != nil {
				t.Fatal(err)
			}
			if result.Placement.ObservedState != tt.want {
				t.Fatalf("state=%s want=%s", result.Placement.ObservedState, tt.want)
			}
		})
	}
}

func TestServiceHashPinsRevision(t *testing.T) {
	remote := &fakeRemoteFS{hash: HashResult{Revision: "sha256:abc", ManifestSHA256: "sha256:abc", FileCount: 2, TotalBytes: 99}}
	service, db := newFileSpaceService(t, remote)
	result, err := service.Hash(context.Background(), "aexp://project/data/raw", "")
	if err != nil || result.Revision != "sha256:abc" {
		t.Fatalf("hash=%#v err=%v", result, err)
	}
	placements, err := db.ListPathPlacements(context.Background(), "aexp://project/data/raw")
	if err != nil || len(placements) != 1 || placements[0].Revision != result.Revision || placements[0].BytesPresent != 99 {
		t.Fatalf("placements=%#v err=%v", placements, err)
	}
}

func TestServiceBrowsesStorageURIWithoutLogicalRoot(t *testing.T) {
	remote := &fakeRemoteFS{
		entry: RemoteEntry{Exists: true, Type: "directory", Size: 4096, ModifiedNS: 123},
		list: ListResult{Entries: []RemoteEntry{
			{Path: "/vol/one/aexp/data/.incoming-transfer", Name: ".incoming-transfer", Exists: true, Type: "directory"},
			{Path: "/vol/one/aexp/data/a.csv", Name: "a.csv", Exists: true, Type: "file", Size: 42, ModifiedNS: 456},
		}},
	}
	service, _ := newFileSpaceService(t, remote)
	stat, err := service.StatURI(context.Background(), "storage://storage/data", "")
	if err != nil {
		t.Fatal(err)
	}
	if stat.Location.State != store.PlacementObservedPresent || stat.Location.Role != "primary" || stat.Location.ResourceName != "nas" || stat.Location.PhysicalPath != "/vol/one/aexp/data" {
		t.Fatalf("stat=%#v", stat)
	}
	listed, err := service.ListURI(context.Background(), "storage://storage/", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 1 || listed.Entries[0].Name != "a.csv" || listed.Entries[0].Size != 42 || remote.paths[len(remote.paths)-1] != "/vol/one/aexp" {
		t.Fatalf("listed=%#v paths=%#v", listed, remote.paths)
	}
}

func TestServiceLocationsIncludesUnobservedPrimaryPlacement(t *testing.T) {
	service, _ := newFileSpaceService(t, &fakeRemoteFS{})
	locations, err := service.LocationsURI(context.Background(), "aexp://project/data/raw")
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 1 || locations[0].Role != "primary" || locations[0].State != store.PlacementObservedUnknown || locations[0].PhysicalPath != "/vol/one/aexp/projects/project/data/raw" {
		t.Fatalf("locations=%#v", locations)
	}
}

func TestServiceRejectsManagedURIPathEscape(t *testing.T) {
	service, _ := newFileSpaceService(t, &fakeRemoteFS{})
	for _, uri := range []string{"storage://storage/../secret", "storage://storage/%2e%2e/secret", "resource://nas/bad%0Aname", "storage:///missing-host"} {
		if _, err := service.ResolveManagedURI(context.Background(), uri); err == nil {
			t.Errorf("URI %q was accepted", uri)
		}
	}
}

type recordingRunner struct {
	stdout  string
	command string
	err     error
}

func (r *recordingRunner) Exec(_ context.Context, _ *store.Resource, command string) (string, string, error) {
	r.command = command
	return r.stdout, "", r.err
}

func TestPythonRemoteFSQuotesPhysicalPath(t *testing.T) {
	runner := &recordingRunner{stdout: `{"path":"/data/a b'c","exists":false}`}
	remote := PythonRemoteFS{Runner: runner}
	entry, err := remote.Stat(context.Background(), RemoteLocation{Resource: &store.Resource{}, PhysicalPath: "/data/a b'c", Boundary: "/data"})
	if err != nil || entry.Exists {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
	if strings.Contains(runner.command, "/data/a b'c") || !strings.Contains(runner.command, "L2RhdGEvYSBiJ2M=") {
		t.Fatalf("physical path was not base64 encoded: %s", runner.command)
	}
}
