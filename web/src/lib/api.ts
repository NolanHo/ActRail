import { sendRealtimeCommand } from "../domains/realtime/client";
import { getJson, postJson } from "./http";
import { getSessionRouteId } from "./session-identity";
import type {
  AttachmentInjectResponse,
  AudioListenerResponse,
  CreateSessionResponse,
  DeleteSessionResponse,
  EditSessionResponse,
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
  VoiceSettingsResponse,
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

export const api = {
  me(signal?: AbortSignal) {
    return getJson<{ ok?: boolean }>("/api/me", signal);
  },
  login(password: string, signal?: AbortSignal) {
    return postJson<LoginResponse>("/api/login", { password }, signal);
  },
  listSessions(options?: { groupKey?: string; offset?: number; limit?: number; groupOffset?: number; groupLimit?: number }, signal?: AbortSignal) {
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
  getSessionDetails(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<SessionDetailsResponse>(`/api/sessions/${routeId}/details`, signal);
  },
  listMessages(sessionId: string, init = false, signal?: AbortSignal, offset?: number, before?: number, limit?: number, runtimeId?: string | null) {
    const query = new URLSearchParams();
    if (init) {
      query.set("init", "1");
    }
    if (typeof offset === "number" && Number.isFinite(offset) && offset > 0) {
      query.set("offset", String(offset));
    }
    if (typeof before === "number" && Number.isFinite(before) && before > 0) {
      query.set("before", String(before));
      query.set("before_seq", String(before));
    }
    if (typeof limit === "number" && Number.isFinite(limit) && limit > 0) {
      query.set("limit", String(limit));
    }
    const suffix = query.size ? `?${query.toString()}` : "";
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<MessagesResponse>(`/api/sessions/${routeId}/messages${suffix}`, signal);
  },
  getSessionUiState(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<SessionUiStateResponse>(`/api/sessions/${routeId}/ui_state`, signal);
  },
  getSessionState(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<LiveSessionResponse>(`/api/sessions/${routeId}/state`, signal);
  },
  getLiveSession(sessionId: string, _offset?: number, _requestsVersion?: string, signal?: AbortSignal, _liveOffset?: number, runtimeId?: string | null, _bridgeOffset?: number) {
    return api.getSessionState(sessionId, signal, runtimeId);
  },
  getWorkspace(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<WorkspaceResponse>(`/api/sessions/${routeId}/workspace`, signal);
  },
  getSessionCommands(sessionId: string, signal?: AbortSignal, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<SessionCommandsResponse>(`/api/sessions/${routeId}/commands`, signal);
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
  deleteSession(sessionId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<DeleteSessionResponse>(`/api/sessions/${routeId}/delete`, {});
  },
  handoffSession(sessionId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<HandoffSessionResponse>(`/api/sessions/${routeId}/handoff`, {});
  },
  restartSession(sessionId: string, runtimeId?: string | null) {
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return postJson<RestartSessionResponse>(`/api/sessions/${routeId}/restart`, {});
  },
  async createSession(payload: Record<string, unknown>) {
    const response = await postJson<CreateSessionResponse>(`/api/sessions`, payload);
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
  getSessionResumeCandidates(cwd: string, agentBackend: string, options?: { offset?: number; limit?: number }) {
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
  getFiles(sessionId: string, path?: string, signal?: AbortSignal, runtimeId?: string | null) {
    const query = new URLSearchParams();
    if (path) {
      query.set("path", path);
    }
    const suffix = query.size ? `?${query.toString()}` : "";
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<SessionFileListResponse>(`/api/sessions/${routeId}/file/list${suffix}`, signal);
  },
  getFileRead(sessionId: string, path: string, signal?: AbortSignal, runtimeId?: string | null) {
    const query = new URLSearchParams();
    query.set("path", path);
    const routeId = getSessionRouteId(sessionId, runtimeId);
    return getJson<SessionFileReadResponse>(`/api/sessions/${routeId}/file/read?${query.toString()}`, signal);
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
