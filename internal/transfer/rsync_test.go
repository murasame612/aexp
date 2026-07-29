package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/store"
)

type rsyncFakeFS struct {
	entries map[string]filespace.RemoteEntry
	hashes  map[string]filespace.HashResult
}

func (f *rsyncFakeFS) Stat(_ context.Context, location filespace.RemoteLocation) (filespace.RemoteEntry, error) {
	if entry, ok := f.entries[location.PhysicalPath]; ok {
		return entry, nil
	}
	return filespace.RemoteEntry{Path: location.PhysicalPath, Exists: false}, nil
}

func (f *rsyncFakeFS) List(context.Context, filespace.RemoteLocation, string, int) (filespace.ListResult, error) {
	return filespace.ListResult{}, errors.New("not implemented")
}

func (f *rsyncFakeFS) Hash(_ context.Context, location filespace.RemoteLocation) (filespace.HashResult, error) {
	result, ok := f.hashes[location.PhysicalPath]
	if !ok {
		return filespace.HashResult{}, errors.New("hash missing")
	}
	return result, nil
}

type rsyncFakeRunner struct {
	resourceID string
	command    string
	records    []string
	stdout     string
	stderr     string
	err        error
}

func (r *rsyncFakeRunner) Exec(_ context.Context, resource *store.Resource, command string) (string, string, error) {
	r.resourceID, r.command = resource.ID, command
	return r.stdout, r.stderr, r.err
}

func (r *rsyncFakeRunner) Stream(_ context.Context, resource *store.Resource, command string, onRecord func(string) error) (string, error) {
	r.resourceID, r.command = resource.ID, command
	for _, record := range r.records {
		if err := onRecord(record); err != nil {
			return r.stderr, err
		}
	}
	return r.stderr, r.err
}

func rsyncFixture(t *testing.T) (*store.SQLite, *rsyncFakeFS, *rsyncFakeRunner, *RsyncTransport, CopyRequest) {
	t.Helper()
	_, db, _ := newPlannerFixture(t)
	ctx := context.Background()
	nas, _ := db.GetResource(ctx, "nas_id")
	nas.User, nas.Port, nas.AuthRef = "nasuser", 22, "/mac/nas-key"
	if err := db.UpdateResource(ctx, nas); err != nil {
		t.Fatal(err)
	}
	gpu, _ := db.GetResource(ctx, "gpu_id")
	gpu.User, gpu.Port, gpu.AuthRef = "gpuuser", 2222, "/mac/gpu-key"
	if err := db.UpdateResource(ctx, gpu); err != nil {
		t.Fatal(err)
	}
	fs := &rsyncFakeFS{
		entries: map[string]filespace.RemoteEntry{"/vol/data/source": {Path: "/vol/data/source", Exists: true, Type: "directory"}},
		hashes:  map[string]filespace.HashResult{"/scratch/cache/.incoming-transfer_test": {Revision: "sha256:source", TotalBytes: 1234, FileCount: 2}},
	}
	runner := &rsyncFakeRunner{stdout: strings.Join([]string{
		"AEXP_OS=Linux",
		"AEXP_LOCK=flock",
		"AEXP_RSYNC=1",
		"AEXP_RSYNC_APPEND_VERIFY=1",
		"AEXP_RSYNC_PROTECT_ARGS=1",
		"AEXP_RSYNC_PROGRESS2=1",
	}, "\n")}
	transport := NewRsyncTransport(db, fs, runner)
	request := CopyRequest{TransferID: "transfer_test", StagingPath: "/scratch/cache/.incoming-transfer_test", Route: Route{Initiator: "nas", CommandResourceID: "nas_id"}, Plan: Plan{
		Source:      Endpoint{ResourceID: "nas_id", PhysicalPath: "/vol/data/source", Boundary: "/vol/data", Revision: "sha256:source"},
		Destination: Endpoint{ResourceID: "gpu_id", PhysicalPath: "/scratch/cache/final", Boundary: "/scratch"},
		TotalBytes:  1234,
	}}
	return db, fs, runner, transport, request
}

func TestRsyncTransportFallsBackToDarwinLockfAndPortableRsync(t *testing.T) {
	_, _, runner, transport, request := rsyncFixture(t)
	runner.stdout = strings.Join([]string{
		"AEXP_OS=Darwin",
		"AEXP_LOCK=lockf",
		"AEXP_RSYNC=1",
		"AEXP_RSYNC_APPEND_VERIFY=0",
		"AEXP_RSYNC_PROTECT_ARGS=0",
		"AEXP_RSYNC_PROGRESS2=0",
	}, "\n")
	if err := transport.Copy(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.command, "lockf -k") || strings.Contains(runner.command, "flock -x") {
		t.Fatalf("Darwin transfer did not use lockf: %s", runner.command)
	}
	for _, unsupported := range []string{"--append-verify", "--protect-args", "--info=progress2"} {
		if strings.Contains(runner.command, unsupported) {
			t.Fatalf("Darwin transfer retained unsupported %s: %s", unsupported, runner.command)
		}
	}
	if !strings.Contains(runner.command, "--partial") || !strings.Contains(runner.command, "--out-format=") {
		t.Fatalf("portable resume/progress flags missing: %s", runner.command)
	}
}

func TestRsyncTransportRejectsInitiatorWithoutLockPrimitive(t *testing.T) {
	_, _, runner, transport, request := rsyncFixture(t)
	runner.stdout = strings.Join([]string{
		"AEXP_OS=Darwin",
		"AEXP_LOCK=none",
		"AEXP_RSYNC=1",
		"AEXP_RSYNC_APPEND_VERIFY=0",
		"AEXP_RSYNC_PROTECT_ARGS=0",
		"AEXP_RSYNC_PROGRESS2=0",
	}, "\n")
	err := transport.Copy(context.Background(), request, nil)
	var operation *OperationError
	if !errors.As(err, &operation) || operation.Code != "transport_lock_unavailable" {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
}

func TestRsyncTransportReportsCapabilityProbeFailure(t *testing.T) {
	_, _, runner, transport, request := rsyncFixture(t)
	runner.stdout = ""
	runner.stderr = "ssh transport unavailable"
	runner.err = errors.New("exit status 255")
	err := transport.Copy(context.Background(), request, nil)
	var operation *OperationError
	if !errors.As(err, &operation) || operation.Code != "transport_capability_probe_failed" || !operation.Retryable {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
}

func TestRsyncTransportReportsMissingRsync(t *testing.T) {
	_, _, runner, transport, request := rsyncFixture(t)
	runner.stdout = strings.Join([]string{
		"AEXP_OS=Darwin",
		"AEXP_LOCK=lockf",
		"AEXP_RSYNC=0",
		"AEXP_RSYNC_APPEND_VERIFY=0",
		"AEXP_RSYNC_PROTECT_ARGS=0",
		"AEXP_RSYNC_PROGRESS2=0",
	}, "\n")
	err := transport.Copy(context.Background(), request, nil)
	var operation *OperationError
	if !errors.As(err, &operation) || operation.Code != "transport_rsync_unavailable" || operation.Retryable {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
}

func TestRsyncTransportNASInitiatedStreamsProgressWithoutMacPayload(t *testing.T) {
	_, _, runner, transport, request := rsyncFixture(t)
	runner.records = []string{"400  32%  1.00MB/s  0:00:01 (xfr#1, to-chk=1/2)", "1,234 100%  1.00MB/s 0:00:01 (xfr#2, to-chk=0/2)"}
	var progress []Progress
	if err := transport.Copy(context.Background(), request, func(value Progress) error {
		progress = append(progress, value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if runner.resourceID != "nas_id" || !strings.Contains(runner.command, "rsync -a") || !strings.Contains(runner.command, "gpuuser@gpu:/scratch/cache/.incoming-transfer_test") {
		t.Fatalf("resource=%s command=%s", runner.resourceID, runner.command)
	}
	if !strings.Contains(runner.command, store.NASInitiatedIdentity) || strings.Contains(runner.command, "/mac/") {
		t.Fatalf("NAS command used the wrong identity: %s", runner.command)
	}
	if !strings.Contains(runner.command, "flock -x") || !strings.Contains(runner.command, "/tmp/aexp-transfer-transfer_test.lock") {
		t.Fatalf("durable transfer replay was not serialized: %s", runner.command)
	}
	if strings.Index(runner.command, "identity=") < strings.Index(runner.command, "flock -x") {
		t.Fatalf("NAS identity was defined outside the locked rsync shell: %s", runner.command)
	}
	if !strings.Contains(runner.command, "mkdir -p") || len(progress) < 2 || progress[len(progress)-1].BytesDone != 1234 || progress[len(progress)-1].FilesDone != 2 {
		t.Fatalf("command=%s progress=%#v", runner.command, progress)
	}
	if !strings.Contains(runner.command, "chmod 700") ||
		!strings.Contains(runner.command, "--no-owner --no-group") ||
		!strings.Contains(runner.command, "--chmod=Du+rwx,Dgo-rwx,Fu+rw,Fgo-rwx") {
		t.Fatalf("cross-storage permissions were not normalized: %s", runner.command)
	}
}

func TestRsyncTransportComputeFallbackPullsFromNAS(t *testing.T) {
	_, _, runner, transport, request := rsyncFixture(t)
	request.Route = Route{Initiator: "compute", CommandResourceID: "gpu_id"}
	runner.records = []string{"1,234 100% 1.00MB/s 0:00:01 (xfr#2, to-chk=0/2)"}
	if err := transport.Copy(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	if runner.resourceID != "gpu_id" || !strings.Contains(runner.command, "nasuser@nas:/vol/data/source/") || strings.Contains(runner.command, store.NASInitiatedIdentity) {
		t.Fatalf("resource=%s command=%s", runner.resourceID, runner.command)
	}
}

func TestRsyncTransportSelectionUsesEncodedFilesFrom(t *testing.T) {
	_, _, runner, transport, request := rsyncFixture(t)
	request.Plan.Selection = []ManifestEntry{
		{Path: "metrics", Type: "directory"},
		{Path: "metrics/seed-1.json", Type: "file", SHA256: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 10},
	}
	request.Plan.FileCount, request.Plan.TotalBytes = 1, 10
	runner.records = []string{"10 100% 1.00MB/s 0:00:01 (xfr#1, to-chk=0/1)"}
	if err := transport.Copy(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.command, "--files-from=\"$files_from\"") || !strings.Contains(runner.command, "base64") || strings.Contains(runner.command, "seed-1.json") {
		t.Fatalf("selection was not passed as an encoded files-from manifest: %s", runner.command)
	}
	if strings.Index(runner.command, "files_from=") < strings.Index(runner.command, "flock -x") {
		t.Fatalf("selection manifest was defined outside the locked rsync shell: %s", runner.command)
	}
}

func TestRsyncTransportVerifiesStagingAndPromotesAtomically(t *testing.T) {
	_, fs, runner, transport, request := rsyncFixture(t)
	verified, err := transport.Verify(context.Background(), request)
	if err != nil || verified.Revision != "sha256:source" || verified.FileCount != 2 {
		t.Fatalf("verified=%#v err=%v", verified, err)
	}
	if err := transport.Promote(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if runner.resourceID != "gpu_id" || !strings.Contains(runner.command, "base64") || strings.Contains(runner.command, request.StagingPath) {
		t.Fatalf("promotion must use encoded paths on destination: resource=%s command=%s", runner.resourceID, runner.command)
	}
	fs.entries[request.Plan.Destination.PhysicalPath] = filespace.RemoteEntry{Exists: true, Type: "directory"}
	fs.hashes[request.Plan.Destination.PhysicalPath] = filespace.HashResult{Revision: "sha256:other"}
	err = transport.Promote(context.Background(), request)
	var operation *OperationError
	if !errors.As(err, &operation) || !operation.Conflict || operation.Code != "destination_conflict" {
		t.Fatalf("conflict=%#v err=%v", operation, err)
	}
	fs.hashes[request.Plan.Destination.PhysicalPath] = filespace.HashResult{Revision: "sha256:source"}
	if err := transport.Promote(context.Background(), request); err != nil {
		t.Fatalf("same revision was not idempotent: %v", err)
	}
}

func TestParseRsyncProgressIsMonotonicFriendly(t *testing.T) {
	current := Progress{BytesDone: 100, FilesDone: 1}
	next, ok := parseRsyncProgress("2,048 100% 2.0MB/s 0:00:01 (xfr#3, to-chk=0/3)", current)
	if !ok || next.BytesDone != 2048 || next.FilesDone != 3 {
		t.Fatalf("next=%#v ok=%v", next, ok)
	}
}

func TestParseRsyncProgressCountsFilesButNotDirectories(t *testing.T) {
	current := Progress{}
	if _, ok := parseRsyncProgress("AEXP_FILE\tcd+++++++++\t4096\tmetrics/", current); ok {
		t.Fatal("directory output was counted as a transferred file")
	}
	next, ok := parseRsyncProgress("AEXP_FILE\t>f+++++++++\t10\tmetrics/a.json", current)
	if !ok || next.BytesDone != 10 || next.FilesDone != 1 {
		t.Fatalf("next=%#v ok=%v", next, ok)
	}
}

func TestHashLocalPathMatchesManagedDirectoryManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "value.txt"), []byte("evidence"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := filespace.HashLocalPath(root, root)
	if err != nil || result.Revision == "" || result.Revision != result.ManifestSHA256 || result.FileCount != 1 || result.TotalBytes != 8 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if err := os.Mkdir(filepath.Join(root, "extra-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	withEmptyDirectory, err := filespace.HashLocalPath(root, root)
	if err != nil || withEmptyDirectory.Revision == result.Revision {
		t.Fatalf("empty directory was absent from the manifest: before=%#v after=%#v err=%v", result, withEmptyDirectory, err)
	}
	outside := t.TempDir()
	if err := filespace.EnsureLocalBoundary(filepath.Join(outside, "payload"), root); err == nil {
		t.Fatal("path escape was accepted")
	}
}
