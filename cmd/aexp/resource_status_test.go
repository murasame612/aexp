package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ziwu/aexp/internal/store"
)

func TestUpdateResourceControlStatusRecordsSuccess(t *testing.T) {
	ctx := context.Background()
	db := newCLITestStore(t)
	res := &store.Resource{
		ID:        "rsrc_ok",
		Name:      "ok-resource",
		Type:      store.ResourceTypeSSH,
		Host:      "127.0.0.1",
		RootDir:   "/workspace",
		Status:    store.ResourceStatusIdle,
		SSHStatus: store.ResourceSSHStatusUnknown,
	}
	if err := db.CreateResource(ctx, res); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}

	err := updateResourceControlStatus(ctx, db, res, doctorReport{
		Checks: []doctorCheck{{Name: "resource reachable", OK: true, Severity: "ok"}},
	})
	if err != nil {
		t.Fatalf("updateResourceControlStatus: %v", err)
	}
	got, err := db.GetResource(ctx, res.ID)
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}
	if got.SSHStatus != store.ResourceSSHStatusOK || got.LastCheckedAt == nil || got.LastSuccessAt == nil || got.LastDoctorError != "" {
		t.Fatalf("unexpected success status: %#v", got)
	}
}

func TestUpdateResourceControlStatusRecordsFailure(t *testing.T) {
	ctx := context.Background()
	db := newCLITestStore(t)
	res := &store.Resource{
		ID:        "rsrc_fail",
		Name:      "fail-resource",
		Type:      store.ResourceTypeSSH,
		Host:      "127.0.0.1",
		RootDir:   "/workspace",
		Status:    store.ResourceStatusIdle,
		SSHStatus: store.ResourceSSHStatusOK,
	}
	if err := db.CreateResource(ctx, res); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}

	err := updateResourceControlStatus(ctx, db, res, doctorReport{
		Checks: []doctorCheck{{Name: "resource reachable", OK: false, Severity: "fail", Detail: "ssh: EOF"}},
	})
	if err != nil {
		t.Fatalf("updateResourceControlStatus: %v", err)
	}
	got, err := db.GetResource(ctx, res.ID)
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}
	if got.SSHStatus != store.ResourceSSHStatusFailed || got.LastDoctorError != "ssh: EOF" || got.LastCheckedAt == nil {
		t.Fatalf("unexpected failure status: %#v", got)
	}
}

func newCLITestStore(t *testing.T) *store.SQLite {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
