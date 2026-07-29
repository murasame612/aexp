package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ziwu/aexp/internal/store"
)

func TestProjectJournalAPIProvidesTimelineAndRunBacklinks(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateProjectDefinition(ctx, &store.ProjectDefinition{ID: "project_api_journal", Name: "Journal"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateResource(ctx, &store.Resource{
		ID: "rsrc_api_journal", Name: "journal-api", Type: "ssh", Host: "localhost", RootDir: "/workspace",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID: "run_api_journal", ProjectID: "project_api_journal", ResourceID: "rsrc_api_journal",
		Status: store.RunStatusSucceeded, Command: "true",
	}); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(db, nil, nil, slog.Default(), "", true).Handler()

	createBody := bytes.NewBufferString(`{
		"actor":"agent",
		"title":"Fix device mismatch",
		"body_md":"The mask now follows the residual device.",
		"next_action":"rerun pilot",
		"run_ids":["run_api_journal"]
	}`)
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/project-definitions/project_api_journal/journal",
		createBody,
	))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var entry store.ProjectJournalEntry
	if err := json.Unmarshal(create.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.ID == "" || entry.NextActionStatus != store.JournalNextActionOpen {
		t.Fatalf("entry = %#v", entry)
	}

	runBacklinks := httptest.NewRecorder()
	handler.ServeHTTP(runBacklinks, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/runs/run_api_journal/journal",
		nil,
	))
	if runBacklinks.Code != http.StatusOK {
		t.Fatalf("backlinks status=%d body=%s", runBacklinks.Code, runBacklinks.Body.String())
	}
	var backlinks []store.ProjectJournalEntry
	if err := json.Unmarshal(runBacklinks.Body.Bytes(), &backlinks); err != nil {
		t.Fatal(err)
	}
	if len(backlinks) != 1 || backlinks[0].ID != entry.ID {
		t.Fatalf("backlinks = %#v", backlinks)
	}

	update := httptest.NewRecorder()
	handler.ServeHTTP(update, httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/journal-entries/"+entry.ID+"/next-action",
		bytes.NewBufferString(`{"status":"done"}`),
	))
	if update.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", update.Code, update.Body.String())
	}
	var updated store.ProjectJournalEntry
	if err := json.Unmarshal(update.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.NextActionStatus != store.JournalNextActionDone {
		t.Fatalf("updated = %#v", updated)
	}
}
