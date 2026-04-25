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
}

export interface BootstrapCapabilities {
  ws_realtime?: boolean;
  voice?: boolean;
  harness?: boolean;
  notifications?: boolean;
  pi_ui?: boolean;
  workspace_read?: boolean;
  workspace_write?: boolean;
}

export interface BootstrapWebSocketConfig {
  url?: string;
  heartbeat_interval_ms?: number;
  resume_buffer_events?: number;
}

export interface AppBootstrapUi {
  deferred_features?: string[];
}

export interface SessionSummary {
  session_id: string;
  runtime_id?: string | null;
  thread_id?: string | null;
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
  pending_startup?: boolean;
  focused?: boolean;
  queue_len?: number;
  updated_ts?: number;
  git_branch?: string | null;
  model?: string | null;
  provider_choice?: string | null;
  reasoning_effort?: string | null;
  service_tier?: string | null;
  transport?: string | null;
  priority_offset?: number | null;
  snooze_until?: number | null;
  dependency_session_id?: string | null;
  blocked?: boolean;
  snoozed?: boolean;
  historical?: boolean;
}

export interface SessionsResponse {
  items?: SessionSummary[];
  sessions?: SessionSummary[];
  remaining_count?: number;
  remaining_by_group?: Record<string, number>;
  omitted_group_count?: number;
  group_key?: string | null;
}

export interface SessionBootstrapResponse {
  protocol_version?: number;
  capabilities?: BootstrapCapabilities;
  ws?: BootstrapWebSocketConfig;
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
  last_updated_ts?: number;
  last_activity_ts?: number;
  historical?: boolean;
  capabilities?: SessionCapabilitySnapshot;
}

export interface CreateSessionResponse {
  ok?: boolean;
  session?: SessionSummary;
  session_id?: string;
  runtime_id?: string | null;
  backend?: string;
  broker_pid?: number;
  pending_startup?: boolean;
  focused?: boolean;
  alias?: string;
}

export interface DeleteSessionResponse {
  ok?: boolean;
}

export interface HandoffSessionResponse extends CreateSessionResponse {
  history_path?: string;
  previous_session_id?: string;
}

export interface RestartSessionResponse extends CreateSessionResponse {
  previous_runtime_id?: string;
  restarted?: boolean;
}

export interface RenameSessionResponse {
  ok?: boolean;
  alias?: string;
}

export interface EditSessionResponse extends RenameSessionResponse {
  priority_offset?: number;
  snooze_until?: number | null;
  dependency_session_id?: string | null;
  focused?: boolean;
}

export interface FocusSessionResponse {
  ok?: boolean;
  focused?: boolean;
}

export interface SwitchSessionModelResponse {
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
  ok?: boolean;
  error?: string;
}

export interface EditCwdGroupResponse {
  ok?: boolean;
  cwd?: string;
  label?: string;
  collapsed?: boolean;
}

export interface LogoutResponse {
  ok?: boolean;
}

export interface SessionResumeCandidate {
  session_id: string;
  title?: string;
  alias?: string;
  first_user_message?: string;
  updated_ts?: number;
  git_branch?: string | null;
}

export interface SessionResumeCandidatesResponse {
  ok?: boolean;
  exists?: boolean;
  will_create?: boolean;
  git_repo?: boolean;
  git_root?: string;
  git_branch?: string;
  offset?: number;
  limit?: number;
  remaining?: number;
  sessions: SessionResumeCandidate[];
}

export interface VoiceSettingsResponse {
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

export interface AudioListenerResponse {
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
  request_id?: string;
  request_state?: string;
  pending_text?: string;
  [key: string]: unknown;
}

export interface MessagesResponse {
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
  offset?: number;
  live_offset?: number;
  bridge_offset?: number;
  has_older?: boolean;
  next_before?: number;
  busy?: boolean;
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
}

export interface SessionCommandsResponse {
  commands: SessionCommand[];
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
