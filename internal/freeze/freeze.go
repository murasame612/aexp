package freeze

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ziwu/aexp/internal/store"
)

type RoleRule struct {
	Role     string   `json:"role"`
	Patterns []string `json:"patterns"`
	Required bool     `json:"required"`
}

type Profile struct {
	Name             string     `json:"name"`
	Storage          string     `json:"storage"`
	StoragePrefix    string     `json:"storage_prefix"`
	Rules            []RoleRule `json:"rules"`
	WorkspaceRoles   []string   `json:"workspace_roles"`
	AggregateCommand string     `json:"aggregate_command,omitempty"`
	AggregateOutputs []string   `json:"aggregate_outputs,omitempty"`
	GateCommand      string     `json:"gate_command,omitempty"`
	GateReport       string     `json:"gate_report,omitempty"`
}

type Blocker struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Role    string `json:"role,omitempty"`
	Message string `json:"message"`
}

type PlannedFile struct {
	ArtifactID   string `json:"artifact_id"`
	Role         string `json:"role"`
	RelativePath string `json:"relative_path"`
	SourceURI    string `json:"source_uri"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Required     bool   `json:"required"`
}

type Plan struct {
	RunID             string                 `json:"run_id"`
	Profile           Profile                `json:"profile"`
	DestinationURI    string                 `json:"destination_uri"`
	WorkspacePath     string                 `json:"workspace_path,omitempty"`
	Eligible          bool                   `json:"eligible"`
	Blockers          []Blocker              `json:"blockers"`
	Files             []PlannedFile          `json:"files"`
	FileCount         int64                  `json:"file_count"`
	TotalBytes        int64                  `json:"total_bytes"`
	RunManifestSHA256 string                 `json:"run_manifest_sha256"`
	ProfileSHA256     string                 `json:"profile_sha256"`
	PlanSHA256        string                 `json:"plan_sha256"`
	FreezeID          string                 `json:"freeze_id"`
	Provenance        map[string]interface{} `json:"provenance"`
	TransferPath      string                 `json:"transfer_path"`
	LocalDataPath     bool                   `json:"local_data_path"`
}

type PersistedPlan struct {
	Plan Plan `json:"plan"`
}

func NewRecord(plan *Plan) (*store.RunFreeze, error) {
	if plan == nil {
		return nil, fmt.Errorf("freeze plan is required")
	}
	raw, err := json.Marshal(PersistedPlan{Plan: *plan})
	if err != nil {
		return nil, err
	}
	blockers, _ := json.Marshal(plan.Blockers)
	return &store.RunFreeze{ID: plan.FreezeID, RunID: plan.RunID, Profile: plan.Profile.Name, ProfileSHA256: plan.ProfileSHA256, PlanSHA256: plan.PlanSHA256, DestinationURI: plan.DestinationURI, WorkspacePath: plan.WorkspacePath, State: store.RunFreezeQueued, Stage: store.RunFreezeQueued, RunManifestSHA256: plan.RunManifestSHA256, ProvenanceJSON: string(raw), BlockersJSON: string(blockers), FileCount: plan.FileCount, TotalBytes: plan.TotalBytes}, nil
}

type Store interface {
	GetRun(context.Context, string) (*store.Run, error)
	GetDatasetVersion(context.Context, string) (*store.DatasetVersion, error)
	GetRunManifest(context.Context, string) (*store.RunManifest, error)
	GetArtifactCollection(context.Context, string) (*store.ArtifactCollection, error)
	ListArtifacts(context.Context, string) ([]store.Artifact, error)
}

func BuildPlan(ctx context.Context, db Store, runID string, profile Profile, destinationURI, workspacePath string) (*Plan, error) {
	run, err := db.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	manifest, err := db.GetRunManifest(ctx, runID)
	if err != nil {
		return nil, err
	}
	collection, err := db.GetArtifactCollection(ctx, runID)
	if err != nil {
		return nil, err
	}
	artifacts, err := db.ListArtifacts(ctx, runID)
	if err != nil {
		return nil, err
	}
	plan := &Plan{RunID: runID, Profile: profile, DestinationURI: destinationURI, WorkspacePath: workspacePath, Blockers: []Blocker{}, Files: []PlannedFile{}, TransferPath: "resource_to_storage", LocalDataPath: false}
	block := func(code, field, role, message string) {
		plan.Blockers = append(plan.Blockers, Blocker{Code: code, Field: field, Role: role, Message: message})
	}
	readiness, err := store.CheckRunClaimReadiness(ctx, db, run)
	if err != nil {
		return nil, err
	}
	for _, blocker := range readiness {
		block(blocker.Code, blocker.Field, "", blocker.Message)
	}
	if manifest == nil || manifest.State != store.RunManifestFinal || manifest.Completeness != store.RunManifestCompletenessCurrent {
		block("run_manifest_not_final", "run_manifest", "", "a final current run manifest is required")
	} else {
		plan.RunManifestSHA256 = manifest.SHA256
	}
	if collection == nil || collection.State != store.ArtifactCollectionIndexed {
		block("artifact_inventory_incomplete", "artifacts", "", "artifact inventory must be fully indexed")
	}
	if strings.TrimSpace(profile.Storage) == "" {
		block("freeze_profile_invalid", "storage", "", "paper profile requires a storage target")
	}
	if profile.Storage != "" && !strings.HasPrefix(destinationURI, "storage://"+profile.Storage+"/") {
		block("invalid_destination", "destination", "", "destination must use the profile storage target")
	}
	if len(profile.WorkspaceRoles) == 0 {
		block("freeze_profile_invalid", "workspace_roles", "", "paper profile must declare workspace roles")
	}
	if strings.TrimSpace(profile.AggregateCommand) == "" {
		block("freeze_profile_invalid", "aggregate.command", "", "paper profile requires an aggregate command")
	}
	if strings.TrimSpace(profile.GateCommand) == "" {
		block("freeze_profile_invalid", "release_gate.command", "", "paper profile requires a release gate command")
	}
	provenance, _ := store.ParseRunProvenance(run)
	plan.Provenance = map[string]interface{}{
		"git_commit": run.GitCommit, "git_dirty": run.GitDirty, "git_diff_hash": run.GitDiffHash,
		"project_config_sha256": run.ProjectConfigSHA256, "datasets": provenance.Datasets, "seeds": provenance.Seeds,
		"split_protocol": run.SplitProtocol, "evaluation_protocol": run.EvaluationProtocol, "recipe_name": run.RecipeName,
	}

	for _, rule := range profile.Rules {
		matched := 0
		for _, artifact := range artifacts {
			if !matchesAny(artifact.RelativePath, rule.Patterns) {
				continue
			}
			matched++
			if artifact.SHA256 == "" {
				block("artifact_checksum_missing", "sha256", rule.Role, artifact.RelativePath+" has no SHA-256")
			}
			plan.Files = append(plan.Files, PlannedFile{ArtifactID: artifact.ID, Role: rule.Role, RelativePath: artifact.RelativePath, SourceURI: artifact.SourceURI, SHA256: artifact.SHA256, Size: artifact.Size, Required: rule.Required})
			plan.TotalBytes += artifact.Size
		}
		if rule.Required && matched == 0 {
			block("missing_required_artifact", "artifacts", rule.Role, "no artifact matched required role "+rule.Role)
		}
	}
	sort.Slice(plan.Files, func(i, j int) bool {
		if plan.Files[i].Role == plan.Files[j].Role {
			return plan.Files[i].RelativePath < plan.Files[j].RelativePath
		}
		return plan.Files[i].Role < plan.Files[j].Role
	})
	plan.FileCount = int64(len(plan.Files))
	plan.Eligible = len(plan.Blockers) == 0
	profileRaw, _ := json.Marshal(profile)
	profileDigest := sha256.Sum256(profileRaw)
	plan.ProfileSHA256 = "sha256:" + hex.EncodeToString(profileDigest[:])
	identity := struct {
		RunManifest, Profile, Destination string
		Files                             []PlannedFile
	}{plan.RunManifestSHA256, plan.ProfileSHA256, destinationURI, plan.Files}
	identityRaw, _ := json.Marshal(identity)
	digest := sha256.Sum256(identityRaw)
	plan.PlanSHA256 = "sha256:" + hex.EncodeToString(digest[:])
	plan.FreezeID = "freeze_" + hex.EncodeToString(digest[:8])
	return plan, nil
}

func matchesAny(value string, patterns []string) bool {
	value = strings.TrimPrefix(strings.ReplaceAll(value, "\\", "/"), "./")
	for _, pattern := range patterns {
		if globRegex(pattern).MatchString(value) {
			return true
		}
	}
	return false
}

func globRegex(pattern string) *regexp.Regexp {
	pattern = strings.TrimPrefix(strings.ReplaceAll(pattern, "\\", "/"), "./")
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c == '*' {
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		} else if c == '?' {
			b.WriteString("[^/]")
		} else {
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
