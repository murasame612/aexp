package store

import "context"

// Store defines the interface for data persistence.
type Store interface {
	// Resources
	CreateResource(ctx context.Context, r *Resource) error
	GetResource(ctx context.Context, id string) (*Resource, error)
	GetResourceByName(ctx context.Context, name string) (*Resource, error)
	ListResources(ctx context.Context) ([]Resource, error)
	UpdateResource(ctx context.Context, r *Resource) error
	DeleteResource(ctx context.Context, id string) error

	// Runs
	CreateRun(ctx context.Context, r *Run) error
	GetRun(ctx context.Context, id string) (*Run, error)
	ListRuns(ctx context.Context, filter RunFilter) ([]Run, error)
	UpdateRun(ctx context.Context, r *Run) error

	// Snapshots
	SaveSnapshot(ctx context.Context, s *Snapshot) error
	GetLatestSnapshot(ctx context.Context, resourceID string) (*Snapshot, error)
	ListSnapshots(ctx context.Context, resourceID string, limit int) ([]Snapshot, error)

	// Logs
	AppendLogLines(ctx context.Context, runID string, lines []LogLine) error
	GetLogLines(ctx context.Context, runID string, source string, offset, limit int) ([]LogLine, error)
	CountLogLines(ctx context.Context, runID string, source string) (int, error)

	// Artifacts
	SaveArtifacts(ctx context.Context, runID string, artifacts []Artifact) error
	ListArtifacts(ctx context.Context, runID string) ([]Artifact, error)

	// Agent Events
	SaveAgentEvent(ctx context.Context, e *AgentEvent) error
	ListAgentEvents(ctx context.Context, runID string) ([]AgentEvent, error)

	// Lifecycle
	Close() error
}
