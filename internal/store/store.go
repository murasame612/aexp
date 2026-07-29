package store

import (
	"context"
	"time"
)

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
	CreateRunWithBindings(ctx context.Context, r *Run, bindings RunBindings) error
	CreateRunWithLaunchJob(ctx context.Context, r *Run, job *RunLaunchJob, bindings ...RunBindings) error
	GetRun(ctx context.Context, id string) (*Run, error)
	AssignRunProject(ctx context.Context, runID, projectID, expectedProjectID, actor, reason string) (*RunProjectAssignmentResult, error)
	ListRuns(ctx context.Context, filter RunFilter) ([]Run, error)
	GetRunSummary(ctx context.Context, id string) (*RunSummary, error)
	ListRunSummaries(ctx context.Context, filter RunFilter) ([]RunSummary, error)
	ListRunChanges(ctx context.Context, afterSeq int64, updatedSince *time.Time, limit int) ([]RunChange, error)
	LatestRunChangeSeq(ctx context.Context) (int64, error)

	// Local experiment receipt printer
	GetPrinterSettings(ctx context.Context) (*PrinterSettings, error)
	ListPrinterRunEvents(ctx context.Context, afterSeq int64, limit int) ([]PrinterRunEvent, error)
	PrinterRunEligible(ctx context.Context, runID string, enabledFromEventSeq int64) (bool, error)
	ConfigurePrinter(ctx context.Context, enabled bool, queue string) (*PrinterSettings, error)
	EnqueuePrintJobsAndAdvanceCursor(ctx context.Context, expectedCursor, nextCursor int64, jobs []PrintJob) (bool, error)
	EnqueuePrintJob(ctx context.Context, job *PrintJob) error
	ClaimNextPrintJob(ctx context.Context) (*PrintJob, bool, error)
	CompletePrintJob(ctx context.Context, id, cupsJobID string) error
	FailPrintJob(ctx context.Context, id, state, lastError string) error
	RecoverSubmittingPrintJobs(ctx context.Context) error
	ListPrintJobs(ctx context.Context, limit int) ([]PrintJob, error)
	RetryPrintJob(ctx context.Context, id string) (*PrintJob, error)
	SaveRunLaunchJob(ctx context.Context, job *RunLaunchJob) error
	ClaimRunLaunchJob(ctx context.Context, runID string) (*RunLaunchJob, bool, error)
	ListPendingRunLaunchJobs(ctx context.Context) ([]RunLaunchJob, error)
	RequeueInterruptedRunLaunchJobs(ctx context.Context) error
	CompleteRunLaunchJob(ctx context.Context, runID, state, lastError string) error
	CountRuns(ctx context.Context, filter RunFilter) (int, error)
	UpdateRunIfStatus(ctx context.Context, r *Run, expectedStatus string) (bool, error)
	UpdateRunStatusObservation(ctx context.Context, id, expectedStatus string, observation RunStatusObservation) (bool, error)
	UpdateRunFailureMetadata(ctx context.Context, id, expectedStatus, failureKind, failureReason, statusSource, statusCheckError string) (bool, error)
	UpdateRunDataFinalization(ctx context.Context, id, state, lastError string, updatedAt time.Time) error
	ArchiveRun(ctx context.Context, id string) error
	RestoreRun(ctx context.Context, id string) error
	DeleteRunLogically(ctx context.Context, id string) error
	ListRunInputBindings(ctx context.Context, runID string) ([]RunInputBinding, error)
	UpdateRunInputBinding(ctx context.Context, binding *RunInputBinding) error
	ListRunOutputBindings(ctx context.Context, runID string) ([]RunOutputBinding, error)
	UpdateRunOutputBinding(ctx context.Context, binding *RunOutputBinding) error

	// Project Profiles
	SaveProjectProfile(ctx context.Context, p *ProjectProfile) error
	GetProjectProfile(ctx context.Context, resourceID string, cwd string) (*ProjectProfile, error)
	CreateProjectDefinition(ctx context.Context, project *ProjectDefinition) error
	SaveProjectDefinition(ctx context.Context, project *ProjectDefinition) error
	GetProjectDefinition(ctx context.Context, id string) (*ProjectDefinition, error)
	ListProjectDefinitions(ctx context.Context) ([]ProjectDefinition, error)
	DeleteProjectDefinition(ctx context.Context, id string) error
	SaveProjectTarget(ctx context.Context, target *ProjectTarget) error
	BeginProjectTargetPrepare(ctx context.Context, id string, observedAt time.Time) (bool, error)
	GetProjectTarget(ctx context.Context, id string) (*ProjectTarget, error)
	ListProjectTargets(ctx context.Context, projectID string) ([]ProjectTarget, error)
	DeleteProjectTarget(ctx context.Context, id string) error

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
	SaveArtifactCollection(ctx context.Context, collection *ArtifactCollection) error
	GetArtifactCollection(ctx context.Context, runID string) (*ArtifactCollection, error)
	SaveRunManifest(ctx context.Context, manifest *RunManifest) error
	GetRunManifest(ctx context.Context, runID string) (*RunManifest, error)
	CreateEvidenceSnapshot(ctx context.Context, runID string) (*EvidenceSnapshot, bool, error)
	GetEvidenceSnapshot(ctx context.Context, id string) (*EvidenceSnapshot, error)
	ListEvidenceSnapshots(ctx context.Context, runID string) ([]EvidenceSnapshot, error)
	AppendEvidenceRelease(ctx context.Context, release *EvidenceRelease) error
	GetEvidenceRelease(ctx context.Context, id string) (*EvidenceRelease, error)
	ListEvidenceReleases(ctx context.Context, snapshotID string) ([]EvidenceRelease, error)
	ListProjectAssets(ctx context.Context, projectID string, limit, offset int) ([]ProjectAsset, int, error)

	// Data center: authoritative stores, immutable dataset versions, and
	// disposable compute-node materializations.
	SaveStorageTarget(ctx context.Context, target *StorageTarget) error
	GetStorageTarget(ctx context.Context, id string) (*StorageTarget, error)
	GetStorageTargetByName(ctx context.Context, name string) (*StorageTarget, error)
	ListStorageTargets(ctx context.Context) ([]StorageTarget, error)
	GetStorageTargetUsage(ctx context.Context, id string) (StorageTargetUsage, error)
	DeleteStorageTarget(ctx context.Context, id string) error
	SaveLogicalRoot(ctx context.Context, root *LogicalRoot) error
	GetLogicalRoot(ctx context.Context, id string) (*LogicalRoot, error)
	ListLogicalRoots(ctx context.Context, workspace string) ([]LogicalRoot, error)
	DeleteLogicalRoot(ctx context.Context, id string) error
	SavePathPlacement(ctx context.Context, placement *PathPlacement) error
	GetPathPlacement(ctx context.Context, id string) (*PathPlacement, error)
	ListPathPlacements(ctx context.Context, logicalURI string) ([]PathPlacement, error)
	UpdatePathPlacementObservation(ctx context.Context, id string, observation PlacementObservation) (bool, error)
	CreateTransferJobWithPlan(ctx context.Context, plan *TransferPlan, job *TransferJob, placements ...*PathPlacement) (*TransferJob, bool, error)
	GetTransferPlan(ctx context.Context, planSHA256 string) (*TransferPlan, error)
	GetTransferJob(ctx context.Context, id string) (*TransferJob, error)
	ListTransferJobs(ctx context.Context, state string, limit int) ([]TransferJob, error)
	ListTransferJobsPage(ctx context.Context, state, workspace string, updatedSince *time.Time, limit, offset int) ([]TransferJob, error)
	ClaimTransferJob(ctx context.Context, id string) (*TransferJob, bool, error)
	RequeueCompletedTransferJob(ctx context.Context, id, planSHA256 string, totalBytes, fileCount int64) (*TransferJob, bool, error)
	TouchTransferJobHeartbeat(ctx context.Context, id, expectedState string, at time.Time) (bool, error)
	UpdateTransferJobIfState(ctx context.Context, job *TransferJob, expectedState string) (bool, error)
	SaveTransferAttempt(ctx context.Context, attempt *TransferAttempt) error
	ListTransferAttempts(ctx context.Context, transferID string) ([]TransferAttempt, error)
	SaveDatasetVersion(ctx context.Context, dataset *DatasetVersion) error
	CreateDatasetVersionImmutable(ctx context.Context, dataset *DatasetVersion) (*DatasetVersion, bool, error)
	GetDatasetVersion(ctx context.Context, id string) (*DatasetVersion, error)
	GetDatasetVersionByRef(ctx context.Context, datasetID, version string) (*DatasetVersion, error)
	ListDatasetVersions(ctx context.Context) ([]DatasetVersion, error)
	SaveDatasetMaterialization(ctx context.Context, materialization *DatasetMaterialization) error
	GetDatasetMaterialization(ctx context.Context, datasetVersionID, resourceID string) (*DatasetMaterialization, error)
	ListDatasetMaterializations(ctx context.Context, datasetVersionID string) ([]DatasetMaterialization, error)
	UpdateDatasetMaterializationIfState(ctx context.Context, materialization *DatasetMaterialization, expectedState string) (bool, error)
	CreateRunFreeze(ctx context.Context, freeze *RunFreeze) error
	GetRunFreeze(ctx context.Context, id string) (*RunFreeze, error)
	ListRunFreezes(ctx context.Context, runID string) ([]RunFreeze, error)
	UpdateRunFreezeIfState(ctx context.Context, freeze *RunFreeze, expectedState string) (bool, error)
	ReplaceRunFreezeFiles(ctx context.Context, freezeID string, files []RunFreezeFile) error
	ListRunFreezeFiles(ctx context.Context, freezeID string) ([]RunFreezeFile, error)

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

	// Project Journal
	CreateProjectJournalEntry(ctx context.Context, entry *ProjectJournalEntry) error
	GetProjectJournalEntry(ctx context.Context, id string) (*ProjectJournalEntry, error)
	ListProjectJournalEntries(ctx context.Context, filter ProjectJournalFilter) ([]ProjectJournalEntry, error)
	UpdateProjectJournalNextActionStatus(ctx context.Context, id, status string) (*ProjectJournalEntry, error)

	// Run Bookmarks
	SaveRunBookmark(ctx context.Context, b *RunBookmark) error
	GetRunBookmark(ctx context.Context, runID string) (*RunBookmark, error)
	ListRunBookmarks(ctx context.Context, filter RunBookmarkFilter) ([]RunBookmark, error)
	DeleteRunBookmark(ctx context.Context, runID string) error

	// Project Run Cards
	SaveProjectRunCard(ctx context.Context, c *ProjectRunCard) error
	ReassignProjectRunCard(ctx context.Context, runID, projectID, projectName string, expectedUpdatedAt time.Time) (*ProjectRunCard, error)
	GetProjectRunCard(ctx context.Context, runID string) (*ProjectRunCard, error)
	ListProjectRunCards(ctx context.Context, filter ProjectRunCardFilter) ([]ProjectRunCard, error)
	SubmitEvidenceGraphProposal(ctx context.Context, c *ProjectRunCard, patch *EvidenceGraphPatch) (*ProjectRunCard, error)
	PlanEvidenceGraphProposal(ctx context.Context, runID string) (*EvidenceGraphProposalPlan, error)
	ReviewEvidenceGraphProposal(ctx context.Context, runID, action, reviewer string) (*ProjectRunCard, error)
	CreateEvidenceProposal(ctx context.Context, proposal *EvidenceProposal, patch *EvidenceGraphPatch) (*EvidenceProposal, error)
	GetEvidenceProposal(ctx context.Context, id string) (*EvidenceProposal, error)
	ListEvidenceProposals(ctx context.Context, filter EvidenceProposalFilter) ([]EvidenceProposal, error)
	PlanEvidenceProposal(ctx context.Context, id string) (*EvidenceGraphProposalPlan, error)
	ReviewEvidenceProposal(ctx context.Context, id, action, reviewer string) (*EvidenceProposal, error)
	RerouteEvidenceProposal(ctx context.Context, id, targetChainID, routingReason string, projectLevelImpact bool) (*EvidenceProposal, error)
	PlanEvidenceMapOwnershipMigration(ctx context.Context, mappings map[string]string) (*EvidenceMapOwnershipMigrationReport, error)
	ApplyEvidenceMapOwnershipMigration(ctx context.Context, mappings map[string]string) (*EvidenceMapOwnershipMigrationReport, error)
	PlanEvidencePromotion(ctx context.Context, request EvidencePromotionRequest) (*EvidencePromotionPlan, error)
	CreateEvidencePromotion(ctx context.Context, request EvidencePromotionRequest, expectedPlanHash string) (*EvidenceProposal, error)

	// Manual Project Categories
	CreateManualProjectCategory(ctx context.Context, c *ManualProjectCategory) error
	GetManualProjectCategory(ctx context.Context, id string) (*ManualProjectCategory, error)
	ListManualProjectCategories(ctx context.Context) ([]ManualProjectCategory, error)
	AssignRunToManualProjectCategory(ctx context.Context, runID string, categoryID string) error
	GetRunProjectAssignment(ctx context.Context, runID string) (*RunProjectAssignment, error)
	ListRunProjectAssignments(ctx context.Context) ([]RunProjectAssignment, error)
	UnassignRunFromManualProjectCategory(ctx context.Context, runID string) error

	// Experiment Matrices
	CreateExperimentMatrix(ctx context.Context, m *ExperimentMatrix) error
	GetExperimentMatrix(ctx context.Context, id string) (*ExperimentMatrix, error)
	ListExperimentMatrices(ctx context.Context, filter ExperimentMatrixFilter) ([]ExperimentMatrix, error)
	UpdateExperimentMatrix(ctx context.Context, m *ExperimentMatrix) error
	DeleteExperimentMatrix(ctx context.Context, id string) error
	GetExperimentMatrixGrid(ctx context.Context, matrixID string) (*ExperimentMatrixGrid, error)
	SaveExperimentMatrixGrid(ctx context.Context, matrixID string, grid ExperimentMatrixGrid) error

	// Evidence Chains
	CreateEvidenceChain(ctx context.Context, c *EvidenceChain) error
	GetEvidenceChain(ctx context.Context, id string) (*EvidenceChain, error)
	GetActivePrimaryEvidenceChain(ctx context.Context, projectID string) (*EvidenceChain, error)
	EnsureProjectPrimaryEvidenceChain(ctx context.Context, projectID string) (*EvidenceChain, error)
	ListEvidenceChains(ctx context.Context, filter EvidenceChainFilter) ([]EvidenceChain, error)
	UpdateEvidenceChain(ctx context.Context, c *EvidenceChain) error
	DeleteEvidenceChain(ctx context.Context, id string) error
	GetEvidenceChainGraph(ctx context.Context, chainID string) (*EvidenceChainGraph, error)
	AuditEvidenceChain(ctx context.Context, chainID string) (*EvidenceChainAuditReport, error)
	SaveEvidenceChainGraph(ctx context.Context, chainID string, graph EvidenceChainGraph) error
	SaveEvidenceChainGraphCAS(ctx context.Context, chainID string, graph EvidenceChainGraph, opts EvidenceGraphSaveOptions) (*EvidenceChain, error)
	ListEvidenceChainRevisions(ctx context.Context, chainID string, limit int) ([]EvidenceChainRevision, error)
	GetEvidenceChainRevision(ctx context.Context, chainID string, revision int64) (*EvidenceChainRevision, error)
	ListEvidenceRunCandidates(ctx context.Context, filter EvidenceRunCandidateFilter) ([]EvidenceChainRunCandidate, error)

	// Exec Events
	SaveExecEvent(ctx context.Context, e *ExecEvent) error
	GetExecEvent(ctx context.Context, id string) (*ExecEvent, error)
	ListExecEvents(ctx context.Context, filter ExecEventFilter) ([]ExecEvent, error)
	CountExecEvents(ctx context.Context, filter ExecEventFilter) (int, error)

	// Lifecycle
	Close() error
}
