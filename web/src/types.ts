export type Locale = "en" | "zh";

export type NullableTime = string | null | { Time?: string; Valid?: boolean };

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
  name?: string;
  status: string;
  kind?: string;
  gpu_index?: number;
  cwd?: string;
  command: string;
  program?: string;
  args_json?: string;
  conda_env?: string;
  project_env?: string;
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
  created_by?: string;
  created_at?: string;
  started_at?: NullableTime;
  finished_at?: NullableTime;
  archived_at?: NullableTime;
  deleted_at?: NullableTime;
}

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
  truncated?: boolean;
  error?: string;
  error_kind?: string;
}

export interface Artifact {
  id: string;
  run_id: string;
  path: string;
  type: string;
  size: number;
  modified_at?: string;
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
  related_runs?: string;
  run?: Run;
  marks?: RunMark[];
}

export interface ProjectView {
  project_id: string;
  project_name?: string;
  total_cards?: number;
  important_runs?: number;
  formal_runs?: number;
  running_runs?: number;
  cards: ProjectRunCard[];
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

export type EvidenceNodeType = "run" | "hypothesis" | "experiment" | "plan" | "conclusion" | "note";
export type EvidenceEdgeType = "supports" | "does_not_prove" | "next_step" | "custom";

export interface EvidenceChain {
  id: string;
  title: string;
  description?: string;
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
  project_card_id?: string;
  x: number;
  y: number;
  width?: number;
  height?: number;
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
}

export interface ConfirmState {
  title: string;
  message: string;
  actionLabel: string;
  run: () => Promise<void>;
}
