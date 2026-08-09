export type Locale = "en" | "zh";

export interface HealthStatus {
  status: string;
  os_type: string;
  hostname: string;
}

export type NullableTime = string | null | { Time?: string; Valid?: boolean };

export interface PrinterStatus {
  enabled: boolean;
  queue: string;
  last_event_seq: number;
  available: boolean;
  queue_state: string;
  worker_state: string;
  last_error?: string;
  last_checked_at?: string;
  queued_jobs: number;
  failed_jobs: number;
  uncertain_jobs: number;
}

export interface PrinterJob {
  ordinal: number;
  id: string;
  run_id?: string;
  phase: "start" | "end" | "test";
  event_seq?: number;
  state: "queued" | "submitting" | "spooled" | "failed" | "uncertain";
  queue: string;
  title: string;
  cups_job_id?: string;
  attempts: number;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

export interface Resource {
  id: string;
  name: string;
  type: string;
  host: string;
  os_type?: string;
  port: number;
  user: string;
  auth_ref?: string;
  socks_proxy?: string;
  proxy_command?: string;
  root_dir: string;
  remote_path?: string;
  conda_base?: string;
  conda_init?: string;
  conda_env?: string;
  gpu_indices?: string;
  tags?: string;
  status: string;
  ssh_status?: string;
  last_doctor_error?: string;
  last_checked_at?: string;
  last_success_at?: string;
  latest_snapshot?: Snapshot;
}

export interface StorageTarget {
  id: string;
  name: string;
  kind: "ssh_rsync" | string;
  resource_id: string;
  root_path: string;
  status: "unknown" | "healthy" | "degraded" | "unreachable" | string;
  last_error?: string;
  last_checked_at?: string;
	health?: StorageTargetHealth;
	created_at?: string;
	updated_at?: string;
}

export interface StorageHealthCheck {
	ok: boolean;
	detail?: string;
}

export interface StorageDataPlaneHealth {
	resource_id: string;
	resource_name: string;
	status: string;
	selected_initiator?: "nas" | "compute";
	compute_initiated?: StorageConnectionHealth;
	nas_initiated?: StorageConnectionHealth;
	latency_ms: number;
	rsync: boolean;
	nas_reachable: boolean;
	error?: string;
	checked_at: string;
}

export interface StorageConnectionHealth {
	status: string;
	latency_ms: number;
	rsync: boolean;
	ssh_reachable: boolean;
	error?: string;
}

export interface StorageTargetHealth {
	status: string;
	control_plane: string;
	usable: boolean;
	hostname?: string;
	latency_ms: number;
	filesystem?: string;
	total_bytes?: number;
	used_bytes?: number;
	available_bytes?: number;
	used_percent?: number;
	checks: Record<string, StorageHealthCheck>;
	data_plane: StorageDataPlaneHealth[];
	error?: string;
	checked_at: string;
}

export interface DatasetMaterialization {
  id: string;
  dataset_version_id: string;
  resource_id: string;
  local_path: string;
  state: "planned" | "transferring" | "verifying" | "ready" | "failed" | string;
  bytes_present: number;
  verified_sha256?: string;
  last_error?: string;
  started_at?: string;
  finished_at?: string;
  verified_at?: string;
  updated_at?: string;
}

export interface DatasetVersion {
  id: string;
  dataset_id: string;
  version: string;
  storage_target_id: string;
  storage_path: string;
  manifest_sha256?: string;
  archive_sha256?: string;
  format?: string;
  file_count: number;
  total_bytes: number;
  state: string;
  materializations: DatasetMaterialization[];
}

export interface Snapshot {
  id?: number;
  resource_id: string;
  run_id?: string;
  timestamp?: string;
  cpu_percent?: number;
  mem_used_mb?: number;
  mem_total_mb?: number;
  gpu_json?: string;
  disk_json?: string;
  load_1m?: number;
  load_5m?: number;
  load_15m?: number;
}

export interface GPUInfo {
  index?: number;
  name?: string;
  util?: number;
  utilization?: number;
  mem_used?: number;
  mem_total?: number;
  mem_used_mb?: number;
  mem_total_mb?: number;
}

export interface Run {
  id: string;
  resource_id: string;
  project_id?: string;
  name?: string;
  status: string;
	lifecycle_status?: string;
	observation_state?: "unknown" | "reachable" | "unreachable" | string;
	observation_error?: RunObservationError;
	status_source?: string;
	status_observed_at?: string;
	status_checked_at?: string;
	status_check_error?: string;
	status_freshness?: "unknown" | "fresh" | "stale" | string;
  kind?: string;
  task_role?: "prepare" | "train" | "evaluate" | "benchmark" | "export" | "analyze" | "other" | string;
  evidence_grade?: "none" | "smoke" | "pilot" | "formal" | string;
  experiment_role?: "baseline" | "treatment" | "ablation" | "replication" | "sweep" | "diagnostic" | "unspecified" | string;
  gpu_index?: number;
  cwd?: string;
  command: string;
  command_preview?: string;
	project_config_sha256?: string;
	datasets_json?: string;
	seeds_json?: string;
	split_protocol?: string;
	evaluation_protocol?: string;
	data_finalization_state?: "pending" | "publishing" | "completed" | "blocked" | "failed" | "skipped" | string;
	data_finalization_error?: string;
	data_finalization_updated_at?: string;
  program?: string;
  args_json?: string;
  conda_env?: string;
  project_env?: string;
  target_env?: string;
  force_reason?: string;
  preempt_run_id?: string;
  preempt_save?: boolean;
  git_repo_root?: string;
  git_remote_url?: string;
  git_branch?: string;
  git_commit?: string;
  git_dirty?: boolean;
  git_status?: string;
  git_diff_hash?: string;
  git_diff_path?: string;
  git_allow_dirty?: boolean;
  resolved_env?: string;
  resolved_python?: string;
  resolved_cwd?: string;
  env_json?: string;
  log_paths_json?: string;
  artifact_paths_json?: string;
  metric_paths_json?: string;
  ui_events_path?: string;
  tmux_session?: string;
  remote_run_dir?: string;
  exit_code?: number | null | { Int64?: number; Valid?: boolean };
  failure_kind?: string;
  failure_reason?: string;
  created_by?: string;
  created_at?: string;
  started_at?: NullableTime;
  finished_at?: NullableTime;
  archived_at?: NullableTime;
  deleted_at?: NullableTime;
}

export interface RunObservationError {
  code: string;
  message: string;
  retryable: boolean;
}

export interface RunSummary {
  id: string;
  resource_id: string;
  project_id?: string;
  name?: string;
  status: string;
  lifecycle_status?: string;
  observation_state?: "unknown" | "reachable" | "unreachable" | string;
  observation_error?: RunObservationError;
  status_source?: string;
  status_observed_at?: string;
  status_checked_at?: string;
  status_check_error?: string;
  status_freshness?: "unknown" | "fresh" | "stale" | string;
  data_finalization_state?: string;
  data_finalization_error?: string;
  data_finalization_updated_at?: string;
  kind?: string;
  task_role?: string;
  evidence_grade?: string;
  experiment_role?: string;
  gpu_index?: number;
  cwd?: string;
  ui_events_path?: string;
  command_preview?: string;
  created_at?: string;
  started_at?: NullableTime;
  finished_at?: NullableTime;
  archived_at?: NullableTime;
  deleted_at?: NullableTime;
}

export interface RunChangeItem {
  seq: number;
  run_id: string;
  operation: "upsert" | "delete" | string;
  changed_at: string;
  run?: RunSummary;
}

export interface RunChangeResponse {
  items: RunChangeItem[];
  next_seq: number;
  server_time: string;
}

export interface FreezeBlocker { code: string; field?: string; role?: string; message: string; }
export interface FreezePlannedFile { artifact_id: string; role: string; relative_path: string; source_uri: string; sha256: string; size: number; required: boolean; }
export interface RunFreezePlan { run_id: string; eligible: boolean; blockers: FreezeBlocker[]; files: FreezePlannedFile[]; file_count: number; total_bytes: number; run_manifest_sha256: string; profile_sha256: string; plan_sha256: string; freeze_id: string; destination_uri: string; workspace_path?: string; transfer_path: string; local_data_path: boolean; provenance: Record<string, unknown>; }
export interface RunFreeze { id: string; run_id: string; profile: string; destination_uri: string; workspace_path?: string; state: string; stage: string; error_code?: string; last_error?: string; raw_transfer_id?: string; workspace_transfer_id?: string; raw_manifest_sha256?: string; release_manifest_sha256?: string; manifest_uri?: string; file_count: number; total_bytes: number; files_done: number; bytes_done: number; created_at: string; updated_at: string; }
export interface RunInputBinding { id:string; run_id:string; ordinal:number; logical_uri:string; target_path:string; revision?:string; mode:string; source_placement_id?:string; destination_placement_id?:string; transfer_id?:string; state:string; error_code?:string; last_error?:string; verified_at?:string; }
export interface RunOutputBinding { id:string; run_id:string; ordinal:number; source_pattern:string; logical_uri:string; role:string; required:boolean; source_placement_id?:string; destination_placement_id?:string; revision?:string; transfer_id?:string; state:string; error_code?:string; last_error?:string; published_at?:string; }
export interface RunDataBindings { inputs:RunInputBinding[]; outputs:RunOutputBinding[]; }
export interface LogicalRoot { id:string; workspace:string; prefix:string; storage_target_id:string; physical_root:string; created_at?:string; updated_at?:string; }
export interface PathPlacement { id:string; logical_uri:string; resource_id:string; storage_target_id?:string; physical_path:string; role:string; desired_state:string; observed_state:string; revision?:string; manifest_sha256?:string; bytes_present:number; observation_source?:string; observed_at?:string; checked_at?:string; observation_error?:string; freshness?:string; }
export interface TransferJob { id:string; plan_sha256:string; state:string; stage:string; attempt:number; bytes_done:number; total_bytes:number; files_done:number; file_count:number; initiator?:string; command_resource_id?:string; heartbeat_at?:string; error_code?:string; last_error?:string; retryable:boolean; created_at:string; updated_at:string; }
export interface TransferEndpoint { uri:string; resource_id?:string; resource_name?:string; physical_path:string; role?:string; observed_state?:string; freshness?:string; revision?:string; }
export interface TransferSummary { job:TransferJob; source?:TransferEndpoint; destination?:TransferEndpoint; initiator?:string; command_resource_id?:string; payload_direction?:string; local_data_path?:boolean; }
export interface RunFreezeFile { id: string; freeze_id: string; kind: string; role: string; relative_path: string; frozen_uri?: string; sha256: string; size: number; required: boolean; }

export interface LogLine {
  id?: number;
  run_id?: string;
  source?: string;
  line_no?: number;
  content: string;
  timestamp?: string;
}

export interface LogsResponse {
  run_id: string;
  source: string;
  path?: string;
  total_lines: number;
  offset: number;
  limit: number;
  lines: LogLine[];
  remote?: boolean;
  tail?: boolean;
  first_line?: number;
  last_line?: number;
	next_cursor?: number;
	reset?: boolean;
  truncated?: boolean;
  error?: string;
  error_kind?: string;
}

export interface Artifact {
  id: string;
  run_id: string;
  path: string;
  relative_path?: string;
  source_uri?: string;
  type: string;
  role?: string;
  mime?: string;
  size: number;
  sha256?: string;
  collection_state?: string;
  collection_error?: string;
  discovered_at?: string;
  modified_at?: string;
}

export interface ArtifactCollection {
  run_id: string;
  state: "declared" | "discovering" | "indexed" | "partial" | "failed" | string;
  error?: string;
  file_count: number;
  total_bytes: number;
  started_at?: string;
  finished_at?: string;
  updated_at?: string;
}

export interface RunManifest {
  run_id: string;
  schema_version: number;
  state: "draft" | "final";
  manifest_json?: string;
  sha256: string;
  completeness: "current" | "legacy_partial" | string;
  created_at?: string;
  finalized_at?: string;
}

export interface EvidenceSnapshot {
  id: string;
  run_id: string;
  project_id: string;
  run_manifest_sha256: string;
  output_set_sha256: string;
  manifest_json: string;
  manifest_sha256: string;
  created_at: string;
}

export interface EvidenceRelease {
  id: string;
  snapshot_id: string;
  project_id: string;
  sequence: number;
  state: "released" | "blocked" | "failed" | string;
  aggregate_result_json: string;
  gate_result_json: string;
  error_code?: string;
  last_error?: string;
  created_at: string;
}

export interface ProjectAsset {
  id: string;
  project_id: string;
  run_id: string;
  logical_uri: string;
  revision: string;
  role?: string;
  state: string;
  published_at?: string;
}

export interface RunComparisonAnalysis {
  run_ids: string[];
  structurally_comparable: boolean;
  claim_ready: boolean;
  issues: Array<{ field: string; severity: "error" | "warning" | string; values: Record<string, string>; message: string }>;
  aggregates: Array<{ run_id: string; metric_key: string; seeds: Record<string, number>; count: number; mean: number; stddev: number; min: number; max: number }>;
  report_markdown: string;
}

export interface RunMark {
  id: string;
  run_id: string;
  actor: string;
  kind: string;
  title?: string;
  statement?: string;
  body_md?: string;
  reason?: string;
  evidence?: string;
  attachments?: RunMarkAttachment[];
  created_at?: string;
}

export interface RunMarkAttachment {
  id: string;
  mark_id: string;
  filename: string;
  local_path: string;
  mime?: string;
  caption?: string;
  size?: number;
  created_at?: string;
}

export interface ProjectJournalEntry {
  id: string;
  project_id: string;
  actor: string;
  title: string;
  body_md?: string;
  next_action?: string;
  next_action_status: "none" | "open" | "done";
  run_ids: string[];
  literature_refs?: LiteratureReference[];
  created_at: string;
  updated_at: string;
}

export interface LiteratureReference {
  source_kind: "frozen_corpus" | "zotero_live";
  zotero_item_key: string;
  zotero_uri: string;
  page_label?: string;
  corpus_revision?: string;
  chunk_sha256?: string;
  item_version?: number;
  library_version?: number;
}

export interface LiteratureCollection {
  key: string;
  name: string;
  parent_key?: string;
  path: string;
  depth: number;
  uri: string;
}

export interface LiteratureProfileStatus {
  name: string;
  status: string;
  zotero_collection_key?: string;
  corpus_revision?: string;
  documents?: number;
  chunks?: number;
  freshness?: string;
  error?: string;
}

export interface LiteratureCatalogResponse {
  project_id: string;
  catalog: {
    collections: LiteratureCollection[];
    profiles: LiteratureProfileStatus[];
    library_version?: number;
  };
}

export interface ProjectLiteratureStatus {
  status: string;
  code?: string;
  detail?: string;
  project_id: string;
  zotero_collection_key?: string;
  service_profile?: string;
  evidence_domain?: "literature";
  claim_scope?: "background_only";
  service?: Record<string, unknown>;
}

export interface LiteratureEvidence {
  zotero_item_key: string;
  zotero_uri: string;
  title?: string;
  page?: number | string;
  page_label?: string;
  chunk_sha256: string;
  text?: string;
  score?: number;
  creators?: string | string[];
  collection_names?: string[];
}

export interface LiteratureQueryResponse {
  answer: string;
  answerability?: string;
  corpus_revision: string;
  zotero_collection_key: string;
  project_id: string;
  evidence_domain: "literature";
  claim_scope: "background_only";
  evidence: LiteratureEvidence[];
}

export interface RunBookmark {
  id: string;
  run_id: string;
  note?: string;
  created_at?: string;
  updated_at?: string;
}

export interface ProjectRunCard {
  id: string;
  project_id: string;
  project_name?: string;
  run_id: string;
  question?: string;
  verdict?: string;
  evidence_level?: string;
  key_metrics?: string;
  artifact_paths?: string;
  supports_claim?: string;
  weakens_claim?: string;
  next_action?: string;
  important?: boolean;
  should_promote?: boolean;
  proposal_reason?: string;
  graph_routing_reason?: string;
  related_runs?: string;
  graph_patch_json?: string;
  graph_status?: "none" | "pending" | "accepted" | "rejected" | "expired";
  proposal_hash?: string;
  base_graph_revision?: number;
  reviewed_at?: string;
  no_graph_impact?: boolean;
  graph_impact_reason?: string;
  run?: Run;
  marks?: RunMark[];
}

export interface ProjectDefinition {
  id: string;
  name: string;
  description?: string;
  local_root?: string;
  config_path?: string;
  config_hash?: string;
  source_repo?: string;
  default_recipe?: string;
  aggregate_command?: string;
  gate_command?: string;
  zotero_collection_key?: string;
  literature_service_profile?: string;
  created_at?: string;
  updated_at?: string;
}

export interface RunProjectAssignmentWarning {
  code: string;
  message: string;
  count?: number;
}

export interface RunProjectAssignmentResult {
  run: Run;
  run_id: string;
  previous_project_id?: string;
  project_id: string;
  actor: string;
  reason?: string;
  changed: boolean;
  changed_at: string;
  provenance_unchanged: boolean;
  warnings: RunProjectAssignmentWarning[];
}

export type TargetReadiness = "unknown" | "checking" | "ready" | "drifted" | "failed";

export interface ProjectTarget {
  id: string;
  project_id: string;
  name: string;
  resource_id: string;
  cwd: string;
  env_strategy: string;
  conda_env?: string;
  desired_env?: string;
  default_gpu: number;
  ui_events_path?: string;
  env_json?: string;
  prepare_command?: string;
  readiness: TargetReadiness;
  readiness_observed_at?: string;
  readiness_error?: string;
  last_prepare_run_id?: string;
  last_prepared_at?: string;
  observed_config_hash?: string;
  created_at?: string;
  updated_at?: string;
}

export interface ProjectTargetPreparePlan {
  project_id: string;
  target_id: string;
  resource_id: string;
  cwd: string;
  command: string;
  evidence_grade: "none";
  stages: Array<{ name: string; description: string; mutates: boolean }>;
  warnings: string[];
}

export interface ProjectTargetPrepareResponse {
  target: ProjectTarget;
  run: Run;
  plan: ProjectTargetPreparePlan;
}

export interface ManualProjectCategory {
  id: string;
  name: string;
  description?: string;
  run_count?: number;
  created_at?: string;
  updated_at?: string;
}

export interface RunProjectAssignment {
  run_id: string;
  category_id: string;
  category_name?: string;
  created_at?: string;
  updated_at?: string;
}

export interface ExperimentMatrix {
  id: string;
  title: string;
  description?: string;
  source_kind?: string;
  source_id?: string;
  source_name?: string;
  default_metric_key?: string;
  default_metric_goal?: string;
  data_json?: string;
  created_at?: string;
  updated_at?: string;
}

export interface ExperimentMatrixRow {
  id: string;
  matrix_id?: string;
  label: string;
  position: number;
  data_json?: string;
  created_at?: string;
  updated_at?: string;
}

export interface ExperimentMatrixColumn {
  id: string;
  matrix_id?: string;
  label: string;
  position: number;
  data_json?: string;
  created_at?: string;
  updated_at?: string;
}

export interface ExperimentMatrixCell {
  id: string;
  matrix_id?: string;
  row_id: string;
  column_id: string;
  run_id?: string;
  project_card_id?: string;
  title?: string;
  statement?: string;
  metric_key?: string;
  metric_value?: string;
  note?: string;
  data_json?: string;
  created_at?: string;
  updated_at?: string;
}

export interface ExperimentMatrixDetail extends ExperimentMatrix {
  rows: ExperimentMatrixRow[];
  columns: ExperimentMatrixColumn[];
  cells: ExperimentMatrixCell[];
}

export type EvidenceNodeType = "dataset" | "protocol" | "run" | "claim" | "issue" | "plan" | "hypothesis" | "experiment" | "conclusion" | "note" | "map_ref" | "group";
export type EvidenceEdgeType = "uses" | "supports" | "weakens" | "reveals_issue" | "supersedes" | "next_step" | "related_to" | "does_not_prove" | "custom";

export interface EvidenceGraphRoutingHints {
  recipes?: string[];
  keywords?: string[];
}

export interface EvidenceChain {
  id: string;
  title: string;
  description?: string;
  routing_hints?: EvidenceGraphRoutingHints;
  project_id?: string;
  role?: "primary" | "secondary" | "archive";
  status?: "active" | "archived";
  revision?: number;
  graph_hash?: string;
  created_at?: string;
  updated_at?: string;
}

export interface EvidenceChainNode {
  id: string;
  chain_id?: string;
  type: EvidenceNodeType;
  title?: string;
  body?: string;
  run_id?: string;
  source_run_ids?: string[];
  source_snapshot_ids?: string[];
  project_card_id?: string;
  x: number;
  y: number;
  width?: number;
  height?: number;
  pinned?: boolean;
  occurred_at?: string;
  data_json?: string;
  created_at?: string;
  updated_at?: string;
}

export interface EvidenceChainEdge {
  id: string;
  chain_id?: string;
  source_node_id: string;
  target_node_id: string;
  type: EvidenceEdgeType;
  label?: string;
  rationale?: string;
  data_json?: string;
  created_at?: string;
  updated_at?: string;
}

export interface EvidenceChainDetail extends EvidenceChain {
  nodes: EvidenceChainNode[];
  edges: EvidenceChainEdge[];
}

export interface EvidenceResearchCardDTO {
  node: EvidenceChainNode;
  relation_count: number;
  member_count?: number;
  member_node_ids?: string[];
  member_titles?: string[];
  shared_thread_ids?: string[];
  canonical_thread_id?: string;
}

export interface EvidenceResearchThreadDTO {
  id: string;
  title: string;
  root_node_id: string;
  parent_thread_id?: string;
  explicit_hypothesis: boolean;
  stages: Record<"hypothesis" | "design" | "result" | "conclusion" | "issue", EvidenceResearchCardDTO[]>;
  interpretations?: Array<{
    id: string;
    result_node_id: string;
    outcome_node_id?: string;
    outcome_type?: "conclusion" | "issue";
    kind: string;
    label: string;
    rationale?: string;
    edge_id?: string;
    legacy_inferred?: boolean;
  }>;
}

export interface EvidenceResearchCompletenessDTO {
  total: number;
  complete: number;
  missing_node_ids?: string[];
}

export interface EvidenceResearchHealthFindingDTO {
  code: string;
  severity: "info" | "warning" | "critical" | string;
  thread_id?: string;
  node_ids?: string[];
  message: string;
}

export interface EvidenceResearchThreadHealthDTO {
  thread_id: string;
  derived_phase: "hypothesis_recorded" | "design_recorded" | "result_recorded" | "outcome_recorded" | string;
  complexity_level: "normal" | "warning" | "critical" | string;
  semantic_node_count: number;
  hypothesis_count: number;
  design_count: number;
  result_count: number;
  conclusion_count: number;
  issue_count: number;
  parallel_design_node_ids?: string[];
  parallel_result_node_ids?: string[];
  provenance_declared: EvidenceResearchCompletenessDTO;
  disposition_complete: EvidenceResearchCompletenessDTO;
  outcome_complete: EvidenceResearchCompletenessDTO;
  issue_follow_up_linked: EvidenceResearchCompletenessDTO;
  possible_duplicate_result_groups?: string[][];
  findings?: EvidenceResearchHealthFindingDTO[];
}

export interface EvidenceResearchStructuralHealthDTO {
  policy_version: "research-health-v2" | string;
  terminology: Record<string, string>;
  readability_status: "clear" | "dense" | "needs_curation" | string;
  compatibility_status: "v2_compliant" | "legacy_readable" | string;
  topic_lifecycle: "draft" | "active" | "archived" | string;
  derived_topic_phase: "empty" | "needs_curation" | "mixed" | "hypothesis_recorded" | "design_recorded" | "result_recorded" | "outcome_recorded" | string;
  semantic_node_count: number;
  assigned_count: number;
  unassigned_count: number;
  unassigned_ratio: number;
  threads: EvidenceResearchThreadHealthDTO[];
  findings?: EvidenceResearchHealthFindingDTO[];
}

export interface EvidenceChainAuditReportDTO {
  schema_version: string;
  chain_id: string;
  project_id: string;
  role: string;
  revision: number;
  stored_graph_hash: string;
  current_graph_hash: string;
  eligible: boolean;
  readability_status?: "v2_readable" | "legacy_readable" | "broken" | string;
  v2_compliance_status?: "v2_compliant" | "legacy_mixed" | "v2_noncompliant" | string;
  publication_status?: "not_applicable" | "publication_ready" | "publication_blocked" | string;
  publication_result_count?: number;
  research_health?: EvidenceResearchStructuralHealthDTO;
  blockers: Array<{ code: string; message: string; node_id?: string; edge_id?: string }>;
  warnings: Array<{ code: string; message: string; node_id?: string; edge_id?: string }>;
}

export interface EvidenceResearchProtocolGroupDTO {
  group: EvidenceChainNode;
  members: Array<{ node: EvidenceChainNode; thread_id?: string }>;
  relations: Array<{ edge: EvidenceChainEdge; scope: "internal" | "external" }>;
}

export interface EvidenceResearchProjectionDTO {
  evidence_contract_version?: string;
  chain_id: string;
  revision: number;
  graph_hash: string;
  stage_order: Array<"hypothesis" | "design" | "result" | "conclusion" | "issue">;
  presentation_stage_order?: Array<"hypothesis" | "design" | "result" | "interpretation" | "outcome">;
  threads: EvidenceResearchThreadDTO[];
  unassigned: Array<{ card: EvidenceResearchCardDTO; reason: string }>;
  cross_thread_relations: Array<{
    edge: EvidenceChainEdge;
    source_thread_id: string;
    target_thread_id: string;
    kind: "branch" | "causal";
  }>;
  protocol_groups?: EvidenceResearchProtocolGroupDTO[];
  owner_by_node: Record<string, string>;
  structural_health?: EvidenceResearchStructuralHealthDTO;
  capacity?: {
    policy_version: string;
    status: "healthy" | "near_limit" | "split_recommended" | "cleanup_required";
    too_large: boolean;
    split_recommended: boolean;
    thread_count: number;
    root_thread_count: number;
    thread_node_count: number;
    unassigned_count: number;
    recommended_max_threads: number;
    recommended_max_thread_nodes: number;
    recommended_max_unassigned: number;
    suggested_topic_count: number;
    reasons: string[];
    thread_families: Array<{
      id: string;
      root_thread_id: string;
      title: string;
      thread_ids: string[];
      thread_count: number;
      semantic_node_count: number;
    }>;
  };
}

export interface EvidenceChainRunCandidate {
  id: string;
  kind: "project_card" | "run";
  run_id: string;
  project_card_id?: string;
  project_id?: string;
  project_name?: string;
  question?: string;
  verdict?: string;
  evidence_level?: string;
  key_metrics?: string;
  next_action?: string;
  run?: Run;
  project_card?: ProjectRunCard;
}

export interface EvidenceGraphBlocker {
  code: string;
  message: string;
  node_id?: string;
  edge_id?: string;
  run_id?: string;
}

export interface EvidenceGraphWarning {
  code: string;
  message: string;
  node_id?: string;
  edge_id?: string;
}

export interface EvidenceLayoutIntent {
  flow: "left_to_right";
  ranks: string[][];
  rationale?: string;
}

export interface EvidenceGraphPatch {
  chain_id: string;
  routing_reason?: string;
  layout_intent?: EvidenceLayoutIntent;
  nodes: EvidenceChainNode[];
  edges: EvidenceChainEdge[];
  upsert_nodes?: EvidenceChainNode[];
  upsert_edges?: EvidenceChainEdge[];
  delete_node_ids?: string[];
  delete_edge_ids?: string[];
}

export interface EvidenceGraphProposalPlan {
  proposal_id?: string;
  project_id?: string;
  run_id?: string;
  project_card_id?: string;
  chain_id: string;
  proposal_hash: string;
  status: string;
  base_graph_revision: number;
  current_graph_revision: number;
  applied_graph_revision: number;
  auto_rebased?: boolean;
  current_graph_hash?: string;
  result_graph_hash?: string;
  nodes_added: number;
  edges_added: number;
  eligible: boolean;
  blockers: EvidenceGraphBlocker[];
  warnings?: EvidenceGraphWarning[];
  routing_reason?: string;
  projected_research?: EvidenceResearchProjectionDTO;
}

export interface EvidenceProposal {
  id: string;
  project_id: string;
  target_map_id?: string;
  base_graph_revision: number;
  actor: string;
  summary: string;
  routing_reason?: string;
  project_level_impact: boolean;
  source_run_ids: string[];
  source_snapshot_ids: string[];
  patch_json: string;
  status: "draft" | "pending" | "accepted" | "rejected" | "expired" | "conflicted";
  proposal_hash: string;
  reviewed_by?: string;
  reviewed_at?: string;
  source_kind?: string;
  source_id?: string;
  created_at: string;
  updated_at: string;
  target_map?: EvidenceChain;
}

export interface EvidencePromotionPlan {
  project_id: string;
  source_map_id: string;
  source_revision: number;
  source_graph_hash: string;
  source_node_ids: string[];
  target_primary_map_id: string;
  target_primary_revision: number;
  summary: string;
  node_type: "claim" | "issue" | "plan";
  patch: EvidenceGraphPatch;
  plan_hash: string;
  eligible: boolean;
  blockers: EvidenceGraphBlocker[];
}

export interface ExecEvent {
  id: string;
  resource_id: string;
  actor: string;
  command: string;
  cwd?: string;
  exit_code?: number | null;
  started_at?: string;
  finished_at?: string;
  duration_ms?: number;
  stdout_tail?: string;
  stderr_tail?: string;
  created_at?: string;
}

export interface Paginated<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
	change_cursor?: number;
}

export interface UIEvent {
  type?: string;
  name?: string;
  metric?: string;
  key?: string;
  label?: string;
  value?: number | string;
  current?: number | string;
  total?: number | string;
  step?: number | string;
  epoch?: number | string;
  time?: number | string;
  series?: string;
  run?: string;
  variant?: string;
  split?: string;
  stage?: string;
  text?: string;
  message?: string;
  [key: string]: unknown;
}

export interface ParsedEvents {
  events: UIEvent[];
  metrics: MetricPoint[];
  latestMetrics: MetricPoint[];
  params: ParamPoint[];
  progress: ProgressPoint[];
  notes: UIEvent[];
  errors: string[];
}

export interface ParamPoint {
  name: string;
  value: string;
  time?: number;
  series?: string;
}

export interface MetricPoint {
  name: string;
  value: number;
  step?: number;
  epoch?: number;
  time?: number;
  series?: string;
  unit?: string;
}

export interface ProgressPoint {
  name: string;
  current: number;
  total?: number;
  percent?: number;
  time?: number;
  series?: string;
  label?: string;
  status?: string;
  best_epoch?: number;
}

export interface ConfirmState {
  title: string;
  message: string;
  actionLabel: string;
  run: () => Promise<void>;
}
