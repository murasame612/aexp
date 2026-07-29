package monitor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/store"
)

type recordingRunRefresher struct {
	runs        []store.Run
	concurrency int
	err         error
}

func (r *recordingRunRefresher) RefreshRunsWithConcurrency(_ context.Context, runs []store.Run, _ time.Duration, concurrency int) ([]store.Run, map[string]bool, error) {
	r.runs = append([]store.Run(nil), runs...)
	r.concurrency = concurrency
	return runs, map[string]bool{}, r.err
}

func TestRunReconcilerRefreshesOnlyActiveRunsWithConfiguredLimit(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, resource := range []*store.Resource{
		{ID: "r1", Name: "r1", Type: "ssh", Host: "one", RootDir: "/ws", Status: store.ResourceStatusIdle},
		{ID: "r2", Name: "r2", Type: "ssh", Host: "two", RootDir: "/ws", Status: store.ResourceStatusIdle},
	} {
		if err := db.CreateResource(ctx, resource); err != nil {
			t.Fatal(err)
		}
	}
	for _, run := range []*store.Run{
		{ID: "running", ResourceID: "r1", Status: store.RunStatusRunning, Command: "train"},
		{ID: "starting", ResourceID: "r2", Status: store.RunStatusStarting, Command: "train"},
		{ID: "done", ResourceID: "r1", Status: store.RunStatusSucceeded, Command: "train"},
	} {
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	refresher := &recordingRunRefresher{}
	reconciler := NewRunReconciler(db, nil, time.Second, time.Second, 2, nil)
	reconciler.executor = refresher
	if err := reconciler.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(refresher.runs) != 2 || refresher.concurrency != 2 {
		t.Fatalf("reconciled runs=%d concurrency=%d", len(refresher.runs), refresher.concurrency)
	}
}

func TestRunReconcilerReturnsStructuredProbeFailure(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.CreateResource(ctx, &store.Resource{ID: "r-timeout", Name: "timeout", Type: "ssh", Host: "timeout", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "run-timeout", ResourceID: "r-timeout", Status: store.RunStatusRunning, Command: "train"}); err != nil {
		t.Fatal(err)
	}
	probeErr := &executor.RunRefreshError{Failures: []executor.RunProbeFailure{{ResourceID: "r-timeout", RunIDs: []string{"run-timeout"}, Code: "remote_timeout", Message: "deadline exceeded", Retryable: true}}}
	refresher := &recordingRunRefresher{err: probeErr}
	reconciler := NewRunReconciler(db, nil, time.Second, time.Second, 1, nil)
	reconciler.executor = refresher
	err = reconciler.reconcile(ctx)
	var structured *executor.RunRefreshError
	if !errors.As(err, &structured) || len(structured.Failures) != 1 || structured.Failures[0].Code != "remote_timeout" {
		t.Fatalf("error=%#v, want structured remote_timeout", err)
	}
}
