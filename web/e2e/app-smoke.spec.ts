import { expect, test } from "@playwright/test";

const emptySessions = {
  ok: true,
  items: [],
  sessions: [],
  remaining_count: 0,
};

const bootstrap = {
  ok: true,
  protocol_version: 1,
  capabilities: {
    ws_realtime: true,
    voice: false,
    harness: false,
    notifications: false,
    workspace_read: true,
    workspace_write: true,
    exp_connect_transport: false,
  },
  ws: {
    url: "/api/ws",
    heartbeat_interval_ms: 15000,
  },
  transport: {
    default: "ws",
    experimental: [],
    connect_path: "/api/connect",
  },
  launch_defaults: {
    default_backend: "codex",
    available_backends: ["codex", "pi"],
  },
  new_session_defaults: {
    default_backend: "codex",
    backends: {
      codex: {
        provider_choices: ["openai"],
        models: ["gpt-5.4"],
        model: "gpt-5.4",
      },
      pi: {
        provider_choices: ["openai"],
        models: ["gpt-5.4"],
        model: "gpt-5.4",
      },
    },
    backend_capabilities: {
      codex: {
        runtime_streaming: true,
        runtime_tool_trace: true,
        runtime_reasoning_trace: true,
        runtime_context_usage: true,
        runtime_interrupt: true,
        iod_unix: true,
      },
      pi: {
        runtime_streaming: true,
        runtime_tool_trace: true,
        runtime_reasoning_trace: true,
        runtime_context_usage: true,
        runtime_ui_requests: true,
        runtime_interrupt: true,
        grpc: true,
      },
    },
  },
};

test.beforeEach(async ({ page }) => {
  await page.route("**/api/connect/**", async (route) => {
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ payloadJson: JSON.stringify(emptySessions) }),
    });
  });

  await page.route("**/api/ws", async (route) => {
    await route.abort();
  });

  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path === "/api/me") {
      await route.fulfill({ json: { ok: true } });
      return;
    }
    if (path === "/api/bootstrap") {
      await route.fulfill({ json: bootstrap });
      return;
    }
    if (path === "/api/sessions") {
      await route.fulfill({ json: emptySessions });
      return;
    }
    if (path === "/api/teams") {
      await route.fulfill({ json: { ok: true, teams: [] } });
      return;
    }
    if (path === "/api/waits/inbox") {
      await route.fulfill({ json: { ok: true, waits: [] } });
      return;
    }
    if (path === "/api/notifications/feed") {
      await route.fulfill({ json: { ok: true, items: [] } });
      return;
    }
    if (path === "/api/settings/voice") {
      await route.fulfill({ json: { ok: true, settings: {} } });
      return;
    }

    await route.fulfill({ json: { ok: true } });
  });
});

test("renders the authenticated application shell", async ({ page }) => {
  await page.goto("/");

  await expect(page.getByTestId("app-shell")).toBeVisible();
  await expect(page.getByTestId("sessions-surface")).toBeVisible();
  await expect(page.getByRole("button", { name: /new session/i })).toBeVisible();
});
