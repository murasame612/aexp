package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DatasetVersionReader is the narrow registry boundary needed to prove that a
// dataset identity captured by a run came from a verified immutable version.
type DatasetVersionReader interface {
	GetDatasetVersion(ctx context.Context, id string) (*DatasetVersion, error)
}

// RunProvenance is the launch-time identity required before a run may be used
// as formal research evidence.
type RunProvenance struct {
	Datasets            []RunDatasetInput
	Seeds               []int64
	ProjectConfigSHA256 string
	GitCommit           string
	GitDirty            bool
	GitDiffHash         string
	SplitProtocol       string
	EvaluationProtocol  string
}

// RunReadinessBlocker is intentionally shared by launch, graph-proposal, and
// freeze gates so the three surfaces cannot drift on provenance semantics.
type RunReadinessBlocker struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// ParseRunProvenance decodes the immutable provenance snapshot stored on a
// Run. Malformed JSON is reported as a blocker instead of being treated as an
// empty but otherwise valid value.
func ParseRunProvenance(run *Run) (RunProvenance, []RunReadinessBlocker) {
	provenance := RunProvenance{}
	if run == nil {
		return provenance, []RunReadinessBlocker{{
			Code: "run_missing", Field: "run", Message: "run is required",
		}}
	}
	provenance.ProjectConfigSHA256 = strings.TrimSpace(run.ProjectConfigSHA256)
	provenance.GitCommit = strings.TrimSpace(run.GitCommit)
	provenance.GitDirty = run.GitDirty
	provenance.GitDiffHash = strings.TrimSpace(run.GitDiffHash)
	provenance.SplitProtocol = strings.TrimSpace(run.SplitProtocol)
	provenance.EvaluationProtocol = strings.TrimSpace(run.EvaluationProtocol)

	blockers := make([]RunReadinessBlocker, 0)
	if raw := strings.TrimSpace(run.DatasetsJSON); raw != "" && raw != "null" {
		if err := json.Unmarshal([]byte(raw), &provenance.Datasets); err != nil {
			blockers = append(blockers, RunReadinessBlocker{
				Code: "datasets_invalid", Field: "datasets", Message: "captured dataset provenance is not valid JSON",
			})
		}
	}
	if raw := strings.TrimSpace(run.SeedsJSON); raw != "" && raw != "null" {
		if err := json.Unmarshal([]byte(raw), &provenance.Seeds); err != nil {
			blockers = append(blockers, RunReadinessBlocker{
				Code: "seeds_invalid", Field: "seeds", Message: "captured seeds are not valid JSON",
			})
		}
	}
	return provenance, blockers
}

// CheckRunProvenance validates explicit provenance and proves every dataset
// identity against the authoritative registry. A nonempty caller-supplied hash
// is not evidence by itself.
func CheckRunProvenance(ctx context.Context, datasets DatasetVersionReader, provenance RunProvenance) ([]RunReadinessBlocker, error) {
	blockers := make([]RunReadinessBlocker, 0)
	if len(provenance.Datasets) == 0 {
		blockers = append(blockers, RunReadinessBlocker{
			Code: "dataset_missing", Field: "datasets", Message: "formal evidence requires at least one verified dataset version",
		})
	}
	for index, input := range provenance.Datasets {
		ref := datasetRef(input)
		if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.DatasetID) == "" || strings.TrimSpace(input.Version) == "" {
			blockers = append(blockers, RunReadinessBlocker{
				Code: "dataset_identity_missing", Field: "datasets",
				Message: fmt.Sprintf("dataset %d (%s) requires immutable id, dataset id, and version", index, ref),
			})
			continue
		}
		if strings.TrimSpace(input.ManifestSHA256) == "" {
			blockers = append(blockers, RunReadinessBlocker{
				Code: "dataset_revision_missing", Field: "datasets",
				Message: fmt.Sprintf("dataset %d (%s) requires a manifest SHA-256", index, ref),
			})
			continue
		}
		if datasets == nil {
			return nil, fmt.Errorf("dataset registry is unavailable")
		}
		registered, err := datasets.GetDatasetVersion(ctx, input.ID)
		if err != nil {
			return nil, fmt.Errorf("load dataset %s: %w", ref, err)
		}
		if registered == nil {
			blockers = append(blockers, RunReadinessBlocker{
				Code: "dataset_not_registered", Field: "datasets",
				Message: fmt.Sprintf("dataset %s (%s) is not present in the immutable registry", ref, input.ID),
			})
			continue
		}
		if registered.DatasetID != input.DatasetID || registered.Version != input.Version || registered.ManifestSHA256 != input.ManifestSHA256 {
			blockers = append(blockers, RunReadinessBlocker{
				Code: "dataset_provenance_mismatch", Field: "datasets",
				Message: fmt.Sprintf("dataset %s does not match immutable registry id %s", ref, input.ID),
			})
			continue
		}
		if registered.State != DatasetStateVerified {
			blockers = append(blockers, RunReadinessBlocker{
				Code: "dataset_not_verified", Field: "datasets",
				Message: fmt.Sprintf("dataset %s is %s; formal evidence requires verified bytes (use dataset ingest)", ref, registered.State),
			})
		}
	}
	if len(provenance.Seeds) == 0 {
		blockers = append(blockers, RunReadinessBlocker{
			Code: "seeds_missing", Field: "seeds", Message: "formal evidence requires explicit seeds",
		})
	}
	if strings.TrimSpace(provenance.ProjectConfigSHA256) == "" {
		blockers = append(blockers, RunReadinessBlocker{
			Code: "project_config_hash_missing", Field: "project_config_sha256", Message: "formal evidence requires a project config SHA-256",
		})
	}
	if strings.TrimSpace(provenance.GitCommit) == "" {
		blockers = append(blockers, RunReadinessBlocker{
			Code: "git_commit_missing", Field: "git_commit", Message: "formal evidence requires a Git commit",
		})
	}
	if provenance.GitDirty && strings.TrimSpace(provenance.GitDiffHash) == "" {
		blockers = append(blockers, RunReadinessBlocker{
			Code: "git_diff_missing", Field: "git_diff_hash", Message: "dirty formal evidence requires a recorded Git diff hash",
		})
	}
	if strings.TrimSpace(provenance.SplitProtocol) == "" {
		blockers = append(blockers, RunReadinessBlocker{
			Code: "split_protocol_missing", Field: "split_protocol", Message: "formal evidence requires an explicit split protocol",
		})
	}
	if strings.TrimSpace(provenance.EvaluationProtocol) == "" {
		blockers = append(blockers, RunReadinessBlocker{
			Code: "evaluation_protocol_missing", Field: "evaluation_protocol", Message: "formal evidence requires an explicit evaluation protocol",
		})
	}
	return blockers, nil
}

// CheckStoredRunProvenance applies the canonical readiness policy to a
// persisted Run while preserving exact malformed-JSON blockers.
func CheckStoredRunProvenance(ctx context.Context, datasets DatasetVersionReader, run *Run) ([]RunReadinessBlocker, error) {
	provenance, parseBlockers := ParseRunProvenance(run)
	blockers, err := CheckRunProvenance(ctx, datasets, provenance)
	if err != nil {
		return nil, err
	}
	for _, parseBlocker := range parseBlockers {
		blockers = removeReadinessField(blockers, parseBlocker.Field)
		blockers = append(blockers, parseBlocker)
	}
	return blockers, nil
}

// CheckRunClaimReadiness adds claim-level lifecycle checks to the shared
// provenance gate. Descriptive graph relationships intentionally need not use
// this stricter helper.
func CheckRunClaimReadiness(ctx context.Context, datasets DatasetVersionReader, run *Run) ([]RunReadinessBlocker, error) {
	blockers := make([]RunReadinessBlocker, 0)
	if run == nil {
		return []RunReadinessBlocker{{Code: "run_missing", Field: "run", Message: "run is required"}}, nil
	}
	if run.Status != RunStatusSucceeded {
		blockers = append(blockers, RunReadinessBlocker{
			Code: "run_not_succeeded", Field: "status", Message: fmt.Sprintf("run %q status is %q", run.ID, run.Status),
		})
	}
	if run.EvidenceGrade != RunEvidenceGradeFormal {
		blockers = append(blockers, RunReadinessBlocker{
			Code: "run_not_formal", Field: "evidence_grade",
			Message: fmt.Sprintf("run %q evidence grade is %q; formal evidence is required", run.ID, run.EvidenceGrade),
		})
	}
	provenanceBlockers, err := CheckStoredRunProvenance(ctx, datasets, run)
	if err != nil {
		return nil, err
	}
	return append(blockers, provenanceBlockers...), nil
}

func datasetRef(input RunDatasetInput) string {
	if strings.TrimSpace(input.DatasetID) == "" && strings.TrimSpace(input.Version) == "" {
		return "<unknown>"
	}
	return strings.TrimSpace(input.DatasetID) + "@" + strings.TrimSpace(input.Version)
}

func removeReadinessField(blockers []RunReadinessBlocker, field string) []RunReadinessBlocker {
	filtered := blockers[:0]
	for _, blocker := range blockers {
		if blocker.Field != field {
			filtered = append(filtered, blocker)
		}
	}
	return filtered
}
