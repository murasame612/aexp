package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCanonicalEvidenceGraphIgnoresOrderingAndLayout(t *testing.T) {
	base := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "claim", Type: EvidenceNodeClaim, Title: "Claim", X: 10, Y: 20, Width: 200, Height: 100, Pinned: true, DataJSON: `{"evidence_context":{"protocol":"rgb"},"pinned":true}`},
			{ID: "run", Type: EvidenceNodeRun, Title: "Run", RunID: "run_hash", X: 300, Y: 40, DataJSON: `{}`},
		},
		Edges: []EvidenceChainEdge{
			{ID: "edge", Type: EvidenceEdgeSupports, SourceNodeID: "run", TargetNodeID: "claim", DataJSON: `{}`},
		},
	}
	_, hashA, err := CanonicalEvidenceGraph(base)
	if err != nil {
		t.Fatalf("CanonicalEvidenceGraph: %v", err)
	}
	reordered := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{base.Nodes[1], base.Nodes[0]},
		Edges: append([]EvidenceChainEdge(nil), base.Edges...),
	}
	reordered.Nodes[1].X = 999
	reordered.Nodes[1].Y = -50
	reordered.Nodes[1].Width = 999
	reordered.Nodes[1].Pinned = false
	reordered.Nodes[1].DataJSON = `{"pinned":false,"evidence_context":{"protocol":"rgb"}}`
	_, hashB, err := CanonicalEvidenceGraph(reordered)
	if err != nil {
		t.Fatalf("CanonicalEvidenceGraph reordered: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("layout/order changed hash: %s != %s", hashA, hashB)
	}
	reordered.Nodes[1].Title = "Changed claim"
	_, hashC, err := CanonicalEvidenceGraph(reordered)
	if err != nil {
		t.Fatalf("CanonicalEvidenceGraph changed: %v", err)
	}
	if hashC == hashA {
		t.Fatal("semantic title change did not change hash")
	}
}

func TestValidateEvidenceChainGraphSemanticBlockers(t *testing.T) {
	valid := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "dataset", Type: EvidenceNodeDataset},
			{ID: "run", Type: EvidenceNodeRun, RunID: "run_valid"},
			{ID: "claim", Type: EvidenceNodeClaim},
			{ID: "plan", Type: EvidenceNodePlan},
		},
		Edges: []EvidenceChainEdge{
			{ID: "uses", Type: EvidenceEdgeUses, SourceNodeID: "dataset", TargetNodeID: "run"},
			{ID: "supports", Type: EvidenceEdgeSupports, SourceNodeID: "run", TargetNodeID: "claim"},
			{ID: "next", Type: EvidenceEdgeNextStep, SourceNodeID: "claim", TargetNodeID: "plan"},
		},
	}
	if err := ValidateEvidenceChainGraph(&valid); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}

	duplicateRun := valid
	duplicateRun.Nodes = append(append([]EvidenceChainNode(nil), valid.Nodes...), EvidenceChainNode{ID: "run2", Type: EvidenceNodeRun, RunID: "run_valid"})
	assertGraphValidationCode(t, ValidateEvidenceChainGraph(&duplicateRun), "DUPLICATE_RUN_NODE")

	badDirection := valid
	badDirection.Edges = append([]EvidenceChainEdge(nil), valid.Edges...)
	badDirection.Edges[1].SourceNodeID, badDirection.Edges[1].TargetNodeID = "claim", "run"
	assertGraphValidationCode(t, ValidateEvidenceChainGraph(&badDirection), "INVALID_EDGE_DIRECTION")

	cycle := valid
	cycle.Edges = append(append([]EvidenceChainEdge(nil), valid.Edges...),
		EvidenceChainEdge{ID: "cycle", Type: EvidenceEdgeSupersedes, SourceNodeID: "plan", TargetNodeID: "plan"})
	assertGraphValidationCode(t, ValidateEvidenceChainGraph(&cycle), "SEMANTIC_SELF_LOOP")
}

func TestValidateEvidenceChainGraphRejectsContradictoryPolarity(t *testing.T) {
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "run_polarity", Type: EvidenceNodeRun, RunID: "run_polarity"},
			{ID: "claim_polarity", Type: EvidenceNodeClaim, Title: "Claim"},
		},
		Edges: []EvidenceChainEdge{
			{ID: "edge_supports", Type: EvidenceEdgeSupports, SourceNodeID: "run_polarity", TargetNodeID: "claim_polarity"},
			{ID: "edge_weakens", Type: EvidenceEdgeWeakens, SourceNodeID: "run_polarity", TargetNodeID: "claim_polarity"},
		},
	}
	err := ValidateEvidenceChainGraph(&graph)
	var validation *EvidenceGraphValidationError
	if !errors.As(err, &validation) || validation.Code != "CONTRADICTORY_EVIDENCE_EDGE" {
		t.Fatalf("error = %#v", err)
	}
	if !strings.Contains(validation.Message, "edge_supports") || !strings.Contains(validation.Message, "edge_weakens") {
		t.Fatalf("conflicting edge ids missing from message: %q", validation.Message)
	}
}

func TestValidateEvidenceChainGraphAllowsLegacySecondaryBoardShape(t *testing.T) {
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "run_a", Type: EvidenceNodeRun, RunID: "run_same", DataJSON: "{}"},
			{ID: "run_b", Type: EvidenceNodeRun, RunID: "run_same", DataJSON: "{}"},
		},
		Edges: []EvidenceChainEdge{
			{ID: "edge_a", SourceNodeID: "run_a", TargetNodeID: "run_b", Type: EvidenceEdgeNextStep, DataJSON: "{}"},
			{ID: "edge_b", SourceNodeID: "run_b", TargetNodeID: "run_a", Type: EvidenceEdgeNextStep, DataJSON: "{}"},
		},
	}
	if err := validateEvidenceChainGraph(&graph, false); err != nil {
		t.Fatalf("legacy secondary board should remain structurally editable: %v", err)
	}
	assertGraphValidationCode(t, ValidateEvidenceChainGraph(&graph), "DUPLICATE_RUN_NODE")
}

func TestSaveEvidenceChainGraphCASAndLayoutOnlyUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProjectDefinition(ctx, &ProjectDefinition{ID: "project_graph_cas", Name: "Graph CAS"}); err != nil {
		t.Fatalf("CreateProjectDefinition: %v", err)
	}
	if err := s.CreateResource(ctx, &Resource{ID: "rsrc_graph_cas", Name: "graph-cas", Type: "ssh", Host: "localhost", RootDir: "/tmp", Status: ResourceStatusIdle}); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}
	if err := s.CreateRun(ctx, &Run{ID: "run_graph_cas", ResourceID: "rsrc_graph_cas", ProjectID: "project_graph_cas", Name: "graph-cas", Status: RunStatusSucceeded, Kind: RunKindFormal, Command: "python train.py"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateEvidenceChain(ctx, &EvidenceChain{ID: "chain_graph_cas", Title: "CAS", ProjectID: "project_graph_cas"}); err != nil {
		t.Fatalf("CreateEvidenceChain: %v", err)
	}
	graph := EvidenceChainGraph{
		Nodes: []EvidenceChainNode{
			{ID: "run", Type: EvidenceNodeRun, RunID: "run_graph_cas", X: 10},
			{ID: "claim", Type: EvidenceNodeClaim, Title: "Claim", X: 300},
		},
		Edges: []EvidenceChainEdge{{ID: "supports", Type: EvidenceEdgeSupports, SourceNodeID: "run", TargetNodeID: "claim"}},
	}
	chain, err := s.SaveEvidenceChainGraphCAS(ctx, "chain_graph_cas", graph, EvidenceGraphSaveOptions{ExpectedRevision: 0, Actor: "test"})
	if err != nil {
		t.Fatalf("first CAS: %v", err)
	}
	if chain.Revision != 1 || chain.GraphHash == "" {
		t.Fatalf("first CAS chain = %#v", chain)
	}
	if _, err := s.SaveEvidenceChainGraphCAS(ctx, "chain_graph_cas", graph, EvidenceGraphSaveOptions{ExpectedRevision: 0}); err == nil {
		t.Fatal("stale CAS unexpectedly succeeded")
	} else {
		var conflict *EvidenceGraphRevisionConflict
		if !errors.As(err, &conflict) || conflict.Current != 1 {
			t.Fatalf("stale CAS err = %#v", err)
		}
	}

	graph.Nodes[0].X = 999
	graph.Nodes[0].Pinned = true
	layoutChain, err := s.SaveEvidenceChainGraphCAS(ctx, "chain_graph_cas", graph, EvidenceGraphSaveOptions{ExpectedRevision: 1, Actor: "test"})
	if err != nil {
		t.Fatalf("layout CAS: %v", err)
	}
	if layoutChain.Revision != 1 || layoutChain.GraphHash != chain.GraphHash {
		t.Fatalf("layout update changed semantic revision/hash: before=%#v after=%#v", chain, layoutChain)
	}
	revisions, err := s.ListEvidenceChainRevisions(ctx, "chain_graph_cas", 10)
	if err != nil {
		t.Fatalf("ListEvidenceChainRevisions: %v", err)
	}
	if len(revisions) != 1 || revisions[0].Revision != 1 || revisions[0].GraphHash != chain.GraphHash {
		t.Fatalf("revisions = %#v", revisions)
	}
}

func TestDirectReplaceGraphAllowsLayoutOnlyAndRejectsSemantics(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProjectDefinition(ctx, &ProjectDefinition{ID: "project_layout_only", Name: "Layout Only"}); err != nil {
		t.Fatal(err)
	}
	chain, err := s.GetActivePrimaryEvidenceChain(ctx, "project_layout_only")
	if err != nil || chain == nil {
		t.Fatalf("chain=%#v err=%v", chain, err)
	}
	graph := EvidenceChainGraph{Nodes: []EvidenceChainNode{{ID: "claim_layout", Type: EvidenceNodeClaim, Title: "Claim", X: 1, Y: 2}}}
	chain, err = s.SaveEvidenceChainGraphCAS(ctx, chain.ID, graph, EvidenceGraphSaveOptions{
		ExpectedRevision: chain.Revision, Actor: "proposal-test", SourceKind: "evidence_proposal",
	})
	if err != nil {
		t.Fatal(err)
	}
	graph.Nodes[0].X = 300
	graph.Nodes[0].Y = 400
	layoutSaved, err := s.SaveEvidenceChainGraphCAS(ctx, chain.ID, graph, EvidenceGraphSaveOptions{
		ExpectedRevision: chain.Revision, Actor: "ui", SourceKind: "replace_graph",
	})
	if err != nil {
		t.Fatalf("layout save: %v", err)
	}
	if layoutSaved.Revision != chain.Revision || layoutSaved.GraphHash != chain.GraphHash {
		t.Fatalf("layout changed semantic identity: before=%#v after=%#v", chain, layoutSaved)
	}
	graph.Nodes[0].Title = "Changed claim"
	_, err = s.SaveEvidenceChainGraphCAS(ctx, chain.ID, graph, EvidenceGraphSaveOptions{
		ExpectedRevision: chain.Revision, Actor: "ui", SourceKind: "replace_graph",
	})
	assertGraphValidationCode(t, err, "SEMANTIC_WRITE_REQUIRES_PROPOSAL")
	_, err = s.SaveEvidenceChainGraphCAS(ctx, chain.ID, graph, EvidenceGraphSaveOptions{
		ExpectedRevision: -1, Actor: "ui", SourceKind: "replace_graph",
	})
	assertGraphValidationCode(t, err, "EXPECTED_REVISION_REQUIRED")
}

func TestEvidenceGraphLegacySchemaMigrationPreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `
CREATE TABLE evidence_chains (
  id TEXT PRIMARY KEY, title TEXT NOT NULL, description TEXT DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE evidence_chain_nodes (
  id TEXT PRIMARY KEY, chain_id TEXT NOT NULL, type TEXT NOT NULL, title TEXT DEFAULT '',
  body TEXT DEFAULT '', run_id TEXT DEFAULT '', project_card_id TEXT DEFAULT '',
  x REAL NOT NULL DEFAULT 0, y REAL NOT NULL DEFAULT 0, width REAL NOT NULL DEFAULT 260,
  height REAL NOT NULL DEFAULT 140, data_json TEXT DEFAULT '{}',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE evidence_chain_edges (
  id TEXT PRIMARY KEY, chain_id TEXT NOT NULL, source_node_id TEXT NOT NULL,
  target_node_id TEXT NOT NULL, type TEXT NOT NULL, label TEXT DEFAULT '',
  rationale TEXT DEFAULT '', data_json TEXT DEFAULT '{}',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO evidence_chains(id,title,description) VALUES ('legacy-chain','Legacy','Historical snapshot');
INSERT INTO evidence_chain_nodes(id,chain_id,type,title,x,y,data_json)
VALUES ('legacy-note','legacy-chain','note','Keep me',123,456,'{"legacy":true}');
INSERT INTO evidence_chain_edges(id,chain_id,source_node_id,target_node_id,type,label)
VALUES ('legacy-custom','legacy-chain','legacy-note','legacy-note','custom','visual');
`
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for pass := 0; pass < 2; pass++ {
		store, err := NewSQLite(path)
		if err != nil {
			t.Fatalf("NewSQLite migration pass %d: %v", pass+1, err)
		}
		chain, err := store.GetEvidenceChain(context.Background(), "legacy-chain")
		if err != nil || chain == nil {
			t.Fatalf("legacy chain pass %d = %#v err=%v", pass+1, chain, err)
		}
		if chain.Role != "secondary" || chain.Status != "active" || chain.Revision != 0 {
			t.Fatalf("legacy defaults pass %d = %#v", pass+1, chain)
		}
		graph, err := store.GetEvidenceChainGraph(context.Background(), chain.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(graph.Nodes) != 1 || len(graph.Edges) != 1 || graph.Nodes[0].X != 123 || graph.Nodes[0].Y != 456 || graph.Nodes[0].DataJSON != `{"legacy":true}` {
			t.Fatalf("legacy graph changed on pass %d: %#v", pass+1, graph)
		}
		var integrity string
		if err := store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("integrity pass %d = %q err=%v", pass+1, integrity, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func assertGraphValidationCode(t *testing.T, err error, want string) {
	t.Helper()
	var validation *EvidenceGraphValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %#v, want EvidenceGraphValidationError %s", err, want)
	}
	if validation.Code != want {
		t.Fatalf("validation code = %q, want %q", validation.Code, want)
	}
}
