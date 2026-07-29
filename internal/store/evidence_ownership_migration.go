package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	EvidenceMapOwnershipBind    = "bind"
	EvidenceMapOwnershipArchive = "archive_legacy"
)

type EvidenceMapOwnershipMigrationEntry struct {
	MapID       string `json:"map_id"`
	Title       string `json:"title"`
	Revision    int64  `json:"revision"`
	GraphHash   string `json:"graph_hash,omitempty"`
	NodeCount   int    `json:"node_count"`
	EdgeCount   int    `json:"edge_count"`
	ProjectID   string `json:"project_id,omitempty"`
	Action      string `json:"action"`
	Explanation string `json:"explanation"`
}

type EvidenceMapOwnershipMigrationReport struct {
	DryRun        bool                                 `json:"dry_run"`
	OrphanCount   int                                  `json:"orphan_count"`
	BoundCount    int                                  `json:"bound_count"`
	ArchivedCount int                                  `json:"archived_count"`
	Entries       []EvidenceMapOwnershipMigrationEntry `json:"entries"`
}

// PlanEvidenceMapOwnershipMigration reports every active orphan Map. Ownership
// is never inferred from titles: only caller-supplied Map->Project mappings bind
// a Map. All unmapped orphans are preserved as archived legacy Maps.
func (s *SQLite) PlanEvidenceMapOwnershipMigration(ctx context.Context, mappings map[string]string) (*EvidenceMapOwnershipMigrationReport, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, title, revision, graph_hash,
		(SELECT COUNT(*) FROM evidence_chain_nodes WHERE chain_id=evidence_chains.id),
		(SELECT COUNT(*) FROM evidence_chain_edges WHERE chain_id=evidence_chains.id)
		FROM evidence_chains
		WHERE TRIM(COALESCE(project_id, '')) = '' AND status = 'active'
		ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	report := &EvidenceMapOwnershipMigrationReport{DryRun: true, Entries: make([]EvidenceMapOwnershipMigrationEntry, 0)}
	for rows.Next() {
		var entry EvidenceMapOwnershipMigrationEntry
		if err := rows.Scan(&entry.MapID, &entry.Title, &entry.Revision, &entry.GraphHash, &entry.NodeCount, &entry.EdgeCount); err != nil {
			return nil, err
		}
		entry.ProjectID = strings.TrimSpace(mappings[entry.MapID])
		if entry.ProjectID == "" {
			entry.Action = EvidenceMapOwnershipArchive
			entry.Explanation = "no explicit ownership mapping; preserve as archived legacy Map"
			report.ArchivedCount++
		} else {
			project, projectErr := s.GetProjectDefinition(ctx, entry.ProjectID)
			if projectErr != nil {
				return nil, projectErr
			}
			if project == nil {
				return nil, graphValidationError("PROJECT_NOT_REGISTERED", fmt.Sprintf("mapping for Map %q references unknown Project %q", entry.MapID, entry.ProjectID))
			}
			entry.Action = EvidenceMapOwnershipBind
			entry.Explanation = "bind using explicit Map-to-Project mapping"
			report.BoundCount++
		}
		report.Entries = append(report.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	report.OrphanCount = len(report.Entries)
	return report, nil
}

// ApplyEvidenceMapOwnershipMigration applies a previously inspectable ownership
// policy transactionally. It only updates Map ownership/status metadata; nodes,
// edges, revisions, hashes, and revision snapshots are untouched.
func (s *SQLite) ApplyEvidenceMapOwnershipMigration(ctx context.Context, mappings map[string]string) (*EvidenceMapOwnershipMigrationReport, error) {
	report, err := s.PlanEvidenceMapOwnershipMigration(ctx, mappings)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now()
	for _, entry := range report.Entries {
		var result sql.Result
		if entry.Action == EvidenceMapOwnershipBind {
			result, err = tx.ExecContext(ctx, `UPDATE evidence_chains
				SET project_id=?, updated_at=?
				WHERE id=? AND TRIM(COALESCE(project_id, ''))='' AND status='active'`,
				entry.ProjectID, now, entry.MapID)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE evidence_chains
				SET role='archive', status='archived', updated_at=?
				WHERE id=? AND TRIM(COALESCE(project_id, ''))='' AND status='active'`,
				now, entry.MapID)
		}
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, graphValidationError("MIGRATION_CONFLICT", fmt.Sprintf("Map %q changed after the migration plan", entry.MapID))
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	report.DryRun = false
	return report, nil
}
