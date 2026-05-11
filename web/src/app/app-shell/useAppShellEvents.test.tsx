/** @jsxImportSource preact */
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import type { RealtimeEnvelope } from "../../lib/types";
import { useAppShellEvents } from "./useAppShellEvents";

const realtimeMocks = vi.hoisted(() => ({
  connect: vi.fn().mockResolvedValue(undefined),
  disconnect: vi.fn(),
  setRealtimeSubscriptions: vi.fn(),
  subscribeRealtimeFrames: vi.fn(),
  subscribeRealtimeState: vi.fn(),
  frameListener: undefined as ((frame: RealtimeEnvelope) => void) | undefined,
}));

vi.mock("../../domains/realtime/client", () => ({
  connect: realtimeMocks.connect,
  disconnect: realtimeMocks.disconnect,
  setRealtimeSubscriptions: realtimeMocks.setRealtimeSubscriptions,
  subscribeRealtimeFrames: realtimeMocks.subscribeRealtimeFrames,
  subscribeRealtimeState: realtimeMocks.subscribeRealtimeState,
}));

function Harness(props: Parameters<typeof useAppShellEvents>[0]) {
  useAppShellEvents(props);
  return <div data-testid="session-events" />;
}

function baseProps(overrides: Partial<Parameters<typeof useAppShellEvents>[0]> = {}): Parameters<typeof useAppShellEvents>[0] {
  return {
    activeSessionBackend: "codex",
    activeSessionHistorical: false,
    activeSessionId: "sess-1",
    activeSessionPending: false,
    activeSessionRuntimeId: "runtime-1",
    bootstrapLoaded: true,
    items: [{ session_id: "sess-1", busy: true }] as any,
    liveSessionStoreApi: {
      applyFrame: vi.fn(),
      getState: vi.fn(() => ({ offsetsBySessionId: { "sess-1": 1 }, streamCursorsBySessionId: {}, uiStreamCursorsBySessionId: {} })),
      poll: vi.fn().mockResolvedValue(undefined),
      setBufferAssistantOutput: vi.fn(),
    } as any,
    onConnectionChange: vi.fn(),
    refreshNotificationsFeed: vi.fn().mockResolvedValue(undefined),
    sessionUiStoreApi: { refresh: vi.fn().mockResolvedValue(undefined) } as any,
    sessionsStoreApi: { applySessionStateFrame: vi.fn(), refresh: vi.fn().mockResolvedValue(undefined) } as any,
    waitsStoreApi: { applyFrame: vi.fn() } as any,
    workspaceOpen: false,
    ...overrides,
  };
}

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

afterEach(() => {
  document.body.innerHTML = "";
  realtimeMocks.connect.mockClear();
  realtimeMocks.disconnect.mockClear();
  realtimeMocks.setRealtimeSubscriptions.mockClear();
  realtimeMocks.subscribeRealtimeFrames.mockReset();
  realtimeMocks.subscribeRealtimeState.mockReset();
  realtimeMocks.frameListener = undefined;
});

it("repairs active messages immediately when a session state frame advertises a newer tail", async () => {
  realtimeMocks.subscribeRealtimeFrames.mockImplementation((listener: (frame: RealtimeEnvelope) => void) => {
    realtimeMocks.frameListener = listener;
    return vi.fn();
  });
  realtimeMocks.subscribeRealtimeState.mockImplementation(() => vi.fn());
  const liveSessionStoreApi = {
    applyFrame: vi.fn(),
    getState: vi.fn(() => ({ offsetsBySessionId: { "sess-1": 1 }, streamCursorsBySessionId: {}, uiStreamCursorsBySessionId: {} })),
    poll: vi.fn().mockResolvedValue(undefined),
    setBufferAssistantOutput: vi.fn(),
  } as any;
  const sessionsStoreApi = { applySessionStateFrame: vi.fn(), refresh: vi.fn().mockResolvedValue(undefined) } as any;

  const root = document.createElement("div");
  document.body.appendChild(root);
  await act(async () => {
    render(<Harness {...baseProps({ liveSessionStoreApi, sessionsStoreApi })} />, root);
    await flush();
  });

  await act(async () => {
    realtimeMocks.frameListener?.({
      type: "session.state",
      stream: "session:sess-1",
      payload: { session_id: "sess-1", busy: true, tail_seq: 2 },
    });
    await flush();
  });

  expect(sessionsStoreApi.applySessionStateFrame).toHaveBeenCalled();
  expect(liveSessionStoreApi.applyFrame).toHaveBeenCalled();
  expect(liveSessionStoreApi.poll).toHaveBeenCalledWith("sess-1", "runtime-1");
});

it("does not repair active messages when the advertised tail is already loaded", async () => {
  realtimeMocks.subscribeRealtimeFrames.mockImplementation((listener: (frame: RealtimeEnvelope) => void) => {
    realtimeMocks.frameListener = listener;
    return vi.fn();
  });
  realtimeMocks.subscribeRealtimeState.mockImplementation(() => vi.fn());
  const liveSessionStoreApi = {
    applyFrame: vi.fn(),
    getState: vi.fn(() => ({ offsetsBySessionId: { "sess-1": 2 }, streamCursorsBySessionId: {}, uiStreamCursorsBySessionId: {} })),
    poll: vi.fn().mockResolvedValue(undefined),
    setBufferAssistantOutput: vi.fn(),
  } as any;

  const root = document.createElement("div");
  document.body.appendChild(root);
  await act(async () => {
    render(<Harness {...baseProps({ liveSessionStoreApi })} />, root);
    await flush();
  });

  await act(async () => {
    realtimeMocks.frameListener?.({
      type: "session.state",
      stream: "session:sess-1",
      payload: { session_id: "sess-1", busy: true, tail_seq: 2 },
    });
    await flush();
  });

  expect(liveSessionStoreApi.poll).not.toHaveBeenCalled();
});
