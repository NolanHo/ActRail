import { sendRealtimeCommand } from "../domains/realtime/client";
import { getJson, postJson } from "./http";
import { getSessionRouteId } from "./session-identity";
import type {
  AttachmentInjectResponse,
  AudioListenerResponse,
  CreateSessionResponse,
  DeleteSessionResponse,
  EditSessionResponse,
  ExecuteSessionCommandResponse,
  HandoffSessionResponse,
  RestartSessionResponse,
  FocusSessionResponse,
  SwitchSessionModelResponse,
  GitFileVersionsResponse,
  HarnessConfigResponse,
  EditCwdGroupResponse,
  LiveSessionResponse,
  LoginResponse,
  MessagesResponse,
  NotificationFeedResponse,
  NotificationMessageResponse,
  NotificationSubscriptionStateResponse,
  RenameSessionResponse,
  LogoutResponse,
  SessionFileListResponse,
  SessionFileReadResponse,
  SessionResumeCandidatesResponse,
  SessionBootstrapResponse,
  SessionCommandsResponse,
  SessionDetailsResponse,
  SessionUiStateResponse,
  SessionsResponse,
  SessionSupervisorSnapshot,
  SupervisorProviderResponse,
  SupervisorRunsResponse,
  SupervisorRunOnceResponse,
  VoiceProviderTestResponse,
  VoiceSettingsResponse,
  WaitInboxResponse,
  WaitLifecycleResponse,
  WaitThreadResponse,
  WaitThreadsResponse,
  WorkspaceResponse,
} from "./types";

function sessionStreamName(sessionId: string) {
  return `session:${sessionId}`;
}

function sessionUiStreamName(sessionId: string) {
  return `session:${sessionId}:ui`;
}

function withSessionIdentity(sessionId: string, runtimeId?: string | null, extra?: Record<string, unknown>) {
  return {
    session_id: sessionId,
    ...(runtimeId ? { runtime_id: runtimeId } : {}),
    ...(extra ?? {}),
  };
}

function uiResponseValue(payload: Record<string, unknown>) {
  if (payload.value !== undefined) {
    return payload.value;
  }
  if (payload.confirmed === true) {
    return "true";
  }
  if (payload.cancelled === true) {
    return "cancelled";
  }
  throw new Error("ui.response value required");
}

function normalizeUiResponsePayload(payload: Record<string, unknown>) {
  const responseTo = typeof payload.response_to === "string" && payload.response_to.trim()
    ? payload.response_to.trim()
    : typeof payload.id === "string" && payload.id.trim()
      ? payload.id.trim()
      : "";
  if (!responseTo) {
    throw new Error("ui.response response_to required");
  }
  return {
    response_to: responseTo,
    value: uiResponseValue(payload),
  };
}

function normalizeSessionDetailsResponse(response: SessionDetailsResponse & { session?: Record<string, unknown> | null }) {
  if (!response.session || typeof response.session !== "object") {
    return response as SessionDetailsResponse;
  }
  return {
    ...response.session,
    session_id: response.session_id ?? response.session.session_id,
    runtime_id: response.runtime_id ?? response.session.runtime_id,
    thread_id: response.thread_id ?? response.session.thread_id,
    agent_backend: response.agent_backend ?? response.session.agent_backend,
    cwd: response.cwd ?? response.session.cwd,
    model: response.model ?? response.session.model,
    priority_offset: response.priority_offset ?? response.session.priority_offset,
  } as SessionDetailsResponse;
}

function legacyProviderToCanonical(payload: Record<string, unknown>) {
  if (typeof payload.provider === "string" && payload.provider.trim()) {
    return payload.provider.trim();
  }
  if (typeof payload.provider_choice === "string" && payload.provider_choice.trim()) {
    return payload.provider_choice.trim();
  }
  const modelProvider = typeof payload.model_provider === "string" && payload.model_provider.trim()
    ? payload.model_provider.trim()
    : "";
  const preferredAuthMethod = typeof payload.preferred_auth_method === "string" && payload.preferred_auth_method.trim()
    ? payload.preferred_auth_method.trim()
    : "";
  if (modelProvider === "openai" && preferredAuthMethod === "chatgpt") {
    return "chatgpt";
  }
  if (modelProvider === "openai" && preferredAuthMethod === "apikey") {
    return "openai-api";
  }
  return modelProvider || undefined;
}

function normalizeCreateSessionPayload(payload: Record<string, unknown>) {
  return {
    agent_backend: typeof payload.agent_backend === "string" && payload.agent_backend.trim()
      ? payload.agent_backend.trim()
      : typeof payload.backend === "string" && payload.backend.trim()
        ? payload.backend.trim()
        : undefined,
    cwd: typeof payload.cwd === "string" ? payload.cwd : undefined,
    provider: legacyProviderToCanonical(payload),
    model: typeof payload.model === "string" && payload.model.trim() ? payload.model.trim() : undefined,
    reasoning_effort: typeof payload.reasoning_effort === "string" && payload.reasoning_effort.trim() ? payload.reasoning_effort.trim() : undefined,
    resume_session_id: typeof payload.resume_session_id === "string" && payload.resume_session_id.trim() ? payload.resume_session_id.trim() : undefined,
    title: typeof payload.title === "string" && payload.title.trim()
      ? payload.title.trim()
      : typeof payload.name === "string" && payload.name.trim()
        ? payload.name.trim()
        : undefined,
    pi_agent_grpc: typeof payload.pi_agent_grpc === "boolean" ? payload.pi_agent_grpc : undefined,
  };
}

export const api = {
  me(signal?: AbortSignal) {
    return getJson<{ ok?: boolean }>("/api/me", signal);
  },
  login(password: string, signal?: AbortSignal) {
    return postJson<LoginResponse>("/api/login", { password }, signal);
  },
  listSessions(options?: { groupKey?: string; offset?: number; limit?: number; groupOffset?: number; groupLimit?: number; agentBackend?: string; cwd?: string; title?: string }, signal?: AbortSignal) {
    const query = new URLSearchParams();
    if (options?.groupKey) {
      query.set("group_key", options.groupKey);
    }
    if (typeof options?.offset === "number" && Number.isFinite(options.offset) && options.offset > 0) {
      query.set("offset", String(options.offset));
    }
    if (typeof options?.limit === "number" && Number.isFinite(options.limit) && options.limit > 0) {
      query.set("limit", String(options.limit));
    }
    if (typeof options?.groupOffset === "number" && Number.isFinite(options.groupOffset) && options.groupOffset > 0) {
      query.set("group_offset", String(options.groupOffset));
    }
    if (typeof options?.groupLimit === "number" && Number.isFinite(options.groupLimit) && options.groupLimit > 0) {
      query.set("group_limit", String(options.groupLimit));
    }
    if (typeof options?.agentBackend === "string" && options.agentBackend.trim()) {
      query.set("agent_backend", options.agentBackend.trim());
    }
    if (typeof options?.cwd === "string" && options.cwd.trim()) {
      query.set("cwd", options.cwd.trim());
    }
    if (typeof options?.title === "string" && options.title.trim()) {
      query.set("title", options.title.trim());
    }
    const suffix = query.size ? `?${query.toString()}` : "";
    return getJson<SessionsResponse>(`/api/sessions${suffix}`, signal);
  },
  getBootstrap(options?: { refreshPiModels?: boolean }, signal?: AbortSignal) {
    const query = new URLSearchParams();
    if (options?.refreshPiModels) {
      query.set("refresh_pi_models", "1");
    }
    const suffix = query.size ? `?${query.toString()}` : "";
    return getJson<SessionBootstrapResponse>(`/api/bootstrap${suffix}`, signal);
  },
  getSessionsBootstrap(options?: { refreshPiModels?: boolean }, signal?: AbortSignal) {
    return api.getBootstrap(options, signal);
  },
  async getSessionDetails(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    const response = await getJson<SessionDetailsResponse & { session?: Record<string, unknown> | null }>(`/api/sessions/${routeId}/details`, signal);
    return normalizeSessionDetailsResponse(response);
  },
  listMessages(sessionId: string, init = false, signal?: AbortSignal, after?: number, before?: number, limit?: number, runtimeId?: string | null, deferred = false) {
    const query = new URLSearchParams();
    if (init) {
      query.set("init", "1");
    }
    if (typeof after === "number" && Number.isFinite(after) && after > 0) {
      query.set("after_seq", String(Math.floor(after)));
    }
    if (typeof before === "number" && Number.isFinite(before) && before > 0) {
      query.set("before", String(before));
      query.set("before_seq", String(before));
    }
    if (typeof limit === "number" && Number.isFinite(limit) && limit > 0) {
      query.set("limit", String(limit));
    }
    if (deferred) {
      query.set("deferred", "1");
    }
    const suffix = query.size ? `?${query.toString()}` : "";
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<MessagesResponse>(`/api/sessions/${routeId}/messages${suffix}`, signal);
  },
  getSessionUiState(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<SessionUiStateResponse>(`/api/sessions/${routeId}/ui_state`, signal);
  },
  getWaitInbox(signal?: AbortSignal) {
    return getJson<WaitInboxResponse>(`/api/waits/inbox`, signal);
  },
  getWaitThreads(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<WaitThreadsResponse>(`/api/sessions/${routeId}/waits/threads`, signal);
  },
  getWaitThread(sessionId: string, threadId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<WaitThreadResponse>(`/api/sessions/${routeId}/waits/threads/${encodeURIComponent(threadId)}`, signal);
  },
  claimWait(sessionId: string, waitId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<WaitLifecycleResponse>(`/api/sessions/${routeId}/waits/${encodeURIComponent(waitId)}/claim`, {});
  },
  answerWait(sessionId: string, waitId: string, answer: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<WaitLifecycleResponse>(`/api/sessions/${routeId}/waits/${encodeURIComponent(waitId)}/answer`, { answer });
  },
  cancelWait(sessionId: string, waitId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<WaitLifecycleResponse>(`/api/sessions/${routeId}/waits/${encodeURIComponent(waitId)}/cancel`, {});
  },
  getSessionState(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<LiveSessionResponse>(`/api/sessions/${routeId}/state`, signal);
  },
  probeSessionState(sessionId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<{ probe_id: string; state: LiveSessionResponse }>(`/api/sessions/${routeId}/state/probe`, {});
  },
  getLiveSession(sessionId: string, _offset?: number, _requestsVersion?: string, signal?: AbortSignal, _liveOffset?: number, runtimeId?: string | null, _bridgeOffset?: number) {
    return api.getSessionState(sessionId, signal, runtimeId);
  },
  getWorkspace(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<WorkspaceResponse>(`/api/sessions/${routeId}/workspace`, signal);
  },
  updateWorkspace(sessionId: string, payload: Record<string, unknown>, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<WorkspaceResponse>(`/api/sessions/${routeId}/workspace`, payload);
  },
  getSessionCommands(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<SessionCommandsResponse>(`/api/sessions/${routeId}/commands`, signal);
  },
executeSessionCommand(sessionId: string, payload: { name?: string; command?: string; args?: string }, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<ExecuteSessionCommandResponse>(`/api/sessions/${routeId}/commands`, payload);
  },
  attachSessionFile(sessionId: string, payload: { filename: string; data_b64: string; attachment_index: number }, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<AttachmentInjectResponse>(`/api/sessions/${routeId}/inject_image`, payload);
  },
  async sendMessage(sessionId: string, text: string, runtimeId?: string | null) {
    return sendRealtimeCommand({
      type: "send",
      stream: sessionStreamName(sessionId),
      payload: withSessionIdentity(sessionId, runtimeId, { text }),
    });
  },
  switchSessionModel(sessionId: string, payload: { model: string; provider?: string }, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<SwitchSessionModelResponse>(`/api/sessions/${routeId}/model`, payload);
  },
  async enqueueMessage(sessionId: string, text: string, runtimeId?: string | null) {
    return sendRealtimeCommand({
      type: "enqueue",
      stream: sessionStreamName(sessionId),
      payload: withSessionIdentity(sessionId, runtimeId, { text }),
    });
  },
  async cancelQueue(sessionId: string, runtimeId?: string | null) {
    return sendRealtimeCommand({
      type: "queue.cancel",
      stream: sessionStreamName(sessionId),
      payload: withSessionIdentity(sessionId, runtimeId),
    });
  },
  deleteSession(sessionId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<DeleteSessionResponse>(`/api/sessions/${routeId}/delete`, {});
  },
  handoffSession(sessionId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<HandoffSessionResponse>(`/api/sessions/${routeId}/handoff`, {}).then((response) => {
      if (response.session && typeof response.session === "object") {
        return {
          ...response,
          session_id: response.session_id ?? response.session.session_id,
          runtime_id: response.runtime_id ?? response.session.runtime_id,
          backend: response.backend ?? response.session.agent_backend,
          focused: response.focused ?? response.session.focused,
          alias: response.alias ?? response.session.alias,
          session_file_path: response.session_file_path ?? response.session.session_file_path,
        };
      }
      return response;
    });
  },
  restartSession(sessionId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<RestartSessionResponse>(`/api/sessions/${routeId}/restart`, {}).then((response) => {
      if (response.session && typeof response.session === "object") {
        return {
          ...response,
          session_id: response.session_id ?? response.session.session_id,
          runtime_id: response.runtime_id ?? response.session.runtime_id,
          backend: response.backend ?? response.session.agent_backend,
          focused: response.focused ?? response.session.focused,
          alias: response.alias ?? response.session.alias,
        };
      }
      return response;
    });
  },
  async createSession(payload: Record<string, unknown>) {
    const response = await postJson<CreateSessionResponse>(`/api/sessions`, normalizeCreateSessionPayload(payload));
    if (response.session && typeof response.session === "object") {
      return {
        ...response,
        session_id: response.session_id ?? response.session.session_id,
        runtime_id: response.runtime_id ?? response.session.runtime_id,
        backend: response.backend ?? response.session.agent_backend,
        pending_startup: response.pending_startup ?? response.session.pending_startup,
        focused: response.focused ?? response.session.focused,
        alias: response.alias ?? response.session.alias,
      };
    }
    return response;
  },
  getSessionResumeCandidates(cwd: string, agentBackend: string, options?: { offset?: number; limit?: number; scanOffset?: number; scanLimit?: number }) {
    const query = new URLSearchParams();
    query.set("cwd", cwd);
    query.set("backend", agentBackend);
    query.set("agent_backend", agentBackend);
    if (typeof options?.offset === "number") {
      query.set("offset", String(options.offset));
    }
    if (typeof options?.limit === "number") {
      query.set("limit", String(options.limit));
    }
    if (typeof options?.scanOffset === "number") {
      query.set("scan_offset", String(options.scanOffset));
    }
    if (typeof options?.scanLimit === "number") {
      query.set("scan_limit", String(options.scanLimit));
    }
    return getJson<SessionResumeCandidatesResponse>(`/api/session_resume_candidates?${query.toString()}`);
  },
  renameSession(sessionId: string, name: string) {
    return postJson<RenameSessionResponse>(`/api/sessions/${sessionId}/rename`, { name });
  },
  setSessionFocus(sessionId: string, focused: boolean, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<FocusSessionResponse>(`/api/sessions/${routeId}/focus`, { focused });
  },
  editSession(sessionId: string, payload: Record<string, unknown>) {
    return postJson<EditSessionResponse>(`/api/sessions/${sessionId}/edit`, payload);
  },
  getSupervisorProvider() {
    return getJson<SupervisorProviderResponse>(`/api/supervisor/provider`);
  },
  saveSupervisorProvider(payload: { base_url: string; api_key?: string; model: string }) {
    return postJson<SupervisorProviderResponse>(`/api/supervisor/provider`, payload);
  },
  getSessionSupervisor(sessionId: string) {
    return getJson<SessionSupervisorSnapshot>(`/api/sessions/${sessionId}/supervisor`);
  },
  saveSessionSupervisor(sessionId: string, payload: Record<string, unknown>) {
    return postJson<SessionSupervisorSnapshot>(`/api/sessions/${sessionId}/supervisor`, payload);
  },
  getSupervisorRuns(sessionId: string, limit?: number) {
    const query = new URLSearchParams();
    if (typeof limit === "number") {
      query.set("limit", String(limit));
    }
    const suffix = query.toString() ? `?${query.toString()}` : "";
    return getJson<SupervisorRunsResponse>(`/api/sessions/${sessionId}/supervisor/runs${suffix}`);
  },
  runSupervisorOnce(sessionId: string, dryRun = true) {
    return postJson<SupervisorRunOnceResponse>(`/api/sessions/${sessionId}/supervisor/run-once`, { dry_run: dryRun });
  },
  logout() {
    return postJson<LogoutResponse>(`/api/logout`, {});
  },
  editCwdGroup(payload: { cwd: string; label?: string; collapsed?: boolean }) {
    return postJson<EditCwdGroupResponse>(`/api/cwd_groups/edit`, payload);
  },
  getVoiceSettings() {
    return getJson<VoiceSettingsResponse>(`/api/settings/voice`);
  },
  saveVoiceSettings(payload: Record<string, unknown>) {
    return postJson<VoiceSettingsResponse>(`/api/settings/voice`, payload);
  },
  testVoiceProvider(payload: Record<string, unknown>) {
    return postJson<VoiceProviderTestResponse>(`/api/settings/voice/test_provider`, payload);
  },
  setAudioListener(clientId: string, enabled: boolean) {
    return postJson<AudioListenerResponse>(`/api/audio/listener`, { client_id: clientId, enabled });
  },
  triggerTestAnnouncement() {
    return postJson(`/api/audio/test_announcement`, {});
  },
  triggerTestPushNotification() {
    return postJson(`/api/notifications/test_push`, {});
  },
  getNotificationsFeed(since: number) {
    const query = new URLSearchParams();
    query.set("since", String(since));
    return getJson<NotificationFeedResponse>(`/api/notifications/feed?${query.toString()}`);
  },
  getNotificationMessage(messageId: string) {
    const query = new URLSearchParams();
    query.set("message_id", messageId);
    return getJson<NotificationMessageResponse>(`/api/notifications/message?${query.toString()}`);
  },
  getNotificationSubscriptionState() {
    return getJson<NotificationSubscriptionStateResponse>(`/api/notifications/subscription`);
  },
  upsertNotificationSubscription(payload: Record<string, unknown>) {
    return postJson<NotificationSubscriptionStateResponse>(`/api/notifications/subscription`, payload);
  },
  toggleNotificationSubscription(endpoint: string, enabled: boolean) {
    return postJson<NotificationSubscriptionStateResponse>(`/api/notifications/subscription/toggle`, { endpoint, enabled });
  },
  getDiagnostics(sessionId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson(`/api/sessions/${routeId}/diagnostics`);
  },
  getQueue(sessionId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson(`/api/sessions/${routeId}/queue`);
  },
  async getFiles(sessionId: string, path?: string, signal?: AbortSignal, runtimeId?: string | null) {
    const query = new URLSearchParams();
    if (path) {
      query.set("path", path);
    }
    const suffix = query.size ? `?${query.toString()}` : "";
    const routeId = getSessionRouteId(sessionId, runtimeId);
    const response = await getJson<SessionFileListResponse>(`/api/sessions/${routeId}/file/list${suffix}`, signal);
    return {
      ...response,
      entries: Array.isArray(response.entries) ? response.entries : Array.isArray(response.items) ? response.items : [],
    } as SessionFileListResponse & { entries: NonNullable<SessionFileListResponse["entries"]> };
  },
  async getFileRead(sessionId: string, path: string, signal?: AbortSignal, runtimeId?: string | null) {
    const query = new URLSearchParams();
    query.set("path", path);
    const routeId = getSessionRouteId(sessionId, runtimeId);
    const response = await getJson<SessionFileReadResponse>(`/api/sessions/${routeId}/file/read?${query.toString()}`, signal);
    return {
      ...response,
      size: response.size ?? response.size_bytes,
      content_type: response.content_type ?? response.mime_type,
    };
  },
  getGitFileVersions(sessionId: string, path: string, signal?: AbortSignal, runtimeId?: string | null) {
    const query = new URLSearchParams();
    query.set("path", path);
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<GitFileVersionsResponse>(`/api/sessions/${routeId}/git/file_versions?${query.toString()}`, signal);
  },
  getHarness(sessionId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<HarnessConfigResponse>(`/api/sessions/${routeId}/harness`);
  },
  saveHarness(sessionId: string, payload: Record<string, unknown>, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<HarnessConfigResponse>(`/api/sessions/${routeId}/harness`, payload);
  },
  interruptSession(sessionId: string, runtimeId?: string | null) {
    return sendRealtimeCommand({
      type: "interrupt",
      stream: sessionStreamName(sessionId),
      payload: withSessionIdentity(sessionId, runtimeId),
    });
  },
  submitUiResponse(sessionId: string, payload: Record<string, unknown>, runtimeId?: string | null) {
    return sendRealtimeCommand({
      type: "ui.response",
      stream: sessionUiStreamName(sessionId),
      payload: withSessionIdentity(sessionId, runtimeId, normalizeUiResponsePayload(payload)),
    });
  },
};
