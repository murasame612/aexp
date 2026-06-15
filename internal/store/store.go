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
	CountRuns(ctx context.Context, filter RunFilter) (int, error)
	UpdateRun(ctx context.Context, r *Run) error
	ArchiveRun(ctx context.Context, id string) error
	RestoreRun(ctx context.Context, id string) error
	DeleteRunLogically(ctx context.Context, id string) error

	// Project Profiles
	SaveProjectProfile(ctx context.Context, p *ProjectProfile) error
	GetProjectProfile(ctx context.Context, resourceID string, cwd string) (*ProjectProfile, error)

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

	// Run Marks
	SaveRunMark(ctx context.Context, m *RunMark) error
	GetRunMark(ctx context.Context, id string) (*RunMark, error)
	ListRunMarks(ctx context.Context, filter RunMarkFilter) ([]RunMark, error)
	SaveRunMarkAttachments(ctx context.Context, markID string, attachments []RunMarkAttachment) error
	GetRunMarkAttachment(ctx context.Context, markID string, attachmentID string) (*RunMarkAttachment, error)
	ListRunMarkAttachments(ctx context.Context, markID string) ([]RunMarkAttachment, error)

	// Run Bookmarks
	SaveRunBookmark(ctx context.Context, b *RunBookmark) error
	GetRunBookmark(ctx context.Context, runID string) (*RunBookmark, error)
	ListRunBookmarks(ctx context.Context, filter RunBookmarkFilter) ([]RunBookmark, error)
	DeleteRunBookmark(ctx context.Context, runID string) error

	// Project Run Cards
	SaveProjectRunCard(ctx context.Context, c *ProjectRunCard) error
	GetProjectRunCard(ctx context.Context, runID string) (*ProjectRunCard, error)
	ListProjectRunCards(ctx context.Context, filter ProjectRunCardFilter) ([]ProjectRunCard, error)

	// Evidence Chains
	CreateEvidenceChain(ctx context.Context, c *EvidenceChain) error
	GetEvidenceChain(ctx context.Context, id string) (*EvidenceChain, error)
	ListEvidenceChains(ctx context.Context, filter EvidenceChainFilter) ([]EvidenceChain, error)
	UpdateEvidenceChain(ctx context.Context, c *EvidenceChain) error
	DeleteEvidenceChain(ctx context.Context, id string) error
	GetEvidenceChainGraph(ctx context.Context, chainID string) (*EvidenceChainGraph, error)
	SaveEvidenceChainGraph(ctx context.Context, chainID string, graph EvidenceChainGraph) error
	ListEvidenceRunCandidates(ctx context.Context, filter EvidenceRunCandidateFilter) ([]EvidenceChainRunCandidate, error)

	// Exec Events
	SaveExecEvent(ctx context.Context, e *ExecEvent) error
	GetExecEvent(ctx context.Context, id string) (*ExecEvent, error)
	ListExecEvents(ctx context.Context, filter ExecEventFilter) ([]ExecEvent, error)
	CountExecEvents(ctx context.Context, filter ExecEventFilter) (int, error)

	// Lifecycle
	Close() error
}
