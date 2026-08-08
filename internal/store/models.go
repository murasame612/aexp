package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Resource represents a compute resource (SSH server, container, etc.)
type Resource struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Type            string     `json:"type"` // ssh, docker, local, slurm, k8s
	Host            string     `json:"host"`
	OSType          string     `json:"os_type"`
	Port            int        `json:"port"`
	User            string     `json:"user"`
	AuthRef         string     `json:"auth_ref"`
	SocksProxy      string     `json:"socks_proxy"`   // host:port for SOCKS5 proxy
	ProxyCommand    string     `json:"proxy_command"` // raw ProxyCommand (future)
	RootDir         string     `json:"root_dir"`
	RemotePath      string     `json:"remote_path"` // PATH prefix for non-interactive remote commands
	CondaBase       string     `json:"conda_base"`
	CondaInit       string     `json:"conda_init"`
	CondaEnv        string     `json:"conda_env"`
	GPUIndices      string     `json:"gpu_indices"`
	Tags            string     `json:"tags"`
	Status          string     `json:"status"`     // idle, busy, error, unreachable
	SSHStatus       string     `json:"ssh_status"` // unknown, ok, failed
	LastDoctorError string     `json:"last_doctor_error,omitempty"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Run represents a single experiment execution.
type Run struct {
	ID                        string               `json:"id"`
	ResourceID                string               `json:"resource_id"`
	ProjectID                 string               `json:"project_id,omitempty"`
	TargetID                  string               `json:"target_id,omitempty"`
	RecipeName                string               `json:"recipe_name,omitempty"`
	ProjectConfigSHA256       string               `json:"project_config_sha256,omitempty"`
	DatasetsJSON              string               `json:"datasets_json,omitempty"`
	SeedsJSON                 string               `json:"seeds_json,omitempty"`
	SplitProtocol             string               `json:"split_protocol,omitempty"`
	EvaluationProtocol        string               `json:"evaluation_protocol,omitempty"`
	DataFinalizationState     string               `json:"data_finalization_state"`
	DataFinalizationError     string               `json:"data_finalization_error,omitempty"`
	DataFinalizationUpdatedAt *time.Time           `json:"data_finalization_updated_at,omitempty"`
	Name                      string               `json:"name"`
	Status                    string               `json:"status"` // created,queued,starting,running,succeeded,failed,cancelled,lost,ssh_unreachable,container_expired,run_lost_but_events_cached
	LifecycleStatus           string               `json:"lifecycle_status"`
	ObservationState          string               `json:"observation_state"`
	ObservationError          *RunObservationError `json:"observation_error,omitempty"`
	StatusSource              string               `json:"status_source"`
	StatusObservedAt          *time.Time           `json:"status_observed_at,omitempty"`
	StatusCheckedAt           *time.Time           `json:"status_checked_at,omitempty"`
	StatusCheckError          string               `json:"status_check_error,omitempty"`
	StatusFreshness           string               `json:"status_freshness"`
	Kind                      string               `json:"kind"` // setup, smoke, pilot, formal, ablation
	TaskRole                  string               `json:"task_role"`
	EvidenceGrade             string               `json:"evidence_grade"`
	ExperimentRole            string               `json:"experiment_role"`
	GPUIndex                  int                  `json:"gpu_index"` // -2 = none, -1 = all, 0+ = specific GPU
	Cwd                       string               `json:"cwd"`
	Command                   string               `json:"command"`
	Program                   string               `json:"program"` // structured: python, bash, etc.
	ArgsJSON                  string               `json:"args_json"`
	CondaEnv                  string               `json:"conda_env"`
	ProjectEnv                string               `json:"project_env"`
	TargetEnv                 string               `json:"target_env"`
	ForceReason               string               `json:"force_reason,omitempty"`
	PreemptRunID              string               `json:"preempt_run_id,omitempty"`
	PreemptSave               bool                 `json:"preempt_save,omitempty"`
	GitRepoRoot               string               `json:"git_repo_root,omitempty"`
	GitRemoteURL              string               `json:"git_remote_url,omitempty"`
	GitBranch                 string               `json:"git_branch,omitempty"`
	GitCommit                 string               `json:"git_commit,omitempty"`
	GitDirty                  bool                 `json:"git_dirty,omitempty"`
	GitStatus                 string               `json:"git_status,omitempty"`
	GitDiffHash               string               `json:"git_diff_hash,omitempty"`
	GitDiffPath               string               `json:"git_diff_path,omitempty"`
	GitAllowDirty             bool                 `json:"git_allow_dirty,omitempty"`
	ResolvedEnv               string               `json:"resolved_env"`
	ResolvedPython            string               `json:"resolved_python"`
	ResolvedCwd               string               `json:"resolved_cwd"`
	EnvJSON                   string               `json:"env_json"`
	LogPathsJSON              string               `json:"log_paths_json"`
	ArtifactPathsJSON         string               `json:"artifact_paths_json"`
	MetricPathsJSON           string               `json:"metric_paths_json"`
	UIEventsPath              string               `json:"ui_events_path"`
	TmuxSession               string               `json:"tmux_session"`
	RemoteRunDir              string               `json:"remote_run_dir"`
	ExitCode                  sql.NullInt64        `json:"exit_code"`
	FailureKind               string               `json:"failure_kind,omitempty"`
	FailureReason             string               `json:"failure_reason,omitempty"`
	CreatedBy                 string               `json:"created_by"`
	CreatedAt                 time.Time            `json:"created_at"`
	StartedAt                 sql.NullTime         `json:"started_at"`
	FinishedAt                sql.NullTime         `json:"finished_at"`
	ArchivedAt                sql.NullTime         `json:"archived_at"`
	DeletedAt                 sql.NullTime         `json:"deleted_at"`
}

// RunSummary is the lightweight control-plane projection used by high-frequency
// run discovery. Large provenance, command arguments, and artifact declarations
// stay on the run detail endpoint.
type RunSummary struct {
	ID                        string               `json:"id"`
	ResourceID                string               `json:"resource_id"`
	ProjectID                 string               `json:"project_id,omitempty"`
	Name                      string               `json:"name"`
	Status                    string               `json:"status"`
	LifecycleStatus           string               `json:"lifecycle_status"`
	ObservationState          string               `json:"observation_state"`
	ObservationError          *RunObservationError `json:"observation_error,omitempty"`
	StatusSource              string               `json:"status_source"`
	StatusObservedAt          *time.Time           `json:"status_observed_at,omitempty"`
	StatusCheckedAt           *time.Time           `json:"status_checked_at,omitempty"`
	StatusCheckError          string               `json:"status_check_error,omitempty"`
	StatusFreshness           string               `json:"status_freshness"`
	DataFinalizationState     string               `json:"data_finalization_state"`
	DataFinalizationError     string               `json:"data_finalization_error,omitempty"`
	DataFinalizationUpdatedAt *time.Time           `json:"data_finalization_updated_at,omitempty"`
	Kind                      string               `json:"kind"`
	TaskRole                  string               `json:"task_role"`
	EvidenceGrade             string               `json:"evidence_grade"`
	ExperimentRole            string               `json:"experiment_role"`
	GPUIndex                  int                  `json:"gpu_index"`
	Cwd                       string               `json:"cwd,omitempty"`
	UIEventsPath              string               `json:"ui_events_path,omitempty"`
	CommandPreview            string               `json:"command_preview,omitempty"`
	CreatedAt                 time.Time            `json:"created_at"`
	StartedAt                 sql.NullTime         `json:"started_at"`
	FinishedAt                sql.NullTime         `json:"finished_at"`
	ArchivedAt                sql.NullTime         `json:"archived_at"`
	DeletedAt                 sql.NullTime         `json:"deleted_at"`
}

type RunChange struct {
	Seq       int64     `json:"seq"`
	RunID     string    `json:"run_id"`
	Operation string    `json:"operation"`
	ChangedAt time.Time `json:"changed_at"`
}

// PrinterSettings is the singleton configuration and durable run-change
// cursor for the local experiment receipt printer.
type PrinterSettings struct {
	Enabled             bool      `json:"enabled"`
	Queue               string    `json:"queue"`
	LastEventSeq        int64     `json:"last_event_seq"`
	EnabledFromEventSeq int64     `json:"enabled_from_event_seq"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// PrinterRunEvent is an immutable run lifecycle snapshot. Unlike run_changes,
// it retains the status at the moment of the write, which is required to keep
// receipt ordering correct when several runs change while the worker is busy.
type PrinterRunEvent struct {
	Seq        int64      `json:"seq"`
	RunID      string     `json:"run_id"`
	Operation  string     `json:"operation"`
	Status     string     `json:"status"`
	Kind       string     `json:"kind"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ChangedAt  time.Time  `json:"changed_at"`
}

// PrintJob is a durable request to submit one self-contained receipt to CUPS.
// "spooled" means CUPS accepted the job; it does not claim physical delivery.
type PrintJob struct {
	Ordinal     int64      `json:"ordinal"`
	ID          string     `json:"id"`
	RunID       string     `json:"run_id,omitempty"`
	Phase       string     `json:"phase"`
	EventSeq    int64      `json:"event_seq,omitempty"`
	State       string     `json:"state"`
	Queue       string     `json:"queue"`
	Title       string     `json:"title"`
	ReceiptText string     `json:"receipt_text,omitempty"`
	CUPSJobID   string     `json:"cups_job_id,omitempty"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

type RunLaunchJob struct {
	RunID       string    `json:"run_id"`
	RequestJSON string    `json:"-"`
	State       string    `json:"state"`
	Attempts    int       `json:"attempts"`
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const (
	RunLaunchQueued    = "queued"
	RunLaunchLaunching = "launching"
	RunLaunchSucceeded = "succeeded"
	RunLaunchBlocked   = "blocked"
	RunLaunchFailed    = "failed"
)

const (
	RunChangeUpsert = "upsert"
	RunChangeDelete = "delete"
)

type RunDatasetInput struct {
	ID             string `json:"id"`
	DatasetID      string `json:"dataset_id"`
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

// RunInputBinding is a pinned logical input that must be present on the
// compute resource before the remote process may start.
type RunInputBinding struct {
	ID                     string     `json:"id"`
	RunID                  string     `json:"run_id"`
	Ordinal                int        `json:"ordinal"`
	LogicalURI             string     `json:"logical_uri"`
	TargetPath             string     `json:"target_path"`
	Revision               string     `json:"revision,omitempty"`
	Mode                   string     `json:"mode"`
	SourcePlacementID      string     `json:"source_placement_id,omitempty"`
	DestinationPlacementID string     `json:"destination_placement_id,omitempty"`
	TransferID             string     `json:"transfer_id,omitempty"`
	State                  string     `json:"state"`
	ErrorCode              string     `json:"error_code,omitempty"`
	LastError              string     `json:"last_error,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	VerifiedAt             *time.Time `json:"verified_at,omitempty"`
}

// RunOutputBinding is a declared compute output and its durable logical
// destination. Its finalization state is independent from process lifecycle.
type RunOutputBinding struct {
	ID                     string     `json:"id"`
	RunID                  string     `json:"run_id"`
	Ordinal                int        `json:"ordinal"`
	SourcePattern          string     `json:"source_pattern"`
	LogicalURI             string     `json:"logical_uri"`
	Role                   string     `json:"role"`
	Required               bool       `json:"required"`
	SourcePlacementID      string     `json:"source_placement_id,omitempty"`
	DestinationPlacementID string     `json:"destination_placement_id,omitempty"`
	Revision               string     `json:"revision,omitempty"`
	TransferID             string     `json:"transfer_id,omitempty"`
	State                  string     `json:"state"`
	ErrorCode              string     `json:"error_code,omitempty"`
	LastError              string     `json:"last_error,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	PublishedAt            *time.Time `json:"published_at,omitempty"`
}

type RunBindings struct {
	Inputs  []RunInputBinding  `json:"inputs"`
	Outputs []RunOutputBinding `json:"outputs"`
}

const (
	RunDataFinalizationPending    = "pending"
	RunDataFinalizationPublishing = "publishing"
	RunDataFinalizationCompleted  = "completed"
	RunDataFinalizationBlocked    = "blocked"
	RunDataFinalizationFailed     = "failed"
	RunDataFinalizationSkipped    = "skipped"

	RunBindingPending    = "pending"
	RunBindingEnsuring   = "ensuring"
	RunBindingReady      = "ready"
	RunBindingPublishing = "publishing"
	RunBindingPublished  = "published"
	RunBindingMissing    = "missing"
	RunBindingBlocked    = "blocked"
	RunBindingFailed     = "failed"
	RunBindingSkipped    = "skipped"
)

// ProjectProfile captures how a project directory should be entered on a resource.
type ProjectProfile struct {
	ResourceID    string    `json:"resource_id"`
	ResourceName  string    `json:"resource"`
	Cwd           string    `json:"cwd"`
	EnvStrategy   string    `json:"env_strategy"`
	ResolvedEnv   string    `json:"resolved_env"`
	EnvName       string    `json:"env_name,omitempty"`
	Python        string    `json:"python"`
	ResolvedCwd   string    `json:"resolved_cwd"`
	CommandPrefix string    `json:"command_prefix,omitempty"`
	PythonOK      bool      `json:"python_ok"`
	TorchOK       bool      `json:"torch_ok"`
	CUDA          string    `json:"cuda"`
	CUDAOK        bool      `json:"cuda_ok"`
	Entrypoints   []string  `json:"entrypoints"`
	Metrics       []string  `json:"metrics"`
	Logs          []string  `json:"logs"`
	Warnings      []string  `json:"warnings,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ProjectDefinition is the durable identity of an executable project. It is
// deliberately separate from the legacy /projects evidence aggregation view.
type ProjectDefinition struct {
	ID                       string    `json:"id"`
	Name                     string    `json:"name"`
	Description              string    `json:"description,omitempty"`
	LocalRoot                string    `json:"local_root,omitempty"`
	ConfigPath               string    `json:"config_path,omitempty"`
	ConfigHash               string    `json:"config_hash,omitempty"`
	SourceRepo               string    `json:"source_repo,omitempty"`
	DefaultRecipe            string    `json:"default_recipe,omitempty"`
	Vault                    string    `json:"vault,omitempty"`
	RunCardIndex             string    `json:"run_card_index,omitempty"`
	ProposalDir              string    `json:"proposal_dir,omitempty"`
	PromotionDefault         string    `json:"promotion_default,omitempty"`
	AggregateCommand         string    `json:"aggregate_command,omitempty"`
	GateCommand              string    `json:"gate_command,omitempty"`
	ZoteroCollectionKey      string    `json:"zotero_collection_key,omitempty"`
	LiteratureServiceProfile string    `json:"literature_service_profile,omitempty"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

const (
	JournalNextActionNone = "none"
	JournalNextActionOpen = "open"
	JournalNextActionDone = "done"
)

// ProjectJournalEntry is the append-only reasoning layer between immutable Run
// execution records and curated Evidence Maps. An entry may stand on its own or
// cite one or more Runs from the same Project.
type ProjectJournalEntry struct {
	ID               string                `json:"id"`
	ProjectID        string                `json:"project_id"`
	Actor            string                `json:"actor"`
	Title            string                `json:"title"`
	BodyMD           string                `json:"body_md,omitempty"`
	NextAction       string                `json:"next_action,omitempty"`
	NextActionStatus string                `json:"next_action_status"`
	RunIDs           []string              `json:"run_ids"`
	LiteratureRefs   []LiteratureReference `json:"literature_refs,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

// LiteratureReference anchors Project reasoning to either an immutable corpus
// chunk or a live Zotero item. It is background provenance and never upgrades
// a Journal entry into experiment evidence.
type LiteratureReference struct {
	SourceKind     string `json:"source_kind"`
	ZoteroItemKey  string `json:"zotero_item_key"`
	ZoteroURI      string `json:"zotero_uri"`
	PageLabel      string `json:"page_label,omitempty"`
	CorpusRevision string `json:"corpus_revision,omitempty"`
	ChunkSHA256    string `json:"chunk_sha256,omitempty"`
	ItemVersion    int64  `json:"item_version,omitempty"`
	LibraryVersion int64  `json:"library_version,omitempty"`
}

type ProjectJournalFilter struct {
	ProjectID        string
	RunID            string
	Query            string
	NextActionStatus string
	Limit            int
	Offset           int
}

// ProjectTarget is the desired execution binding between one project and one
// resource. ProjectProfile remains an observed runtime cache, not desired state.
type ProjectTarget struct {
	ID                      string     `json:"id"`
	ProjectID               string     `json:"project_id"`
	Name                    string     `json:"name"`
	ResourceID              string     `json:"resource_id"`
	Cwd                     string     `json:"cwd"`
	EnvStrategy             string     `json:"env_strategy"`
	CondaEnv                string     `json:"conda_env,omitempty"`
	DesiredEnv              string     `json:"desired_env,omitempty"`
	DefaultGPU              int        `json:"default_gpu"`
	UIEventsPath            string     `json:"ui_events_path,omitempty"`
	EnvJSON                 string     `json:"env_json,omitempty"`
	SyncSource              string     `json:"sync_source,omitempty"`
	SyncTarget              string     `json:"sync_target,omitempty"`
	SyncProfile             string     `json:"sync_profile,omitempty"`
	PrepareCommand          string     `json:"prepare_command,omitempty"`
	Readiness               string     `json:"readiness"`
	ReadinessObservedAt     *time.Time `json:"readiness_observed_at,omitempty"`
	ReadinessError          string     `json:"readiness_error,omitempty"`
	LastPrepareRunID        string     `json:"last_prepare_run_id,omitempty"`
	LastPreparedAt          *time.Time `json:"last_prepared_at,omitempty"`
	ObservedConfigHash      string     `json:"observed_config_hash,omitempty"`
	ObservedEnvironmentHash string     `json:"observed_environment_hash,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

const (
	TargetReadinessUnknown  = "unknown"
	TargetReadinessChecking = "checking"
	TargetReadinessReady    = "ready"
	TargetReadinessDrifted  = "drifted"
	TargetReadinessFailed   = "failed"
)

const (
	RunTaskRolePrepare   = "prepare"
	RunTaskRoleTrain     = "train"
	RunTaskRoleEvaluate  = "evaluate"
	RunTaskRoleBenchmark = "benchmark"
	RunTaskRoleExport    = "export"
	RunTaskRoleAnalyze   = "analyze"
	RunTaskRoleOther     = "other"

	RunEvidenceGradeNone   = "none"
	RunEvidenceGradeSmoke  = "smoke"
	RunEvidenceGradePilot  = "pilot"
	RunEvidenceGradeFormal = "formal"

	RunExperimentRoleBaseline    = "baseline"
	RunExperimentRoleTreatment   = "treatment"
	RunExperimentRoleAblation    = "ablation"
	RunExperimentRoleReplication = "replication"
	RunExperimentRoleSweep       = "sweep"
	RunExperimentRoleDiagnostic  = "diagnostic"
	RunExperimentRoleUnspecified = "unspecified"
)

// NormalizeRunSemantics maps the legacy kind field onto three independent
// dimensions. If a caller sends both representations they must agree, which
// prevents a run from being labelled formal in one field and smoke in another.
func NormalizeRunSemantics(kind, taskRole, evidenceGrade, experimentRole string) (string, string, string, string, error) {
	kind = strings.TrimSpace(kind)
	taskRole = strings.TrimSpace(taskRole)
	evidenceGrade = strings.TrimSpace(evidenceGrade)
	experimentRole = strings.TrimSpace(experimentRole)
	if kind == "" && taskRole == "" && evidenceGrade == "" && experimentRole == "" {
		kind = RunKindFormal
	}
	legacyTask, legacyGrade, legacyExperiment, known := legacyRunKindSemantics(kind)
	if kind != "" && !known {
		return "", "", "", "", fmt.Errorf("invalid run kind %q", kind)
	}
	if kind != "" && (taskRole != "" || evidenceGrade != "" || experimentRole != "") {
		if taskRole != "" && taskRole != legacyTask || evidenceGrade != "" && evidenceGrade != legacyGrade || experimentRole != "" && experimentRole != legacyExperiment {
			return "", "", "", "", fmt.Errorf("legacy kind %q conflicts with task_role/evidence_grade/experiment_role", kind)
		}
	}
	if taskRole == "" {
		taskRole = legacyTask
	}
	if evidenceGrade == "" {
		evidenceGrade = legacyGrade
	}
	if experimentRole == "" {
		experimentRole = legacyExperiment
	}
	if !validRunTaskRole(taskRole) || !validRunEvidenceGrade(evidenceGrade) || !validRunExperimentRole(experimentRole) {
		return "", "", "", "", fmt.Errorf("invalid run semantics task_role=%q evidence_grade=%q experiment_role=%q", taskRole, evidenceGrade, experimentRole)
	}
	if kind == "" {
		kind = compatibilityRunKind(taskRole, evidenceGrade, experimentRole)
	}
	return kind, taskRole, evidenceGrade, experimentRole, nil
}

func legacyRunKindSemantics(kind string) (string, string, string, bool) {
	switch kind {
	case RunKindSetup:
		return RunTaskRolePrepare, RunEvidenceGradeNone, RunExperimentRoleUnspecified, true
	case RunKindSmoke:
		return RunTaskRoleOther, RunEvidenceGradeSmoke, RunExperimentRoleUnspecified, true
	case RunKindPilot:
		return RunTaskRoleOther, RunEvidenceGradePilot, RunExperimentRoleUnspecified, true
	case RunKindFormal:
		return RunTaskRoleOther, RunEvidenceGradeFormal, RunExperimentRoleUnspecified, true
	case RunKindAblation:
		return RunTaskRoleOther, RunEvidenceGradeFormal, RunExperimentRoleAblation, true
	default:
		return "", "", "", false
	}
}

func compatibilityRunKind(taskRole, evidenceGrade, experimentRole string) string {
	if taskRole == RunTaskRolePrepare && evidenceGrade == RunEvidenceGradeNone {
		return RunKindSetup
	}
	if experimentRole == RunExperimentRoleAblation && evidenceGrade == RunEvidenceGradeFormal {
		return RunKindAblation
	}
	switch evidenceGrade {
	case RunEvidenceGradeSmoke:
		return RunKindSmoke
	case RunEvidenceGradePilot:
		return RunKindPilot
	default:
		return RunKindFormal
	}
}

func validRunTaskRole(value string) bool {
	switch value {
	case RunTaskRolePrepare, RunTaskRoleTrain, RunTaskRoleEvaluate, RunTaskRoleBenchmark, RunTaskRoleExport, RunTaskRoleAnalyze, RunTaskRoleOther:
		return true
	default:
		return false
	}
}

func validRunEvidenceGrade(value string) bool {
	switch value {
	case RunEvidenceGradeNone, RunEvidenceGradeSmoke, RunEvidenceGradePilot, RunEvidenceGradeFormal:
		return true
	default:
		return false
	}
}

func validRunExperimentRole(value string) bool {
	switch value {
	case RunExperimentRoleBaseline, RunExperimentRoleTreatment, RunExperimentRoleAblation, RunExperimentRoleReplication, RunExperimentRoleSweep, RunExperimentRoleDiagnostic, RunExperimentRoleUnspecified:
		return true
	default:
		return false
	}
}

// RunFilter is used to filter runs when listing.
type RunFilter struct {
	ResourceID     string
	ProjectID      string
	ProjectScopeID string
	Status         string
	Query          string
	KindGroup      string
	Active         bool
	Trash          bool
	Deleted        bool
	ImportantOnly  bool
	Limit          int
	Offset         int
}

type RunStatusObservation struct {
	Source     string
	ObservedAt *time.Time
	CheckedAt  time.Time
	Error      string
}

type RunObservationError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Snapshot represents a resource's state at a point in time.
type Snapshot struct {
	ID         int64     `json:"id"`
	ResourceID string    `json:"resource_id"`
	RunID      string    `json:"run_id"`
	Timestamp  time.Time `json:"timestamp"`
	CPUPercent float64   `json:"cpu_percent"`
	MemUsedMB  float64   `json:"mem_used_mb"`
	MemTotalMB float64   `json:"mem_total_mb"`
	GPUJSON    string    `json:"gpu_json"`
	DiskJSON   string    `json:"disk_json"`
	Load1m     float64   `json:"load_1m"`
	Load5m     float64   `json:"load_5m"`
	Load15m    float64   `json:"load_15m"`
}

// LogLine represents a single log line from a run.
type LogLine struct {
	ID        int64     `json:"id"`
	RunID     string    `json:"run_id"`
	Source    string    `json:"source"` // stdout, stderr
	LineNo    int       `json:"line_no"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Artifact represents a file produced by a run.
type Artifact struct {
	ID              string    `json:"id"`
	RunID           string    `json:"run_id"`
	Path            string    `json:"path"`
	RelativePath    string    `json:"relative_path,omitempty"`
	SourceURI       string    `json:"source_uri,omitempty"`
	Type            string    `json:"type"` // file, dir
	Role            string    `json:"role,omitempty"`
	Mime            string    `json:"mime,omitempty"`
	Size            int64     `json:"size"`
	SHA256          string    `json:"sha256,omitempty"`
	CollectionState string    `json:"collection_state"`
	CollectionError string    `json:"collection_error,omitempty"`
	DiscoveredAt    time.Time `json:"discovered_at,omitempty"`
	ModifiedAt      time.Time `json:"modified_at"`
}

type ArtifactCollection struct {
	RunID      string     `json:"run_id"`
	State      string     `json:"state"`
	Error      string     `json:"error,omitempty"`
	FileCount  int        `json:"file_count"`
	TotalBytes int64      `json:"total_bytes"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// StorageTarget describes an authoritative external data store. ResourceID
// points at the registered machine hosting the store; the Mac keeps only this
// control-plane record and never stores dataset payloads.
type StorageTarget struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Kind          string               `json:"kind"`
	ResourceID    string               `json:"resource_id"`
	RootPath      string               `json:"root_path"`
	ConfigJSON    string               `json:"config_json,omitempty"`
	Status        string               `json:"status"`
	LastError     string               `json:"last_error,omitempty"`
	LastCheckedAt *time.Time           `json:"last_checked_at,omitempty"`
	Health        *StorageTargetHealth `json:"health,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

// StorageTargetHealth is the last persisted, read-only readiness probe for an
// authoritative store. It describes control-plane access only; it never
// transfers, changes, or deletes payload data.
type StorageTargetHealth struct {
	Status         string                        `json:"status"`
	ControlPlane   string                        `json:"control_plane"`
	Usable         bool                          `json:"usable"`
	Hostname       string                        `json:"hostname,omitempty"`
	LatencyMS      int64                         `json:"latency_ms"`
	Filesystem     string                        `json:"filesystem,omitempty"`
	TotalBytes     int64                         `json:"total_bytes,omitempty"`
	UsedBytes      int64                         `json:"used_bytes,omitempty"`
	AvailableBytes int64                         `json:"available_bytes,omitempty"`
	UsedPercent    int                           `json:"used_percent,omitempty"`
	Checks         map[string]StorageHealthCheck `json:"checks"`
	DataPlane      []StorageDataPlaneHealth      `json:"data_plane"`
	Error          string                        `json:"error,omitempty"`
	CheckedAt      time.Time                     `json:"checked_at"`
}

type StorageDataPlaneHealth struct {
	ResourceID        string                  `json:"resource_id"`
	ResourceName      string                  `json:"resource_name"`
	Status            string                  `json:"status"`
	SelectedInitiator string                  `json:"selected_initiator,omitempty"`
	ComputeInitiated  StorageConnectionHealth `json:"compute_initiated"`
	NASInitiated      StorageConnectionHealth `json:"nas_initiated"`
	// Legacy summary fields mirror the selected connection for older clients.
	LatencyMS    int64     `json:"latency_ms"`
	Rsync        bool      `json:"rsync"`
	NASReachable bool      `json:"nas_reachable"`
	Error        string    `json:"error,omitempty"`
	CheckedAt    time.Time `json:"checked_at"`
}

type StorageConnectionHealth struct {
	Status       string `json:"status"`
	LatencyMS    int64  `json:"latency_ms"`
	Rsync        bool   `json:"rsync"`
	SSHReachable bool   `json:"ssh_reachable"`
	Error        string `json:"error,omitempty"`
}

const (
	StorageInitiatorCompute = "compute"
	StorageInitiatorNAS     = "nas"
	NASInitiatedIdentity    = ".ssh/aexp_transfer_ed25519"
)

// SelectStorageInitiator prefers a NAS-side connection because it keeps NAS
// credentials off compute nodes. Compute-initiated transfer remains a fallback.
func SelectStorageInitiator(target *StorageTarget, resourceID string) string {
	if target == nil || target.Health == nil {
		return StorageInitiatorCompute
	}
	for _, edge := range target.Health.DataPlane {
		if edge.ResourceID != resourceID {
			continue
		}
		if edge.NASInitiated.Status == StorageStatusHealthy || edge.SelectedInitiator == StorageInitiatorNAS {
			return StorageInitiatorNAS
		}
		return StorageInitiatorCompute
	}
	return StorageInitiatorCompute
}

type StorageHealthCheck struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// StorageTargetUsage explains why deleting a control-plane record may be
// refused. Deleting a target never deletes files on the NAS.
type StorageTargetUsage struct {
	DatasetVersions int `json:"dataset_versions"`
	RunFreezes      int `json:"run_freezes"`
}

const (
	StorageKindSSHRsync      = "ssh_rsync"
	StorageStatusUnknown     = "unknown"
	StorageStatusHealthy     = "healthy"
	StorageStatusDegraded    = "degraded"
	StorageStatusUnreachable = "unreachable"
)

// LogicalRoot maps a workspace URI prefix to a relative directory within an
// existing StorageTarget. PhysicalRoot is deliberately relative: the backing
// StorageTarget owns the absolute machine-specific boundary.
type LogicalRoot struct {
	ID              string    `json:"id"`
	Workspace       string    `json:"workspace"`
	Prefix          string    `json:"prefix"`
	StorageTargetID string    `json:"storage_target_id"`
	PhysicalRoot    string    `json:"physical_root"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// PathPlacement is a known physical copy of a LogicalPath. DesiredState is
// policy intent; ObservedState is only the result of an actual observation and
// may truthfully disagree with it.
type PathPlacement struct {
	ID                string     `json:"id"`
	LogicalURI        string     `json:"logical_uri"`
	ResourceID        string     `json:"resource_id"`
	StorageTargetID   string     `json:"storage_target_id,omitempty"`
	PhysicalPath      string     `json:"physical_path"`
	Role              string     `json:"role"`
	DesiredState      string     `json:"desired_state"`
	ObservedState     string     `json:"observed_state"`
	Revision          string     `json:"revision,omitempty"`
	ManifestSHA256    string     `json:"manifest_sha256,omitempty"`
	BytesPresent      int64      `json:"bytes_present"`
	ObservationSource string     `json:"observation_source,omitempty"`
	ObservedAt        *time.Time `json:"observed_at,omitempty"`
	CheckedAt         *time.Time `json:"checked_at,omitempty"`
	ObservationError  string     `json:"observation_error,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type PlacementObservation struct {
	State          string
	Revision       string
	ManifestSHA256 string
	BytesPresent   int64
	Source         string
	ObservedAt     *time.Time
	CheckedAt      time.Time
	Error          string
}

const (
	PlacementRoleAuthoritative = "authoritative"
	PlacementRoleReplica       = "replica"
	PlacementRoleCache         = "cache"
	PlacementRoleProjection    = "projection"

	PlacementDesiredPresent = "present"
	PlacementDesiredAbsent  = "absent"

	PlacementObservedPresent     = "present"
	PlacementObservedMissing     = "missing"
	PlacementObservedUnknown     = "unknown"
	PlacementObservedUnreachable = "unreachable"
	PlacementObservedConflict    = "conflict"
)

// TransferPlan is the immutable normalized intent accepted by a worker. The
// JSON retains route candidates and blockers without placing large manifests
// in high-frequency TransferJob reads.
type TransferPlan struct {
	PlanSHA256             string    `json:"plan_sha256"`
	Workspace              string    `json:"workspace,omitempty"`
	SourceURI              string    `json:"source_uri"`
	DestinationURI         string    `json:"destination_uri"`
	SourcePlacementID      string    `json:"source_placement_id,omitempty"`
	DestinationPlacementID string    `json:"destination_placement_id,omitempty"`
	SourceRevision         string    `json:"source_revision,omitempty"`
	PlanJSON               string    `json:"plan_json"`
	ExpiresAt              time.Time `json:"expires_at"`
	CreatedAt              time.Time `json:"created_at"`
}

type TransferJob struct {
	ID                string     `json:"id"`
	PlanSHA256        string     `json:"plan_sha256"`
	State             string     `json:"state"`
	Stage             string     `json:"stage"`
	Attempt           int        `json:"attempt"`
	BytesDone         int64      `json:"bytes_done"`
	TotalBytes        int64      `json:"total_bytes"`
	FilesDone         int64      `json:"files_done"`
	FileCount         int64      `json:"file_count"`
	Initiator         string     `json:"initiator,omitempty"`
	CommandResourceID string     `json:"command_resource_id,omitempty"`
	HeartbeatAt       *time.Time `json:"heartbeat_at,omitempty"`
	ErrorCode         string     `json:"error_code,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	Retryable         bool       `json:"retryable"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
}

type TransferAttempt struct {
	ID         string     `json:"id"`
	TransferID string     `json:"transfer_id"`
	Number     int        `json:"number"`
	Initiator  string     `json:"initiator"`
	State      string     `json:"state"`
	ErrorCode  string     `json:"error_code,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

const (
	TransferQueued       = "queued"
	TransferPlanning     = "planning"
	TransferTransferring = "transferring"
	TransferVerifying    = "verifying"
	TransferPromoting    = "promoting"
	TransferCompleted    = "completed"
	TransferBlocked      = "blocked"
	TransferFailed       = "failed"
	TransferCancelling   = "cancelling"
	TransferCancelled    = "cancelled"
)

// DatasetVersion is an immutable logical dataset version whose authoritative
// bytes live on a StorageTarget.
type DatasetVersion struct {
	ID              string    `json:"id"`
	DatasetID       string    `json:"dataset_id"`
	Version         string    `json:"version"`
	StorageTargetID string    `json:"storage_target_id"`
	StoragePath     string    `json:"storage_path"`
	LogicalURI      string    `json:"logical_uri,omitempty"`
	Revision        string    `json:"revision,omitempty"`
	ManifestSHA256  string    `json:"manifest_sha256,omitempty"`
	ArchiveSHA256   string    `json:"archive_sha256,omitempty"`
	Format          string    `json:"format,omitempty"`
	FileCount       int64     `json:"file_count"`
	TotalBytes      int64     `json:"total_bytes"`
	State           string    `json:"state"`
	ManifestJSON    string    `json:"manifest_json,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type DatasetVersionConflictError struct {
	DatasetID         string
	Version           string
	ExistingURI       string
	ExistingRevision  string
	RequestedURI      string
	RequestedRevision string
}

func (e *DatasetVersionConflictError) Error() string {
	return fmt.Sprintf("dataset %s@%s is already pinned to %s at %s", e.DatasetID, e.Version, e.ExistingRevision, e.ExistingURI)
}

const (
	DatasetStateRegistered = "registered"
	DatasetStateVerified   = "verified"
	DatasetStateFailed     = "failed"
)

// DatasetMaterialization tracks a disposable cache of a dataset version on a
// compute resource. The authoritative copy remains on the storage target.
type DatasetMaterialization struct {
	ID               string     `json:"id"`
	DatasetVersionID string     `json:"dataset_version_id"`
	ResourceID       string     `json:"resource_id"`
	LocalPath        string     `json:"local_path"`
	State            string     `json:"state"`
	BytesPresent     int64      `json:"bytes_present"`
	VerifiedSHA256   string     `json:"verified_sha256,omitempty"`
	TransferID       string     `json:"transfer_id,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	VerifiedAt       *time.Time `json:"verified_at,omitempty"`
	LastAccessedAt   *time.Time `json:"last_accessed_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

const (
	MaterializationPlanned      = "planned"
	MaterializationTransferring = "transferring"
	MaterializationVerifying    = "verifying"
	MaterializationReady        = "ready"
	MaterializationFailed       = "failed"
)

type RunFreeze struct {
	ID                    string     `json:"id"`
	RunID                 string     `json:"run_id"`
	Profile               string     `json:"profile"`
	ProfileSHA256         string     `json:"profile_sha256"`
	PlanSHA256            string     `json:"plan_sha256"`
	DestinationURI        string     `json:"destination_uri"`
	WorkspacePath         string     `json:"workspace_path,omitempty"`
	State                 string     `json:"state"`
	Stage                 string     `json:"stage"`
	ErrorCode             string     `json:"error_code,omitempty"`
	LastError             string     `json:"last_error,omitempty"`
	RunManifestSHA256     string     `json:"run_manifest_sha256"`
	ProvenanceJSON        string     `json:"provenance_json"`
	BlockersJSON          string     `json:"blockers_json"`
	RawManifestSHA256     string     `json:"raw_manifest_sha256,omitempty"`
	ReleaseManifestSHA256 string     `json:"release_manifest_sha256,omitempty"`
	ManifestURI           string     `json:"manifest_uri,omitempty"`
	RawTransferID         string     `json:"raw_transfer_id,omitempty"`
	WorkspaceTransferID   string     `json:"workspace_transfer_id,omitempty"`
	FileCount             int64      `json:"file_count"`
	TotalBytes            int64      `json:"total_bytes"`
	FilesDone             int64      `json:"files_done"`
	BytesDone             int64      `json:"bytes_done"`
	AggregateResultJSON   string     `json:"aggregate_result_json,omitempty"`
	GateResultJSON        string     `json:"gate_result_json,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	FrozenAt              *time.Time `json:"frozen_at,omitempty"`
	ReleasedAt            *time.Time `json:"released_at,omitempty"`
}

type RunFreezeFile struct {
	ID               string    `json:"id"`
	FreezeID         string    `json:"freeze_id"`
	Kind             string    `json:"kind"`
	Role             string    `json:"role"`
	RelativePath     string    `json:"relative_path"`
	SourceURI        string    `json:"source_uri"`
	FrozenURI        string    `json:"frozen_uri,omitempty"`
	SHA256           string    `json:"sha256"`
	Size             int64     `json:"size"`
	Required         bool      `json:"required"`
	SourceArtifactID string    `json:"source_artifact_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

const (
	RunFreezeQueued       = "queued"
	RunFreezeCollecting   = "collecting"
	RunFreezeTransferring = "transferring"
	RunFreezeVerifying    = "verifying"
	RunFreezeFrozen       = "frozen"
	RunFreezeAggregating  = "aggregating"
	RunFreezeGateChecking = "gate_checking"
	RunFreezeReleased     = "released"
	RunFreezeBlocked      = "blocked"
	RunFreezeFailed       = "failed"
)

const (
	ArtifactCollectionDeclared    = "declared"
	ArtifactCollectionDiscovering = "discovering"
	ArtifactCollectionIndexed     = "indexed"
	ArtifactCollectionPartial     = "partial"
	ArtifactCollectionFailed      = "failed"
)

type RunManifest struct {
	RunID         string     `json:"run_id"`
	SchemaVersion int        `json:"schema_version"`
	State         string     `json:"state"`
	ManifestJSON  string     `json:"manifest_json"`
	SHA256        string     `json:"sha256"`
	Completeness  string     `json:"completeness"`
	CreatedAt     time.Time  `json:"created_at"`
	FinalizedAt   *time.Time `json:"finalized_at,omitempty"`
}

// EvidenceSnapshot is an immutable, transport-free reference to a final run
// manifest and the exact set of output revisions already published by RunIO.
type EvidenceSnapshot struct {
	ID                string    `json:"id"`
	RunID             string    `json:"run_id"`
	ProjectID         string    `json:"project_id"`
	RunManifestSHA256 string    `json:"run_manifest_sha256"`
	OutputSetSHA256   string    `json:"output_set_sha256"`
	ManifestJSON      string    `json:"manifest_json"`
	ManifestSHA256    string    `json:"manifest_sha256"`
	CreatedAt         time.Time `json:"created_at"`
}

type EvidenceSnapshotOutput struct {
	BindingID  string `json:"binding_id"`
	Ordinal    int    `json:"ordinal"`
	LogicalURI string `json:"logical_uri"`
	Role       string `json:"role,omitempty"`
	Revision   string `json:"revision"`
	Required   bool   `json:"required"`
}

// EvidenceSnapshotBlocker is returned before any snapshot row is written.
type EvidenceSnapshotBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type EvidenceSnapshotBlockedError struct {
	Blockers []EvidenceSnapshotBlocker `json:"blockers"`
}

func (e *EvidenceSnapshotBlockedError) Error() string {
	if e == nil || len(e.Blockers) == 0 {
		return "evidence snapshot is blocked"
	}
	return e.Blockers[0].Message
}

type EvidenceRelease struct {
	ID                  string    `json:"id"`
	SnapshotID          string    `json:"snapshot_id"`
	ProjectID           string    `json:"project_id"`
	Sequence            int       `json:"sequence"`
	State               string    `json:"state"`
	AggregateResultJSON string    `json:"aggregate_result_json"`
	GateResultJSON      string    `json:"gate_result_json"`
	ErrorCode           string    `json:"error_code,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

// ProjectAsset is the compact Project-scoped read model for already-published
// Run outputs. Logical URI + immutable revision are the identity; placements
// and transfer jobs remain diagnostic internals.
type ProjectAsset struct {
	ID          string     `json:"id"`
	ProjectID   string     `json:"project_id"`
	RunID       string     `json:"run_id"`
	LogicalURI  string     `json:"logical_uri"`
	Revision    string     `json:"revision"`
	Role        string     `json:"role,omitempty"`
	State       string     `json:"state"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

const (
	EvidenceReleaseReleased = "released"
	EvidenceReleaseBlocked  = "blocked"
	EvidenceReleaseFailed   = "failed"
)

const (
	RunManifestDraft                     = "draft"
	RunManifestFinal                     = "final"
	RunManifestCompletenessCurrent       = "current"
	RunManifestCompletenessLegacyPartial = "legacy_partial"
)

// AgentEvent records an agent's action for audit.
type AgentEvent struct {
	ID         int64     `json:"id"`
	RunID      string    `json:"run_id"`
	Actor      string    `json:"actor"`
	ToolName   string    `json:"tool_name"`
	InputJSON  string    `json:"input_json"`
	OutputJSON string    `json:"output_json"`
	Timestamp  time.Time `json:"timestamp"`
}

// RunMark records a human/agent interpretation of a run.
type RunMark struct {
	ID          string              `json:"id"`
	RunID       string              `json:"run_id"`
	Actor       string              `json:"actor"`
	Kind        string              `json:"kind"`
	Title       string              `json:"title"`
	Statement   string              `json:"statement"`
	BodyMD      string              `json:"body_md"`
	Reason      string              `json:"reason"`
	Evidence    string              `json:"evidence"`
	Attachments []RunMarkAttachment `json:"attachments,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
}

// RunMarkAttachment is a local file copied into a run mark note.
type RunMarkAttachment struct {
	ID        string    `json:"id"`
	MarkID    string    `json:"mark_id"`
	Filename  string    `json:"filename"`
	LocalPath string    `json:"local_path"`
	Mime      string    `json:"mime"`
	Caption   string    `json:"caption"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// RunMarkFilter is used to filter run marks when listing.
type RunMarkFilter struct {
	RunID  string
	RunIDs []string
	Actor  string
	Kind   string
	Limit  int
	Offset int
}

// RunBookmark records a human-curated run favorite with a small note.
type RunBookmark struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RunBookmarkFilter is used to filter run bookmarks when listing.
type RunBookmarkFilter struct {
	Limit  int
	Offset int
}

// ProjectRunCard records the project-level interpretation of a run.
type ProjectRunCard struct {
	ID                 string     `json:"id"`
	ProjectID          string     `json:"project_id"`
	ProjectName        string     `json:"project_name"`
	RunID              string     `json:"run_id"`
	Question           string     `json:"question"`
	Verdict            string     `json:"verdict"`
	EvidenceLevel      string     `json:"evidence_level"`
	KeyMetrics         string     `json:"key_metrics"`
	ArtifactPaths      string     `json:"artifact_paths"`
	SupportsClaim      string     `json:"supports_claim"`
	WeakensClaim       string     `json:"weakens_claim"`
	NextAction         string     `json:"next_action"`
	Important          bool       `json:"important"`
	ShouldPromote      bool       `json:"should_promote"`
	ProposalReason     string     `json:"proposal_reason"`
	GraphRoutingReason string     `json:"graph_routing_reason"`
	RelatedRuns        string     `json:"related_runs"`
	GraphPatchJSON     string     `json:"graph_patch_json"`
	GraphStatus        string     `json:"graph_status"`
	ProposalHash       string     `json:"proposal_hash"`
	BaseGraphRevision  int64      `json:"base_graph_revision"`
	ReviewedAt         *time.Time `json:"reviewed_at,omitempty"`
	NoGraphImpact      bool       `json:"no_graph_impact"`
	GraphImpactReason  string     `json:"graph_impact_reason"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// ProjectRunCardFilter is used to filter project-level run cards.
type ProjectRunCardFilter struct {
	ProjectID     string
	RunID         string
	ImportantOnly bool
	Limit         int
	Offset        int
}

// ManualProjectCategory is a lightweight user-managed grouping for runs.
type ManualProjectCategory struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	RunCount    int       `json:"run_count,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// RunProjectAssignment records a manual run-to-category assignment.
type RunProjectAssignment struct {
	RunID        string    `json:"run_id"`
	CategoryID   string    `json:"category_id"`
	CategoryName string    `json:"category_name,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ExperimentMatrix is a human-readable comparison grid for a research question.
type ExperimentMatrix struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	SourceKind        string    `json:"source_kind"`
	SourceID          string    `json:"source_id"`
	SourceName        string    `json:"source_name"`
	DefaultMetricKey  string    `json:"default_metric_key"`
	DefaultMetricGoal string    `json:"default_metric_goal"`
	DataJSON          string    `json:"data_json"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ExperimentMatrixRow is a row axis entry in an Experiment Matrix.
type ExperimentMatrixRow struct {
	ID        string    `json:"id"`
	MatrixID  string    `json:"matrix_id"`
	Label     string    `json:"label"`
	Position  int       `json:"position"`
	DataJSON  string    `json:"data_json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExperimentMatrixColumn is a column axis entry in an Experiment Matrix.
type ExperimentMatrixColumn struct {
	ID        string    `json:"id"`
	MatrixID  string    `json:"matrix_id"`
	Label     string    `json:"label"`
	Position  int       `json:"position"`
	DataJSON  string    `json:"data_json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExperimentMatrixCell links evidence to a row/column comparison slot.
type ExperimentMatrixCell struct {
	ID            string    `json:"id"`
	MatrixID      string    `json:"matrix_id"`
	RowID         string    `json:"row_id"`
	ColumnID      string    `json:"column_id"`
	RunID         string    `json:"run_id"`
	ProjectCardID string    `json:"project_card_id"`
	Title         string    `json:"title"`
	Statement     string    `json:"statement"`
	MetricKey     string    `json:"metric_key"`
	MetricValue   string    `json:"metric_value"`
	Note          string    `json:"note"`
	DataJSON      string    `json:"data_json"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ExperimentMatrixGrid stores editable matrix axes and cells.
type ExperimentMatrixGrid struct {
	Rows    []ExperimentMatrixRow    `json:"rows"`
	Columns []ExperimentMatrixColumn `json:"columns"`
	Cells   []ExperimentMatrixCell   `json:"cells"`
}

// ExperimentMatrixDetail returns a matrix with its grid.
type ExperimentMatrixDetail struct {
	ExperimentMatrix
	Rows    []ExperimentMatrixRow    `json:"rows"`
	Columns []ExperimentMatrixColumn `json:"columns"`
	Cells   []ExperimentMatrixCell   `json:"cells"`
}

// ExperimentMatrixFilter is used to list Experiment Matrices.
type ExperimentMatrixFilter struct {
	Query      string
	SourceKind string
	SourceID   string
	Limit      int
	Offset     int
}

// EvidenceChain is a human-curated research reasoning board.
type EvidenceGraphRoutingHints struct {
	Recipes  []string `json:"recipes,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
}

type EvidenceChain struct {
	ID           string                    `json:"id"`
	Title        string                    `json:"title"`
	Description  string                    `json:"description"`
	RoutingHints EvidenceGraphRoutingHints `json:"routing_hints"`
	ProjectID    string                    `json:"project_id"`
	Role         string                    `json:"role"`
	Status       string                    `json:"status"`
	Revision     int64                     `json:"revision"`
	GraphHash    string                    `json:"graph_hash"`
	CreatedAt    time.Time                 `json:"created_at"`
	UpdatedAt    time.Time                 `json:"updated_at"`
}

// EvidenceChainNode is a node placed on an Evidence Chain board.
type EvidenceChainNode struct {
	ID      string `json:"id"`
	ChainID string `json:"chain_id"`
	Type    string `json:"type"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	RunID   string `json:"run_id"`
	// SourceRunIDs and SourceSnapshotIDs bind an observed result to the
	// immutable executions that produced it. RunID remains the compatibility
	// identity for a legacy type=run graph node.
	SourceRunIDs      []string   `json:"source_run_ids,omitempty"`
	SourceSnapshotIDs []string   `json:"source_snapshot_ids,omitempty"`
	ProjectCardID     string     `json:"project_card_id"`
	X                 float64    `json:"x"`
	Y                 float64    `json:"y"`
	Width             float64    `json:"width"`
	Height            float64    `json:"height"`
	Pinned            bool       `json:"pinned"`
	OccurredAt        *time.Time `json:"occurred_at,omitempty"`
	DataJSON          string     `json:"data_json"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// EvidenceChainEdge is a typed relationship between two Evidence Chain nodes.
type EvidenceChainEdge struct {
	ID           string    `json:"id"`
	ChainID      string    `json:"chain_id"`
	SourceNodeID string    `json:"source_node_id"`
	TargetNodeID string    `json:"target_node_id"`
	Type         string    `json:"type"`
	Label        string    `json:"label"`
	Rationale    string    `json:"rationale"`
	DataJSON     string    `json:"data_json"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// EvidenceChainGraph stores the current board graph.
type EvidenceChainGraph struct {
	Nodes []EvidenceChainNode `json:"nodes"`
	Edges []EvidenceChainEdge `json:"edges"`
}

// EvidenceChainRevision is an immutable semantic snapshot of an accepted graph.
type EvidenceChainRevision struct {
	ID         string    `json:"id"`
	ChainID    string    `json:"chain_id"`
	Revision   int64     `json:"revision"`
	GraphHash  string    `json:"graph_hash"`
	GraphJSON  string    `json:"graph_json"`
	Actor      string    `json:"actor"`
	SourceKind string    `json:"source_kind"`
	SourceID   string    `json:"source_id"`
	CreatedAt  time.Time `json:"created_at"`
}

type EvidenceMapReference struct {
	TargetMapID     string   `json:"target_map_id"`
	TargetRevision  int64    `json:"target_revision"`
	TargetGraphHash string   `json:"target_graph_hash"`
	TargetNodeIDs   []string `json:"target_node_ids,omitempty"`
	Summary         string   `json:"summary,omitempty"`
}

type EvidencePromotionRequest struct {
	SourceMapID   string   `json:"source_map_id"`
	SourceNodeIDs []string `json:"source_node_ids"`
	Summary       string   `json:"summary"`
	NodeType      string   `json:"node_type,omitempty"`
	Actor         string   `json:"actor,omitempty"`
}

type EvidencePromotionPlan struct {
	ProjectID             string                 `json:"project_id"`
	SourceMapID           string                 `json:"source_map_id"`
	SourceRevision        int64                  `json:"source_revision"`
	SourceGraphHash       string                 `json:"source_graph_hash"`
	SourceNodeIDs         []string               `json:"source_node_ids"`
	TargetPrimaryMapID    string                 `json:"target_primary_map_id"`
	TargetPrimaryRevision int64                  `json:"target_primary_revision"`
	Summary               string                 `json:"summary"`
	NodeType              string                 `json:"node_type"`
	Patch                 EvidenceGraphPatch     `json:"patch"`
	PlanHash              string                 `json:"plan_hash"`
	Eligible              bool                   `json:"eligible"`
	Blockers              []EvidenceGraphBlocker `json:"blockers"`
}

// EvidenceReorganizationPlan is a side-effect-free, revision-bound preview of
// one bounded semantic cleanup inside an existing Evidence Map.
type EvidenceReorganizationPlan struct {
	ProjectID       string                     `json:"project_id"`
	MapID           string                     `json:"map_id"`
	BaseRevision    int64                      `json:"base_revision"`
	BaseGraphHash   string                     `json:"base_graph_hash"`
	ResultGraphHash string                     `json:"result_graph_hash,omitempty"`
	Patch           EvidenceGraphPatch         `json:"patch"`
	PlanHash        string                     `json:"plan_hash"`
	Eligible        bool                       `json:"eligible"`
	Blockers        []EvidenceGraphBlocker     `json:"blockers"`
	Warnings        []EvidenceGraphWarning     `json:"warnings"`
	Before          EvidenceResearchProjection `json:"before"`
	After           EvidenceResearchProjection `json:"after"`
}

// EvidenceGraphSaveOptions controls compare-and-swap graph persistence.
// ExpectedRevision < 0 is reserved for compatibility callers.
type EvidenceGraphSaveOptions struct {
	ExpectedRevision int64
	Actor            string
	SourceKind       string
	SourceID         string
}

// EvidenceGraphPatch is an additive, reviewable Agent proposal.
type EvidenceLayoutIntent struct {
	Flow      string     `json:"flow"`
	Ranks     [][]string `json:"ranks"`
	Rationale string     `json:"rationale,omitempty"`
}

type EvidenceGraphPatch struct {
	ChainID       string                `json:"chain_id"`
	RoutingReason string                `json:"routing_reason,omitempty"`
	LayoutIntent  *EvidenceLayoutIntent `json:"layout_intent,omitempty"`
	Nodes         []EvidenceChainNode   `json:"nodes"`
	Edges         []EvidenceChainEdge   `json:"edges"`
	UpsertNodes   []EvidenceChainNode   `json:"upsert_nodes,omitempty"`
	UpsertEdges   []EvidenceChainEdge   `json:"upsert_edges,omitempty"`
	DeleteNodeIDs []string              `json:"delete_node_ids,omitempty"`
	DeleteEdgeIDs []string              `json:"delete_edge_ids,omitempty"`
}

// EvidenceGraphBlocker is a stable reason why a proposal cannot be accepted.
type EvidenceGraphBlocker struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	NodeID     string `json:"node_id,omitempty"`
	EdgeID     string `json:"edge_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"`
}

// EvidenceGraphWarning is advisory authoring feedback. Unlike a blocker, it
// never prevents a proposal from being reviewed or accepted.
type EvidenceGraphWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	NodeID  string `json:"node_id,omitempty"`
	EdgeID  string `json:"edge_id,omitempty"`
}

// EvidenceGraphProposalPlan is a side-effect-free acceptance preview.
type EvidenceGraphProposalPlan struct {
	ProposalID           string                      `json:"proposal_id,omitempty"`
	ProjectID            string                      `json:"project_id,omitempty"`
	RunID                string                      `json:"run_id"`
	ProjectCardID        string                      `json:"project_card_id"`
	ChainID              string                      `json:"chain_id"`
	RoutingReason        string                      `json:"routing_reason,omitempty"`
	ProposalHash         string                      `json:"proposal_hash"`
	Status               string                      `json:"status"`
	BaseGraphRevision    int64                       `json:"base_graph_revision"`
	CurrentGraphRevision int64                       `json:"current_graph_revision"`
	AppliedGraphRevision int64                       `json:"applied_graph_revision"`
	AutoRebased          bool                        `json:"auto_rebased,omitempty"`
	CurrentGraphHash     string                      `json:"current_graph_hash"`
	ResultGraphHash      string                      `json:"result_graph_hash,omitempty"`
	NodesAdded           int                         `json:"nodes_added"`
	EdgesAdded           int                         `json:"edges_added"`
	Eligible             bool                        `json:"eligible"`
	Blockers             []EvidenceGraphBlocker      `json:"blockers"`
	Warnings             []EvidenceGraphWarning      `json:"warnings"`
	ProjectedResearch    *EvidenceResearchProjection `json:"projected_research,omitempty"`
}

// EvidenceProposal is the Project-scoped, Run-optional proposal envelope used
// by Evidence Workspace V2. Project Run Card proposals remain as a compatibility
// projection and are adapted to this model at API boundaries.
type EvidenceProposal struct {
	ID                 string     `json:"id"`
	ProjectID          string     `json:"project_id"`
	TargetChainID      string     `json:"target_map_id,omitempty"`
	BaseGraphRevision  int64      `json:"base_graph_revision"`
	Actor              string     `json:"actor"`
	Summary            string     `json:"summary"`
	RoutingReason      string     `json:"routing_reason,omitempty"`
	ProjectLevelImpact bool       `json:"project_level_impact"`
	SourceRunIDs       []string   `json:"source_run_ids"`
	SourceSnapshotIDs  []string   `json:"source_snapshot_ids"`
	PatchJSON          string     `json:"patch_json"`
	Status             string     `json:"status"`
	ProposalHash       string     `json:"proposal_hash"`
	ReviewedBy         string     `json:"reviewed_by,omitempty"`
	ReviewedAt         *time.Time `json:"reviewed_at,omitempty"`
	SourceKind         string     `json:"source_kind,omitempty"`
	SourceID           string     `json:"source_id,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type EvidenceProposalFilter struct {
	ProjectID     string
	TargetChainID string
	Status        string
	Limit         int
	Offset        int
}

const (
	GraphProposalDraft      = "draft"
	GraphProposalNone       = "none"
	GraphProposalPending    = "pending"
	GraphProposalAccepted   = "accepted"
	GraphProposalRejected   = "rejected"
	GraphProposalExpired    = "expired"
	GraphProposalConflicted = "conflicted"
)

// EvidenceChainRunCandidate is a draggable run-like candidate for an Evidence Chain.
type EvidenceChainRunCandidate struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	RunID          string          `json:"run_id"`
	ProjectCardID  string          `json:"project_card_id"`
	ProjectID      string          `json:"project_id"`
	ProjectName    string          `json:"project_name"`
	Question       string          `json:"question"`
	Verdict        string          `json:"verdict"`
	EvidenceLevel  string          `json:"evidence_level"`
	KeyMetrics     string          `json:"key_metrics"`
	NextAction     string          `json:"next_action"`
	Run            *Run            `json:"run,omitempty"`
	ProjectRunCard *ProjectRunCard `json:"project_card,omitempty"`
}

// EvidenceChainFilter is used to list Evidence Chains.
type EvidenceChainFilter struct {
	Query     string
	ProjectID string
	Role      string
	Status    string
	Limit     int
	Offset    int
}

// EvidenceRunCandidateFilter is used to list draggable run candidates.
type EvidenceRunCandidateFilter struct {
	Query string
	Limit int
}

// Evidence Chain node type constants.
const (
	EvidenceNodeRun        = "run"
	EvidenceNodeDataset    = "dataset"
	EvidenceNodeProtocol   = "protocol"
	EvidenceNodeClaim      = "claim"
	EvidenceNodeIssue      = "issue"
	EvidenceNodeHypothesis = "hypothesis"
	EvidenceNodeExperiment = "experiment"
	EvidenceNodePlan       = "plan"
	EvidenceNodeConclusion = "conclusion"
	EvidenceNodeNote       = "note"
	EvidenceNodeMapRef     = "map_ref"
	EvidenceNodeGroup      = "group"
)

// Evidence Chain edge type constants.
const (
	EvidenceEdgeSupports     = "supports"
	EvidenceEdgeUses         = "uses"
	EvidenceEdgeWeakens      = "weakens"
	EvidenceEdgeRevealsIssue = "reveals_issue"
	EvidenceEdgeSupersedes   = "supersedes"
	EvidenceEdgeRelatedTo    = "related_to"
	EvidenceEdgeDoesNotProve = "does_not_prove"
	EvidenceEdgeNextStep     = "next_step"
	EvidenceEdgeCustom       = "custom"
)

// ExecEvent records a one-shot exec command for audit.
type ExecEvent struct {
	ID         string        `json:"id"`
	ResourceID string        `json:"resource_id"`
	Actor      string        `json:"actor"`
	Command    string        `json:"command"`
	Cwd        string        `json:"cwd"`
	ExitCode   sql.NullInt64 `json:"exit_code"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt sql.NullTime  `json:"finished_at"`
	DurationMs int64         `json:"duration_ms"`
	StdoutTail string        `json:"stdout_tail"`
	StderrTail string        `json:"stderr_tail"`
	CreatedAt  time.Time     `json:"created_at"`
}

// ExecEventFilter is used to filter exec events when listing.
type ExecEventFilter struct {
	ResourceID string
	Actor      string
	Limit      int
	Offset     int
}

// RunStatus constants
const (
	PrintPhaseStart = "start"
	PrintPhaseEnd   = "end"
	PrintPhaseTest  = "test"

	PrintJobQueued     = "queued"
	PrintJobSubmitting = "submitting"
	PrintJobSpooled    = "spooled"
	PrintJobFailed     = "failed"
	PrintJobUncertain  = "uncertain"

	RunStatusCreated             = "created"
	RunStatusQueued              = "queued"
	RunStatusPreflighting        = "preflighting"
	RunStatusStarting            = "starting"
	RunStatusRunning             = "running"
	RunStatusSucceeded           = "succeeded"
	RunStatusFailed              = "failed"
	RunStatusCancelled           = "cancelled"
	RunStatusLost                = "lost"
	RunStatusSSHUnreachable      = "ssh_unreachable"
	RunStatusContainerExpired    = "container_expired"
	RunStatusLostButEventsCached = "run_lost_but_events_cached"
)

// IsRunActiveLifecycleStatus returns true while a run is still expected to
// progress, including local queue/preflight phases that do not have a remote
// process to probe yet.
func IsRunActiveLifecycleStatus(status string) bool {
	switch status {
	case RunStatusCreated, RunStatusQueued, RunStatusPreflighting, RunStatusStarting, RunStatusRunning, RunStatusSSHUnreachable:
		return true
	default:
		return false
	}
}

// Run failure classification constants. These are intentionally coarse: they
// make the first UI answer useful without pretending to replace log reading.
const (
	RunFailureDependencyError  = "dependency_error"
	RunFailureImportError      = "import_error"
	RunFailureGPUBusy          = "gpu_busy"
	RunFailureNetworkReset     = "network_reset"
	RunFailureDataMissing      = "data_missing"
	RunFailureDiskFull         = "disk_full"
	RunFailureKilled137        = "killed_137"
	RunFailureEnvMismatch      = "env_mismatch"
	RunFailurePreflightBlocked = "preflight_blocked"
	RunFailureLaunchOrphaned   = "launch_orphaned"
	RunFailureUnknown          = "unknown"
)

// IsRunRefreshableStatus returns true when a status may still represent a live
// process and should be checked again against the resource control plane.
func IsRunRefreshableStatus(status string) bool {
	switch status {
	case RunStatusStarting, RunStatusRunning, RunStatusSSHUnreachable:
		return true
	default:
		return false
	}
}

// IsRunTerminalStatus returns true when a run should not be resumed by passive
// status refresh. It may still be used as provenance for a manual rerun.
func IsRunTerminalStatus(status string) bool {
	switch status {
	case RunStatusSucceeded, RunStatusFailed, RunStatusCancelled, RunStatusLost, RunStatusContainerExpired, RunStatusLostButEventsCached:
		return true
	default:
		return false
	}
}

const (
	RunStatusSourceLocalCache     = "local_cache"
	RunStatusSourceRemoteExitCode = "remote_exit_code"
	RunStatusSourceRemoteTmux     = "remote_tmux"
	RunStatusSourceRemoteProbe    = "remote_probe"

	RunStatusFreshnessUnknown = "unknown"
	RunStatusFreshnessFresh   = "fresh"
	RunStatusFreshnessStale   = "stale"

	RunObservationUnknown     = "unknown"
	RunObservationReachable   = "reachable"
	RunObservationUnreachable = "unreachable"
)

// RunObservationState describes whether the remote lifecycle authority can be
// observed independently of the last known lifecycle status.
func RunObservationState(source, checkError string) string {
	if checkError != "" {
		return RunObservationUnreachable
	}
	switch source {
	case RunStatusSourceRemoteExitCode, RunStatusSourceRemoteTmux, RunStatusSourceRemoteProbe:
		return RunObservationReachable
	default:
		return RunObservationUnknown
	}
}

func RunObservationErrorFrom(message string) *RunObservationError {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}
	lower := strings.ToLower(message)
	code := "remote_unreachable"
	switch {
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"):
		code = "remote_timeout"
	case strings.Contains(lower, "proxy") || strings.Contains(lower, "socks"):
		code = "proxy_unreachable"
	case strings.Contains(lower, "authentication"), strings.Contains(lower, "permission denied"), strings.Contains(lower, "handshake"):
		code = "ssh_auth_failed"
	}
	return &RunObservationError{Code: code, Message: message, Retryable: true}
}

const DefaultRunStatusFreshnessMaxAge = 45 * time.Second

// RunStatusFreshnessAt derives freshness from the most recent successful
// remote observation. Freshness is intentionally not persisted because it
// changes as wall-clock time advances.
func RunStatusFreshnessAt(run *Run, now time.Time, maxAge time.Duration) string {
	if run == nil {
		return RunStatusFreshnessUnknown
	}
	if IsRunTerminalStatus(run.Status) {
		return RunStatusFreshnessFresh
	}
	if run.StatusCheckError != "" {
		return RunStatusFreshnessStale
	}
	if run.StatusObservedAt == nil {
		return RunStatusFreshnessUnknown
	}
	if maxAge <= 0 {
		maxAge = DefaultRunStatusFreshnessMaxAge
	}
	if now.Sub(*run.StatusObservedAt) > maxAge {
		return RunStatusFreshnessStale
	}
	return RunStatusFreshnessFresh
}

// RefreshRunStatusFreshness updates the derived JSON-facing freshness field.
func RefreshRunStatusFreshness(run *Run, now time.Time) {
	if run != nil {
		if run.Status == RunStatusSSHUnreachable {
			run.LifecycleStatus = RunStatusRunning
			run.ObservationState = RunObservationUnreachable
			reason := run.StatusCheckError
			if reason == "" {
				reason = "legacy ssh_unreachable status; remote lifecycle observation unavailable"
			}
			run.ObservationError = RunObservationErrorFrom(reason)
			run.StatusFreshness = RunStatusFreshnessStale
			return
		}
		run.LifecycleStatus = run.Status
		run.ObservationState = RunObservationState(run.StatusSource, run.StatusCheckError)
		run.ObservationError = RunObservationErrorFrom(run.StatusCheckError)
		run.StatusFreshness = RunStatusFreshnessAt(run, now, DefaultRunStatusFreshnessMaxAge)
	}
}

// RunKind constants
const (
	RunKindSetup    = "setup"    // environment/data preparation; not experiment evidence
	RunKindSmoke    = "smoke"    // quick test, not for real results
	RunKindPilot    = "pilot"    // preliminary run to verify setup
	RunKindFormal   = "formal"   // legacy candidate-formal label; claim readiness still requires provenance/comparability gates
	RunKindAblation = "ablation" // systematic variation experiment
)

// GPU index sentinel values used by runs.
const (
	GPUIndexNone = -2 // no GPU lock and no CUDA_VISIBLE_DEVICES injection
	GPUIndexAll  = -1 // lock all GPUs
)

// ResourceStatus constants
const (
	ResourceStatusIdle        = "idle"
	ResourceStatusBusy        = "busy"
	ResourceStatusError       = "error"
	ResourceStatusUnreachable = "unreachable"
	ResourceStatusUnknown     = "unknown"
	ResourceStatusDeleted     = "deleted"
)

// Resource SSH/control-channel status constants.
const (
	ResourceSSHStatusUnknown = "unknown"
	ResourceSSHStatusOK      = "ok"
	ResourceSSHStatusFailed  = "failed"
)

// ResourceType constants
const (
	ResourceTypeSSH    = "ssh"
	ResourceTypeDocker = "docker"
	ResourceTypeLocal  = "local"
	ResourceTypeSlurm  = "slurm"
	ResourceTypeK8s    = "k8s"
	// ResourceTypeTombstone preserves historical foreign-key provenance while
	// keeping a deleted resource out of active monitoring and UI inventories.
	ResourceTypeTombstone = "tombstone"
)
