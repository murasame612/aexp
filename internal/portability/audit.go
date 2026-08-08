package portability

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ziwu/aexp/internal/store"
)

const (
	SchemaVersion = "portability-audit-v1"
	ModeReadOnly  = "read_only"
	TargetMode    = "portable_control_plane"
)

const (
	ScopeControllerLocal = "controller_local"
	ScopeRemoteResource  = "remote_resource"
	ScopeStorageTarget   = "storage_target"
	ScopeLogical         = "logical"
)

const (
	StatePresent    = "present"
	StateMissing    = "missing"
	StateUnreadable = "unreadable"
	StateNotChecked = "not_checked"
	StateRecorded   = "recorded"
)

type AuditStore interface {
	ListProjectDefinitions(context.Context) ([]store.ProjectDefinition, error)
	ListProjectTargets(context.Context, string) ([]store.ProjectTarget, error)
	ListResources(context.Context) ([]store.Resource, error)
	ListStorageTargets(context.Context) ([]store.StorageTarget, error)
	ListRuns(context.Context, store.RunFilter) ([]store.Run, error)
	ListArtifacts(context.Context, string) ([]store.Artifact, error)
	ListDatasetVersions(context.Context) ([]store.DatasetVersion, error)
	ListLogicalRoots(context.Context, string) ([]store.LogicalRoot, error)
	ListPathPlacements(context.Context, string) ([]store.PathPlacement, error)
	ListRunMarks(context.Context, store.RunMarkFilter) ([]store.RunMark, error)
}

type Service struct {
	Store           AuditStore
	DatabasePath    string
	AttachmentsRoot string
	Now             func() time.Time
	Stat            func(string) (os.FileInfo, error)
}

type Report struct {
	SchemaVersion   string            `json:"schema_version"`
	Mode            string            `json:"mode"`
	TargetMode      string            `json:"target_mode"`
	GeneratedAt     time.Time         `json:"generated_at"`
	Status          string            `json:"status"`
	ReadyForBundle  bool              `json:"ready_for_bundle"`
	DatabasePath    string            `json:"database_path"`
	AttachmentsRoot string            `json:"attachments_root"`
	Summary         Summary           `json:"summary"`
	Resources       []ResourceBinding `json:"resources"`
	Paths           []PathReference   `json:"paths"`
	Findings        []Finding         `json:"findings"`
}

type Summary struct {
	Projects                   int `json:"projects"`
	ProjectTargets             int `json:"project_targets"`
	Runs                       int `json:"runs"`
	Artifacts                  int `json:"artifacts"`
	Datasets                   int `json:"datasets"`
	Resources                  int `json:"resources"`
	StorageTargets             int `json:"storage_targets"`
	LogicalRoots               int `json:"logical_roots"`
	PathPlacements             int `json:"path_placements"`
	Attachments                int `json:"attachments"`
	MissingAttachments         int `json:"missing_attachments"`
	MachineBoundPaths          int `json:"machine_bound_paths"`
	ControllerLocalPaths       int `json:"controller_local_paths"`
	MissingControllerFiles     int `json:"missing_controller_files"`
	ResourcesRequiringRebind   int `json:"resources_requiring_rebind"`
	DatasetsWithoutLogicalURI  int `json:"datasets_without_logical_uri"`
	ArtifactsWithoutLogicalRef int `json:"artifacts_without_logical_reference"`
	BlockingFindings           int `json:"blocking_findings"`
	Warnings                   int `json:"warnings"`
}

type ResourceBinding struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Endpoint       string `json:"endpoint,omitempty"`
	RootDir        string `json:"root_dir,omitempty"`
	SSHStatus      string `json:"ssh_status,omitempty"`
	RebindRequired bool   `json:"rebind_required"`
}

type PathReference struct {
	Scope        string `json:"scope"`
	EntityType   string `json:"entity_type"`
	EntityID     string `json:"entity_id"`
	Field        string `json:"field"`
	Path         string `json:"path"`
	LogicalURI   string `json:"logical_uri,omitempty"`
	State        string `json:"state"`
	MachineBound bool   `json:"machine_bound"`
}

type Finding struct {
	Severity          string `json:"severity"`
	Code              string `json:"code"`
	EntityType        string `json:"entity_type"`
	EntityID          string `json:"entity_id"`
	Field             string `json:"field,omitempty"`
	Message           string `json:"message"`
	RecommendedAction string `json:"recommended_action"`
}

func (s Service) Audit(ctx context.Context) (Report, error) {
	if s.Store == nil {
		return Report{}, fmt.Errorf("portability audit store is required")
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	stat := s.Stat
	if stat == nil {
		stat = os.Stat
	}
	report := Report{
		SchemaVersion:   SchemaVersion,
		Mode:            ModeReadOnly,
		TargetMode:      TargetMode,
		GeneratedAt:     now().UTC(),
		DatabasePath:    filepath.Clean(s.DatabasePath),
		AttachmentsRoot: filepath.Clean(s.AttachmentsRoot),
		Resources:       []ResourceBinding{},
		Paths:           []PathReference{},
		Findings:        []Finding{},
	}

	addFinding := func(severity, code, entityType, entityID, field, message, action string) {
		report.Findings = append(report.Findings, Finding{Severity: severity, Code: code, EntityType: entityType, EntityID: entityID, Field: field, Message: message, RecommendedAction: action})
	}
	addPath := func(scope, entityType, entityID, field, value, logicalURI string, checkLocal bool) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		ref := PathReference{Scope: scope, EntityType: entityType, EntityID: entityID, Field: field, Path: value, LogicalURI: strings.TrimSpace(logicalURI), State: StateNotChecked}
		ref.MachineBound = filepath.IsAbs(value) && scope != ScopeLogical
		if scope == ScopeControllerLocal {
			report.Summary.ControllerLocalPaths++
		}
		if checkLocal {
			if _, err := stat(filepath.Clean(value)); err == nil {
				ref.State = StatePresent
			} else if os.IsNotExist(err) {
				ref.State = StateMissing
				report.Summary.MissingControllerFiles++
				addFinding("error", localMissingCode(entityType, field), entityType, entityID, field, "controller-local durable path is missing", "restore the file or update the durable reference before creating a portability bundle")
				if entityType == "attachment" {
					report.Summary.MissingAttachments++
				}
			} else {
				ref.State = StateUnreadable
				report.Summary.MissingControllerFiles++
				addFinding("error", "CONTROLLER_PATH_UNREADABLE", entityType, entityID, field, "controller-local durable path cannot be inspected", "repair local permissions or the stored path before creating a portability bundle")
			}
		} else if scope == ScopeLogical {
			ref.State = StateRecorded
		}
		if ref.MachineBound {
			report.Summary.MachineBoundPaths++
		}
		report.Paths = append(report.Paths, ref)
	}

	addPath(ScopeControllerLocal, "workspace", "database", "database_path", report.DatabasePath, "", true)

	projects, err := s.Store.ListProjectDefinitions(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("list projects: %w", err)
	}
	report.Summary.Projects = len(projects)
	for _, project := range projects {
		addPath(ScopeControllerLocal, "project", project.ID, "local_root", project.LocalRoot, "", filepath.IsAbs(strings.TrimSpace(project.LocalRoot)))
		addPath(ScopeControllerLocal, "project", project.ID, "config_path", project.ConfigPath, "", filepath.IsAbs(strings.TrimSpace(project.ConfigPath)))
		addPath(ScopeControllerLocal, "project", project.ID, "vault", project.Vault, "", filepath.IsAbs(strings.TrimSpace(project.Vault)))
		addPath(ScopeControllerLocal, "project", project.ID, "run_card_index", project.RunCardIndex, "", filepath.IsAbs(strings.TrimSpace(project.RunCardIndex)))
		addPath(ScopeControllerLocal, "project", project.ID, "proposal_dir", project.ProposalDir, "", filepath.IsAbs(strings.TrimSpace(project.ProposalDir)))
	}

	targets, err := s.Store.ListProjectTargets(ctx, "")
	if err != nil {
		return Report{}, fmt.Errorf("list project targets: %w", err)
	}
	report.Summary.ProjectTargets = len(targets)
	for _, target := range targets {
		addPath(ScopeRemoteResource, "project_target", target.ID, "cwd", target.Cwd, "", false)
		addPath(ScopeRemoteResource, "project_target", target.ID, "ui_events_path", target.UIEventsPath, "", false)
		addPath(ScopeControllerLocal, "project_target", target.ID, "sync_source", target.SyncSource, "", filepath.IsAbs(strings.TrimSpace(target.SyncSource)))
		addPath(ScopeRemoteResource, "project_target", target.ID, "sync_target", target.SyncTarget, "", false)
	}

	resources, err := s.Store.ListResources(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("list resources: %w", err)
	}
	report.Summary.Resources = len(resources)
	for _, resource := range resources {
		rebind := resource.Type == store.ResourceTypeSSH
		endpoint := strings.TrimSpace(resource.Host)
		if endpoint != "" && resource.Port > 0 {
			endpoint = fmt.Sprintf("%s:%d", endpoint, resource.Port)
		}
		report.Resources = append(report.Resources, ResourceBinding{ID: resource.ID, Name: resource.Name, Type: resource.Type, Endpoint: endpoint, RootDir: resource.RootDir, SSHStatus: resource.SSHStatus, RebindRequired: rebind})
		addPath(ScopeRemoteResource, "resource", resource.ID, "root_dir", resource.RootDir, "", false)
		addPath(ScopeRemoteResource, "resource", resource.ID, "remote_path", resource.RemotePath, "", false)
		if rebind {
			report.Summary.ResourcesRequiringRebind++
			addFinding("warning", "RESOURCE_REBIND_REQUIRED", "resource", resource.ID, "", "SSH resource identity is preserved but controller credentials and reachability are not portable", "rebind authentication and run resource doctor on the restored controller")
		}
	}

	storageTargets, err := s.Store.ListStorageTargets(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("list storage targets: %w", err)
	}
	report.Summary.StorageTargets = len(storageTargets)
	for _, target := range storageTargets {
		addPath(ScopeStorageTarget, "storage_target", target.ID, "root_path", target.RootPath, "", false)
	}

	runs, err := s.Store.ListRuns(ctx, store.RunFilter{})
	if err != nil {
		return Report{}, fmt.Errorf("list runs: %w", err)
	}
	report.Summary.Runs = len(runs)
	for _, run := range runs {
		addPath(ScopeRemoteResource, "run", run.ID, "cwd", run.Cwd, "", false)
		addPath(ScopeRemoteResource, "run", run.ID, "resolved_cwd", run.ResolvedCwd, "", false)
		addPath(ScopeRemoteResource, "run", run.ID, "git_repo_root", run.GitRepoRoot, "", false)
		addPath(ScopeControllerLocal, "run", run.ID, "git_diff_path", run.GitDiffPath, "", filepath.IsAbs(strings.TrimSpace(run.GitDiffPath)))
		addPath(ScopeRemoteResource, "run", run.ID, "ui_events_path", run.UIEventsPath, "", false)
		addPath(ScopeRemoteResource, "run", run.ID, "remote_run_dir", run.RemoteRunDir, "", false)
		artifacts, err := s.Store.ListArtifacts(ctx, run.ID)
		if err != nil {
			return Report{}, fmt.Errorf("list artifacts for run %s: %w", run.ID, err)
		}
		report.Summary.Artifacts += len(artifacts)
		for _, artifact := range artifacts {
			scope := ScopeRemoteResource
			if strings.HasPrefix(strings.TrimSpace(artifact.SourceURI), "aexp://") || strings.HasPrefix(strings.TrimSpace(artifact.SourceURI), "storage://") || strings.HasPrefix(strings.TrimSpace(artifact.SourceURI), "resource://") {
				scope = ScopeLogical
			}
			addPath(scope, "artifact", artifact.ID, "path", artifact.Path, artifact.SourceURI, false)
			if filepath.IsAbs(strings.TrimSpace(artifact.Path)) && strings.TrimSpace(artifact.SourceURI) == "" && strings.TrimSpace(artifact.RelativePath) == "" {
				report.Summary.ArtifactsWithoutLogicalRef++
				addFinding("warning", "ARTIFACT_LOGICAL_REFERENCE_MISSING", "artifact", artifact.ID, "path", "absolute artifact path has no logical URI or relative path", "publish or record a logical artifact reference before relying on cross-controller recovery")
			}
		}
	}

	datasets, err := s.Store.ListDatasetVersions(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("list datasets: %w", err)
	}
	report.Summary.Datasets = len(datasets)
	for _, dataset := range datasets {
		addPath(ScopeStorageTarget, "dataset", dataset.ID, "storage_path", dataset.StoragePath, dataset.LogicalURI, false)
		if strings.TrimSpace(dataset.LogicalURI) == "" {
			report.Summary.DatasetsWithoutLogicalURI++
			addFinding("warning", "DATASET_LOGICAL_URI_MISSING", "dataset", dataset.ID, "logical_uri", "dataset version has no portable logical URI", "assign a verified logical URI without rewriting the immutable dataset identity")
		} else {
			addPath(ScopeLogical, "dataset", dataset.ID, "logical_uri", dataset.LogicalURI, dataset.LogicalURI, false)
		}
	}

	logicalRoots, err := s.Store.ListLogicalRoots(ctx, "")
	if err != nil {
		return Report{}, fmt.Errorf("list logical roots: %w", err)
	}
	report.Summary.LogicalRoots = len(logicalRoots)
	for _, root := range logicalRoots {
		logicalURI := "aexp://" + root.Workspace + "/" + strings.TrimPrefix(root.Prefix, "/")
		addPath(ScopeLogical, "logical_root", root.ID, "physical_root", root.PhysicalRoot, logicalURI, false)
	}

	placements, err := s.Store.ListPathPlacements(ctx, "")
	if err != nil {
		return Report{}, fmt.Errorf("list path placements: %w", err)
	}
	report.Summary.PathPlacements = len(placements)
	for _, placement := range placements {
		previousPathCount := len(report.Paths)
		addPath(ScopeRemoteResource, "path_placement", placement.ID, "physical_path", placement.PhysicalPath, placement.LogicalURI, false)
		if len(report.Paths) > previousPathCount {
			report.Paths[len(report.Paths)-1].State = firstNonEmpty(placement.ObservedState, StateNotChecked)
		}
	}

	marks, err := s.Store.ListRunMarks(ctx, store.RunMarkFilter{})
	if err != nil {
		return Report{}, fmt.Errorf("list run marks: %w", err)
	}
	for _, mark := range marks {
		for _, attachment := range mark.Attachments {
			report.Summary.Attachments++
			addPath(ScopeControllerLocal, "attachment", attachment.ID, "local_path", attachment.LocalPath, "aexp-attachment://"+attachment.ID, true)
			if report.AttachmentsRoot != "." && !pathWithin(report.AttachmentsRoot, attachment.LocalPath) {
				addFinding("warning", "ATTACHMENT_OUTSIDE_ROOT", "attachment", attachment.ID, "local_path", "attachment is outside the configured attachment root", "copy the attachment into the managed attachment root before export")
			}
		}
	}

	for _, finding := range report.Findings {
		switch finding.Severity {
		case "error":
			report.Summary.BlockingFindings++
		case "warning":
			report.Summary.Warnings++
		}
	}
	report.ReadyForBundle = report.Summary.BlockingFindings == 0
	report.Status = "ready"
	if !report.ReadyForBundle {
		report.Status = "blocked"
	} else if report.Summary.Warnings > 0 {
		report.Status = "needs_mapping"
	}
	sortReport(&report)
	return report, nil
}

func WriteHuman(w io.Writer, report Report) error {
	if _, err := fmt.Fprintf(w, "Portability audit: %s\n", report.Status); err != nil {
		return err
	}
	fmt.Fprintf(w, "mode:             %s\n", report.Mode)
	fmt.Fprintf(w, "target:           %s\n", report.TargetMode)
	fmt.Fprintf(w, "database:         %s\n", report.DatabasePath)
	fmt.Fprintf(w, "attachment root:  %s\n", report.AttachmentsRoot)
	fmt.Fprintf(w, "projects/runs:    %d / %d\n", report.Summary.Projects, report.Summary.Runs)
	fmt.Fprintf(w, "resources:        %d (%d require rebind)\n", report.Summary.Resources, report.Summary.ResourcesRequiringRebind)
	fmt.Fprintf(w, "artifacts:        %d (%d lack logical reference)\n", report.Summary.Artifacts, report.Summary.ArtifactsWithoutLogicalRef)
	fmt.Fprintf(w, "datasets:         %d (%d lack logical URI)\n", report.Summary.Datasets, report.Summary.DatasetsWithoutLogicalURI)
	fmt.Fprintf(w, "attachments:      %d (%d missing)\n", report.Summary.Attachments, report.Summary.MissingAttachments)
	fmt.Fprintf(w, "machine paths:    %d\n", report.Summary.MachineBoundPaths)
	fmt.Fprintf(w, "logical coverage: %d roots / %d placements\n", report.Summary.LogicalRoots, report.Summary.PathPlacements)
	fmt.Fprintf(w, "findings:         %d blocking / %d warnings\n", report.Summary.BlockingFindings, report.Summary.Warnings)
	if len(report.Findings) > 0 {
		fmt.Fprintln(w, "\nFindings:")
		for _, finding := range report.Findings {
			field := ""
			if finding.Field != "" {
				field = "." + finding.Field
			}
			fmt.Fprintf(w, "[%s] %s %s:%s%s — %s\n", strings.ToUpper(finding.Severity), finding.Code, finding.EntityType, finding.EntityID, field, finding.RecommendedAction)
		}
	}
	fmt.Fprintln(w, "\nRead-only audit: no data was moved or rewritten; remote availability was not checked.")
	return nil
}

func localMissingCode(entityType, field string) string {
	if entityType == "attachment" {
		return "ATTACHMENT_MISSING"
	}
	if entityType == "run" && field == "git_diff_path" {
		return "GIT_DIFF_MISSING"
	}
	if entityType == "workspace" {
		return "DATABASE_MISSING"
	}
	return "PROJECT_LOCAL_PATH_MISSING"
}

func pathWithin(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sortReport(report *Report) {
	sort.Slice(report.Resources, func(i, j int) bool {
		if report.Resources[i].Name != report.Resources[j].Name {
			return report.Resources[i].Name < report.Resources[j].Name
		}
		return report.Resources[i].ID < report.Resources[j].ID
	})
	sort.Slice(report.Paths, func(i, j int) bool {
		a, b := report.Paths[i], report.Paths[j]
		return pathSortKey(a) < pathSortKey(b)
	})
	severityOrder := map[string]int{"error": 0, "warning": 1, "info": 2}
	sort.Slice(report.Findings, func(i, j int) bool {
		a, b := report.Findings[i], report.Findings[j]
		if severityOrder[a.Severity] != severityOrder[b.Severity] {
			return severityOrder[a.Severity] < severityOrder[b.Severity]
		}
		return findingSortKey(a) < findingSortKey(b)
	})
}

func pathSortKey(ref PathReference) string {
	return strings.Join([]string{ref.Scope, ref.EntityType, ref.EntityID, ref.Field, ref.Path}, "\x00")
}

func findingSortKey(f Finding) string {
	return strings.Join([]string{f.Code, f.EntityType, f.EntityID, f.Field}, "\x00")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
