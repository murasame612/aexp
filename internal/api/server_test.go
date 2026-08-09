package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ziwu/aexp/internal/eventcache"
	"github.com/ziwu/aexp/internal/executor"
	"github.com/ziwu/aexp/internal/filespace"
	"github.com/ziwu/aexp/internal/literature"
	"github.com/ziwu/aexp/internal/store"
	"github.com/ziwu/aexp/internal/transfer"
)

type apiFakeRemoteFS struct{}

func (apiFakeRemoteFS) Stat(_ context.Context, location filespace.RemoteLocation) (filespace.RemoteEntry, error) {
	if location.Resource != nil && strings.Contains(location.Resource.ID, "gpu") {
		return filespace.RemoteEntry{Path: location.PhysicalPath, Exists: false}, nil
	}
	return filespace.RemoteEntry{Path: location.PhysicalPath, Exists: true, Type: "directory", Size: 4096}, nil
}

type fakeLiteratureService struct{}

func (fakeLiteratureService) Catalog(context.Context) (*literature.Catalog, error) {
	return &literature.Catalog{
		Collections: []literature.Collection{{Key: "COLLECTION", Name: "Methods", Path: "Methods"}},
		Profiles:    []literature.ProfileStatus{{Name: "paperqa", Status: "ready", CollectionKey: "COLLECTION", CorpusRevision: "corpus_test", Documents: 12, Chunks: 34}},
	}, nil
}

func (fakeLiteratureService) Status(_ context.Context, project *store.ProjectDefinition, _ time.Duration) (map[string]interface{}, error) {
	return map[string]interface{}{"status": "ready", "project_id": project.ID, "zotero_collection_key": project.ZoteroCollectionKey}, nil
}

func (fakeLiteratureService) Query(_ context.Context, project *store.ProjectDefinition, request literature.QueryRequest, _ time.Duration) (map[string]interface{}, error) {
	return map[string]interface{}{"answer": "Pinned answer", "query": request.Query, "project_id": project.ID}, nil
}

func TestProjectLiteratureEndpoints(t *testing.T) {
	if literatureQueryTimeout < 3*time.Minute {
		t.Fatalf("literature query timeout = %s, want at least 3m for PaperQA synthesis", literatureQueryTimeout)
	}
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	project := &store.ProjectDefinition{ID: "literature-project", Name: "Literature", ZoteroCollectionKey: "COLLECTION", LiteratureServiceProfile: "paperqa"}
	if err := db.CreateProjectDefinition(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(db, nil, nil, slog.Default(), "", true, WithLiteratureService(fakeLiteratureService{})).Handler()

	for _, path := range []string{
		"/api/v1/project-definitions/literature-project/literature/catalog",
		"/api/v1/project-definitions/literature-project/literature/status",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "COLLECTION") {
			t.Fatalf("GET %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/project-definitions/literature-project/literature/query", bytes.NewBufferString(`{"query":"What transfers?"}`))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Pinned answer") {
		t.Fatalf("query status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	notFound := httptest.NewRecorder()
	handler.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/api/v1/project-definitions/missing/literature/status", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", notFound.Code, notFound.Body.String())
	}
}

func TestUnknownAPIRouteReturnsJSON404InsteadOfSPA(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/not-in-this-binary", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("content type = %q, want JSON", contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["error"] != "API_ROUTE_NOT_FOUND" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestProjectRunCardAPIRequiresExpectedRevisionAndReturnsConflict(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateProjectDefinition(ctx, &store.ProjectDefinition{ID: "project-a", Name: "Project A"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_api_cas", Name: "api-cas", Type: "ssh", Host: "localhost", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "run_api_cas", ResourceID: "rsrc_api_cas", Status: store.RunStatusSucceeded, Kind: store.RunKindPilot, Command: "true"}); err != nil {
		t.Fatal(err)
	}
	card := &store.ProjectRunCard{
		ID: "card_api_cas", RunID: "run_api_cas", ProjectID: "project-a",
		ProjectName: "Project A", Verdict: "first",
	}
	if err := db.SaveProjectRunCard(ctx, card); err != nil {
		t.Fatal(err)
	}
	staleRevision := card.UpdatedAt
	srv := NewServer(db, nil, nil, slog.Default(), "", true)

	missing := httptest.NewRecorder()
	srv.Handler().ServeHTTP(missing, httptest.NewRequest(
		http.MethodPut, "/api/v1/runs/run_api_cas/project-card",
		bytes.NewBufferString(`{"card":{"verdict":"missing"}}`),
	))
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "EXPECTED_REVISION_REQUIRED") {
		t.Fatalf("missing revision status=%d body=%s", missing.Code, missing.Body.String())
	}

	body, _ := json.Marshal(map[string]interface{}{
		"card":                map[string]interface{}{"verdict": "second"},
		"expected_updated_at": staleRevision,
	})
	ok := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ok, httptest.NewRequest(http.MethodPut, "/api/v1/runs/run_api_cas/project-card", bytes.NewReader(body)))
	if ok.Code != http.StatusOK {
		t.Fatalf("current revision status=%d body=%s", ok.Code, ok.Body.String())
	}

	stale := httptest.NewRecorder()
	srv.Handler().ServeHTTP(stale, httptest.NewRequest(http.MethodPut, "/api/v1/runs/run_api_cas/project-card", bytes.NewReader(body)))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("stale revision status=%d body=%s", stale.Code, stale.Body.String())
	}
	current, err := db.GetProjectRunCard(ctx, card.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Verdict != "second" || current.ProjectID != "project-a" {
		t.Fatalf("unexpected stored card: %#v", current)
	}
}

func TestAssignRunProjectAPIRequiresCASAndReturnsAuditedResult(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, project := range []*store.ProjectDefinition{
		{ID: "project-api-a", Name: "API A"},
		{ID: "project-api-b", Name: "API B"},
	} {
		if err := db.CreateProjectDefinition(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_project_api", Name: "project-api", Type: "ssh", Host: "localhost", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "run_project_api", ResourceID: "rsrc_project_api", Status: store.RunStatusSucceeded, Kind: store.RunKindPilot, Command: "true"}); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(db, nil, nil, slog.Default(), "", true).Handler()

	missingCAS := httptest.NewRecorder()
	handler.ServeHTTP(missingCAS, httptest.NewRequest(http.MethodPut, "/api/v1/runs/run_project_api/project", bytes.NewBufferString(`{"project_id":"project-api-a"}`)))
	if missingCAS.Code != http.StatusBadRequest || !strings.Contains(missingCAS.Body.String(), "EXPECTED_PROJECT_ID_REQUIRED") {
		t.Fatalf("missing CAS status=%d body=%s", missingCAS.Code, missingCAS.Body.String())
	}

	ok := httptest.NewRecorder()
	handler.ServeHTTP(ok, httptest.NewRequest(http.MethodPut, "/api/v1/runs/run_project_api/project", bytes.NewBufferString(`{"project_id":"project-api-a","expected_project_id":"","actor":"ui-v2","reason":"historical assignment"}`)))
	if ok.Code != http.StatusOK {
		t.Fatalf("assign status=%d body=%s", ok.Code, ok.Body.String())
	}
	var result store.RunProjectAssignmentResult
	if err := json.Unmarshal(ok.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Run == nil || result.Run.ProjectID != "project-api-a" || !result.ProvenanceUnchanged {
		t.Fatalf("assignment result=%#v", result)
	}

	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, httptest.NewRequest(http.MethodPut, "/api/v1/runs/run_project_api/project", bytes.NewBufferString(`{"project_id":"project-api-b","expected_project_id":""}`)))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "RUN_PROJECT_CONFLICT") {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
	run, _ := db.GetRun(ctx, "run_project_api")
	if run.ProjectID != "project-api-a" {
		t.Fatalf("stale request overwrote assignment: %#v", run)
	}
}

func (apiFakeRemoteFS) List(_ context.Context, location filespace.RemoteLocation, _ string, _ int) (filespace.ListResult, error) {
	return filespace.ListResult{Entries: []filespace.RemoteEntry{{Path: location.PhysicalPath + "/a.csv", Name: "a.csv", Exists: true, Type: "file", Size: 12}}}, nil
}

func (apiFakeRemoteFS) Hash(_ context.Context, _ filespace.RemoteLocation) (filespace.HashResult, error) {
	return filespace.HashResult{Revision: "sha256:api", ManifestSHA256: "sha256:api", FileCount: 1, TotalBytes: 12}, nil
}

func TestSubmitRunReturnsQueuedBeforeRemoteLaunch(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_submit_async", Name: "async", Type: "ssh", Host: "127.0.0.1", Port: 1, User: "ziwu", RootDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateProjectDefinition(ctx, &store.ProjectDefinition{ID: "project-submit-async", Name: "Async"}); err != nil {
		t.Fatal(err)
	}
	exec := executor.NewExecutor(executor.NewSSHPool(10*time.Millisecond), db)
	srv := NewServer(db, exec, nil, slog.Default(), "", true)
	body := bytes.NewBufferString(`{"resource_id":"rsrc_submit_async","project_id":"project-submit-async","name":"queued now","kind":"smoke","gpu_index":-2,"cwd":"/workspace/project","command":"python smoke.py","allow_ephemeral_paths":true}`)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var run store.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.ID == "" || run.Status != store.RunStatusQueued {
		t.Fatalf("run=%#v", run)
	}
	persisted, err := db.GetRun(ctx, run.ID)
	if err != nil || persisted == nil {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		persisted, _ = db.GetRun(ctx, run.ID)
		if persisted != nil && store.IsRunTerminalStatus(persisted.Status) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background launch did not settle before cleanup: %#v", persisted)
}

func TestLogReadErrorKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "missing", err: fmt.Errorf("log file not found: /tmp/events.jsonl"), want: "file_missing"},
		{name: "unreachable", err: fmt.Errorf("resource mu is unreachable; cannot read remote log file events.jsonl"), want: "resource_unreachable"},
		{name: "timeout", err: context.DeadlineExceeded, want: "remote_timeout"},
		{name: "other", err: fmt.Errorf("ssh handshake failed"), want: "read_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logReadErrorKind(tt.err); got != tt.want {
				t.Fatalf("logReadErrorKind(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestUIV2CacheControlSeparatesHTMLAndFingerprintedAssets(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "", want: "no-store"},
		{name: "index.html", want: "no-store"},
		{name: "assets/index-a1b2c3d4.js", want: "public, max-age=31536000, immutable"},
		{name: "assets/index-DCa-1RJt.js", want: "public, max-age=31536000, immutable"},
		{name: "assets/index.js", want: "no-cache"},
	}
	for _, tt := range tests {
		if got := uiV2CacheControl(tt.name); got != tt.want {
			t.Errorf("uiV2CacheControl(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestActiveRunSummariesAreIndependentFromListFilters(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_active", Name: "active", Type: "ssh", Host: "active", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []*store.Run{
		{ID: "run_active", ResourceID: "rsrc_active", Status: store.RunStatusRunning, Name: "active", Command: strings.Repeat("x", 500)},
		{ID: "run_finished", ResourceID: "rsrc_active", Status: store.RunStatusSucceeded, Name: "finished", Command: "done"},
	} {
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs/active?limit=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items        []store.RunSummary `json:"items"`
		ChangeCursor int64              `json:"change_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != "run_active" || body.ChangeCursor == 0 {
		t.Fatalf("active summaries=%#v", body.Items)
	}
	if len(body.Items[0].CommandPreview) != 240 || strings.Contains(rec.Body.String(), `"command":`) {
		t.Fatalf("response is not lightweight: %s", rec.Body.String())
	}
}

func TestRunSummaryListFiltersWithoutReturningHeavyRunPayload(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_summary_list", Name: "summary-list", Type: "ssh", Host: "summary", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []*store.Run{
		{ID: "run_summary_running", ResourceID: "rsrc_summary_list", Status: store.RunStatusRunning, Command: strings.Repeat("a", 500)},
		{ID: "run_summary_done", ResourceID: "rsrc_summary_list", Status: store.RunStatusSucceeded, Cwd: "/workspace/paper-project", UIEventsPath: ".aexp/events/run_summary_done.jsonl", Command: strings.Repeat("b", 500)},
	} {
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs/summaries?status=succeeded&limit=20", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []store.RunSummary `json:"items"`
		Total int                `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Items) != 1 || body.Items[0].ID != "run_summary_done" || len(body.Items[0].CommandPreview) != 240 || body.Items[0].Cwd != "/workspace/paper-project" || body.Items[0].UIEventsPath == "" {
		t.Fatalf("summary list=%#v body=%s", body, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "git_status") || strings.Contains(rec.Body.String(), "env_json") {
		t.Fatalf("heavy fields leaked: %s", rec.Body.String())
	}
}

func TestRunSummaryListAppliesProjectScopeBeforePagination(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateProjectDefinition(ctx, &store.ProjectDefinition{ID: "project-a", Name: "Project A"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_project_scope_api", Name: "project-scope-api", Type: "ssh", Host: "summary", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []*store.Run{
		{ID: "run_project_scope_api_native", ResourceID: "rsrc_project_scope_api", ProjectID: "project-a", Status: store.RunStatusSucceeded, Command: "python native.py"},
		{ID: "run_project_scope_api_card", ResourceID: "rsrc_project_scope_api", Status: store.RunStatusSucceeded, Command: "python card.py"},
		{ID: "run_project_scope_api_other", ResourceID: "rsrc_project_scope_api", Status: store.RunStatusSucceeded, Command: "python other.py"},
	} {
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveProjectRunCard(ctx, &store.ProjectRunCard{
		ID:          "card_project_scope_api",
		ProjectID:   "project-a",
		ProjectName: "Project A",
		RunID:       "run_project_scope_api_card",
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	seen := map[string]bool{}
	for offset := 0; offset < 2; offset++ {
		rec := httptest.NewRecorder()
		path := fmt.Sprintf("/api/v1/runs/summaries?project_scope=project-a&limit=1&offset=%d", offset)
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("offset=%d status=%d body=%s", offset, rec.Code, rec.Body.String())
		}
		var body struct {
			Items []store.RunSummary `json:"items"`
			Total int                `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Total != 2 || len(body.Items) != 1 {
			t.Fatalf("offset=%d items=%d total=%d body=%s", offset, len(body.Items), body.Total, rec.Body.String())
		}
		seen[body.Items[0].ID] = true
	}
	for _, runID := range []string{"run_project_scope_api_native", "run_project_scope_api_card"} {
		if !seen[runID] {
			t.Errorf("project-scoped API omitted %s: %#v", runID, seen)
		}
	}
	if seen["run_project_scope_api_other"] {
		t.Fatal("project-scoped API included unrelated run")
	}
}

func TestRunChangesEndpointObservesExternalDatabaseUpdates(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_delta", Name: "delta", Type: "ssh", Host: "delta", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "run_delta", ResourceID: "rsrc_delta", Status: store.RunStatusQueued, Command: "train"}); err != nil {
		t.Fatal(err)
	}
	first, err := db.ListRunChanges(ctx, 0, nil, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("initial changes=%#v err=%v", first, err)
	}
	run, err := db.GetRun(ctx, "run_delta")
	if err != nil || run == nil {
		t.Fatalf("get run=%#v err=%v", run, err)
	}
	run.Status = store.RunStatusRunning
	if err := db.UpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	rec := httptest.NewRecorder()
	path := fmt.Sprintf("/api/v1/runs/changes?after_seq=%d", first[0].Seq)
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Seq int64             `json:"seq"`
			Run *store.RunSummary `json:"run"`
		} `json:"items"`
		NextSeq int64 `json:"next_seq"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Run == nil || body.Items[0].Run.Status != store.RunStatusRunning || body.NextSeq != body.Items[0].Seq {
		t.Fatalf("delta response=%#v body=%s", body, rec.Body.String())
	}
}

func TestRunChangesUsesUpdatedSinceOnlyWithoutSequenceCheckpoint(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_since", Name: "since", Type: "ssh", Host: "since", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "run_since", ResourceID: "rsrc_since", Status: store.RunStatusQueued, Command: "train"}); err != nil {
		t.Fatal(err)
	}
	cursor, _ := db.LatestRunChangeSeq(ctx)
	run, _ := db.GetRun(ctx, "run_since")
	run.Status = store.RunStatusRunning
	if err := db.UpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	withCursor := httptest.NewRecorder()
	url := fmt.Sprintf("/api/v1/runs/changes?after_seq=%d&updated_since=%s", cursor, future)
	srv.Handler().ServeHTTP(withCursor, httptest.NewRequest(http.MethodGet, url, nil))
	var replay runChangeResponse
	if err := json.Unmarshal(withCursor.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.Items) != 1 || replay.Items[0].Run == nil || replay.Items[0].Run.Status != store.RunStatusRunning {
		t.Fatalf("sequence checkpoint lost a same-window update: %s", withCursor.Body.String())
	}
	withoutCursor := httptest.NewRecorder()
	srv.Handler().ServeHTTP(withoutCursor, httptest.NewRequest(http.MethodGet, "/api/v1/runs/changes?updated_since="+future, nil))
	var filtered runChangeResponse
	if err := json.Unmarshal(withoutCursor.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) != 0 {
		t.Fatalf("updated_since fallback ignored: %s", withoutCursor.Body.String())
	}
}

func TestRunChangeStreamReplaysDurableChanges(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_stream", Name: "stream", Type: "ssh", Host: "stream", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "run_stream", ResourceID: "rsrc_stream", Status: store.RunStatusQueued, Command: "train"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(db, nil, nil, slog.Default(), "", true).Handler())
	t.Cleanup(srv.Close)
	requestCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, srv.URL+"/api/v1/runs/changes/stream?after_seq=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(resp.Body)
	var eventName, data string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventName = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if eventName != "run-change" || !strings.Contains(data, `"id":"run_stream"`) || !strings.Contains(data, `"status":"queued"`) {
		t.Fatalf("event=%q data=%s scanErr=%v", eventName, data, scanner.Err())
	}
}

func TestRunChangeStreamReceivesExternalWriterAfterConnect(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aexp.db")
	db, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_live_stream", Name: "live", Type: "ssh", Host: "live", RootDir: "/ws"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "run_live_stream", ResourceID: "rsrc_live_stream", Status: store.RunStatusQueued, Command: "train"}); err != nil {
		t.Fatal(err)
	}
	cursor, err := db.LatestRunChangeSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(db, nil, nil, slog.Default(), "", true).Handler())
	t.Cleanup(srv.Close)
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(requestCtx, http.MethodGet, srv.URL+"/api/v1/runs/changes/stream", nil)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(cursor, 10))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	external, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	run, err := external.GetRun(ctx, "run_live_stream")
	if err != nil || run == nil {
		t.Fatalf("run=%#v err=%v", run, err)
	}
	run.Status = store.RunStatusRunning
	if err := external.UpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(resp.Body)
	data := ""
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			data = strings.TrimPrefix(scanner.Text(), "data: ")
			break
		}
	}
	if !strings.Contains(data, `"id":"run_live_stream"`) || !strings.Contains(data, `"status":"running"`) {
		t.Fatalf("external change was not streamed: data=%s err=%v", data, scanner.Err())
	}
}

func TestParseStorageHealthInitializesCollectionsOnSSHFailure(t *testing.T) {
	health := parseStorageHealth("", "", fmt.Errorf("authentication failed"), time.Millisecond)
	if health.DataPlane == nil {
		t.Fatal("data_plane is nil, want an empty JSON array")
	}
	if health.Checks == nil {
		t.Fatal("checks is nil, want an empty JSON object")
	}
}

func TestGetUIEventLogsFallsBackToLocalCacheWhenResourceOffline(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AEXP_EVENT_CACHE_DIR", t.TempDir())
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_event_cache",
		Name:    "offline-resource",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusUnreachable,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:           "run_event_cache",
		ResourceID:   "rsrc_event_cache",
		Name:         "cached-events",
		Status:       store.RunStatusLost,
		Command:      "python train.py",
		UIEventsPath: ".aexp/events/run_event_cache.jsonl",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := eventcache.Write("run_event_cache", []eventcache.Line{
		{LineNo: 1, Content: `{"type":"progress","name":"epoch","current":3,"total":10}`},
	}); err != nil {
		t.Fatalf("cache event: %v", err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_event_cache/logs?path=.aexp/events/run_event_cache.jsonl&limit=50&tail=true", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Remote    bool            `json:"remote"`
		ErrorKind string          `json:"error_kind"`
		Lines     []store.LogLine `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if body.Remote {
		t.Fatalf("remote = true, want false for cache fallback")
	}
	if body.ErrorKind != "" {
		t.Fatalf("error_kind = %q, want empty when cache lines are usable", body.ErrorKind)
	}
	if len(body.Lines) != 1 || body.Lines[0].Content == "" {
		t.Fatalf("cached lines missing: %#v", body.Lines)
	}
}

func TestExecutableProjectDefinitionAndTargetAPI(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_launch", Name: "launch-resource", Type: "ssh", Host: "localhost", RootDir: "/workspace", Status: store.ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, nil, slog.Default(), "", true)

	projectReq := httptest.NewRequest(http.MethodPost, "/api/v1/project-definitions/", bytes.NewBufferString(`{"id":"project_launch","name":"Launch project","config_hash":"sha256:one","default_recipe":"train","zotero_collection_key":"SHUMTSPS","literature_service_profile":"mu-paperqa"}`))
	projectReq.Header.Set("Content-Type", "application/json")
	projectResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(projectResp, projectReq)
	if projectResp.Code != http.StatusCreated {
		t.Fatalf("create project status=%d body=%s", projectResp.Code, projectResp.Body.String())
	}
	mapReq := httptest.NewRequest(http.MethodGet, "/api/v1/project-definitions/project_launch/evidence-map", nil)
	mapResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(mapResp, mapReq)
	if mapResp.Code != http.StatusOK {
		t.Fatalf("get primary evidence map status=%d body=%s", mapResp.Code, mapResp.Body.String())
	}
	var primaryMap store.EvidenceChain
	if err := json.NewDecoder(mapResp.Body).Decode(&primaryMap); err != nil {
		t.Fatal(err)
	}
	if primaryMap.ProjectID != "project_launch" || primaryMap.Role != "primary" || primaryMap.Status != "active" {
		t.Fatalf("unexpected primary evidence map: %#v", primaryMap)
	}
	duplicateReq := httptest.NewRequest(http.MethodPost, "/api/v1/project-definitions/", bytes.NewBufferString(`{"id":"project_launch","name":"Duplicate"}`))
	duplicateReq.Header.Set("Content-Type", "application/json")
	duplicateResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(duplicateResp, duplicateReq)
	if duplicateResp.Code != http.StatusConflict || !strings.Contains(duplicateResp.Body.String(), "PROJECT_EXISTS") {
		t.Fatalf("duplicate project status=%d body=%s", duplicateResp.Code, duplicateResp.Body.String())
	}

	targetReq := httptest.NewRequest(http.MethodPost, "/api/v1/project-definitions/project_launch/targets", bytes.NewBufferString(`{"id":"target_launch","name":"mu","resource_id":"rsrc_launch","cwd":"/workspace/project","env_strategy":"auto","prepare_command":"uv sync"}`))
	targetReq.Header.Set("Content-Type", "application/json")
	targetResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(targetResp, targetReq)
	if targetResp.Code != http.StatusCreated {
		t.Fatalf("create target status=%d body=%s", targetResp.Code, targetResp.Body.String())
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/project-definitions/project_launch", nil)
	detailResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("get project status=%d body=%s", detailResp.Code, detailResp.Body.String())
	}
	var detail struct {
		store.ProjectDefinition
		Targets []store.ProjectTarget `json:"targets"`
	}
	if err := json.NewDecoder(detailResp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != "project_launch" || detail.ZoteroCollectionKey != "SHUMTSPS" || detail.LiteratureServiceProfile != "mu-paperqa" || len(detail.Targets) != 1 || detail.Targets[0].PrepareCommand != "uv sync" {
		t.Fatalf("unexpected project detail: %#v", detail)
	}

	planReq := httptest.NewRequest(http.MethodPost, "/api/v1/project-definitions/project_launch/targets/target_launch/prepare-plan", nil)
	planResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(planResp, planReq)
	if planResp.Code != http.StatusOK || !strings.Contains(planResp.Body.String(), `"evidence_grade":"none"`) || !strings.Contains(planResp.Body.String(), `"command":"uv sync"`) {
		t.Fatalf("prepare plan status=%d body=%s", planResp.Code, planResp.Body.String())
	}
	prepareReq := httptest.NewRequest(http.MethodPost, "/api/v1/project-definitions/project_launch/targets/target_launch/prepare", nil)
	prepareResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(prepareResp, prepareReq)
	if prepareResp.Code != http.StatusServiceUnavailable {
		t.Fatalf("prepare without executor status=%d body=%s", prepareResp.Code, prepareResp.Body.String())
	}

	legacyReq := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	legacyResp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(legacyResp, legacyReq)
	if legacyResp.Code != http.StatusOK {
		t.Fatalf("legacy projects route regressed: status=%d body=%s", legacyResp.Code, legacyResp.Body.String())
	}
}

func TestDataCenterMetadataAPIReportsNoLocalDataPath(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_nas", Name: "nas", Type: "ssh", Host: "nas", RootDir: "/data", Status: store.ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveStorageTarget(ctx, &store.StorageTarget{ID: "storage_nas", Name: "nas", ResourceID: "rsrc_nas", RootPath: "/data/aexp"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveDatasetVersion(ctx, &store.DatasetVersion{ID: "dataset_one", DatasetID: "facade", Version: "v1", StorageTargetID: "storage_nas", StoragePath: "datasets/facade/v1"}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	for _, path := range []string{"/api/v1/storage-targets", "/api/v1/dataset-versions"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		var body map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if local, ok := body["local_data_path"].(bool); !ok || local {
			t.Fatalf("GET %s local_data_path=%#v", path, body["local_data_path"])
		}
	}
}

func TestCreateStorageTargetAPI(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_nas_create", Name: "nas-create", Type: "ssh", Host: "nas", RootDir: "/data", Status: store.ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-targets", bytes.NewBufferString(`{"name":"feiniu","resource_id":"rsrc_nas_create","root_path":"/data/aexp"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	target, err := db.GetStorageTargetByName(ctx, "feiniu")
	if err != nil || target == nil || target.RootPath != "/data/aexp" {
		t.Fatalf("saved target=%#v err=%v", target, err)
	}
}

func TestLogicalPathAndTransferAPIsKeepPlanSideEffectFree(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, resource := range []store.Resource{
		{ID: "api_nas", Name: "api-nas", Type: store.ResourceTypeSSH, Host: "nas", RootDir: "/vol/data"},
		{ID: "api_gpu", Name: "api-gpu", Type: store.ResourceTypeSSH, Host: "gpu", RootDir: "/scratch"},
	} {
		if err := db.CreateResource(ctx, &resource); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	target := &store.StorageTarget{ID: "api_storage", Name: "api-store", ResourceID: "api_nas", RootPath: "/vol/data/aexp", Health: &store.StorageTargetHealth{CheckedAt: now, DataPlane: []store.StorageDataPlaneHealth{{
		ResourceID: "api_gpu", CheckedAt: now,
		NASInitiated:     store.StorageConnectionHealth{Status: store.StorageStatusHealthy, SSHReachable: true, Rsync: true},
		ComputeInitiated: store.StorageConnectionHealth{Status: store.StorageStatusHealthy, SSHReachable: true, Rsync: true},
	}}}}
	if err := db.SaveStorageTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	files := filespace.NewService(db, apiFakeRemoteFS{})
	planner := transfer.NewPlanner(db, files)
	transfers := transfer.NewService(db, planner)
	srv := NewServer(db, nil, nil, slog.Default(), "", true, WithFileSpaceService(files), WithTransferServices(planner, transfers))
	handler := srv.Handler()

	rootBody := `{"id":"api_root","workspace":"api-project","prefix":"data","storage_target_id":"api_storage","physical_root":"projects/api/data"}`
	rootRec := httptest.NewRecorder()
	handler.ServeHTTP(rootRec, httptest.NewRequest(http.MethodPost, "/api/v1/logical-roots", bytes.NewBufferString(rootBody)))
	if rootRec.Code != http.StatusCreated {
		t.Fatalf("create root status=%d body=%s", rootRec.Code, rootRec.Body.String())
	}

	resolveRec := httptest.NewRecorder()
	handler.ServeHTTP(resolveRec, httptest.NewRequest(http.MethodPost, "/api/v1/paths/resolve", bytes.NewBufferString(`{"uri":"aexp://api-project/data/raw"}`)))
	if resolveRec.Code != http.StatusOK || !strings.Contains(resolveRec.Body.String(), `"physical_path":"/vol/data/aexp/projects/api/data/raw"`) {
		t.Fatalf("resolve status=%d body=%s", resolveRec.Code, resolveRec.Body.String())
	}
	if placements, err := db.ListPathPlacements(ctx, "aexp://api-project/data/raw"); err != nil || len(placements) != 0 {
		t.Fatalf("resolve wrote placements=%#v err=%v", placements, err)
	}

	hashRec := httptest.NewRecorder()
	handler.ServeHTTP(hashRec, httptest.NewRequest(http.MethodPost, "/api/v1/paths/hash", bytes.NewBufferString(`{"uri":"aexp://api-project/data/raw"}`)))
	if hashRec.Code != http.StatusOK || !strings.Contains(hashRec.Body.String(), `"revision":"sha256:api"`) {
		t.Fatalf("hash status=%d body=%s", hashRec.Code, hashRec.Body.String())
	}

	storageStatRec := httptest.NewRecorder()
	handler.ServeHTTP(storageStatRec, httptest.NewRequest(http.MethodGet, "/api/v1/storage/stat?uri=storage%3A%2F%2Fapi-store%2Fprojects%2Fapi%2Fdata%2Fraw", nil))
	if storageStatRec.Code != http.StatusOK || !strings.Contains(storageStatRec.Body.String(), `"role":"primary"`) || !strings.Contains(storageStatRec.Body.String(), `"state":"present"`) {
		t.Fatalf("storage stat status=%d body=%s", storageStatRec.Code, storageStatRec.Body.String())
	}
	storageListRec := httptest.NewRecorder()
	handler.ServeHTTP(storageListRec, httptest.NewRequest(http.MethodGet, "/api/v1/storage/list?uri=storage%3A%2F%2Fapi-store%2F&limit=50", nil))
	if storageListRec.Code != http.StatusOK || !strings.Contains(storageListRec.Body.String(), `"name":"a.csv"`) {
		t.Fatalf("storage list status=%d body=%s", storageListRec.Code, storageListRec.Body.String())
	}
	locationsRec := httptest.NewRecorder()
	handler.ServeHTTP(locationsRec, httptest.NewRequest(http.MethodGet, "/api/v1/storage/locations?uri=aexp%3A%2F%2Fapi-project%2Fdata%2Fraw", nil))
	if locationsRec.Code != http.StatusOK || !strings.Contains(locationsRec.Body.String(), `"role":"primary"`) || !strings.Contains(locationsRec.Body.String(), `"total":1`) {
		t.Fatalf("storage locations status=%d body=%s", locationsRec.Code, locationsRec.Body.String())
	}
	copyRec := httptest.NewRecorder()
	handler.ServeHTTP(copyRec, httptest.NewRequest(http.MethodPost, "/api/v1/storage/copy", bytes.NewBufferString(`{"source":"storage://api-store/projects/api/data/raw","destination":"resource://api-gpu/cache/copied"}`)))
	if copyRec.Code != http.StatusAccepted || !strings.Contains(copyRec.Body.String(), `"accepted":true`) || !strings.Contains(copyRec.Body.String(), `"source_revision":"sha256:api"`) {
		t.Fatalf("storage copy status=%d body=%s", copyRec.Code, copyRec.Body.String())
	}
	blockedCopyRec := httptest.NewRecorder()
	handler.ServeHTTP(blockedCopyRec, httptest.NewRequest(http.MethodPost, "/api/v1/storage/copy", bytes.NewBufferString(`{"source":"resource://api-gpu/missing","destination":"storage://api-store/blocked"}`)))
	if blockedCopyRec.Code != http.StatusUnprocessableEntity || !strings.Contains(blockedCopyRec.Body.String(), `"accepted":false`) || !strings.Contains(blockedCopyRec.Body.String(), `"source_not_present"`) {
		t.Fatalf("blocked storage copy status=%d body=%s", blockedCopyRec.Code, blockedCopyRec.Body.String())
	}

	planRequest := `{"source":"aexp://api-project/data/raw","destination":"resource://api-gpu/cache/raw","verification":"manifest"}`
	planRec := httptest.NewRecorder()
	handler.ServeHTTP(planRec, httptest.NewRequest(http.MethodPost, "/api/v1/transfers/plan", bytes.NewBufferString(planRequest)))
	if planRec.Code != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", planRec.Code, planRec.Body.String())
	}
	var plan transfer.Plan
	if err := json.Unmarshal(planRec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.PlanSHA256 == "" || len(plan.Blockers) != 0 || plan.LocalDataPath {
		t.Fatalf("plan=%#v", plan)
	}
	if stored, err := db.GetTransferPlan(ctx, plan.PlanSHA256); err != nil || stored != nil {
		t.Fatalf("plan endpoint had side effect stored=%#v err=%v", stored, err)
	}

	var create map[string]any
	if err := json.Unmarshal([]byte(planRequest), &create); err != nil {
		t.Fatal(err)
	}
	create["expected_plan_sha256"] = plan.PlanSHA256
	createBody, _ := json.Marshal(create)
	createRec := httptest.NewRecorder()
	handler.ServeHTTP(createRec, httptest.NewRequest(http.MethodPost, "/api/v1/transfers", bytes.NewReader(createBody)))
	if createRec.Code != http.StatusAccepted || !strings.Contains(createRec.Body.String(), `"state":"queued"`) {
		t.Fatalf("create transfer status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	ensureRec := httptest.NewRecorder()
	handler.ServeHTTP(ensureRec, httptest.NewRequest(http.MethodPost, "/api/v1/paths/ensure", bytes.NewReader(createBody)))
	if ensureRec.Code != http.StatusAccepted || !strings.Contains(ensureRec.Body.String(), `"created":false`) {
		t.Fatalf("ensure path status=%d body=%s", ensureRec.Code, ensureRec.Body.String())
	}
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/v1/transfers?workspace=api-project&limit=1&cursor=0", nil))
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), `"payload_direction":"source_to_destination"`) || !strings.Contains(listRec.Body.String(), `"next_cursor":1`) {
		t.Fatalf("transfer list status=%d body=%s", listRec.Code, listRec.Body.String())
	}

	stale := create
	stale["expected_plan_sha256"] = "sha256:stale"
	staleBody, _ := json.Marshal(stale)
	staleRec := httptest.NewRecorder()
	handler.ServeHTTP(staleRec, httptest.NewRequest(http.MethodPost, "/api/v1/transfers", bytes.NewReader(staleBody)))
	if staleRec.Code != http.StatusConflict || !strings.Contains(staleRec.Body.String(), `"TRANSFER_PLAN_CHANGED"`) {
		t.Fatalf("stale transfer status=%d body=%s", staleRec.Code, staleRec.Body.String())
	}
}

func TestUpdateAndDeleteStorageTargetAPI(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_manage_nas", Name: "manage-nas", Type: "ssh", Host: "nas", RootDir: "/data"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveStorageTarget(ctx, &store.StorageTarget{ID: "storage_manage_nas", Name: "old", ResourceID: "rsrc_manage_nas", RootPath: "/data/old"}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, nil, slog.Default(), "", true)

	update := httptest.NewRequest(http.MethodPut, "/api/v1/storage-targets/storage_manage_nas", bytes.NewBufferString(`{"name":"new","resource_id":"rsrc_manage_nas","root_path":"/data/new"}`))
	updateRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(updateRec, update)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}
	got, _ := db.GetStorageTarget(ctx, "storage_manage_nas")
	if got == nil || got.Name != "new" || got.RootPath != "/data/new" {
		t.Fatalf("updated target=%#v", got)
	}

	remove := httptest.NewRequest(http.MethodDelete, "/api/v1/storage-targets/storage_manage_nas", nil)
	removeRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(removeRec, remove)
	if removeRec.Code != http.StatusOK || !strings.Contains(removeRec.Body.String(), `"nas_data_deleted":false`) {
		t.Fatalf("delete status=%d body=%s", removeRec.Code, removeRec.Body.String())
	}
	resource, _ := db.GetResource(ctx, "rsrc_manage_nas")
	if resource == nil {
		t.Fatal("backing resource must be preserved")
	}
}

func TestDeleteStorageTargetAPIRejectsEvidenceDependencies(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_used_nas", Name: "used-nas", Type: "ssh", Host: "nas", RootDir: "/data"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveStorageTarget(ctx, &store.StorageTarget{ID: "storage_used_nas", Name: "used", ResourceID: "rsrc_used_nas", RootPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveDatasetVersion(ctx, &store.DatasetVersion{ID: "dataset_used", DatasetID: "d", Version: "v1", StorageTargetID: "storage_used_nas", StoragePath: "d/v1"}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/storage-targets/storage_used_nas", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"dataset_versions":1`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := db.GetStorageTarget(ctx, "storage_used_nas")
	if got == nil {
		t.Fatal("referenced target was deleted")
	}
}

func TestParseStorageHealthDistinguishesDegradedAndUnreachable(t *testing.T) {
	healthy := parseStorageHealth("hostname\tnas\nrsync\tok\nroot_exists\tok\nroot_read\tok\nroot_write\tok\ndf\t/dev/x\t100\t25\t75\t25\n", "", nil, 25*time.Millisecond)
	if healthy.Status != store.StorageStatusHealthy || healthy.AvailableBytes != 75*1024 || !healthy.Checks["rsync"].OK {
		t.Fatalf("healthy=%#v", healthy)
	}
	degraded := parseStorageHealth("hostname\tnas\nrsync\tmissing\nroot_exists\tok\nroot_read\tok\nroot_write\tok\ndf\t/dev/x\t100\t25\t75\t25\n", "", nil, time.Millisecond)
	if degraded.Status != store.StorageStatusDegraded || degraded.ControlPlane != store.StorageStatusDegraded {
		t.Fatalf("degraded=%#v", degraded)
	}
	unreachable := parseStorageHealth("", "timeout", context.DeadlineExceeded, time.Second)
	if unreachable.Status != store.StorageStatusUnreachable || unreachable.Checks["ssh"].OK {
		t.Fatalf("unreachable=%#v", unreachable)
	}
}

func TestRunFreezeReadAPIs(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateResource(ctx, &store.Resource{ID: "rfreeze", Name: "rfreeze", Type: "ssh", Host: "localhost", RootDir: "/w"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "run_api_freeze", ResourceID: "rfreeze", Status: store.RunStatusSucceeded, Kind: store.RunKindFormal, EvidenceGrade: store.RunEvidenceGradeFormal, Command: "x"}); err != nil {
		t.Fatal(err)
	}
	f := &store.RunFreeze{ID: "freeze_api", RunID: "run_api_freeze", Profile: "paper", ProfileSHA256: "sha256:p", PlanSHA256: "sha256:plan", DestinationURI: "storage://nas/paper", RunManifestSHA256: "sha256:run"}
	if err := db.CreateRunFreeze(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceRunFreezeFiles(ctx, f.ID, []store.RunFreezeFile{{ID: "file_api", FreezeID: f.ID, Kind: "raw", Role: "metrics", RelativePath: "metrics.json", SourceURI: "ssh://r/metrics.json", SHA256: "sha256:file"}}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	for _, path := range []string{"/api/v1/runs/run_api_freeze/freezes", "/api/v1/freezes/freeze_api", "/api/v1/freezes/freeze_api/manifest"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestRunComparisonAggregatesSeedsAndBuildsReport(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AEXP_EVENT_CACHE_DIR", t.TempDir())
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_compare", Name: "compare", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: store.ResourceStatusUnreachable}); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run_seed_a", "run_seed_b"} {
		run := &store.Run{ID: runID, ResourceID: "rsrc_compare", ProjectID: "project_compare", TargetID: "target_compare", RecipeName: "train", Name: runID, Status: store.RunStatusSucceeded, TaskRole: store.RunTaskRoleTrain, EvidenceGrade: store.RunEvidenceGradeFormal, ExperimentRole: store.RunExperimentRoleTreatment, Command: "python train.py", UIEventsPath: ".aexp/events/" + runID + ".jsonl", GitCommit: "abcdef", ResolvedEnv: "uv", ResolvedPython: "/ws/.venv/bin/python"}
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if err := db.SaveRunManifest(ctx, &store.RunManifest{RunID: runID, SchemaVersion: 1, State: store.RunManifestFinal, ManifestJSON: `{}`, SHA256: "sha256:" + runID, Completeness: store.RunManifestCompletenessCurrent, FinalizedAt: &now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eventcache.Write("run_seed_a", []eventcache.Line{{LineNo: 1, Content: `{"type":"metric","name":"test/mse","value":1,"seed":1}`}, {LineNo: 2, Content: `{"type":"metric","name":"test/mse","value":3,"seed":2}`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eventcache.Write("run_seed_b", []eventcache.Line{{LineNo: 1, Content: `{"type":"metric","name":"test/mse","value":2,"seed":1}`}, {LineNo: 2, Content: `{"type":"metric","name":"test/mse","value":4,"seed":2}`}}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/run-comparisons/analyze", bytes.NewBufferString(`{"run_ids":["run_seed_a","run_seed_b"],"metric_key":"test/mse"}`))
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("comparison status=%d body=%s", resp.Code, resp.Body.String())
	}
	var analysis runComparisonAnalysis
	if err := json.NewDecoder(resp.Body).Decode(&analysis); err != nil {
		t.Fatal(err)
	}
	if !analysis.StructurallyComparable || !analysis.ClaimReady || len(analysis.Aggregates) != 2 {
		t.Fatalf("unexpected comparison analysis: %#v", analysis)
	}
	if analysis.Aggregates[0].Count != 2 || analysis.Aggregates[0].Mean != 2 || !strings.Contains(analysis.ReportMarkdown, "Seed aggregates") {
		t.Fatalf("unexpected seed aggregate/report: %#v", analysis)
	}
}

func TestGetUIEventLogsPrefersLocalCacheForTerminalRuns(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AEXP_EVENT_CACHE_DIR", t.TempDir())
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_event_cache_online",
		Name:    "online-resource",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:           "run_event_cache_online",
		ResourceID:   "rsrc_event_cache_online",
		Name:         "cached-events-online",
		Status:       store.RunStatusSucceeded,
		Command:      "python train.py",
		UIEventsPath: ".aexp/events/run_event_cache_online.jsonl",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := eventcache.Write("run_event_cache_online", []eventcache.Line{
		{LineNo: 4, Content: `{"type":"metric","name":"train/loss","value":0.1}`},
	}); err != nil {
		t.Fatalf("cache event: %v", err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_event_cache_online/logs?path=.aexp/events/run_event_cache_online.jsonl&limit=50&tail=true", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Remote    bool            `json:"remote"`
		ErrorKind string          `json:"error_kind"`
		Lines     []store.LogLine `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if body.Remote {
		t.Fatalf("remote = true, want local cache")
	}
	if body.ErrorKind != "" {
		t.Fatalf("error_kind = %q, want empty for terminal cache-first event logs", body.ErrorKind)
	}
	if len(body.Lines) != 1 || !strings.Contains(body.Lines[0].Content, "train/loss") {
		t.Fatalf("cached lines missing: %#v", body.Lines)
	}
}

func TestGetUIEventLogsAfterLineReturnsStableCursor(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AEXP_EVENT_CACHE_DIR", t.TempDir())
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_cursor", Name: "cursor-resource", Type: "ssh", Host: "localhost", RootDir: "/ws", Status: store.ResourceStatusIdle}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "run_cursor", ResourceID: "rsrc_cursor", Status: store.RunStatusSucceeded, Command: "python train.py", UIEventsPath: ".aexp/events/run_cursor.jsonl"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eventcache.Write("run_cursor", []eventcache.Line{{LineNo: 1, Content: "one"}, {LineNo: 2, Content: "two"}, {LineNo: 3, Content: "three"}}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_cursor/logs?path=.aexp/events/run_cursor.jsonl&limit=50&tail=true&after_line=2", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Lines      []store.LogLine `json:"lines"`
		NextCursor int             `json:"next_cursor"`
		Reset      bool            `json:"reset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Reset || body.NextCursor != 3 || len(body.Lines) != 1 || body.Lines[0].LineNo != 3 || body.Lines[0].Content != "three" {
		t.Fatalf("cursor response = %#v", body)
	}
}

func TestListRunsMetaResponseIsOptIn(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_runs_meta",
		Name:    "meta-resource",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []*store.Run{
		{ID: "run_meta_1", ResourceID: "rsrc_runs_meta", Name: "first", Status: store.RunStatusSucceeded, Command: "echo 1"},
		{ID: "run_meta_2", ResourceID: "rsrc_runs_meta", Name: "second", Status: store.RunStatusRunning, Command: "echo 2"},
	} {
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	legacyReq := httptest.NewRequest(http.MethodGet, "/api/v1/runs?limit=1&refresh=false", nil)
	legacyRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(legacyRec, legacyReq)
	if legacyRec.Code != http.StatusOK {
		t.Fatalf("legacy status = %d body=%s", legacyRec.Code, legacyRec.Body.String())
	}
	var legacy []store.Run
	if err := json.Unmarshal(legacyRec.Body.Bytes(), &legacy); err != nil {
		t.Fatalf("legacy response should remain an array: %v body=%s", err, legacyRec.Body.String())
	}
	if len(legacy) != 1 {
		t.Fatalf("legacy len = %d, want 1", len(legacy))
	}

	metaReq := httptest.NewRequest(http.MethodGet, "/api/v1/runs?limit=1&offset=1&refresh=false&meta=true", nil)
	metaRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(metaRec, metaReq)
	if metaRec.Code != http.StatusOK {
		t.Fatalf("meta status = %d body=%s", metaRec.Code, metaRec.Body.String())
	}
	var meta struct {
		Items  []store.Run `json:"items"`
		Total  int         `json:"total"`
		Limit  int         `json:"limit"`
		Offset int         `json:"offset"`
	}
	if err := json.Unmarshal(metaRec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("decode meta: %v body=%s", err, metaRec.Body.String())
	}
	if len(meta.Items) != 1 || meta.Total != 2 || meta.Limit != 1 || meta.Offset != 1 {
		t.Fatalf("unexpected meta payload: %#v", meta)
	}
}

func TestResearchOSIsTheOnlyWebEntrypoint(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	for _, path := range []string{"/", "/index.html"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/ui-v2/" {
			t.Fatalf("%s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{"/ui-v2/", "/ui-v2/runs/test"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ResearchOS") {
			t.Fatalf("%s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "aexp UI v2") {
			t.Fatalf("%s still exposes legacy branding", path)
		}
	}
}

func TestRunDetailSummaryEndpointsAvoidLargeInitialPayloads(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_detail", Name: "detail", Type: "local", Host: "127.0.0.1", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{ID: "run_detail", ResourceID: "rsrc_detail", Status: store.RunStatusSucceeded, Command: "true"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.SaveArtifacts(ctx, "run_detail", []store.Artifact{
		{ID: "artifact_a", Path: "/tmp/a", Type: "file", Size: 1, CollectionState: store.ArtifactCollectionIndexed, DiscoveredAt: now, ModifiedAt: now},
		{ID: "artifact_b", Path: "/tmp/b", Type: "file", Size: 2, CollectionState: store.ArtifactCollectionIndexed, DiscoveredAt: now, ModifiedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunManifest(ctx, &store.RunManifest{RunID: "run_detail", SchemaVersion: 1, State: store.RunManifestFinal, ManifestJSON: strings.Repeat("x", 32_000), SHA256: "sha256:detail", Completeness: store.RunManifestCompletenessCurrent, FinalizedAt: &now}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	artifacts := httptest.NewRecorder()
	srv.Handler().ServeHTTP(artifacts, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_detail/artifacts?limit=1", nil))
	if artifacts.Code != http.StatusOK {
		t.Fatalf("artifacts status=%d body=%s", artifacts.Code, artifacts.Body.String())
	}
	var items []store.Artifact
	if err := json.Unmarshal(artifacts.Body.Bytes(), &items); err != nil || len(items) != 1 {
		t.Fatalf("limited artifacts=%#v err=%v", items, err)
	}

	manifest := httptest.NewRecorder()
	srv.Handler().ServeHTTP(manifest, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_detail/manifest?summary=true", nil))
	if manifest.Code != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", manifest.Code, manifest.Body.String())
	}
	if strings.Contains(manifest.Body.String(), "manifest_json") || !strings.Contains(manifest.Body.String(), "sha256:detail") {
		t.Fatalf("unexpected manifest summary: %s", manifest.Body.String())
	}
}

func TestStatsCountsRunsWithoutChangingVisibilitySemantics(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateResource(ctx, &store.Resource{ID: "rsrc_stats", Name: "stats", Type: "local", Host: "127.0.0.1", RootDir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []*store.Run{
		{ID: "run_running", ResourceID: "rsrc_stats", Status: store.RunStatusRunning, Command: "sleep 1"},
		{ID: "run_done", ResourceID: "rsrc_stats", Status: store.RunStatusSucceeded, Command: "true"},
		{ID: "run_archived", ResourceID: "rsrc_stats", Status: store.RunStatusSucceeded, Command: "true"},
	} {
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ArchiveRun(ctx, "run_archived"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	NewServer(db, nil, nil, slog.Default(), "", true).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stats status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["total_runs"] != 2 || payload["active_runs"] != 1 || payload["total_resources"] != 1 {
		t.Fatalf("stats=%#v", payload)
	}
}

func TestProjectViewsEnrichCardsWithRunsAndMarks(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateProjectDefinition(ctx, &store.ProjectDefinition{ID: "dam-imputation", Name: "Dam Imputation"}); err != nil {
		t.Fatal(err)
	}

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_project_api",
		Name:    "mu",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_project_api",
		ResourceID: "rsrc_project_api",
		Name:       "caf-100e",
		Kind:       store.RunKindFormal,
		Status:     store.RunStatusSucceeded,
		Command:    "python train.py",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMark(ctx, &store.RunMark{
		ID:     "mark_project_api",
		RunID:  "run_project_api",
		Actor:  "agent",
		Kind:   "key_result",
		Title:  "CAF improves mAP",
		Reason: "mAP50-95 improved.",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProjectRunCard(ctx, &store.ProjectRunCard{
		ID:            "card_project_api",
		ProjectID:     "dam-imputation",
		ProjectName:   "Dam Imputation",
		RunID:         "run_project_api",
		Question:      "Does CAF help?",
		Verdict:       "CAF improves mAP.",
		EvidenceLevel: "B",
		KeyMetrics:    "mAP50-95=0.606",
		Important:     true,
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	projects, err := srv.projectViews(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects = %#v, want one", projects)
	}
	project := projects[0]
	if project.ProjectID != "dam-imputation" || project.TotalCards != 1 || project.ImportantRuns != 1 || project.FormalRuns != 1 {
		t.Fatalf("unexpected project aggregate: %#v", project)
	}
	if len(project.Cards) != 1 || project.Cards[0].Run == nil || project.Cards[0].Run.Name != "caf-100e" {
		t.Fatalf("card run not enriched: %#v", project.Cards)
	}
	if len(project.Cards[0].Marks) != 1 || project.Cards[0].Marks[0].Title != "CAF improves mAP" {
		t.Fatalf("card marks not enriched: %#v", project.Cards[0].Marks)
	}
}

func TestManualProjectCategoryAPI(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_manual_project_api",
		Name:    "mu-manual",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_manual_project_api",
		ResourceID: "rsrc_manual_project_api",
		Name:       "manual-category-run",
		Kind:       store.RunKindAblation,
		Status:     store.RunStatusSucceeded,
		Command:    "python train.py",
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/manual-project-categories", bytes.NewBufferString(`{"name":"Dam downstream","description":"Manual bucket"}`))
	createRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusGone || !strings.Contains(createRec.Body.String(), "MANUAL_PROJECT_WRITE_DEPRECATED") {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	category := store.ManualProjectCategory{ID: "legacy_manual", Name: "Dam downstream", Description: "Legacy bucket"}
	if err := db.CreateManualProjectCategory(ctx, &category); err != nil {
		t.Fatal(err)
	}
	if err := db.AssignRunToManualProjectCategory(ctx, "run_manual_project_api", category.ID); err != nil {
		t.Fatal(err)
	}

	assignBody := fmt.Sprintf(`{"category_id":%q}`, category.ID)
	assignReq := httptest.NewRequest(http.MethodPut, "/api/v1/runs/run_manual_project_api/manual-project-category", bytes.NewBufferString(assignBody))
	assignRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(assignRec, assignReq)
	if assignRec.Code != http.StatusGone || !strings.Contains(assignRec.Body.String(), "MANUAL_PROJECT_WRITE_DEPRECATED") {
		t.Fatalf("assign status = %d body=%s", assignRec.Code, assignRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/manual-project-categories", nil)
	listRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var categories []store.ManualProjectCategory
	if err := json.Unmarshal(listRec.Body.Bytes(), &categories); err != nil {
		t.Fatalf("decode categories: %v", err)
	}
	if len(categories) != 1 || categories[0].RunCount != 1 {
		t.Fatalf("categories = %#v, want one category with one run", categories)
	}

	assignmentsReq := httptest.NewRequest(http.MethodGet, "/api/v1/manual-run-project-assignments", nil)
	assignmentsRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(assignmentsRec, assignmentsReq)
	if assignmentsRec.Code != http.StatusOK {
		t.Fatalf("assignments status = %d body=%s", assignmentsRec.Code, assignmentsRec.Body.String())
	}
	var assignments []store.RunProjectAssignment
	if err := json.Unmarshal(assignmentsRec.Body.Bytes(), &assignments); err != nil {
		t.Fatalf("decode assignments: %v", err)
	}
	if len(assignments) != 1 || assignments[0].RunID != "run_manual_project_api" {
		t.Fatalf("assignments = %#v, want run_manual_project_api", assignments)
	}

	unassignReq := httptest.NewRequest(http.MethodDelete, "/api/v1/runs/run_manual_project_api/manual-project-category", nil)
	unassignRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unassignRec, unassignReq)
	if unassignRec.Code != http.StatusNoContent {
		t.Fatalf("unassign status = %d body=%s", unassignRec.Code, unassignRec.Body.String())
	}
	got, err := db.GetRunProjectAssignment(ctx, "run_manual_project_api")
	if err != nil {
		t.Fatalf("GetRunProjectAssignment: %v", err)
	}
	if got != nil {
		t.Fatalf("assignment after unassign = %#v, want nil", got)
	}
}

func TestRunMarkAttachmentBlob(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_mark_blob",
		Name:    "mark-resource",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_mark_blob",
		ResourceID: "rsrc_mark_blob",
		Name:       "blob-run",
		Status:     store.RunStatusSucceeded,
		Command:    "python train.py",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMark(ctx, &store.RunMark{
		ID:        "mark_blob",
		RunID:     "run_mark_blob",
		Actor:     "agent",
		Kind:      "key_result",
		Title:     "Plot attached",
		Statement: "Diagnostic plot is stored locally.",
		BodyMD:    "![plot](aexp-attachment://att_blob)",
	}); err != nil {
		t.Fatal(err)
	}
	attachmentPath := filepath.Join(t.TempDir(), "plot.svg")
	attachmentBytes := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><rect width="8" height="8"/></svg>`)
	if err := os.WriteFile(attachmentPath, attachmentBytes, 0644); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRunMarkAttachments(ctx, "mark_blob", []store.RunMarkAttachment{{
		ID:        "att_blob",
		MarkID:    "mark_blob",
		Filename:  "plot.svg",
		LocalPath: attachmentPath,
		Mime:      "image/svg+xml",
		Caption:   "plot",
		Size:      int64(len(attachmentBytes)),
	}}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/run-marks/mark_blob/attachments/att_blob/blob", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("content type = %q, want image/svg+xml", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), attachmentBytes) {
		t.Fatalf("body = %q, want attachment bytes", rec.Body.String())
	}
}

func TestProjectViewsIncludesUnassignedRuns(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateProjectDefinition(ctx, &store.ProjectDefinition{ID: "dam-imputation", Name: "Dam Imputation"}); err != nil {
		t.Fatal(err)
	}

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_unassigned_api",
		Name:    "mu",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_with_project_card",
		ResourceID: "rsrc_unassigned_api",
		Name:       "formal-carded",
		Kind:       store.RunKindFormal,
		Status:     store.RunStatusSucceeded,
		Command:    "python train.py",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRun(ctx, &store.Run{
		ID:         "run_without_project_card",
		ResourceID: "rsrc_unassigned_api",
		Name:       "scratch-check",
		Kind:       store.RunKindSetup,
		Status:     store.RunStatusRunning,
		GPUIndex:   -2,
		Command:    "ls",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProjectRunCard(ctx, &store.ProjectRunCard{
		ID:          "card_assigned_api",
		ProjectID:   "dam-imputation",
		ProjectName: "Dam Imputation",
		RunID:       "run_with_project_card",
		Verdict:     "Assigned run.",
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	projects, err := srv.projectViews(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	var unassigned *projectView
	for i := range projects {
		if projects[i].ProjectID == unassignedProjectID {
			unassigned = &projects[i]
			break
		}
	}
	if unassigned == nil {
		t.Fatalf("missing unassigned project: %#v", projects)
	}
	if len(unassigned.Cards) != 1 {
		t.Fatalf("unassigned cards = %#v, want exactly one", unassigned.Cards)
	}
	if unassigned.Cards[0].Run == nil || unassigned.Cards[0].Run.ID != "run_without_project_card" {
		t.Fatalf("unexpected unassigned run: %#v", unassigned.Cards[0])
	}
	if unassigned.RunningRuns != 1 {
		t.Fatalf("unassigned running runs = %d, want 1", unassigned.RunningRuns)
	}
}

func TestProjectViewsDoNotTreatManualAssignmentsAsCanonicalOwnership(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateProjectDefinition(ctx, &store.ProjectDefinition{ID: "auto-project", Name: "Auto Project"}); err != nil {
		t.Fatal(err)
	}

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_manual_project_view",
		Name:    "mu",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []*store.Run{
		{ID: "run_manual_override_card", ResourceID: "rsrc_manual_project_view", Name: "carded", Kind: store.RunKindFormal, Status: store.RunStatusSucceeded, Command: "python train.py"},
		{ID: "run_manual_without_card", ResourceID: "rsrc_manual_project_view", Name: "free", Kind: store.RunKindPilot, Status: store.RunStatusSucceeded, Command: "python pilot.py"},
		{ID: "run_still_unassigned", ResourceID: "rsrc_manual_project_view", Name: "scratch", Kind: store.RunKindSetup, Status: store.RunStatusRunning, Command: "ls"},
	} {
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveProjectRunCard(ctx, &store.ProjectRunCard{
		ID:            "card_manual_override",
		ProjectID:     "auto-project",
		ProjectName:   "Auto Project",
		RunID:         "run_manual_override_card",
		Verdict:       "Auto card verdict.",
		EvidenceLevel: "B",
		Important:     true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateManualProjectCategory(ctx, &store.ManualProjectCategory{
		ID:   "mpc_manual_view",
		Name: "Auto Project",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AssignRunToManualProjectCategory(ctx, "run_manual_override_card", "mpc_manual_view"); err != nil {
		t.Fatal(err)
	}
	if err := db.AssignRunToManualProjectCategory(ctx, "run_manual_without_card", "mpc_manual_view"); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	projects, err := srv.projectViews(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	var auto, manualCategoryDuplicate, unassigned *projectView
	for i := range projects {
		switch projects[i].ProjectID {
		case "mpc_manual_view":
			manualCategoryDuplicate = &projects[i]
		case "auto-project":
			auto = &projects[i]
		case unassignedProjectID:
			unassigned = &projects[i]
		}
	}
	if auto == nil || auto.ProjectName != "Auto Project" || len(auto.Cards) != 1 {
		t.Fatalf("auto project = %#v, want only its canonical card", auto)
	}
	if auto.TotalCards != 1 || auto.ImportantRuns != 1 || auto.FormalRuns != 1 {
		t.Fatalf("auto aggregates = %#v, want preserved card metadata and run enrichment", auto)
	}
	if manualCategoryDuplicate != nil {
		t.Fatalf("manual category became a canonical project: %#v", manualCategoryDuplicate)
	}
	if unassigned == nil || len(unassigned.Cards) != 2 {
		t.Fatalf("unassigned project = %#v, want both runs without canonical ownership", unassigned)
	}

	filtered, err := srv.projectViews(ctx, "auto-project", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ProjectID != "auto-project" || len(filtered[0].Cards) != 1 {
		t.Fatalf("filtered project = %#v, want only the canonical auto-project card", filtered)
	}
}

func TestEvidenceChainAPIAndValidation(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_evidence_api",
		Name:    "mu",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateProjectDefinition(ctx, &store.ProjectDefinition{ID: "dam", Name: "Dam"}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []*store.Run{
		{ID: "run_card_candidate", ResourceID: "rsrc_evidence_api", ProjectID: "dam", Name: "carded", Kind: store.RunKindFormal, Status: store.RunStatusSucceeded, Command: "python train.py"},
		{ID: "run_free_candidate", ResourceID: "rsrc_evidence_api", ProjectID: "dam", Name: "free pilot", Kind: store.RunKindPilot, Status: store.RunStatusSucceeded, Command: "python pilot.py"},
	} {
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveProjectRunCard(ctx, &store.ProjectRunCard{
		ID:            "card_candidate",
		ProjectID:     "dam",
		ProjectName:   "Dam",
		RunID:         "run_card_candidate",
		Question:      "Does it help?",
		Verdict:       "It helps.",
		EvidenceLevel: "B",
		KeyMetrics:    "mAP=0.6",
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	createBody := bytes.NewBufferString(`{"title":"IR anchor","description":"fusion reasoning","project_id":"dam","routing_hints":{"recipes":["formal-ir"],"keywords":["paired","fusion"]}}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/evidence-chains", createBody)
	createRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	var chain store.EvidenceChain
	if err := json.Unmarshal(createRec.Body.Bytes(), &chain); err != nil {
		t.Fatalf("decode chain: %v", err)
	}
	if chain.ID == "" || chain.Title != "IR anchor" {
		t.Fatalf("unexpected chain: %#v", chain)
	}
	if len(chain.RoutingHints.Recipes) != 1 || chain.RoutingHints.Recipes[0] != "formal-ir" ||
		len(chain.RoutingHints.Keywords) != 2 {
		t.Fatalf("routing hints were not persisted: %#v", chain.RoutingHints)
	}

	candidatesReq := httptest.NewRequest(http.MethodGet, "/api/v1/evidence-run-candidates?limit=10", nil)
	candidatesRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(candidatesRec, candidatesReq)
	if candidatesRec.Code != http.StatusOK {
		t.Fatalf("candidates status = %d body=%s", candidatesRec.Code, candidatesRec.Body.String())
	}
	var candidates []store.EvidenceChainRunCandidate
	if err := json.Unmarshal(candidatesRec.Body.Bytes(), &candidates); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	if len(candidates) < 2 || candidates[0].Kind != "project_card" || candidates[0].RunID != "run_card_candidate" {
		t.Fatalf("candidates = %#v, want project card first", candidates)
	}

	graphBody := `{
		"nodes":[
			{"id":"node_h","type":"hypothesis","title":"IR anchors fusion","x":10,"y":20},
			{"id":"node_r","type":"run","title":"carded","run_id":"run_card_candidate","project_card_id":"card_candidate","x":320,"y":20}
		],
		"edges":[{"id":"edge_1","source_node_id":"node_r","target_node_id":"node_h","type":"supports","label":"supports","rationale":"mAP improved"}]
	}`
	saveReq := httptest.NewRequest(http.MethodPut, "/api/v1/evidence-chains/"+chain.ID+"/graph", bytes.NewBufferString(graphBody))
	saveRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(saveRec, saveReq)
	if saveRec.Code != http.StatusBadRequest || !strings.Contains(saveRec.Body.String(), "EXPECTED_REVISION_REQUIRED") {
		t.Fatalf("semantic direct save status = %d body=%s", saveRec.Code, saveRec.Body.String())
	}
	seedGraph := store.EvidenceChainGraph{
		Nodes: []store.EvidenceChainNode{
			{ID: "node_h", Type: store.EvidenceNodeHypothesis, Title: "IR anchors fusion", X: 10, Y: 20},
			{ID: "node_r", Type: store.EvidenceNodeRun, Title: "carded", RunID: "run_card_candidate", ProjectCardID: "card_candidate", X: 320, Y: 20},
		},
		Edges: []store.EvidenceChainEdge{{ID: "edge_1", SourceNodeID: "node_r", TargetNodeID: "node_h", Type: store.EvidenceEdgeSupports, Label: "supports", Rationale: "mAP improved"}},
	}
	seeded, err := db.SaveEvidenceChainGraphCAS(context.Background(), chain.ID, seedGraph, store.EvidenceGraphSaveOptions{
		ExpectedRevision: 0, Actor: "test-migration", SourceKind: "legacy-client",
	})
	if err != nil {
		t.Fatal(err)
	}
	layoutBody := strings.Replace(graphBody, `"nodes":[`, fmt.Sprintf(`"expected_revision":%d,"nodes":[`, seeded.Revision), 1)
	saveReq = httptest.NewRequest(http.MethodPut, "/api/v1/evidence-chains/"+chain.ID+"/graph", bytes.NewBufferString(layoutBody))
	saveRec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(saveRec, saveReq)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("layout save status = %d body=%s", saveRec.Code, saveRec.Body.String())
	}
	var detail struct {
		store.EvidenceChain
		Nodes []store.EvidenceChainNode `json:"nodes"`
		Edges []store.EvidenceChainEdge `json:"edges"`
	}
	if err := json.Unmarshal(saveRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if len(detail.Nodes) != 2 || len(detail.Edges) != 1 {
		t.Fatalf("detail = %#v, want saved graph", detail)
	}
	if detail.Revision != 1 || detail.GraphHash == "" {
		t.Fatalf("detail revision/hash = %#v", detail.EvidenceChain)
	}
	threadsReq := httptest.NewRequest(http.MethodGet, "/api/v1/evidence-chains/"+chain.ID+"/threads", nil)
	threadsRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(threadsRec, threadsReq)
	if threadsRec.Code != http.StatusOK {
		t.Fatalf("threads status = %d body=%s", threadsRec.Code, threadsRec.Body.String())
	}
	var research store.EvidenceResearchProjection
	if err := json.Unmarshal(threadsRec.Body.Bytes(), &research); err != nil {
		t.Fatalf("decode research threads: %v", err)
	}
	if research.Revision != detail.Revision || research.GraphHash != detail.GraphHash || len(research.Threads) != 1 || research.Threads[0].RootNodeID != "node_h" {
		t.Fatalf("research projection = %#v", research)
	}
	if !reflect.DeepEqual(research.PresentationStages, []string{"hypothesis", "design", "result", "interpretation", "outcome"}) {
		t.Fatalf("research presentation stages = %#v", research.PresentationStages)
	}
	if research.Capacity.PolicyVersion != store.EvidenceTopicCapacityPolicyVersion || research.Capacity.Status != "healthy" || research.Capacity.TooLarge || research.Capacity.SplitRecommended || research.Capacity.ThreadCount != 1 || research.Capacity.SuggestedTopicCount != 1 || len(research.Capacity.ThreadFamilies) != 1 || research.Capacity.ThreadFamilies[0].RootThreadID != "thread:node_h" {
		t.Fatalf("research capacity = %#v", research.Capacity)
	}
	if research.StructuralHealth.PolicyVersion != store.EvidenceResearchHealthPolicyVersion || research.StructuralHealth.CompatibilityStatus != "legacy_readable" {
		t.Fatalf("research health = %#v", research.StructuralHealth)
	}
	auditReq := httptest.NewRequest(http.MethodGet, "/api/v1/evidence-chains/"+chain.ID+"/audit", nil)
	auditRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK {
		t.Fatalf("audit status = %d body=%s", auditRec.Code, auditRec.Body.String())
	}
	var audit store.EvidenceChainAuditReport
	if err := json.Unmarshal(auditRec.Body.Bytes(), &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if audit.SchemaVersion != "evidence-map-audit-v1" || len(audit.Warnings) == 0 || audit.ResearchHealth.PolicyVersion != store.EvidenceResearchHealthPolicyVersion || audit.ReadabilityStatus != "legacy_readable" || audit.V2ComplianceStatus != "legacy_mixed" {
		t.Fatalf("audit contract = %#v", audit)
	}
	if !reflect.DeepEqual(audit.ResearchHealth, research.StructuralHealth) {
		t.Fatalf("thread and audit health diverged: threads=%#v audit=%#v", research.StructuralHealth, audit.ResearchHealth)
	}
	missingAudit := httptest.NewRecorder()
	srv.Handler().ServeHTTP(missingAudit, httptest.NewRequest(http.MethodGet, "/api/v1/evidence-chains/chain_missing/audit", nil))
	if missingAudit.Code != http.StatusNotFound {
		t.Fatalf("missing audit status = %d body=%s", missingAudit.Code, missingAudit.Body.String())
	}
	staleBody := strings.Replace(graphBody, `"nodes":[`, `"expected_revision":0,"nodes":[`, 1)
	staleReq := httptest.NewRequest(http.MethodPut, "/api/v1/evidence-chains/"+chain.ID+"/graph", bytes.NewBufferString(staleBody))
	staleRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(staleRec, staleReq)
	if staleRec.Code != http.StatusConflict || !strings.Contains(staleRec.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("stale save status = %d body=%s", staleRec.Code, staleRec.Body.String())
	}
	revisionsReq := httptest.NewRequest(http.MethodGet, "/api/v1/evidence-chains/"+chain.ID+"/revisions", nil)
	revisionsRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(revisionsRec, revisionsReq)
	if revisionsRec.Code != http.StatusOK || !strings.Contains(revisionsRec.Body.String(), detail.GraphHash) {
		t.Fatalf("revisions status = %d body=%s", revisionsRec.Code, revisionsRec.Body.String())
	}
	proposalBody := fmt.Sprintf(`{
		"card":{"project_id":"dam","question":"Does the pilot change the claim?","base_graph_revision":1},
		"patch":{"chain_id":%q,"routing_reason":"This topic graph is the explicit scope of the API test.","nodes":[
			{"id":"node_pilot","type":"run","run_id":"run_free_candidate"},
			{"id":"claim_pilot","type":"claim","title":"Pilot claim"}
		],"edges":[{"id":"edge_pilot","source_node_id":"node_pilot","target_node_id":"claim_pilot","type":"supports"}]}
	}`, chain.ID)
	proposalReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_free_candidate/evidence-proposal", bytes.NewBufferString(proposalBody))
	proposalRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(proposalRec, proposalReq)
	if proposalRec.Code != http.StatusAccepted || !strings.Contains(proposalRec.Body.String(), `"graph_status":"pending"`) {
		t.Fatalf("proposal status = %d body=%s", proposalRec.Code, proposalRec.Body.String())
	}
	planReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_free_candidate/evidence-proposal/plan", bytes.NewBufferString(`{}`))
	planRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(planRec, planReq)
	if planRec.Code != http.StatusOK || !strings.Contains(planRec.Body.String(), `"eligible":false`) || !strings.Contains(planRec.Body.String(), "RUN_NOT_FORMAL_EVIDENCE") {
		t.Fatalf("proposal plan status = %d body=%s", planRec.Code, planRec.Body.String())
	}

	badEdge := `{"nodes":[{"id":"node_h","type":"hypothesis"}],"edges":[{"id":"edge_bad","source_node_id":"node_h","target_node_id":"missing","type":"supports"}]}`
	badReq := httptest.NewRequest(http.MethodPut, "/api/v1/evidence-chains/"+chain.ID+"/graph", bytes.NewBufferString(badEdge))
	badRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("bad edge status = %d body=%s", badRec.Code, badRec.Body.String())
	}

	badRun := `{"nodes":[{"id":"node_r","type":"run","run_id":"run_missing"}],"edges":[]}`
	badRunReq := httptest.NewRequest(http.MethodPut, "/api/v1/evidence-chains/"+chain.ID+"/graph", bytes.NewBufferString(badRun))
	badRunRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRunRec, badRunReq)
	if badRunRec.Code != http.StatusBadRequest {
		t.Fatalf("bad run status = %d body=%s", badRunRec.Code, badRunRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/evidence-chains/"+chain.ID, nil)
	deleteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	purgeWithProposalReq := httptest.NewRequest(http.MethodDelete, "/api/v1/evidence-chains/"+chain.ID+"?permanent=true", nil)
	purgeWithProposalRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(purgeWithProposalRec, purgeWithProposalReq)
	if purgeWithProposalRec.Code != http.StatusNoContent {
		t.Fatalf("purge archived status = %d body=%s", purgeWithProposalRec.Code, purgeWithProposalRec.Body.String())
	}

	emptyCreateReq := httptest.NewRequest(http.MethodPost, "/api/v1/evidence-chains", bytes.NewBufferString(`{"title":"Disposable topic","project_id":"dam"}`))
	emptyCreateRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(emptyCreateRec, emptyCreateReq)
	if emptyCreateRec.Code != http.StatusCreated {
		t.Fatalf("create empty Topic status = %d body=%s", emptyCreateRec.Code, emptyCreateRec.Body.String())
	}
	var emptyTopic store.EvidenceChain
	if err := json.Unmarshal(emptyCreateRec.Body.Bytes(), &emptyTopic); err != nil {
		t.Fatal(err)
	}
	purgeEmptyReq := httptest.NewRequest(http.MethodDelete, "/api/v1/evidence-chains/"+emptyTopic.ID+"?permanent=true", nil)
	purgeEmptyRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(purgeEmptyRec, purgeEmptyReq)
	if purgeEmptyRec.Code != http.StatusNoContent {
		t.Fatalf("purge empty status = %d body=%s", purgeEmptyRec.Code, purgeEmptyRec.Body.String())
	}

	primary, err := db.GetActivePrimaryEvidenceChain(ctx, "dam")
	if err != nil || primary == nil {
		t.Fatalf("primary = %#v, err = %v", primary, err)
	}
	deletePrimaryReq := httptest.NewRequest(http.MethodDelete, "/api/v1/evidence-chains/"+primary.ID, nil)
	deletePrimaryRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(deletePrimaryRec, deletePrimaryReq)
	if deletePrimaryRec.Code != http.StatusConflict || !strings.Contains(deletePrimaryRec.Body.String(), "PRIMARY_MAP_REQUIRED") {
		t.Fatalf("delete primary status = %d body=%s", deletePrimaryRec.Code, deletePrimaryRec.Body.String())
	}
}

func TestEvidenceWorkspaceBootstrapProposalAPIWithoutRun(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	project := &store.ProjectDefinition{ID: "project_bootstrap_api", Name: "Bootstrap API"}
	if err := db.CreateProjectDefinition(ctx, project); err != nil {
		t.Fatalf("CreateProjectDefinition: %v", err)
	}
	primary, err := db.GetActivePrimaryEvidenceChain(ctx, project.ID)
	if err != nil || primary == nil {
		t.Fatalf("primary = %#v, err = %v", primary, err)
	}
	srv := NewServer(db, nil, nil, slog.Default(), "", true)

	mapReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/project-definitions/"+project.ID+"/evidence-maps",
		bytes.NewBufferString(`{"title":"Initial protocol","description":"Owns the first research question and protocol."}`))
	mapRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(mapRec, mapReq)
	if mapRec.Code != http.StatusCreated {
		t.Fatalf("create map status = %d body=%s", mapRec.Code, mapRec.Body.String())
	}
	var topic store.EvidenceChain
	if err := json.Unmarshal(mapRec.Body.Bytes(), &topic); err != nil {
		t.Fatal(err)
	}
	if topic.ProjectID != project.ID || topic.Role != "secondary" || topic.Status != "active" {
		t.Fatalf("topic = %#v", topic)
	}

	proposalBody := fmt.Sprintf(`{
		"target_map_id":%q,
		"actor":"agent",
		"summary":"Bootstrap the first usable research map.",
		"routing_reason":"This Topic owns the initial protocol.",
		"source_run_ids":[],
		"patch":{
			"chain_id":%q,
			"nodes":[
				{"id":"hypothesis_api","type":"hypothesis","title":"Starting hypothesis"},
				{"id":"claim_api","type":"claim","title":"Working claim"},
				{"id":"issue_api","type":"issue","title":"Protocol is not yet verified"},
				{"id":"child_hypothesis_api","type":"claim","title":"Verified data will resolve the protocol issue","data_json":"{\"claimKind\":\"hypothesis\"}"}
			],
			"edges":[
				{"id":"edge_api_support","type":"supports","source_node_id":"hypothesis_api","target_node_id":"claim_api"},
				{"id":"edge_api_next","type":"next_step","source_node_id":"issue_api","target_node_id":"child_hypothesis_api"}
			]
		}
	}`, topic.ID, topic.ID)
	createReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/project-definitions/"+project.ID+"/evidence-proposals",
		bytes.NewBufferString(proposalBody))
	createRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create proposal status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created evidenceProposalView
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != store.GraphProposalPending || created.TargetMap == nil || created.TargetMap.ID != topic.ID {
		t.Fatalf("created proposal = %#v", created)
	}

	planReq := httptest.NewRequest(http.MethodPost, "/api/v1/evidence-proposals/"+created.ID+"/plan", bytes.NewBufferString(`{}`))
	planRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(planRec, planReq)
	if planRec.Code != http.StatusOK {
		t.Fatalf("plan status = %d body=%s", planRec.Code, planRec.Body.String())
	}
	var plan store.EvidenceGraphProposalPlan
	if err := json.Unmarshal(planRec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if !plan.Eligible || plan.ProposalID != created.ID || plan.ProjectID != project.ID || plan.ResultGraphHash == "" {
		t.Fatalf("plan = %#v", plan)
	}
	secondBody := fmt.Sprintf(`{
		"target_map_id":%q,
		"actor":"agent",
		"summary":"Add an independent follow-up.",
		"routing_reason":"This Topic owns the follow-up.",
		"source_run_ids":[],
		"patch":{"chain_id":%q,"nodes":[{"id":"plan_follow_up","type":"plan","title":"Independent follow-up"}],"edges":[]}
	}`, topic.ID, topic.ID)
	secondCreateReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/project-definitions/"+project.ID+"/evidence-proposals",
		bytes.NewBufferString(secondBody))
	secondCreateRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(secondCreateRec, secondCreateReq)
	if secondCreateRec.Code != http.StatusAccepted {
		t.Fatalf("create second proposal status = %d body=%s", secondCreateRec.Code, secondCreateRec.Body.String())
	}
	var secondCreated evidenceProposalView
	if err := json.Unmarshal(secondCreateRec.Body.Bytes(), &secondCreated); err != nil {
		t.Fatal(err)
	}

	reviewReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/evidence-proposals/"+created.ID+"/review",
		bytes.NewBufferString(`{"action":"accept","reviewer":"user"}`))
	reviewRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(reviewRec, reviewReq)
	if reviewRec.Code != http.StatusOK || !strings.Contains(reviewRec.Body.String(), `"status":"accepted"`) {
		t.Fatalf("review status = %d body=%s", reviewRec.Code, reviewRec.Body.String())
	}
	acceptedTopic, _ := db.GetEvidenceChain(ctx, topic.ID)
	if acceptedTopic.Revision != 1 || acceptedTopic.GraphHash != plan.ResultGraphHash {
		t.Fatalf("accepted topic = %#v", acceptedTopic)
	}
	secondPlanReq := httptest.NewRequest(http.MethodPost, "/api/v1/evidence-proposals/"+secondCreated.ID+"/plan", bytes.NewBufferString(`{}`))
	secondPlanRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(secondPlanRec, secondPlanReq)
	if secondPlanRec.Code != http.StatusOK {
		t.Fatalf("second plan status = %d body=%s", secondPlanRec.Code, secondPlanRec.Body.String())
	}
	var secondPlan store.EvidenceGraphProposalPlan
	if err := json.Unmarshal(secondPlanRec.Body.Bytes(), &secondPlan); err != nil {
		t.Fatal(err)
	}
	if !secondPlan.Eligible || !secondPlan.AutoRebased || secondPlan.AppliedGraphRevision != 1 {
		t.Fatalf("second plan = %#v", secondPlan)
	}
	secondReviewReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/evidence-proposals/"+secondCreated.ID+"/review",
		bytes.NewBufferString(`{"action":"accept","reviewer":"user"}`))
	secondReviewRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(secondReviewRec, secondReviewReq)
	if secondReviewRec.Code != http.StatusOK || !strings.Contains(secondReviewRec.Body.String(), `"status":"accepted"`) {
		t.Fatalf("second review status = %d body=%s", secondReviewRec.Code, secondReviewRec.Body.String())
	}
	unchangedPrimary, _ := db.GetEvidenceChain(ctx, primary.ID)
	if unchangedPrimary.Revision != primary.Revision || unchangedPrimary.GraphHash != primary.GraphHash {
		t.Fatalf("bootstrap changed primary: before=%#v after=%#v", primary, unchangedPrimary)
	}

	listReq := httptest.NewRequest(http.MethodGet,
		"/api/v1/project-definitions/"+project.ID+"/evidence-proposals?status=accepted", nil)
	listRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), created.ID) {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}

	promotionPlanReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/evidence-chains/"+topic.ID+"/promotion/plan",
		bytes.NewBufferString(`{"source_node_ids":["claim_api"],"summary":"Promote the working claim.","node_type":"claim","actor":"agent"}`))
	promotionPlanRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(promotionPlanRec, promotionPlanReq)
	if promotionPlanRec.Code != http.StatusOK {
		t.Fatalf("promotion plan status = %d body=%s", promotionPlanRec.Code, promotionPlanRec.Body.String())
	}
	var promotionPlan store.EvidencePromotionPlan
	if err := json.Unmarshal(promotionPlanRec.Body.Bytes(), &promotionPlan); err != nil {
		t.Fatal(err)
	}
	if !promotionPlan.Eligible || promotionPlan.PlanHash == "" || promotionPlan.TargetPrimaryMapID != primary.ID {
		t.Fatalf("promotion plan = %#v", promotionPlan)
	}
	primaryAfterPlan, _ := db.GetEvidenceChain(ctx, primary.ID)
	if primaryAfterPlan.Revision != primary.Revision {
		t.Fatalf("promotion plan mutated Primary: %#v", primaryAfterPlan)
	}
	promotionCreateBody := fmt.Sprintf(`{
		"source_node_ids":["claim_api"],
		"summary":"Promote the working claim.",
		"node_type":"claim",
		"actor":"agent",
		"expected_plan_hash":%q
	}`, promotionPlan.PlanHash)
	promotionCreateReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/evidence-chains/"+topic.ID+"/promotions",
		bytes.NewBufferString(promotionCreateBody))
	promotionCreateRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(promotionCreateRec, promotionCreateReq)
	if promotionCreateRec.Code != http.StatusAccepted {
		t.Fatalf("promotion create status = %d body=%s", promotionCreateRec.Code, promotionCreateRec.Body.String())
	}
	var promotion evidenceProposalView
	if err := json.Unmarshal(promotionCreateRec.Body.Bytes(), &promotion); err != nil {
		t.Fatal(err)
	}
	if promotion.TargetMap == nil || promotion.TargetMap.ID != primary.ID || promotion.Status != store.GraphProposalPending || promotion.SourceKind != "promotion" {
		t.Fatalf("promotion = %#v", promotion)
	}
}

func TestExperimentMatrixAPIAndValidation(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateProjectDefinition(ctx, &store.ProjectDefinition{ID: "dam", Name: "Dam"}); err != nil {
		t.Fatal(err)
	}

	if err := db.CreateResource(ctx, &store.Resource{
		ID:      "rsrc_matrix_api",
		Name:    "mu",
		Type:    "ssh",
		Host:    "localhost",
		RootDir: "/ws",
		Status:  store.ResourceStatusIdle,
	}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []*store.Run{
		{ID: "run_matrix_card", ResourceID: "rsrc_matrix_api", Name: "carded", Kind: store.RunKindFormal, Status: store.RunStatusSucceeded, Command: "python train.py"},
		{ID: "run_matrix_free", ResourceID: "rsrc_matrix_api", Name: "free", Kind: store.RunKindPilot, Status: store.RunStatusSucceeded, Command: "python pilot.py"},
	} {
		if err := db.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveProjectRunCard(ctx, &store.ProjectRunCard{
		ID:          "card_matrix",
		ProjectID:   "dam",
		ProjectName: "Dam",
		RunID:       "run_matrix_card",
		Question:    "Does the ablation help?",
		Verdict:     "It helps.",
		KeyMetrics:  "val_loss=0.12",
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/experiment-matrices", bytes.NewBufferString(`{"title":"Dam ablations","source_kind":"project","source_id":"dam","source_name":"Dam","seed_from_source":true}`))
	createRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	var detail store.ExperimentMatrixDetail
	if err := json.Unmarshal(createRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode matrix: %v", err)
	}
	if detail.ID == "" || len(detail.Rows) != 1 || len(detail.Columns) != 1 || len(detail.Cells) != 1 {
		t.Fatalf("seeded detail = %#v, want one seeded cell", detail)
	}
	if detail.Cells[0].RunID != "run_matrix_card" || detail.Cells[0].ProjectCardID != "card_matrix" {
		t.Fatalf("seeded cell = %#v, want carded run", detail.Cells[0])
	}

	saveBody := `{
		"rows":[{"id":"row_a","label":"Ablation A","position":0}],
		"columns":[{"id":"col_result","label":"Result","position":0}],
		"cells":[{"id":"cell_a","row_id":"row_a","column_id":"col_result","run_id":"run_matrix_free","title":"free","statement":"pilot only"}]
	}`
	saveReq := httptest.NewRequest(http.MethodPut, "/api/v1/experiment-matrices/"+detail.ID+"/grid", bytes.NewBufferString(saveBody))
	saveRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(saveRec, saveReq)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("save status = %d body=%s", saveRec.Code, saveRec.Body.String())
	}
	var saved store.ExperimentMatrixDetail
	if err := json.Unmarshal(saveRec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode saved matrix: %v", err)
	}
	if len(saved.Cells) != 1 || saved.Cells[0].RunID != "run_matrix_free" {
		t.Fatalf("saved detail = %#v, want free run cell", saved)
	}

	badRun := `{"rows":[{"id":"row_a","label":"A","position":0}],"columns":[{"id":"col_a","label":"A","position":0}],"cells":[{"id":"cell_bad","row_id":"row_a","column_id":"col_a","run_id":"missing"}]}`
	badReq := httptest.NewRequest(http.MethodPut, "/api/v1/experiment-matrices/"+detail.ID+"/grid", bytes.NewBufferString(badRun))
	badRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("bad run status = %d body=%s", badRec.Code, badRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/experiment-matrices/"+detail.ID, nil)
	deleteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
}
