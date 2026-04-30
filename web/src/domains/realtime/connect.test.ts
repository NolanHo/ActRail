import { afterEach, describe, expect, it, vi } from "vitest";
import { sendRealtimeCommand, configureRealtimeClient } from "./client";

const encoder = new TextEncoder();

function payloadJson(value: unknown) {
  const bytes = encoder.encode(JSON.stringify(value));
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

describe("Connect realtime transport", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    configureRealtimeClient({ transport: "ws", url: null });
  });

  it("sends unary commands to the Connect SessionCommandService", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({
      payloadJson: payloadJson({ busy: true }),
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    configureRealtimeClient({ transport: "connect", connectBasePath: "/api/connect" });

    const response = await sendRealtimeCommand({
      type: "send",
      stream: "session:s_1",
      payload: { session_id: "s_1", text: "hello" },
    });

    expect(response).toEqual({ busy: true });
    expect(fetchMock).toHaveBeenCalledWith("api/connect/actrail.v1.SessionCommandService/Send", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ session: { sessionId: "s_1" }, text: "hello" }),
    }));
  });
});
