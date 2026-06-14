package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
