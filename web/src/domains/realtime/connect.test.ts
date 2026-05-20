import { create, toBinary } from "@bufbuild/protobuf";
import { CommandResponseSchema } from "../../gen/actrail/v1/transport_pb";
import { afterEach, describe, expect, it, vi } from "vitest";
import { connect, disconnect, sendRealtimeCommand, configureRealtimeClient } from "./client";

const encoder = new TextEncoder();

function commandResponseProto(value: unknown) {
  return toBinary(CommandResponseSchema, create(CommandResponseSchema, { payloadJson: encoder.encode(JSON.stringify(value)) }));
}

function payloadJson(value: unknown) {
  const bytes = encoder.encode(JSON.stringify(value));
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

describe("Connect realtime transport", () => {
  afterEach(() => {
    disconnect();
    vi.restoreAllMocks();
    configureRealtimeClient({ connectBasePath: "/api/connect", connectWireFormat: "json" });
  });

  it("opens the Connect EventService stream and reconnects when the base path changes", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockReturnValue(new Promise(() => undefined) as never);
    configureRealtimeClient({ connectBasePath: "/api/connect-a" });

    await connect();
    configureRealtimeClient({ connectBasePath: "/api/connect-b" });
    await Promise.resolve();

    expect(fetchMock).toHaveBeenNthCalledWith(1, "api/connect-a/actrail.v1.EventService/Subscribe", expect.objectContaining({ method: "POST" }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "api/connect-b/actrail.v1.EventService/Subscribe", expect.objectContaining({ method: "POST" }));
  });

  it("sends unary commands to the Connect SessionCommandService", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({
      payloadJson: payloadJson({ busy: true }),
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    configureRealtimeClient({ connectBasePath: "/api/connect" });

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

  it("sends protobuf unary commands when Connect proto wire format is enabled", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(commandResponseProto({ busy: true }), {
      status: 200,
      headers: { "Content-Type": "application/connect+proto" },
    }));
    configureRealtimeClient({ connectBasePath: "/api/connect", connectWireFormat: "proto" });

    const response = await sendRealtimeCommand({
      type: "send",
      stream: "session:s_1",
      payload: { session_id: "s_1", text: "hello" },
    });

    expect(response).toEqual({ busy: true });
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(fetchMock.mock.calls[0]?.[0]).toBe("api/connect/actrail.v1.SessionCommandService/Send");
    expect(init.headers).toEqual({ "Content-Type": "application/connect+proto", Accept: "application/connect+proto" });
    expect(init.body).toBeInstanceOf(Uint8Array);
  });
});
