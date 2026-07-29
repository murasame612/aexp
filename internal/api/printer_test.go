package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	printerservice "github.com/ziwu/aexp/internal/printer"
	"github.com/ziwu/aexp/internal/store"
)

type apiPrinterDevice struct{}

func (apiPrinterDevice) Print(context.Context, string, string, string) (string, error) {
	return "Printer_POS_80-1", nil
}
func (apiPrinterDevice) Status(context.Context, string) (printerservice.QueueStatus, error) {
	return printerservice.QueueStatus{Available: true, State: "idle"}, nil
}

func printerAPIForTest(t *testing.T) (*store.SQLite, http.Handler) {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	manager := printerservice.NewManager(db, apiPrinterDevice{}, time.Second, slog.Default())
	return db, NewServer(db, nil, nil, slog.Default(), "", true, WithPrinterManager(manager)).Handler()
}

func TestPrinterAPIConfigTestListAndRetry(t *testing.T) {
	db, handler := printerAPIForTest(t)
	config := httptest.NewRecorder()
	handler.ServeHTTP(config, httptest.NewRequest(http.MethodPatch, "/api/v1/printer/config", strings.NewReader(`{"enabled":true,"queue":"Printer_POS_80"}`)))
	if config.Code != http.StatusOK || !strings.Contains(config.Body.String(), `"enabled":true`) {
		t.Fatalf("config: %d %s", config.Code, config.Body.String())
	}

	testRec := httptest.NewRecorder()
	handler.ServeHTTP(testRec, httptest.NewRequest(http.MethodPost, "/api/v1/printer/test", strings.NewReader(`{}`)))
	if testRec.Code != http.StatusAccepted || !strings.Contains(testRec.Body.String(), `"phase":"test"`) {
		t.Fatalf("test: %d %s", testRec.Code, testRec.Body.String())
	}
	jobs, err := db.ListPrintJobs(t.Context(), 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs: %#v %v", jobs, err)
	}
	if err := db.FailPrintJob(t.Context(), jobs[0].ID, store.PrintJobFailed, "offline"); err != nil {
		t.Fatal(err)
	}

	retry := httptest.NewRecorder()
	handler.ServeHTTP(retry, httptest.NewRequest(http.MethodPost, "/api/v1/printer/jobs/"+jobs[0].ID+"/retry", strings.NewReader(`{}`)))
	if retry.Code != http.StatusAccepted || !strings.Contains(retry.Body.String(), `"state":"queued"`) {
		t.Fatalf("retry: %d %s", retry.Code, retry.Body.String())
	}

	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, httptest.NewRequest(http.MethodPost, "/api/v1/printer/jobs/"+jobs[0].ID+"/retry", strings.NewReader(`{}`)))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict: %d %s", conflict.Code, conflict.Body.String())
	}
}

func TestPrinterAPIUnavailableWithoutManager(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rec := httptest.NewRecorder()
	NewServer(db, nil, nil, slog.Default(), "", true).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/printer/status", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}
