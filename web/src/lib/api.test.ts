import { afterEach, describe, expect, it, vi } from "vitest";
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { sendRealtimeCommand } from "../domains/realtime/client";
import { CommandResponseSchema, ListSessionsRequestSchema, SessionMessagesResponseSchema } from "../gen/actrail/v1/transport_pb";
import { api } from "./api";
import { getJson, HttpError, subscribeUnauthorized } from "./http";
import type { LiveSessionResponse, MessagesResponse, SessionBootstrapResponse, SessionDetailsResponse, SessionUiStateResponse, SessionsResponse, WorkspaceResponse } from "./types";

vi.mock("../domains/realtime/client", () => ({
  sendRealtimeCommand: vi.fn(),
}));

describe("getJson", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("throws server error messages", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 400,
        text: async () => '{"error":"bad request"}',
      }),
    );

    await expect(getJson("/api/sessions")).rejects.toThrow("bad request");
  });

  it("parses successful json responses", async () => {
    const payload: SessionsResponse = {
      sessions: [{ session_id: "s1", agent_backend: "pi", busy: true }],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        text: async () => JSON.stringify(payload),
      }),
    );

    await expect(getJson<SessionsResponse>("/api/sessions")).resolves.toEqual(payload);
  });

  it("throws an HttpError with status and notifies unauthorized listeners on 401", async () => {
    const listener = vi.fn();
    const unsubscribe = subscribeUnauthorized(listener);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 401,
        text: async () => '{"error":"Unauthorized"}',
      }),
    );

    await expect(getJson("/api/sessions")).rejects.toMatchObject({
      message: "Unauthorized",
      status: 401,
    } satisfies Partial<HttpError>);
    expect(listener).toHaveBeenCalledTimes(1);
    unsubscribe();
  });
});

describe("api", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("loads sessions through Connect proto by default", async () => {
    const signal = new AbortController().signal;
    const payload = new TextEncoder().encode(JSON.stringify({ sessions: [] }));
    const body = toBinary(CommandResponseSchema, create(CommandResponseSchema, { payloadJson: payload }));
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      arrayBuffer: async () => body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength),
    });
    vi.stubGlobal("fetch", fetchMock);

    const response = await api.listSessions(undefined, signal);

    expect(response).toEqual({ sessions: [] });
    expect(fetchMock).toHaveBeenCalledWith("api/connect/actrail.v1.SessionCommandService/ListSessions", expect.objectContaining({
      method: "POST",
      headers: { "Content-Type": "application/connect+proto", Accept: "application/connect+proto" },
      signal,
    }));
  });

  it("requests sessions over REST when Connect is disabled", async () => {
    const signal = new AbortController().signal;
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"sessions":[]}',
    });
    vi.stubGlobal("fetch", fetchMock);

    const payload = await api.listSessions(undefined, signal, false);

    expect(payload).toEqual({ sessions: [] });
    expect(fetchMock).toHaveBeenCalledWith("api/sessions", {
      headers: { Accept: "application/json" },
      signal,
    });
  });

  it("requests sessions bootstrap metadata", async () => {
    const payload: SessionBootstrapResponse = {
      recent_cwds: ["/tmp/project"],
      cwd_groups: { "/tmp/project": { label: "Project", collapsed: false } },
      new_session_defaults: { default_backend: "pi" },
      tmux_available: true,
    };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getSessionsBootstrap()).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/bootstrap", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests bootstrap with a forced Pi model refresh", async () => {
    const payload: SessionBootstrapResponse = {
      new_session_defaults: { default_backend: "pi" },
    };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getSessionsBootstrap({ refreshPiModels: true })).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/bootstrap?refresh_pi_models=1", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests live subagents with closed actors included", async () => {
    const payload = { ok: true, roots: [], total_count: 0, non_leaf_count: 0 };
    const signal = new AbortController().signal;
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.listSubagents({ includeClosed: true }, signal)).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/subagents?include_closed=1", {
      headers: { Accept: "application/json" },
      signal,
    });
  });

  it("requests session details and normalizes the flat backend shape", async () => {
    const payload: SessionDetailsResponse = {
      session_id: "sess-1",
      agent_backend: "pi",
      model: "gpt-5.4",
      priority_offset: 0.25,
    };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getSessionDetails("sess-1")).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/sess-1/details", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("posts canonical create-session fields and flattens the session envelope", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({
        ok: true,
        session: {
          session_id: "sess-2",
          runtime_id: "rt-2",
          agent_backend: "pi",
          alias: "Inbox cleanup",
          cwd: "/tmp/project",
          busy: false,
        },
      }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.createSession({
      cwd: "/tmp/project",
      title: "Inbox cleanup",
      agent_backend: "pi",
      provider: "openrouter",
      model: "gpt-5.4",
      reasoning_effort: "high",
    })).resolves.toEqual(expect.objectContaining({
      ok: true,
      session_id: "sess-2",
      runtime_id: "rt-2",
      backend: "pi",
      alias: "Inbox cleanup",
    }));

    expect(fetchMock).toHaveBeenCalledWith("api/sessions", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({
        agent_backend: "pi",
        cwd: "/tmp/project",
        provider: "openrouter",
        model: "gpt-5.4",
        reasoning_effort: "high",
        resume_session_id: undefined,
        title: "Inbox cleanup",
      }),
    }));
  });

  it("posts restart to the durable session route", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({
        ok: true,
        session_id: "sess-1",
        runtime_id: "rt-2",
        previous_runtime_id: "rt-1",
        restarted: true,
      }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.restartSession("sess-1", "rt-1")).resolves.toEqual(expect.objectContaining({
      ok: true,
      session_id: "sess-1",
      runtime_id: "rt-2",
      previous_runtime_id: "rt-1",
    }));

    expect(fetchMock).toHaveBeenCalledWith("api/sessions/sess-1/restart", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({}),
    }));
  });

  it("posts handoff and flattens the session envelope", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({
        ok: true,
        previous_session_id: "sess-1",
        history_path: "/tmp/old.jsonl",
        sidecar_path: "/tmp/sidecar.json",
        session: {
          session_id: "sess-2",
          runtime_id: "rt-2",
          agent_backend: "pi",
          alias: "Inbox cleanup",
          cwd: "/tmp/project",
          session_file_path: "/tmp/new.jsonl",
        },
      }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.handoffSession("sess-1", "rt-1")).resolves.toEqual(expect.objectContaining({
      ok: true,
      session_id: "sess-2",
      runtime_id: "rt-2",
      backend: "pi",
      previous_session_id: "sess-1",
      history_path: "/tmp/old.jsonl",
      sidecar_path: "/tmp/sidecar.json",
      session_file_path: "/tmp/new.jsonl",
    }));

    expect(fetchMock).toHaveBeenCalledWith("api/sessions/rt-1/handoff", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({}),
    }));
  });

  it("requests more sessions for a specific cwd group", async () => {
    const payload: SessionsResponse = {
      sessions: [{ session_id: "sess-6", cwd: "/work/docs" }],
      remaining_by_group: { "/work/docs": 1 },
    };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.listSessions({ groupKey: "/work/docs", offset: 5, limit: 5 }, undefined, false)).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/sessions?group_key=%2Fwork%2Fdocs&offset=5&limit=5", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("encodes sessions list filters for Connect proto", async () => {
    const payload = new TextEncoder().encode(JSON.stringify({ sessions: [{ session_id: "sess-6" }] }));
    const body = toBinary(CommandResponseSchema, create(CommandResponseSchema, { payloadJson: payload }));
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      arrayBuffer: async () => body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.listSessions({ groupKey: "/work/docs", offset: 5, limit: 50, agentBackend: "pi", cwd: "/work/docs", title: "Docs" })).resolves.toEqual({ sessions: [{ session_id: "sess-6" }] });
    const requestBody = fetchMock.mock.calls[0]?.[1]?.body as Uint8Array;
    const request = fromBinary(ListSessionsRequestSchema, requestBody);
    expect(request).toMatchObject({ groupKey: "/work/docs", offset: 5, limit: 50, agentBackend: "pi", cwd: "/work/docs", title: "Docs" });
  });

  it("requests more omitted session groups", async () => {
    const payload: SessionsResponse = {
      sessions: [{ session_id: "group-4", cwd: "/work/group-4" }],
      omitted_group_count: 0,
    };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.listSessions({ groupOffset: 3, groupLimit: 3 }, undefined, false)).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/sessions?group_offset=3&group_limit=3", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("posts credentials to the login endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.login("123456")).resolves.toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledWith("api/login", {
      method: "POST",
      signal: undefined,
      headers: {
        "Content-Type": "application/json",
        Accept: "application/json",
      },
      body: JSON.stringify({ password: "123456" }),
    });
  });

  it("loads messages through Connect proto by default", async () => {
    const body = toBinary(SessionMessagesResponseSchema, create(SessionMessagesResponseSchema, {
      eventsJson: [new TextEncoder().encode(JSON.stringify({ seq: 1, role: "user", text: "hi" }))],
      tailSeq: 1n,
    }));
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      arrayBuffer: async () => body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.listMessages("session-1", true)).resolves.toMatchObject({
      events: [{ seq: 1, role: "user", text: "hi" }],
      tail_seq: 1,
    });
    expect(fetchMock).toHaveBeenCalledWith("api/connect/actrail.v1.SessionCommandService/SessionMessages", expect.objectContaining({
      method: "POST",
      headers: { "Content-Type": "application/connect+proto", Accept: "application/connect+proto" },
    }));
  });

  it("builds the init messages REST route when Connect is disabled", async () => {
    const payload: MessagesResponse = { events: [], offset: 0, ui_version: "v1" };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.listMessages("session-1", true, undefined, undefined, undefined, undefined, undefined, false, false)).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/session-1/messages?init=1", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("includes after_seq when polling from a known message cursor over REST", async () => {
    const payload: MessagesResponse = { events: [{ id: "m1" }], offset: 9 };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.listMessages("session-1", false, undefined, 4, undefined, undefined, undefined, false, false)).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/session-1/messages?after_seq=4", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("includes before and limit when loading older REST history pages", async () => {
    const payload: MessagesResponse = { events: [{ id: "m0" }], offset: 9, has_older: true, next_before: 12 };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.listMessages("session-1", true, undefined, undefined, 6, 40, undefined, false, false)).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/session-1/messages?init=1&before=6&before_seq=6&limit=40", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests the session ui state", async () => {
    const payload: SessionUiStateResponse = {
      requests: [{ id: "ui-req-1", method: "select" }],
    };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getSessionUiState("pi-session")).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/pi-session/ui_state", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests session state snapshots through Connect proto by default", async () => {
    const payload: LiveSessionResponse = {
      ok: true,
      session_id: "session-1",
      offset: 6,
      busy: true,
      events: [{ id: "m2" }],
      requests: [{ id: "r1", method: "select" }],
    };
    const body = toBinary(CommandResponseSchema, create(CommandResponseSchema, {
      payloadJson: new TextEncoder().encode(JSON.stringify(payload)),
    }));
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      arrayBuffer: async () => body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getLiveSession("session-1", 4)).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/connect/actrail.v1.SessionCommandService/SessionState", expect.objectContaining({
      method: "POST",
      headers: { "Content-Type": "application/connect+proto", Accept: "application/connect+proto" },
    }));
  });

  it("requests session state snapshots when compatibility cursors are provided", async () => {
    const payload: LiveSessionResponse = {
      ok: true,
      session_id: "session-1",
      offset: 6,
      busy: true,
      events: [{ id: "m2" }],
      requests_version: "v1",
      requests: [{ id: "r1", method: "select" }],
    };
    const body = toBinary(CommandResponseSchema, create(CommandResponseSchema, {
      payloadJson: new TextEncoder().encode(JSON.stringify(payload)),
    }));
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      arrayBuffer: async () => body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getLiveSession("session-1", 4, "v1", undefined, undefined, undefined, undefined)).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith(
      "api/connect/actrail.v1.SessionCommandService/SessionState",
      expect.objectContaining({
        method: "POST",
        headers: { "Content-Type": "application/connect+proto", Accept: "application/connect+proto" },
      }),
    );
  });

  it("ignores legacy live cursor arguments and requests the state snapshot route", async () => {
    const payload: LiveSessionResponse = {
      ok: true,
      session_id: "session-1",
      offset: 6,
      live_offset: 3,
      bridge_offset: 9,
      busy: true,
      events: [{ id: "m2" }],
      requests_version: "v1",
      requests: [{ id: "r1", method: "select" }],
    };
    const body = toBinary(CommandResponseSchema, create(CommandResponseSchema, {
      payloadJson: new TextEncoder().encode(JSON.stringify(payload)),
    }));
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      arrayBuffer: async () => body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getLiveSession("session-1", 4, "v1", undefined, 2, undefined, 7)).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith(
      "api/connect/actrail.v1.SessionCommandService/SessionState",
      expect.objectContaining({
        method: "POST",
        headers: { "Content-Type": "application/connect+proto", Accept: "application/connect+proto" },
      }),
    );
  });

  it("requests workspace data", async () => {
    const payload: WorkspaceResponse = {
      root_path: "/tmp/project",
      selected_path: "src/main.tsx",
      open_paths: ["src/main.tsx"],
      history_items: [{ path: "README.md", label: "README" }],
    };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getWorkspace("session-1")).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/session-1/workspace", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("updates workspace data", async () => {
    const payload: WorkspaceResponse = {
      root_path: "/tmp/project",
      selected_path: "src/main.tsx",
      open_paths: ["src/main.tsx", "src"],
      history_items: [{ path: "src/main.tsx", label: "main.tsx" }],
    };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.updateWorkspace("session-1", {
      selected_path: "src/main.tsx",
      open_paths: ["src/main.tsx", "src"],
      history_items: [{ path: "src/main.tsx", label: "main.tsx" }],
    })).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/session-1/workspace", expect.objectContaining({
      method: "POST",
      headers: expect.objectContaining({ "Content-Type": "application/json" }),
      body: JSON.stringify({
        selected_path: "src/main.tsx",
        open_paths: ["src/main.tsx", "src"],
        history_items: [{ path: "src/main.tsx", label: "main.tsx" }],
      }),
    }));
  });

  it("requests the session command list", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"commands":[{"name":"reload","description":"Reload Pi"}]}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getSessionCommands("pi-session")).resolves.toEqual({
      commands: [{ name: "reload", description: "Reload Pi" }],
    });
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/pi-session/commands", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("executes a session command through the runtime route", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true,"command":"handoff","session_id":"sess-2","runtime_id":"rt-2"}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.executeSessionCommand("sess-1", { name: "handoff", args: "now" }, "rt-1")).resolves.toEqual({
      ok: true,
      command: "handoff",
      session_id: "sess-2",
      runtime_id: "rt-2",
    });
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/rt-1/commands", expect.objectContaining({
      method: "POST",
      headers: expect.objectContaining({ "Content-Type": "application/json" }),
      body: JSON.stringify({ name: "handoff", args: "now" }),
    }));
  });

  it("posts attachment payloads for a session", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true,"path":"/tmp/upload.txt"}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.attachSessionFile("codex-session", {
      filename: "notes.txt",
      data_b64: "aGVsbG8=",
      attachment_index: 1,
    })).resolves.toEqual({ ok: true, path: "/tmp/upload.txt" });

    expect(fetchMock).toHaveBeenCalledWith("api/sessions/codex-session/inject_image", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({
        filename: "notes.txt",
        data_b64: "aGVsbG8=",
        attachment_index: 1,
      }),
    }));
  });

  it("requests file reads for a session path", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"kind":"text","mime_type":"text/plain","size_bytes":5,"text":"hello"}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getFileRead("pi-session", "src/main.tsx")).resolves.toEqual({ kind: "text", mime_type: "text/plain", content_type: "text/plain", size_bytes: 5, size: 5, text: "hello" });
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/pi-session/file/read?path=src%2Fmain.tsx", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests the session file list", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"root_path":"/tmp/project","path":"","items":[{"name":"src","path":"src","kind":"dir"}],"truncated":false}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getFiles("pi-session")).resolves.toEqual({
      root_path: "/tmp/project",
      path: "",
      items: [{ name: "src", path: "src", kind: "dir" }],
      entries: [{ name: "src", path: "src", kind: "dir" }],
      truncated: false,
    });
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/pi-session/file/list", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests a nested session directory listing", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"path":"src","items":[{"name":"main.tsx","path":"src/main.tsx","kind":"file"}]}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getFiles("pi-session", "src")).resolves.toEqual({
      path: "src",
      items: [{ name: "main.tsx", path: "src/main.tsx", kind: "file" }],
      entries: [{ name: "main.tsx", path: "src/main.tsx", kind: "file" }],
    });
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/pi-session/file/list?path=src", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests git file versions for a session path", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"path":"src/main.tsx","items":[{"version_id":"workspace","label":"Workspace","current":true}],"fallback_reason":"workspace only"}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getGitFileVersions("pi-session", "src/main.tsx")).resolves.toEqual({ path: "src/main.tsx", items: [{ version_id: "workspace", label: "Workspace", current: true }], fallback_reason: "workspace only" });
    expect(fetchMock).toHaveBeenCalledWith("api/sessions/pi-session/git/file_versions?path=src%2Fmain.tsx", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests and saves harness settings", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        text: async () => '{"ok":true,"enabled":true}',
      })
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        text: async () => '{"ok":true,"enabled":false}',
      });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getHarness("sess-1")).resolves.toEqual({ ok: true, enabled: true });
    await expect(api.saveHarness("sess-1", { enabled: false })).resolves.toEqual({ ok: true, enabled: false });

    expect(fetchMock).toHaveBeenNthCalledWith(1, "api/sessions/sess-1/harness", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
    expect(fetchMock).toHaveBeenNthCalledWith(2, "api/sessions/sess-1/harness", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ enabled: false }),
    }));
  });

  it("posts session edit payloads", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true,"alias":"Updated"}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.editSession("sess-1", {
      name: "Updated",
      priority_offset: 0.25,
      snooze_until: null,
      dependency_session_id: "sess-2",
    })).resolves.toEqual({ ok: true, alias: "Updated" });

    expect(fetchMock).toHaveBeenCalledWith("api/sessions/sess-1/edit", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({
        name: "Updated",
        priority_offset: 0.25,
        snooze_until: null,
        dependency_session_id: "sess-2",
      }),
    }));
  });

  it("requests voice settings", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true,"tts_base_url":"https://example.test/v1"}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getVoiceSettings()).resolves.toEqual({ ok: true, tts_base_url: "https://example.test/v1" });
    expect(fetchMock).toHaveBeenCalledWith("api/settings/voice", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("posts announcement listener heartbeats", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true,"active_listener_count":1}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.setAudioListener("listener-1", true)).resolves.toEqual({ ok: true, active_listener_count: 1 });
    expect(fetchMock).toHaveBeenCalledWith("api/audio/listener", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ client_id: "listener-1", enabled: true }),
    }));
  });

  it("requests the notification feed with a since cursor", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true,"items":[]}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getNotificationsFeed(123.5)).resolves.toEqual({ ok: true, items: [] });
    expect(fetchMock).toHaveBeenCalledWith("api/notifications/feed?since=123.5", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests notification message details by id", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true,"notification_text":"ready"}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getNotificationMessage("msg-1")).resolves.toEqual({ ok: true, notification_text: "ready" });
    expect(fetchMock).toHaveBeenCalledWith("api/notifications/message?message_id=msg-1", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("requests notification subscription state", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true,"subscriptions":[]}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.getNotificationSubscriptionState()).resolves.toEqual({ ok: true, subscriptions: [] });
    expect(fetchMock).toHaveBeenCalledWith("api/notifications/subscription", {
      headers: { Accept: "application/json" },
      signal: undefined,
    });
  });

  it("posts notification subscriptions", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true,"subscriptions":[]}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.upsertNotificationSubscription({ subscription: { endpoint: "https://push.test/sub/1" } })).resolves.toEqual({ ok: true, subscriptions: [] });
    expect(fetchMock).toHaveBeenCalledWith("api/notifications/subscription", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ subscription: { endpoint: "https://push.test/sub/1" } }),
    }));
  });

  it("toggles notification subscriptions by endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => '{"ok":true,"subscriptions":[]}',
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.toggleNotificationSubscription("https://push.test/sub/1", false)).resolves.toEqual({ ok: true, subscriptions: [] });
    expect(fetchMock).toHaveBeenCalledWith("api/notifications/subscription/toggle", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ endpoint: "https://push.test/sub/1", enabled: false }),
    }));
  });

  it("edits cwd group metadata", async () => {
    const payload = { ok: true, cwd: "/tmp", label: "New Label", collapsed: true };
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(payload),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.editCwdGroup({ cwd: "/tmp", label: "New Label", collapsed: true })).resolves.toEqual(payload);
    expect(fetchMock).toHaveBeenCalledWith("api/cwd_groups/edit", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ cwd: "/tmp", label: "New Label", collapsed: true }),
    }));
  });

  it("submits ui.response frames with protocol payload keys", async () => {
    vi.mocked(sendRealtimeCommand).mockResolvedValue({ accepted: true });

    await expect(api.submitUiResponse("sess-1", { id: "ask-1", value: "A" })).resolves.toEqual({ accepted: true });
    expect(sendRealtimeCommand).toHaveBeenCalledWith({
      type: "ui.response",
      stream: "session:sess-1:ui",
      payload: {
        session_id: "sess-1",
        response_to: "ask-1",
        value: "A",
      },
    });
  });

  it("maps confirm and cancel payloads onto protocol value fields", async () => {
    vi.mocked(sendRealtimeCommand).mockResolvedValue({ accepted: true });

    await api.submitUiResponse("sess-1", { id: "ask-1", confirmed: true });
    await api.submitUiResponse("sess-1", { id: "ask-2", cancelled: true });

    expect(sendRealtimeCommand).toHaveBeenNthCalledWith(1, {
      type: "ui.response",
      stream: "session:sess-1:ui",
      payload: {
        session_id: "sess-1",
        response_to: "ask-1",
        value: "true",
      },
    });
    expect(sendRealtimeCommand).toHaveBeenNthCalledWith(2, {
      type: "ui.response",
      stream: "session:sess-1:ui",
      payload: {
        session_id: "sess-1",
        response_to: "ask-2",
        value: "cancelled",
      },
    });
  });
});
