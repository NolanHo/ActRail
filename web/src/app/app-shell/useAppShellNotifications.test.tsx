/** @jsxImportSource preact */
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import { useAppShellNotifications } from "./useAppShellNotifications";

vi.mock("../../lib/api", () => ({
  api: {
    getNotificationMessage: vi.fn().mockResolvedValue({ ok: true, notification_text: "" }),
    getNotificationSubscriptionState: vi.fn().mockResolvedValue({ ok: true, subscriptions: [] }),
    getNotificationsFeed: vi.fn().mockResolvedValue({ ok: true, items: [] }),
    toggleNotificationSubscription: vi.fn().mockResolvedValue({ ok: true, subscriptions: [] }),
    upsertNotificationSubscription: vi.fn().mockResolvedValue({ ok: true, subscriptions: [] }),
  },
}));

function Harness({
  activeSessionId,
  bootstrapLoaded = true,
  bySessionId,
  notificationsSupported = true,
  voiceSettings,
}: {
  activeSessionId: string | null;
  bootstrapLoaded?: boolean;
  bySessionId: Record<string, unknown[]>;
  notificationsSupported?: boolean;
  voiceSettings: any;
}) {
  const finalResponseSignatures = Object.entries(bySessionId).flatMap(([sessionId, events]) => {
    const finalResponseEvent = [...events].reverse().find((item) => {
      if (!item || typeof item !== "object") return false;
      const row = item as Record<string, unknown>;
      return row.role === "assistant" && row.pending !== true && row.message_class === "final_response";
    }) as Record<string, unknown> | undefined;
    if (!finalResponseEvent) return [];
    const notificationText = typeof finalResponseEvent.notification_text === "string"
      ? finalResponseEvent.notification_text
      : typeof finalResponseEvent.text === "string"
        ? finalResponseEvent.text
        : "";
    return [{
      key: [
        sessionId,
        typeof finalResponseEvent.message_id === "string" ? finalResponseEvent.message_id : "",
        typeof finalResponseEvent.event_id === "string" ? finalResponseEvent.event_id : typeof finalResponseEvent.seq === "number" ? finalResponseEvent.seq : "",
        typeof finalResponseEvent.ts === "number" ? finalResponseEvent.ts : "",
        notificationText,
      ].join("\u0001"),
      notificationText,
      sessionId,
    }];
  });
  const state = useAppShellNotifications({
    activeSessionId,
    activeTitle: "Legacy shell",
    bootstrapLoaded,
    finalResponseSignatures,
    notificationsSupported,
    playReplyBeep: vi.fn(),
    suppressedReplySoundSessionIdsRef: { current: new Set<string>() },
    voiceSettings,
  });

  return (
    <div>
      <div
        data-label={state.notificationLabel}
        data-push-enabled={state.pushNotificationsEnabled ? "yes" : "no"}
        data-reply-sound={state.replySoundEnabled ? "yes" : "no"}
      />
      <button type="button" onClick={() => { void state.toggleNotifications(); }}>Toggle</button>
      <button type="button" onClick={() => state.showRealtimeNotification({ title: "Background shell", body: "ready", message_id: "msg-realtime" })}>Realtime</button>
    </div>
  );
}

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

afterEach(() => {
  document.body.innerHTML = "";
  localStorage.clear();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

it("dedupes desktop notifications by message id", async () => {
  const notificationSpy = vi.fn();
  vi.stubGlobal("Notification", class NotificationMock {
    static permission = "granted";
    static requestPermission = vi.fn().mockResolvedValue("granted");

    constructor(title: string, options?: NotificationOptions) {
      notificationSpy(title, options);
    }
  } as any);
  localStorage.setItem("actrail.notificationEnabled", "1");

  const root = document.createElement("div");
  document.body.appendChild(root);

  await act(async () => {
    render(
      <Harness
        activeSessionId="sess-1"
        bySessionId={{
          "sess-1": [
            { role: "assistant", message_class: "final_response", message_id: "msg-1", notification_text: "done" },
            { role: "assistant", message_class: "final_response", message_id: "msg-1", notification_text: "done" },
          ],
        }}
        voiceSettings={{ notifications: { vapid_public_key: "" } }}
      />,
      root,
    );
    await flush();
  });

  expect(notificationSpy).toHaveBeenCalledTimes(1);
  expect(notificationSpy).toHaveBeenCalledWith("Legacy shell", expect.objectContaining({ body: "done" }));
});

it("shows realtime desktop notifications", async () => {
  const notificationSpy = vi.fn();
  vi.stubGlobal("Notification", class NotificationMock {
    static permission = "granted";
    static requestPermission = vi.fn().mockResolvedValue("granted");

    constructor(title: string, options?: NotificationOptions) {
      notificationSpy(title, options);
    }
  } as any);
  localStorage.setItem("actrail.notificationEnabled", "1");
  const root = document.createElement("div");
  document.body.appendChild(root);

  await act(async () => {
    render(
      <Harness
        activeSessionId="sess-1"
        bySessionId={{ "sess-1": [] }}
        voiceSettings={{ notifications: { vapid_public_key: "" } }}
      />,
      root,
    );
    await flush();
  });

  await act(async () => {
    (root.querySelector("button:nth-of-type(2)") as HTMLButtonElement).click();
    await flush();
  });

  expect(notificationSpy).toHaveBeenCalledWith("Background shell", expect.objectContaining({ body: "ready" }));
});

it("reads the persisted reply-sound preference", async () => {
  localStorage.setItem("actrail.replySoundEnabled", "0");
  const root = document.createElement("div");
  document.body.appendChild(root);

  await act(async () => {
    render(
      <Harness
        activeSessionId="sess-1"
        bySessionId={{ "sess-1": [] }}
        voiceSettings={{ notifications: { vapid_public_key: "ZmFrZS1rZXk" } }}
      />,
      root,
    );
    await flush();
  });

  expect(root.querySelector("[data-reply-sound]")?.getAttribute("data-reply-sound")).toBe("no");
});

it("skips notification routes when bootstrap disables notifications", async () => {
  const { api } = await import("../../lib/api");
  const root = document.createElement("div");
  document.body.appendChild(root);

  await act(async () => {
    render(
      <Harness
        activeSessionId="sess-1"
        bootstrapLoaded
        bySessionId={{ "sess-1": [] }}
        notificationsSupported={false}
        voiceSettings={{ notifications: { vapid_public_key: "ZmFrZS1rZXk" } }}
      />,
      root,
    );
    await flush();
  });

  expect(api.getNotificationSubscriptionState).not.toHaveBeenCalled();
  expect(api.getNotificationsFeed).not.toHaveBeenCalled();
  expect(root.querySelector("[data-label]")?.getAttribute("data-label")).toBe("Notifications unavailable");
});

it("ignores failed notification feed polling", async () => {
  const { api } = await import("../../lib/api");
  vi.mocked(api.getNotificationsFeed).mockRejectedValueOnce(new Error("missing feed route"));
  const root = document.createElement("div");
  document.body.appendChild(root);

  await act(async () => {
    render(
      <Harness
        activeSessionId="sess-1"
        bootstrapLoaded
        bySessionId={{ "sess-1": [] }}
        voiceSettings={{ notifications: { vapid_public_key: "" } }}
      />,
      root,
    );
    await flush();
  });

  expect(api.getNotificationsFeed).toHaveBeenCalled();
  expect(root.querySelector("[data-label]")?.getAttribute("data-label")).toBe("Notifications off");
});
