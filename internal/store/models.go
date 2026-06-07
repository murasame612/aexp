package store

import (
	"database/sql"
	"time"
)

// Resource represents a compute resource (SSH server, container, etc.)
type Resource struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Type         string    `json:"type"` // ssh, docker, local, slurm, k8s
	Host         string    `json:"host"`
	Port         int       `json:"port"`
	User         string    `json:"user"`
	AuthRef      string    `json:"auth_ref"`
	SocksProxy   string    `json:"socks_proxy"`   // host:port for SOCKS5 proxy
	ProxyCommand string    `json:"proxy_command"` // raw ProxyCommand (future)
	RootDir      string    `json:"root_dir"`
	CondaBase    string    `json:"conda_base"`
	CondaInit    string    `json:"conda_init"`
	CondaEnv     string    `json:"conda_env"`
	GPUIndices   string    `json:"gpu_indices"`
	Tags         string    `json:"tags"`
	Status       string    `json:"status"` // idle, busy, error, unreachable
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Run represents a single experiment execution.
type Run struct {
	ID                string        `json:"id"`
	ResourceID        string        `json:"resource_id"`
	Name              string        `json:"name"`
	Status            string        `json:"status"`    // created,queued,starting,running,succeeded,failed,cancelled,lost
	Kind              string        `json:"kind"`      // smoke, pilot, formal, ablation
	GPUIndex          int           `json:"gpu_index"` // -1 = all, 0+ = specific GPU
	Cwd               string        `json:"cwd"`
	Command           string        `json:"command"`
	Program           string        `json:"program"` // structured: python, bash, etc.
	ArgsJSON          string        `json:"args_json"`
	CondaEnv          string        `json:"conda_env"`
	EnvJSON           string        `json:"env_json"`
	LogPathsJSON      string        `json:"log_paths_json"`
	ArtifactPathsJSON string        `json:"artifact_paths_json"`
	MetricPathsJSON   string        `json:"metric_paths_json"`
	TmuxSession       string        `json:"tmux_session"`
	RemoteRunDir      string        `json:"remote_run_dir"`
	ExitCode          sql.NullInt64 `json:"exit_code"`
	CreatedBy         string        `json:"created_by"`
	CreatedAt         time.Time     `json:"created_at"`
	StartedAt         sql.NullTime  `json:"started_at"`
	FinishedAt        sql.NullTime  `json:"finished_at"`
}

// RunFilter is used to filter runs when listing.
type RunFilter struct {
	ResourceID string
	Status     string
	Limit      int
	Offset     int
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
	ID         string    `json:"id"`
	RunID      string    `json:"run_id"`
	Path       string    `json:"path"`
	Type       string    `json:"type"` // file, dir
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

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

// RunStatus constants
const (
	RunStatusCreated   = "created"
	RunStatusQueued    = "queued"
	RunStatusStarting  = "starting"
	RunStatusRunning   = "running"
	RunStatusSucceeded = "succeeded"
	RunStatusFailed    = "failed"
	RunStatusCancelled = "cancelled"
	RunStatusLost      = "lost"
)

// RunKind constants
const (
	RunKindSmoke    = "smoke"    // quick test, not for real results
	RunKindPilot    = "pilot"    // preliminary run to verify setup
	RunKindFormal   = "formal"   // real experiment, results are trustworthy
	RunKindAblation = "ablation" // systematic variation experiment
)

// ResourceStatus constants
const (
	ResourceStatusIdle        = "idle"
	ResourceStatusBusy        = "busy"
	ResourceStatusError       = "error"
	ResourceStatusUnreachable = "unreachable"
	ResourceStatusUnknown     = "unknown"
)

// ResourceType constants
const (
	ResourceTypeSSH    = "ssh"
	ResourceTypeDocker = "docker"
	ResourceTypeLocal  = "local"
	ResourceTypeSlurm  = "slurm"
	ResourceTypeK8s    = "k8s"
)
