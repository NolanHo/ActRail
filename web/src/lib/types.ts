export interface LaunchBackendDefaults {
  provider_choice?: string;
  provider_choices?: string[];
  model?: string | null;
  models?: string[];
  provider_models?: Record<string, string[]>;
  model_provider?: string | null;
  model_providers?: string[];
  preferred_auth_method?: string | null;
  reasoning_effort?: string | null;
  reasoning_efforts?: string[];
  service_tier?: string | null;
  supports_fast?: boolean;
  models_cached_at?: number | null;
}

export interface NewSessionDefaults {
  default_backend?: string;
  backends?: Record<string, LaunchBackendDefaults>;
  backend_capabilities?: Record<string, BackendCapabilitySnapshot>;
  pi_agent_grpc_default?: boolean;
}

export interface BackendCapabilitySnapshot {
  launch_provider?: boolean;
  launch_model?: boolean;
  launch_reasoning_effort?: boolean;
  runtime_streaming?: boolean;
  runtime_tool_trace?: boolean;
  runtime_reasoning_trace?: boolean;
  runtime_context_usage?: boolean;
  runtime_ui_requests?: boolean;
  runtime_interrupt?: boolean;
  runtime_probe?: boolean;
  iod_stdio?: boolean;
  iod_unix?: boolean;
  grpc?: boolean;
  supervisor?: boolean;
  resume_history?: boolean;
}

export interface BootstrapCapabilities {
  ws_realtime?: boolean;
  voice?: boolean;
  harness?: boolean;
  notifications?: boolean;
  pi_ui?: boolean;
  workspace_read?: boolean;
  workspace_write?: boolean;
  exp_connect_transport?: boolean;
}

export interface BootstrapTransportConfig {
  default?: string;
  experimental?: string[];
  connect_path?: string;
}

export interface BootstrapWebSocketConfig {
  url?: string;
  heartbeat_interval_ms?: number;
  resume_buffer_events?: number;
}

export interface AppBootstrapUi {
  deferred_features?: string[];
}

export type WaitState = "pending_unread" | "claimed" | "answered" | "timed_out_locked" | "cancelled" | "orphaned";

export interface ActiveWaitSummary {
  wait_id: string;
  thread_id: string;
  session_id?: string;
  state: Extract<WaitState, "pending_unread" | "claimed"> | WaitState;
  question: string;
  blocking_reason?: string;
  attempted?: string;
  default_if_no_reply?: string;
  claimed_at?: number | null;
  timeout_at?: number | null;
  created_at?: number | null;
  updated_at?: number | null;
}

export interface WaitRecord extends ActiveWaitSummary {
  context?: string;
  answer?: string;
  fallback_used?: string;
  files?: string[];
  answered_at?: number | null;
  cancelled_at?: number | null;
  timed_out_at?: number | null;
  orphaned_at?: number | null;
}

export interface WaitThreadSummary {
  thread_id: string;
  session_id: string;
  title?: string;
  active_wait?: ActiveWaitSummary | null;
  created_at?: number | null;
  updated_at?: number | null;
  closed_at?: number | null;
  wait_count?: number;
}

export interface WaitThreadResponse {
  ok?: boolean;
  thread?: WaitThreadSummary | null;
  waits?: WaitRecord[];
}

export interface WaitThreadsResponse {
  ok?: boolean;
  threads?: WaitThreadSummary[];
}

export interface WaitInboxResponse {
  ok?: boolean;
  waits?: ActiveWaitSummary[];
}

export interface WaitLifecycleResponse {
  ok?: boolean;
  wait?: WaitRecord | ActiveWaitSummary | null;
  active_wait?: ActiveWaitSummary | null;
}

export interface SessionTransportSnapshot {
  generation_id?: string;
  state?: string;
  reset_required?: boolean;
  reason?: string | null;
}

export interface SupervisorRunSummary {
  run_id: string;
  anchor_assistant_event_id: string;
  anchor_assistant_ts?: number;
  status: "stop" | "injected" | "error" | string;
  action?: "stop" | "inject" | string;
  injected_text?: string;
  reason?: string;
  error?: string;
  model?: string;
  base_url?: string;
  created_ts?: number;
}

export interface SessionSupervisorSnapshot {
  ok?: boolean;
  supported: boolean;
  enabled: boolean;
  status: "idle" | "checking" | "cooldown" | "limit_reached" | "error" | string;
  idle_after_minutes: number;
  max_consecutive_injections: number;
  consecutive_injections: number;
  goal?: string;
  acceptance_criteria?: string;
  context_files?: string[];
}

export interface SupervisorProviderResponse {
  ok?: boolean;
  base_url: string;
  model: string;
  api_key_configured: boolean;
  complete: boolean;
}

export interface SchedulerSettings {
  idle_before_delivery_seconds: number;
  updated_ts?: number;
}

export interface SchedulerItem {
  item_id: string;
  session_id: string;
  kind: string;
  source_ref?: string;
  title?: string;
  message?: string;
  due_ts: number;
  state: string;
  created_by?: string;
  created_ts: number;
  updated_ts: number;
}

export interface InboxItem {
  item_id: string;
  session_id: string;
  source: string;
  source_id?: string;
  title?: string;
  message?: string;
  priority?: number;
  due_ts: number;
  state: string;
  blocked_reason?: string;
  delivered_message_id?: string;
  error?: string;
  claimed_ts?: number;
  delivered_ts?: number;
  created_ts: number;
  updated_ts: number;
}

export interface SchedulerSnapshotResponse {
  ok?: boolean;
  settings: SchedulerSettings;
  items: SchedulerItem[];
  inbox: InboxItem[];
}

export interface SessionInboxResponse {
  ok?: boolean;
  items: InboxItem[];
}

export interface SupervisorRunsResponse {
  ok?: boolean;
  runs: SupervisorRunSummary[];
}

export interface SupervisorRunOnceResponse {
  ok?: boolean;
  run: SupervisorRunSummary;
}

export interface SessionSummary {
  session_id: string;
  runtime_id?: string | null;
  thread_id?: string | null;
  generation_id?: string | null;
  resume_session_id?: string | null;
  display_name?: string;
  title?: string;
  alias?: string;
  first_user_message?: string;
  cwd?: string;
  files?: string[];
  agent_backend?: "codex" | "pi" | string;
  broker_pid?: number;
  owned?: boolean;
  busy?: boolean;
  busy_reason?: string;
  pending_startup?: boolean;
  focused?: boolean;
  has_unread_assistant?: boolean;
  queue_len?: number;
  transport_state?: string | null;
  probing?: boolean;
  reset_required?: boolean;
  transport_reason?: string | null;
  updated_ts?: number;
  last_assistant_message_ts?: number;
  iod?: {
    build_date?: string;
    git_sha?: string;
    start_ts?: number;
    mode?: "grpc" | "std" | string;
  } | null;
  git_branch?: string | null;
  model?: string | null;
  provider_choice?: string | null;
  reasoning_effort?: string | null;
  service_tier?: string | null;
  transport?: string | null;
  priority_offset?: number | null;
  snooze_until?: number | null;
  dependency_session_id?: string | null;
  session_file_path?: string | null;
  backend_session_id?: string | null;
  blocked?: boolean;
  snoozed?: boolean;
  historical?: boolean;
  active_wait?: ActiveWaitSummary | null;
  supervisor?: SessionSupervisorSnapshot | null;
}

export interface SessionsResponse {
  trace_id?: string;
  items?: SessionSummary[];
  sessions?: SessionSummary[];
  remaining_count?: number;
  total_count?: number;
  remaining_by_group?: Record<string, number>;
  omitted_group_count?: number;
  group_key?: string | null;
}

export interface TeamMessage {
  message_id: string;
  kind: "leader" | "member" | "system" | string;
  label: string;
  body: string;
  ts?: number;
  meta?: string;
}

export interface TeamNodeResponse {
  actor_id: string;
  child_session_id: string;
  parent_session_id: string;
  parent_actor_id?: string;
  name: string;
  role: string;
  status: "waiting_for_parent" | "running" | "failed" | "idle" | "completed" | "aborted" | "closed" | string;
  turn_id?: string;
  question?: string;
  last_event_id?: string;
  last_event_ts?: number;
  model?: string;
  cwd?: string;
  messages?: TeamMessage[];
  children?: TeamNodeResponse[];
}

export interface TeamsResponse {
  trace_id?: string;
  ok?: boolean;
  roots?: TeamNodeResponse[];
  total_count?: number;
  non_leaf_count?: number;
}

export interface SessionBootstrapResponse {
  trace_id?: string;
  protocol_version?: number;
  capabilities?: BootstrapCapabilities;
  ws?: BootstrapWebSocketConfig;
  transport?: BootstrapTransportConfig;
  launch_defaults?: {
    default_backend?: string;
    available_backends?: string[];
    providers?: string[];
    models?: string[];
  };
  ui?: AppBootstrapUi;
  recent_cwds?: string[];
  cwd_groups?: Record<string, CwdGroupMeta>;
  new_session_defaults?: NewSessionDefaults;
  tmux_available?: boolean;
}

export interface SessionCapabilitySnapshot {
  ws_realtime?: boolean;
  voice?: boolean;
  harness?: boolean;
  notifications?: boolean;
  pi_ui?: boolean;
  workspace_read?: boolean;
  workspace_write?: boolean;
}

export interface SessionDetailsResponse {
  trace_id?: string;
  session_id: string;
  runtime_id?: string | null;
  thread_id?: string | null;
  title?: string;
  alias?: string;
  display_name?: string;
  first_user_message?: string;
  cwd?: string;
  agent_backend?: string;
  provider?: string;
  model?: string;
  busy?: boolean;
  focused?: boolean;
  queue_length?: number;
  priority_offset?: number;
  snooze_until?: number | null;
  dependency_session_id?: string | null;
  session_file_path?: string | null;
  backend_session_id?: string | null;
  last_updated_ts?: number;
  last_activity_ts?: number;
  historical?: boolean;
  capabilities?: SessionCapabilitySnapshot;
  supervisor?: SessionSupervisorSnapshot | null;
}

export interface CreateSessionResponse {
  trace_id?: string;
  ok?: boolean;
  session?: SessionSummary;
  session_id?: string;
  runtime_id?: string | null;
  backend?: string;
  broker_pid?: number;
  pending_startup?: boolean;
  focused?: boolean;
  alias?: string;
  session_file_path?: string | null;
}

export interface DeleteSessionResponse {
  trace_id?: string;
  ok?: boolean;
}

export interface HandoffSessionResponse extends CreateSessionResponse {
  history_path?: string;
  sidecar_path?: string;
  previous_session_id?: string;
}

export interface RestartSessionResponse extends CreateSessionResponse {
  previous_runtime_id?: string;
  restarted?: boolean;
}

export interface RenameSessionResponse {
  trace_id?: string;
  ok?: boolean;
  alias?: string;
}

export interface EditSessionResponse extends RenameSessionResponse {
  priority_offset?: number;
  snooze_until?: number | null;
  dependency_session_id?: string | null;
  focused?: boolean;
  iod?: SessionSummary["iod"];
}

export interface FocusSessionResponse {
  trace_id?: string;
  ok?: boolean;
  focused?: boolean;
}

export interface SwitchSessionModelResponse {
  trace_id?: string;
  ok?: boolean;
  model?: string | null;
  provider?: string | null;
  data?: Record<string, unknown>;
}

export interface CwdGroupMeta {
  label?: string;
  collapsed?: boolean;
}

export interface LoginResponse {
  trace_id?: string;
  ok?: boolean;
  error?: string;
}

export interface EditCwdGroupResponse {
  trace_id?: string;
  ok?: boolean;
  cwd?: string;
  label?: string;
  collapsed?: boolean;
}

export interface LogoutResponse {
  trace_id?: string;
  ok?: boolean;
}

export interface SessionResumeCandidate {
  session_id: string;
  title?: string;
  alias?: string;
  display_name?: string;
  cwd?: string;
  first_user_message?: string;
  updated_ts?: number;
  git_branch?: string | null;
}

export interface SessionResumeCandidatesResponse {
  trace_id?: string;
  ok?: boolean;
  exists?: boolean;
  will_create?: boolean;
  git_repo?: boolean;
  git_root?: string;
  git_branch?: string;
  offset?: number;
  limit?: number;
  remaining?: number;
  scan_offset?: number;
  scan_limit?: number;
  scanned?: number;
  scan_remaining?: number;
  scan_complete?: boolean;
  sessions: SessionResumeCandidate[];
}

export interface VoiceSettingsResponse {
  trace_id?: string;
  ok?: boolean;
  tts_enabled_for_narration?: boolean;
  tts_enabled_for_final_response?: boolean;
  tts_base_url?: string;
  tts_api_key?: string;
  audio?: {
    queue_depth?: number;
    active_listener_count?: number;
    segment_count?: number;
    stream_url?: string;
    last_error?: string;
  };
  notifications?: {
    enabled_devices?: number;
    total_devices?: number;
    vapid_public_key?: string;
  };
}

export interface VoiceProviderTestResponse {
  trace_id?: string;
  ok?: boolean;
  status?: string;
  status_code?: number;
}

export interface AudioListenerResponse {
  trace_id?: string;
  ok?: boolean;
  active_listener_count?: number;
}

export interface NotificationFeedItem {
  message_id?: string;
  session_display_name?: string;
  notification_text?: string;
  updated_ts?: number;
}

export interface NotificationFeedResponse {
  ok?: boolean;
  items: NotificationFeedItem[];
}

export interface NotificationSubscriptionRecord {
  endpoint?: string;
  notifications_enabled?: boolean;
  device_class?: string;
  device_label?: string;
  created_ts?: number;
  updated_ts?: number;
}

export interface NotificationSubscriptionStateResponse {
  ok?: boolean;
  vapid_public_key?: string;
  subscriptions: NotificationSubscriptionRecord[];
}

export interface NotificationMessageResponse {
  ok?: boolean;
  notification_text?: string;
  summary_status?: string;
}

export interface MessageEvent {
  seq?: number;
  type?: string;
  role?: string;
  ts?: number;
  text?: string;
  display?: boolean;
  name?: string;
  summary?: string;
  subject?: string;
  description?: string;
  details?: Record<string, unknown>;
  is_error?: boolean;
  answer?: string | string[];
  cancelled?: boolean;
  resolved?: boolean;
  tool_call_id?: string | null;
  was_custom?: boolean;
  agent?: string;
  task?: string;
  output?: string | null;
  progress_text?: string;
  operation?: string;
  custom_type?: string;
  task_id?: string;
  task_list_id?: string;
  owner?: string;
  assigned_by?: string;
  items?: TodoSnapshotItem[];
  options?: Array<{ label?: string; value?: string; title?: string; description?: string } | string>;
  allow_freeform?: boolean;
  allow_multiple?: boolean;
  timeout_ms?: number | null;
  message?: {
    role?: string;
    content?: Array<{ type?: string; text?: string }>;
  };
  question?: string;
  context?: string;
  questions?: Array<{ header?: string; question?: string; options?: Array<{ label?: string; description?: string; preview?: string }>; multiSelect?: boolean }>;
  answers_by_question?: Record<string, string | string[]>;
  toolName?: string;
  prompt_fallback_available?: boolean;
  streaming?: boolean;
  completed?: boolean;
  pending?: boolean;
  bridge_pseudo?: boolean;
  stream_id?: string;
  turn_id?: string;
  event_id?: string;
  parent_event_id?: string;
  request_id?: string;
  request_state?: string;
  session_id?: string;
  pending_text?: string;
  supervisor_runs?: SupervisorRunSummary[];
  [key: string]: unknown;
}

export interface MessagesResponse {
  trace_id?: string;
  items?: MessageEvent[];
  events: MessageEvent[];
  offset?: number;
  has_older?: boolean;
  has_more?: boolean;
  next_before?: number;
  next_before_seq?: number;
  tail_seq?: number;
  ui_version?: string;
}

export interface SessionUiRequestOption {
  label?: string;
  value?: string;
  title?: string;
  description?: string;
}

export interface SessionUiRequestQuestion {
  header?: string;
  question?: string;
  options?: SessionUiRequestOption[];
  multiSelect?: boolean;
}

export interface SessionUiRequest {
  id?: string;
  request_id?: string;
  kind?: string;
  method?: "select" | "confirm" | "input" | "editor" | string;
  label?: string;
  title?: string;
  message?: string;
  prompt?: string;
  question?: string;
  context?: string;
  prefill?: string;
  value?: string | string[];
  confirmed?: boolean;
  cancelled?: boolean;
  allow_freeform?: boolean;
  allow_multiple?: boolean;
  options?: Array<SessionUiRequestOption | string>;
  questions?: SessionUiRequestQuestion[];
  metadata?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface SessionUiStateResponse {
  requests: SessionUiRequest[];
}

export interface ContextUsagePayload {
  used_tokens?: number;
  total_tokens?: number;
  percent_used?: number;
}

export interface TurnTimingPayload {
  started_ts?: number;
  last_event_ts?: number | null;
}

export interface SessionStreamCursors {
  session?: number;
  ui?: number;
}

export interface SessionResumeCursors {
  session?: string | number;
  ui?: string | number;
  transport?: string | number;
}

export interface SessionQueueItem {
  id?: string;
  queue_id?: string;
  text?: string;
  state?: string;
}

export interface PartialAssistantTurnSnapshot {
  turn_id?: string;
  text?: string;
}

export interface LiveSessionResponse {
  ok?: boolean;
  session_id?: string;
  runtime_id?: string | null;
  generation_id?: string | null;
  transport?: SessionTransportSnapshot | null;
  transport_reason?: string | null;
  reset_required?: boolean;
  offset?: number;
  live_offset?: number;
  bridge_offset?: number;
  has_older?: boolean;
  next_before?: number;
  busy?: boolean;
  busy_reason?: string;
  token?: Record<string, unknown> | null;
  context_usage?: ContextUsagePayload | null;
  turn_timing?: TurnTimingPayload | null;
  transport_state?: string;
  transport_error?: string | null;
  requests_version?: string;
  tail_seq?: number;
  stream_seq?: number;
  ui_stream_seq?: number;
  stream_cursors?: SessionStreamCursors | null;
  resume_cursors?: SessionResumeCursors | null;
  queue?: { items?: SessionQueueItem[] } | null;
  ui_request?: SessionUiRequest | null;
  partial_assistant_turn?: PartialAssistantTurnSnapshot | null;
  events?: MessageEvent[];
  requests?: SessionUiRequest[];
  active_wait?: ActiveWaitSummary | null;
}

export interface RealtimeEnvelope {
  type?: string;
  id?: string;
  ts?: number;
  stream?: string;
  request_id?: string;
  payload?: Record<string, unknown>;
}

export interface WorkspaceHistoryItem {
  path: string;
  label?: string;
}

export interface WorkspaceResponse {
  trace_id?: string;
  ok?: boolean;
  session_id?: string;
  runtime_id?: string | null;
  root_path?: string;
  selected_path?: string;
  open_paths?: string[];
  history_items?: WorkspaceHistoryItem[];
}

export interface SessionCommand {
  name: string;
  description?: string;
  source?: string;
  source_info?: Record<string, unknown>;
}

export interface SessionCommandsResponse {
  trace_id?: string;
  commands: SessionCommand[];
}

export interface ExecuteSessionCommandResponse {
  trace_id?: string;
  ok?: boolean;
  command?: string;
  message?: string;
  session_id?: string;
  runtime_id?: string | null;
}

export interface AttachmentInjectResponse {
  ok?: boolean;
  path?: string;
  inject_text?: string;
  broker?: Record<string, unknown>;
}

export interface SessionFileListEntry {
  name: string;
  path: string;
  kind: "dir" | "file";
}

export interface SessionFileListResponse {
  ok?: boolean;
  root_path?: string;
  cwd?: string;
  path?: string;
  entries?: SessionFileListEntry[];
  items?: SessionFileListEntry[];
  truncated?: boolean;
}

export interface SessionFileReadResponse {
  ok?: boolean;
  kind?: "text" | "image" | string;
  path?: string;
  rel?: string;
  size?: number;
  size_bytes?: number;
  text?: string;
  editable?: boolean;
  version?: string;
  image_url?: string;
  content_type?: string;
  mime_type?: string;
  encoding?: string;
  download_name?: string;
  unsupported_reason?: string;
}

export interface GitFileVersion {
  version_id: string;
  label: string;
  commit_hash?: string;
  author?: string;
  commit_ts?: number;
  message?: string;
  current?: boolean;
}

export interface GitFileVersionsResponse {
  ok?: boolean;
  cwd?: string;
  path?: string;
  abs_path?: string;
  current_exists?: boolean;
  current_size?: number;
  current_text?: string;
  base_exists?: boolean;
  base_text?: string;
  fallback_reason?: string;
  items?: GitFileVersion[];
}

export interface HarnessConfigResponse {
  ok?: boolean;
  enabled?: boolean;
  request?: string;
  cooldown_minutes?: number;
  remaining_injections?: number;
}

export interface TodoSnapshotItem {
  id?: number | string;
  title?: string;
  status?: string;
  description?: string;
  owner?: string;
  assigned_by?: string;
  updated_at?: string;
  source?: string;
}

export interface TodoSnapshot {
  available?: boolean;
  error?: boolean;
  progress_text?: string;
  items: TodoSnapshotItem[];
  counts?: Record<string, number>;
}
