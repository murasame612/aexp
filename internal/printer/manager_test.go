package printer

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

type fakeDevice struct {
	mu       sync.Mutex
	titles   []string
	receipts []string
	failNext bool
}

func (f *fakeDevice) Print(_ context.Context, _, title, receipt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.titles = append(f.titles, title)
	f.receipts = append(f.receipts, receipt)
	if f.failNext {
		f.failNext = false
		return "", errors.New("offline")
	}
	return "Printer_POS_80-test", nil
}

func (f *fakeDevice) Status(context.Context, string) (QueueStatus, error) {
	return QueueStatus{Available: true, State: "idle"}, nil
}

func printerTestDB(t *testing.T) *store.SQLite {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(t.Context(), &store.Resource{ID: "r", Name: "gpu", Host: "localhost", User: "u", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	return db
}

func createPrinterRun(t *testing.T, db *store.SQLite, id string) *store.Run {
	t.Helper()
	run := &store.Run{ID: id, ResourceID: "r", Name: id, Status: store.RunStatusQueued, Kind: store.RunKindFormal, EvidenceGrade: store.RunEvidenceGradeFormal, Command: "train"}
	if err := db.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestManagerPreservesInterleavedLifecycleOrder(t *testing.T) {
	db := printerTestDB(t)
	if _, err := db.ConfigurePrinter(t.Context(), true, "Printer_POS_80"); err != nil {
		t.Fatal(err)
	}
	a := createPrinterRun(t, db, "run_A")
	b := createPrinterRun(t, db, "run_B")
	now := time.Now()
	a.Status, a.StartedAt = store.RunStatusRunning, validSQLTime(now)
	if err := db.UpdateRun(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	b.Status, b.StartedAt = store.RunStatusRunning, validSQLTime(now.Add(time.Second))
	if err := db.UpdateRun(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	b.Status, b.FinishedAt = store.RunStatusSucceeded, validSQLTime(now.Add(2*time.Second))
	if err := db.UpdateRun(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	a.Status, a.FinishedAt = store.RunStatusSucceeded, validSQLTime(now.Add(3*time.Second))
	if err := db.UpdateRun(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	device := &fakeDevice{}
	manager := NewManager(db, device, time.Second, slog.Default())
	if err := manager.SyncOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"AEXP RUN STARTED run_A", "AEXP RUN STARTED run_B", "AEXP RUN SUCCEEDED run_B", "AEXP RUN SUCCEEDED run_A"}
	if strings.Join(device.titles, "|") != strings.Join(want, "|") {
		t.Fatalf("titles = %#v, want %#v", device.titles, want)
	}
	if err := manager.SyncOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(device.titles) != 4 {
		t.Fatalf("replay printed %d jobs", len(device.titles))
	}
}

func TestPrintFailureDoesNotChangeRunOrBlockLaterJobs(t *testing.T) {
	db := printerTestDB(t)
	if _, err := db.ConfigurePrinter(t.Context(), true, "Printer_POS_80"); err != nil {
		t.Fatal(err)
	}
	a := createPrinterRun(t, db, "run_fail_print")
	b := createPrinterRun(t, db, "run_after")
	now := time.Now()
	for _, run := range []*store.Run{a, b} {
		run.Status, run.StartedAt = store.RunStatusRunning, validSQLTime(now)
		if err := db.UpdateRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	device := &fakeDevice{failNext: true}
	manager := NewManager(db, device, time.Second, slog.Default())
	if err := manager.SyncOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(device.titles) != 2 {
		t.Fatalf("later job was blocked: %#v", device.titles)
	}
	got, err := db.GetRun(t.Context(), a.ID)
	if err != nil || got.Status != store.RunStatusRunning {
		t.Fatalf("print changed run status: %#v, %v", got, err)
	}
	jobs, _ := db.ListPrintJobs(t.Context(), 10)
	states := map[string]int{}
	for _, job := range jobs {
		states[job.State]++
	}
	if states[store.PrintJobFailed] != 1 || states[store.PrintJobSpooled] != 1 {
		t.Fatalf("states = %#v", states)
	}
}

func TestFirstEnableSkipsHistoricalRuns(t *testing.T) {
	db := printerTestDB(t)
	run := createPrinterRun(t, db, "run_old")
	now := time.Now()
	run.Status, run.StartedAt = store.RunStatusRunning, validSQLTime(now)
	if err := db.UpdateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfigurePrinter(t.Context(), true, "Printer_POS_80"); err != nil {
		t.Fatal(err)
	}
	run.Status, run.FinishedAt = store.RunStatusSucceeded, validSQLTime(now.Add(time.Minute))
	if err := db.UpdateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	device := &fakeDevice{}
	if err := NewManager(db, device, time.Second, slog.Default()).SyncOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(device.titles) != 0 {
		t.Fatalf("historical run was printed: %#v", device.titles)
	}
}

func validSQLTime(value time.Time) sql.NullTime { return sql.NullTime{Time: value, Valid: true} }
