package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ziwu/aexp/internal/eventcache"
	"github.com/ziwu/aexp/internal/store"
)

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
	if body.ErrorKind != "resource_unreachable" {
		t.Fatalf("error_kind = %q, want resource_unreachable", body.ErrorKind)
	}
	if len(body.Lines) != 1 || body.Lines[0].Content == "" {
		t.Fatalf("cached lines missing: %#v", body.Lines)
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

func TestUIV2StaticRoutesStayParallelToLegacyRoot(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	srv := NewServer(db, nil, nil, slog.Default(), "", true)
	for _, path := range []string{"/", "/ui-v2/", "/ui-v2/runs/test"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("%s returned empty body", path)
		}
	}
}

func TestProjectViewsEnrichCardsWithRunsAndMarks(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "aexp.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

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
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	var category store.ManualProjectCategory
	if err := json.Unmarshal(createRec.Body.Bytes(), &category); err != nil {
		t.Fatalf("decode category: %v", err)
	}
	if category.ID == "" || category.Name != "Dam downstream" {
		t.Fatalf("category = %#v", category)
	}

	assignBody := fmt.Sprintf(`{"category_id":%q}`, category.ID)
	assignReq := httptest.NewRequest(http.MethodPut, "/api/v1/runs/run_manual_project_api/manual-project-category", bytes.NewBufferString(assignBody))
	assignRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(assignRec, assignReq)
	if assignRec.Code != http.StatusOK {
		t.Fatalf("assign status = %d body=%s", assignRec.Code, assignRec.Body.String())
	}
	var assignment store.RunProjectAssignment
	if err := json.Unmarshal(assignRec.Body.Bytes(), &assignment); err != nil {
		t.Fatalf("decode assignment: %v", err)
	}
	if assignment.RunID != "run_manual_project_api" || assignment.CategoryID != category.ID || assignment.CategoryName != "Dam downstream" {
		t.Fatalf("assignment = %#v", assignment)
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

	missingReq := httptest.NewRequest(http.MethodPut, "/api/v1/runs/run_manual_project_api/manual-project-category", bytes.NewBufferString(`{"category_id":"missing"}`))
	missingRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing category status = %d body=%s", missingRec.Code, missingRec.Body.String())
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
	for _, run := range []*store.Run{
		{ID: "run_card_candidate", ResourceID: "rsrc_evidence_api", Name: "carded", Kind: store.RunKindFormal, Status: store.RunStatusSucceeded, Command: "python train.py"},
		{ID: "run_free_candidate", ResourceID: "rsrc_evidence_api", Name: "free pilot", Kind: store.RunKindPilot, Status: store.RunStatusSucceeded, Command: "python pilot.py"},
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
	createBody := bytes.NewBufferString(`{"title":"IR anchor","description":"fusion reasoning"}`)
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
	if saveRec.Code != http.StatusOK {
		t.Fatalf("save status = %d body=%s", saveRec.Code, saveRec.Body.String())
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
}
