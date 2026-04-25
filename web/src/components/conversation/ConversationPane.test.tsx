import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../app/providers";
import { ConversationPane } from "./ConversationPane";

function createStaticStore<TState extends object, TActions extends Record<string, (...args: any[]) => any>>(
  state: TState,
  actions: TActions,
) {
  return {
    getState: () => state,
    subscribe: () => () => undefined,
    ...actions,
  };
}

function createMutableStore<TState extends object, TActions extends Record<string, (...args: any[]) => any>>(
  initialState: TState,
  actions: (getState: () => TState, setState: (next: TState) => void) => TActions,
) {
  let state = initialState;
  const listeners = new Set<() => void>();

  const setState = (next: TState) => {
    state = next;
    listeners.forEach((listener) => listener());
  };

  return {
    getState: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    setState,
    ...actions(() => state, setState),
  };
}

describe("ConversationPane", () => {
  let root: HTMLDivElement | null = null;

  afterEach(() => {
    if (root) {
      render(null, root);
      root.remove();
      root = null;
    }
  });

  it("renders user, assistant, and ask_user events with card surfaces", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-1", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-1": [
            { role: "user", text: "Hello" },
            { type: "ask_user", question: "Choose a provider", answer: "openai", resolved: true },
            { type: "ask_user", question: "Need anything else?", cancelled: true, resolved: true },
            { role: "assistant", text: "All done." },
          ],
        },
        offsetsBySessionId: { "sess-1": 3 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root!,
    );

    const text = root.textContent || "";
    expect(root.querySelectorAll("[data-testid='message-surface']").length).toBeGreaterThanOrEqual(4);
    const userSurface = root.querySelector("[data-testid='message-surface'][data-kind='user']") as HTMLElement | null;
    expect(userSurface).not.toBeNull();
    expect(userSurface?.className).toContain("text-foreground");
    expect(userSurface?.className).not.toContain("text-primary-foreground");
    expect(root.querySelector("[data-testid='message-surface'][data-kind='assistant']")).not.toBeNull();
    expect(root.querySelector("[data-testid='message-surface'][data-kind='ask_user']")).not.toBeNull();
    expect(text).toContain("Hello");
    expect(text).toContain("Choose a provider");
    expect(text).toContain("Answer: openai");
    expect(text).toContain("Need anything else?");
    expect(text).toContain("Cancelled");
    expect(text).toContain("All done.");
  });

  it("renders legacy-visible activity events in the main conversation flow", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-2", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-2": [
            { role: "user", text: "Please fix this" },
            { type: "reasoning", text: "thinking...", summary: "Inspecting current state" },
            { type: "tool", name: "read" },
            { type: "tool_result", name: "read", text: "{\"ok\":true}" },
            { type: "subagent", agent: "reviewer", task: "Check the patch", output: "Looks good" },
            {
              type: "todo_snapshot",
              progress_text: "2/3 completed",
              items: [
                { title: "Explore project context", status: "completed" },
                { title: "Render message types", status: "in-progress" },
              ],
            },
            { type: "pi_model_change", summary: "Switched to gpt-5.4" },
            { role: "assistant", text: "Fixed." },
          ],
        },
        offsetsBySessionId: { "sess-2": 7 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const text = root.textContent || "";
    expect(root.querySelectorAll(".messageRow")).toHaveLength(6);
    const traceStrips = Array.from(root.querySelectorAll("[data-testid='machine-trace-strip']")) as HTMLElement[];
    expect(traceStrips).toHaveLength(2);
    expect(root.querySelectorAll(".machineTraceToken")).toHaveLength(4);
    const todoToken = root.querySelector(".machineTraceToken.todo_snapshot") as HTMLButtonElement | null;
    expect(todoToken).not.toBeNull();
    expect(root.querySelector("[data-testid='message-surface'][data-kind='subagent']")).not.toBeNull();
    expect(root.querySelector("[data-testid='message-surface'][data-kind='subagent'].subagent.messageCard")).not.toBeNull();
    expect(root.querySelector("[data-testid='message-surface'][data-kind='todo_snapshot']")).toBeNull();
    expect(root.querySelector("[data-testid='message-surface'][data-kind='pi_model_change']")).not.toBeNull();
    expect(text).toContain("Please fix this");
    expect(text).toContain("reviewer");
    expect(text).toContain("Check the patch");
    expect(text).toContain("Looks good");
    expect(text).toContain("Switched to gpt-5.4");
    expect(text).toContain("Fixed.");

    act(() => {
      todoToken?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("2/3 completed");
  });

  it("renders Claude Todo V2 task-assignment custom messages as dedicated timeline events", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-custom", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-custom": [
            {
              type: "custom_message",
              custom_type: "claude-todo-v2-task-assignment",
              text: "Task #1 assigned to @Codex",
              subject: "Clarify Claude Todo V2 compatibility goal",
              description: "Ask the user a focused requirement question.",
              owner: "Codex",
              assigned_by: "team-lead",
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-custom": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.querySelector("[data-testid='message-surface'][data-kind='custom_message']")).not.toBeNull();
    expect(root.textContent).toContain("Task #1 assigned to @Codex");
    expect(root.textContent).toContain("Clarify Claude Todo V2 compatibility goal");
    expect(root.textContent).toContain("Codex");
    expect(root.textContent).toContain("team-lead");
    expect(root.textContent).toContain("Ask the user a focused requirement question.");
  });

  it("renders unknown custom messages through a safe fallback card", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-custom-fallback", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-custom-fallback": [
            {
              type: "custom_message",
              custom_type: "claude-todo-v2-task-note",
              text: "Task note added",
              details: {
                note: "Need another sample before folding into snapshot.",
              },
              ts: 200,
            },
          ],
        },
        offsetsBySessionId: { "sess-custom-fallback": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.querySelector("[data-testid='message-surface'][data-kind='custom_message']")).not.toBeNull();
    expect(root.textContent).toContain("Task note added");
    expect(root.textContent).toContain("Need another sample before folding into snapshot.");
  });

  it("renders highlighted empty-output pi events as expandable trace icons with raw details", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-empty-assistant", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-empty-assistant": [
            {
              type: "pi_event",
              summary: "Assistant returned empty message",
              text: "Provider emitted an assistant message with no visible content.",
              details: {
                provider: "openai",
                model: "gpt-5.4",
                stopReason: "stop",
                errorMessage: "upstream returned zero visible tokens",
                usage: { totalTokens: 0 },
              },
              is_error: true,
              ts: 210,
            },
          ],
        },
        offsetsBySessionId: { "sess-empty-assistant": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.querySelector("[data-testid='message-surface'][data-kind='pi_event']")).toBeNull();
    const token = root.querySelector(".machineTraceToken.pi_event.isAlert[data-variant='empty_output']") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Assistant returned empty message");
    expect(root.textContent).toContain("Provider emitted an assistant message with no visible content.");
    expect(root.textContent).toContain("gpt-5.4");
    expect(root.textContent).toContain("errorMessage");
    expect(root.textContent).toContain("upstream returned zero visible tokens");
  });

  it("renders turn-terminal pi events with a dedicated trace variant", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-turn-terminal", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-turn-terminal": [
            { role: "assistant", text: "before", ts: 100 },
            {
              type: "pi_event",
              summary: "Turn finished without assistant output",
              text: "Pi ended the turn after tool or reasoning activity without a final assistant message.",
              details: {
                source_event: "turn.completed",
              },
              is_error: true,
              ts: 110,
            },
            { role: "assistant", text: "after", ts: 120 },
          ],
        },
        offsetsBySessionId: { "sess-turn-terminal": 3 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.pi_event.isTurnTerminal.isAlert[data-variant='turn_terminal']") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Turn finished without assistant output");
    expect(root.textContent).toContain("without a final assistant message");
    expect(root.textContent).toContain("source_event");
    expect(root.textContent).toContain("turn.completed");
  });

  it("renders retry pi events as highlighted alert trace icons with expandable detail", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-retry", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-retry": [
            { role: "assistant", text: "before", ts: 100 },
            {
              type: "pi_event",
              summary: "Retrying request (1/3)",
              text: '429 "Rate limit exceeded"',
              details: {
                errorMessage: '429 "Rate limit exceeded"',
                attempt: 1,
                maxAttempts: 3,
              },
              is_error: true,
              ts: 110,
            },
            { role: "assistant", text: "after", ts: 120 },
          ],
        },
        offsetsBySessionId: { "sess-retry": 3 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.pi_event.isAlert[data-variant='retry_error']") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Retrying request (1/3)");
    expect(root.textContent).toContain('429 "Rate limit exceeded"');
    expect(root.textContent).toContain("maxAttempts");
  });

  it("renders compaction pi events as highlighted trace icons with expandable detail", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-compaction", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-compaction": [
            { role: "assistant", text: "before", ts: 100 },
            {
              type: "pi_event",
              summary: "Compaction finished",
              text: "Retrying with compacted context.",
              details: {
                reason: "threshold",
                summary: "Compaction finished successfully",
              },
              ts: 110,
            },
            { role: "assistant", text: "after", ts: 120 },
          ],
        },
        offsetsBySessionId: { "sess-compaction": 3 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.pi_event.isCompaction[data-variant='compaction']") as HTMLButtonElement | null;
    expect(token).not.toBeNull();
    expect(root.textContent).toContain("before");
    expect(root.textContent).toContain("after");

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Compaction finished");
    expect(root.textContent).toContain("Retrying with compacted context.");
    expect(root.textContent).toContain("threshold");
  });

  it("shows bash tool call command as code block instead of json args", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-tool-args", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-tool-args": [
            { role: "assistant", text: "before", ts: 100 },
            {
              type: "tool",
              name: "bash",
              details: {
                arguments: {
                  command: "ls -la",
                  timeout: 10,
                },
              },
              ts: 110,
            },
            { role: "assistant", text: "after", ts: 120 },
          ],
        },
        offsetsBySessionId: { "sess-tool-args": 3 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    const code = root.querySelector(".machineTraceDetail.tool .messageCardPre code");
    expect(code?.textContent).toContain("ls -la");
    expect(root.textContent).toContain("Timeout");
    expect(root.textContent).not.toContain('"command"');
  });

  it("renders grep tool call with structured fields instead of raw json", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-grep-args", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-grep-args": [
            {
              type: "tool",
              name: "grep",
              details: {
                arguments: {
                  pattern: "manage_todo_list",
                  path: "/tmp/repo",
                  limit: 20,
                },
              },
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-grep-args": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Path");
    expect(root.textContent).toContain("/tmp/repo");
    expect(root.textContent).toContain("Limit");
    expect(root.textContent).toContain("20");
    expect(root.textContent).toContain("manage_todo_list");
    expect(root.textContent).not.toContain('"pattern"');
  });

  it("renders edit tool call with structured fields instead of raw json", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-edit-args", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-edit-args": [
            {
              type: "tool",
              name: "edit",
              details: {
                arguments: {
                  path: "/tmp/repo/file.ts",
                  edits: [{ oldText: "a", newText: "b" }, { oldText: "c", newText: "d" }],
                },
              },
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-edit-args": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Mode");
    expect(root.textContent).toContain("multi");
    expect(root.textContent).toContain("Blocks");
    expect(root.textContent).toContain("2");
    expect(root.textContent).toContain("/tmp/repo/file.ts");
    expect(root.textContent).not.toContain('"edits"');
  });

  it("renders bash tool output as raw code block without markdown list parsing", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-bash-raw", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-bash-raw": [
            {
              type: "tool_result",
              name: "bash",
              text: "- [x] item\\n<raw>&value",
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-bash-raw": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool_result") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    const code = root.querySelector(".machineTraceDetail.tool_result .messageCardPre code");
    expect(code?.textContent).toContain("- [x] item");
    expect(code?.textContent).toContain("<raw>&value");
    expect(root.querySelector(".machineTraceDetail.tool_result .messageBody ul")).toBeNull();
  });

  it("renders process tool_result events with dedicated process styling and fields", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-process-result", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-process-result": [
            {
              type: "tool_result",
              name: "process",
              text: "Started \"demo\" (proc_1, PID: 1234)",
              details: {
                action: "start",
                success: true,
                process: {
                  id: "proc_1",
                  name: "demo",
                  status: "running",
                },
              },
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-process-result": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool_result.isProcessTool") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    const detail = root.querySelector(".machineTraceDetail.tool_result.isProcessTool");
    expect(detail).not.toBeNull();
    expect(root.textContent).toContain("Action");
    expect(root.textContent).toContain("start");
    expect(root.textContent).toContain("Process ID");
    expect(root.textContent).toContain("proc_1");
  });

  it("renders write tool_result as structured path and bytes fields", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-write-tool", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-write-tool": [
            {
              type: "tool_result",
              name: "write",
              text: "Successfully wrote 20824 bytes to scripts/tools/bench_issue432_mixed_queue.py",
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-write-tool": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool_result") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Bytes");
    expect(root.textContent).toContain("20824");
    expect(root.textContent).toContain("scripts/tools/bench_issue432_mixed_queue.py");
  });

  it("renders context_tag tool_result as structured tag metadata", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-context-tag", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-context-tag": [
            {
              type: "tool_result",
              name: "context_tag",
              text: "Created tag 'issue141-actor-inventory-start' at 09582f3d",
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-context-tag": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool_result") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Tag");
    expect(root.textContent).toContain("issue141-actor-inventory-start");
    expect(root.textContent).toContain("Target");
    expect(root.textContent).toContain("09582f3d");
  });

  it("renders context_log dashboard as structured usage card", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-context-log", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-context-log": [
            {
              type: "tool_result",
              name: "context_log",
              text: "[Context Dashboard]\\n• Context Usage:    8.7% (87k/1.0M)\\n• Segment Size:     1542 steps since last tag 'None'",
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-context-log": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool_result") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Context Usage");
    expect(root.textContent).toContain("8.7% (87k/1.0M)");
    expect(root.textContent).toContain("Segment Size");
    expect(root.textContent).toContain("1542 steps since last tag 'None'");
  });

  it("renders context_checkout start as structured status card", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-context-checkout", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-context-checkout": [
            {
              type: "tool_result",
              name: "context_checkout",
              text: "checkout start",
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-context-checkout": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool_result") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Checkout");
    expect(root.textContent).toContain("start");
    expect(root.textContent).toContain("Checkout procedure started");
  });

  it("renders read tool_result as raw code block output", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-read-raw", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-read-raw": [
            {
              type: "tool_result",
              name: "read",
              text: "- [x] line-one\\n<raw>&token",
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-read-raw": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool_result") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    const code = root.querySelector(".machineTraceDetail.tool_result .messageCardPre code");
    expect(code?.textContent).toContain("- [x] line-one");
    expect(code?.textContent).toContain("<raw>&token");
  });

  it("renders manage_todo_list tool_result details as todo cards", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-manage-todo", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-manage-todo": [
            {
              type: "tool_result",
              name: "manage_todo_list",
              text: "Todos updated",
              details: {
                operation: "write",
                todos: [
                  { id: 1, title: "alpha", status: "completed" },
                  { id: 2, title: "beta", status: "in_progress" },
                ],
              },
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-manage-todo": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool_result") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.querySelectorAll(".machineTraceDetail.tool_result .messageTodoItem")).toHaveLength(2);
    expect(root.textContent).toContain("alpha");
    expect(root.textContent).toContain("beta");
    expect(root.textContent).not.toContain('"todos"');
  });

  it("renders manage_todo_list todo cards when todos are embedded in text json", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-manage-todo-text", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-manage-todo-text": [
            {
              type: "tool_result",
              name: "manage_todo_list",
              text: '{"operation":"write","todos":[{"id":1,"title":"gamma","status":"not-started"},{"id":2,"title":"delta","status":"completed"}]}',
              ts: 100,
            },
          ],
        },
        offsetsBySessionId: { "sess-manage-todo-text": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.tool_result") as HTMLButtonElement | null;
    expect(token).not.toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.querySelectorAll(".machineTraceDetail.tool_result .messageTodoItem")).toHaveLength(2);
    expect(root.textContent).toContain("gamma");
    expect(root.textContent).toContain("delta");
    expect(root.textContent).not.toContain('"todos"');
  });

  it("renders ad-process custom messages as compact process icons", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-process-custom", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-process-custom": [
            { role: "assistant", text: "before", ts: 100 },
            {
              type: "custom_message",
              custom_type: "ad-process:update",
              text: "Process 'issue512-manual-rayclient-server' was terminated",
              details: {
                processName: "issue512-manual-rayclient-server",
                status: "exited",
                exitCode: 1,
                runtime: "12s",
              },
              ts: 110,
            },
            { role: "assistant", text: "after", ts: 120 },
          ],
        },
        offsetsBySessionId: { "sess-process-custom": 3 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const token = root.querySelector(".machineTraceToken.custom_message") as HTMLButtonElement | null;
    expect(token).not.toBeNull();
    expect(root.querySelector("[data-testid='message-surface'][data-kind='custom_message']")).toBeNull();

    act(() => {
      token?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });

    expect(root.textContent).toContain("Process 'issue512-manual-rayclient-server' was terminated");
    expect(root.textContent).toContain("ad-process:update");
    expect(root.textContent).toContain("Status");
    expect(root.textContent).toContain("exited");
    expect(root.textContent).toContain("Exit Code");
  });

  it("groups consecutive assistant messages and avoids showing role labels as body text", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-3", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-3": [
            { role: "assistant", text: "First answer" },
            { role: "assistant", text: "Second answer" },
            { role: "user", text: "Thanks" },
          ],
        },
        offsetsBySessionId: { "sess-3": 3 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.querySelectorAll(".messageRow")).toHaveLength(3);
    expect(root.querySelectorAll(".messageRow.grouped")).toHaveLength(1);
    expect(root.textContent).not.toContain("assistantassistant");
  });

  it("renders markdown code blocks and inline code inside assistant messages", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-4", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-4": [
            {
              role: "assistant",
              text: "Run `npm test` before `<div>` output.\n\n```ts\nconst answer = 42;\n```",
            },
          ],
        },
        offsetsBySessionId: { "sess-4": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const inlineCode = Array.from(root.querySelectorAll(".messageBody p code")).map((node) => node.textContent || "");
    const blockCode = root.querySelector(".messageBody pre code");
    expect(inlineCode).toContain("npm test");
    expect(inlineCode).toContain("<div>");
    expect(blockCode?.textContent).toContain("const answer = 42;");
  });

  it("renders markdown unordered and ordered lists with explicit list styling", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-4-list", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-4-list": [
            {
              role: "assistant",
              text: "- first\n- second\n\n1. alpha\n2. beta",
            },
          ],
        },
        offsetsBySessionId: { "sess-4-list": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const unordered = root.querySelector(".messageBody ul") as HTMLUListElement | null;
    const ordered = root.querySelector(".messageBody ol") as HTMLOListElement | null;
    expect(unordered).not.toBeNull();
    expect(ordered).not.toBeNull();
    expect(unordered?.className).toContain("list-disc");
    expect(unordered?.textContent).toContain("first");
    expect(unordered?.textContent).toContain("second");
    expect(ordered?.className).toContain("list-decimal");
    expect(ordered?.textContent).toContain("alpha");
    expect(ordered?.textContent).toContain("beta");
  });

  it("copies plain chat bubble text from the copy button", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(globalThis.navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-copy", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-copy": [
            { role: "assistant", text: "Copy me please" },
          ],
        },
        offsetsBySessionId: { "sess-copy": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const copyButton = root.querySelector("button[aria-label='Copy assistant message']") as HTMLButtonElement | null;
    expect(copyButton).not.toBeNull();

    await act(async () => {
      copyButton?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });

    expect(writeText).toHaveBeenCalledWith("Copy me please");
    expect(copyButton?.textContent?.trim()).toBe("");
    expect(copyButton?.className).toContain("isCopied");
  });

  it("only binds the newest duplicate ask_user card to a matching live request", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-dup", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-dup": [
            {
              type: "ask_user",
              tool_call_id: "historic-old",
              question: "Choose a provider",
              context: "Pick one option.",
              options: ["OpenAI", "Anthropic"],
              allow_freeform: false,
              allow_multiple: false,
              resolved: false,
              ts: 100,
            },
            {
              type: "ask_user",
              tool_call_id: "historic-new",
              question: "Choose a provider",
              context: "Pick one option.",
              options: ["OpenAI", "Anthropic"],
              allow_freeform: false,
              allow_multiple: false,
              resolved: false,
              ts: 200,
            },
          ],
        },
        offsetsBySessionId: { "sess-dup": 2 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );
    const sessionUiStore = createStaticStore(
      {
        sessionId: "sess-dup",
        diagnostics: null,
        queue: null,
        files: [],
        loading: false,
        requests: [
          {
            id: "ui-live-1",
            method: "select",
            question: "Choose a provider",
            context: "Pick one option.",
            options: ["OpenAI", "Anthropic"],
            allow_freeform: false,
            allow_multiple: false,
          },
        ],
      },
      { refresh: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any} sessionUiStore={sessionUiStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const openAiButtons = Array.from(root.querySelectorAll("button")).filter((button) => button.textContent?.includes("OpenAI")) as HTMLButtonElement[];
    expect(openAiButtons).toHaveLength(2);
    expect(openAiButtons[0].disabled).toBe(true);
    expect(openAiButtons[1].disabled).toBe(false);
  });

  it("enables unresolved historical ask_user cards for legacy pi sessions without live ui transport", () => {
    const sessionsStore = createStaticStore(
      {
        items: [{ session_id: "sess-legacy", alias: "Legacy Pi", agent_backend: "pi", transport: null }],
        activeSessionId: "sess-legacy",
        loading: false,
        newSessionDefaults: null,
      },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-legacy": [
            {
              type: "ask_user",
              tool_call_id: "legacy-ask-1",
              question: "Choose a provider",
              context: "This prompt should still be answerable.",
              options: ["OpenAI", "Anthropic"],
              allow_freeform: true,
              allow_multiple: false,
              resolved: false,
            },
          ],
        },
        offsetsBySessionId: { "sess-legacy": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );
    const sessionUiStore = createStaticStore(
      {
        sessionId: "sess-legacy",
        diagnostics: null,
        queue: null,
        files: [],
        loading: false,
        requests: [],
      },
      { refresh: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any} sessionUiStore={sessionUiStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const optionButton = Array.from(root.querySelectorAll("button")).find((button) => button.textContent?.includes("OpenAI")) as
      | HTMLButtonElement
      | undefined;
    expect(optionButton).toBeDefined();
    expect(optionButton?.disabled).toBe(false);
  });

  it("resets expandable message state when switching sessions", async () => {
    const longReasoningA = Array.from({ length: 10 }, (_value, index) => `session A reasoning ${index + 1}`).join("\n");
    const longReasoningB = Array.from({ length: 10 }, (_value, index) => `session B reasoning ${index + 1}`).join("\n");
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-expand-a": [{ type: "reasoning", text: longReasoningA }],
          "sess-expand-b": [{ type: "reasoning", text: longReasoningB }],
        },
        offsetsBySessionId: { "sess-expand-a": 1, "sess-expand-b": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders
        sessionsStore={createStaticStore(
          { items: [], activeSessionId: "sess-expand-a", loading: false, newSessionDefaults: null },
          { refresh: () => Promise.resolve(), select: () => undefined },
        ) as any}
        messagesStore={messagesStore as any}
      >
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const firstToken = root.querySelector(".machineTraceToken.reasoning") as HTMLButtonElement | null;

    expect(root.querySelector("[data-testid='machine-trace-detail']")).toBeNull();
    await act(async () => {
      firstToken?.click();
      await Promise.resolve();
    });
    expect(root.querySelector("[data-testid='machine-trace-detail'] .messageExpandableContent")).not.toBeNull();

    await act(async () => {
      render(
        <AppProviders
          sessionsStore={createStaticStore(
            { items: [], activeSessionId: "sess-expand-b", loading: false, newSessionDefaults: null },
            { refresh: () => Promise.resolve(), select: () => undefined },
          ) as any}
          messagesStore={messagesStore as any}
        >
          <ConversationPane />
        </AppProviders>,
        root!,
      );
      await Promise.resolve();
    });

    expect(root.querySelector("[data-testid='machine-trace-detail']")).toBeNull();
    expect(root.textContent).not.toContain("session A reasoning 1");
  });

  it("expands compact machine-trace details and keeps long reasoning collapsible", async () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-6", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const longReasoning = Array.from({ length: 10 }, (_value, index) => `reasoning line ${index + 1}`).join("\n");
    const longToolResult = Array.from({ length: 12 }, (_value, index) => `result line ${index + 1}`).join("\n");
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-6": [
            { type: "reasoning", text: longReasoning },
            { type: "tool_result", name: "bash", text: longToolResult },
          ],
        },
        offsetsBySessionId: { "sess-6": 2 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const traceTokens = Array.from(root.querySelectorAll(".machineTraceToken")) as HTMLButtonElement[];
    const reasoningToken = traceTokens.find((node) => node.dataset.kind === "reasoning") || null;
    const toolResultToken = traceTokens.find((node) => node.dataset.kind === "tool_result") || null;

    expect(traceTokens).toHaveLength(2);
    expect(root.querySelector("[data-testid='machine-trace-detail']")).toBeNull();

    await act(async () => {
      reasoningToken?.click();
      await Promise.resolve();
    });

    const reasoningButton = root.querySelector("[data-testid='machine-trace-detail'] .messageExpandButton") as HTMLButtonElement | null;
    const reasoningContent = root.querySelector("[data-testid='machine-trace-detail'] .messageExpandableContent") as HTMLDivElement | null;
    expect(reasoningButton?.textContent).toBe("Show more");
    expect(reasoningButton?.getAttribute("aria-expanded")).toBe("false");
    expect(reasoningContent?.classList.contains("isCollapsed")).toBe(true);

    await act(async () => {
      reasoningButton?.click();
      await Promise.resolve();
    });

    expect(reasoningButton?.textContent).toBe("Show less");
    expect(reasoningButton?.getAttribute("aria-expanded")).toBe("true");
    expect(reasoningContent?.classList.contains("isCollapsed")).toBe(false);

    await act(async () => {
      toolResultToken?.click();
      await Promise.resolve();
    });

    expect(root.querySelector("[data-testid='machine-trace-detail']")?.textContent).toContain("result line 1");
  });

  it("shows compact machine-trace tokens and switches detail selection", async () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-tool-compact", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-tool-compact": [
            { type: "tool", name: "read", text: "web/src/components/conversation/ConversationPane.tsx" },
            { type: "tool_result", name: "read", text: '{"ok":true,"path":"ConversationPane.tsx"}' },
          ],
        },
        offsetsBySessionId: { "sess-tool-compact": 2 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const traceStrip = root.querySelector("[data-testid='machine-trace-strip']") as HTMLElement | null;
    const tokens = Array.from(root.querySelectorAll(".machineTraceToken")) as HTMLButtonElement[];
    const toolToken = tokens.find((node) => node.dataset.kind === "tool") || null;
    const toolResultToken = tokens.find((node) => node.dataset.kind === "tool_result") || null;

    expect(traceStrip).not.toBeNull();
    expect(tokens).toHaveLength(2);
    expect(toolToken?.getAttribute("aria-expanded")).toBe("false");
    expect(toolResultToken?.getAttribute("aria-expanded")).toBe("false");
    expect(root.querySelector("[data-testid='machine-trace-detail']")).toBeNull();

    await act(async () => {
      toolToken?.click();
      await Promise.resolve();
    });

    expect(root.querySelector("[data-testid='machine-trace-detail']")?.textContent).toContain("web/src/components/conversation/ConversationPane.tsx");
    expect(toolToken?.getAttribute("aria-expanded")).toBe("true");
    expect(toolResultToken?.getAttribute("aria-expanded")).toBe("false");

    await act(async () => {
      toolResultToken?.click();
      await Promise.resolve();
    });

    expect(root.querySelector("[data-testid='machine-trace-detail']")?.textContent).toContain('{"ok":true,"path":"ConversationPane.tsx"}');
    expect(toolToken?.getAttribute("aria-expanded")).toBe("false");
    expect(toolResultToken?.getAttribute("aria-expanded")).toBe("true");
  });

  it("orders machine-trace tokens by parsed timestamp with fallback for missing ts", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-trace-order", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-trace-order": [
            { type: "tool_result", name: "bash", text: "late", ts: 1761177605 },
            { type: "pi_event", summary: "Compaction finished", text: "done", timestamp: "2025-10-23T00:00:04Z" },
            { type: "tool", name: "read", text: "early", ts: 1761177603 },
            { type: "reasoning", text: "no timestamp available" },
          ],
        },
        offsetsBySessionId: { "sess-trace-order": 4 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const kinds = Array.from(root.querySelectorAll<HTMLButtonElement>(".machineTraceToken"))
      .map((node) => String(node.dataset.kind || ""));

    expect(kinds).toEqual(["tool", "pi_event", "tool_result", "reasoning"]);
  });

  it("orders conversation rows by parsed event timestamp before grouping", () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-row-order", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-row-order": [
            { role: "assistant", text: "late assistant", ts: 1761177605 },
            { type: "pi_event", summary: "Turn finished without assistant output", text: "no assistant output", ts: 1761177604 },
            { role: "assistant", text: "early assistant", ts: 1761177603 },
          ],
        },
        offsetsBySessionId: { "sess-row-order": 3 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const rowKinds = Array.from(root.querySelectorAll<HTMLElement>(".messageRow")).map((node) => {
      if (node.classList.contains("machine_trace")) return "machine_trace";
      if (node.classList.contains("assistant")) return "assistant";
      return "other";
    });

    expect(rowKinds).toEqual(["assistant", "machine_trace", "assistant"]);
    expect(root.textContent).toContain("early assistant");
    expect(root.textContent).toContain("late assistant");
  });

  it("marks the latest unfinished tool token as running while the session is busy", () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-trace-running", busy: true }], activeSessionId: "sess-trace-running", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const liveSessionStore = createStaticStore(
      { offsetsBySessionId: {}, liveOffsetsBySessionId: {}, requestsBySessionId: {}, requestVersionsBySessionId: {}, busyBySessionId: { "sess-trace-running": true }, loadingBySessionId: {} },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-trace-running": [
            { type: "tool", name: "read", text: "package.json" },
            { type: "tool_result", name: "read", text: '{"ok":true}' },
            { type: "reasoning", text: "next step" },
            { type: "tool", name: "bash", text: "npm test" },
          ],
        },
        offsetsBySessionId: { "sess-trace-running": 4 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} liveSessionStore={liveSessionStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const runningTokens = Array.from(root.querySelectorAll(".machineTraceToken.isRunning")) as HTMLButtonElement[];
    expect(runningTokens).toHaveLength(1);
    expect(runningTokens[0]?.dataset.kind).toBe("tool");
  });

  it("scrolls the conversation pane to the bottom on initial render when messages exist", async () => {
    const sessionsStore = createStaticStore(
      { items: [], activeSessionId: "sess-7", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-7": [
            { role: "assistant", text: "One" },
            { role: "assistant", text: "Two" },
            { role: "assistant", text: "Three" },
          ],
        },
        offsetsBySessionId: { "sess-7": 3 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    const scrollTo = vi.fn();
    const originalScrollTo = HTMLElement.prototype.scrollTo;
    HTMLElement.prototype.scrollTo = scrollTo;
    const scrollHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
    const clientHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
    Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
      configurable: true,
      get() {
        return this.classList?.contains("conversationPane") ? 500 : (scrollHeightDescriptor?.get?.call(this) ?? 0);
      },
    });
    Object.defineProperty(HTMLElement.prototype, "clientHeight", {
      configurable: true,
      get() {
        return this.classList?.contains("conversationPane") ? 200 : (clientHeightDescriptor?.get?.call(this) ?? 0);
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    await Promise.resolve();
    await Promise.resolve();

    expect(scrollTo).toHaveBeenCalledWith({ top: 500 });
    HTMLElement.prototype.scrollTo = originalScrollTo;

    if (scrollHeightDescriptor) {
      Object.defineProperty(HTMLElement.prototype, "scrollHeight", scrollHeightDescriptor);
    }
    if (clientHeightDescriptor) {
      Object.defineProperty(HTMLElement.prototype, "clientHeight", clientHeightDescriptor);
    }
  });

  it("renders legacy timestamps plus markdown tables, local file refs, and local images", () => {
    const sessionsStore = createStaticStore(
      {
        items: [{ session_id: "sess-8", cwd: "/repo/docs", agent_backend: "pi" }],
        activeSessionId: "sess-8",
        loading: false,
        newSessionDefaults: null,
      },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-8": [
            {
              role: "assistant",
              ts: 1_744_000_000,
              text: "[server.py](/repo/codoxear/server.py#L123)\n\n| Name | Urgency |\n|---|---:|\n| offline | 2 |\n\n![diagram](./flow.png)",
            },
            { role: "assistant", ts: 1_650_000, text: "synthetic timestamp should stay hidden" },
          ],
        },
        offsetsBySessionId: { "sess-8": 2 },
        hasOlderBySessionId: {},
        olderBeforeBySessionId: {},
        loadingOlderBySessionId: {},
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const timestamps = root.querySelectorAll("time.messageTimestamp");
    expect(timestamps).toHaveLength(1);
    expect(timestamps[0]?.getAttribute("datetime")).toBe(new Date(1_744_000_000 * 1000).toISOString());

    const fileLink = root.querySelector(".messageBody a[data-file-path='/repo/codoxear/server.py']") as HTMLAnchorElement | null;
    expect(fileLink?.textContent).toBe("server.py#L123");
    expect(fileLink?.getAttribute("href")).toContain("api/sessions/sess-8/file/blob?path=%2Frepo%2Fcodoxear%2Fserver.py");

    const table = root.querySelector(".messageBody .mdTableWrap table");
    expect(table).not.toBeNull();
    expect(table?.textContent).toContain("Name");
    expect(table?.textContent).toContain("offline");

    const image = root.querySelector(".messageBody img") as HTMLImageElement | null;
    expect(image?.getAttribute("src")).toContain("api/sessions/sess-8/file/blob?path=%2Frepo%2Fdocs%2Fflow.png");
    expect(image?.getAttribute("alt")).toBe("diagram");
  });

  it("rewrites memory citation blocks into clickable memory links and can open local file refs", () => {
    const sessionsStore = createStaticStore(
      {
        items: [{ session_id: "sess-citation", cwd: "/repo", agent_backend: "pi" }],
        activeSessionId: "sess-citation",
        loading: false,
        newSessionDefaults: null,
      },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-citation": [
            {
              role: "assistant",
              text: "<oai-mem-citation>\n<citation_entries>\nMEMORY.md:4773-4779|note=[used corrected guidance]\n</citation_entries>\n<rollout_ids>\n</rollout_ids>\n</oai-mem-citation>",
            },
          ],
        },
        offsetsBySessionId: { "sess-citation": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.textContent).toContain("Memory citations");
    const link = root.querySelector("a[data-file-path]") as HTMLAnchorElement | null;
    expect(link).not.toBeNull();
    expect(link?.getAttribute("data-file-path")).toBe("~/.codex/memories/MEMORY.md");
    expect(link?.getAttribute("data-file-line")).toBe("4773");
  });

  it("shows history controls and wires load-older plus jump-to-latest actions", async () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-9", agent_backend: "pi" }], activeSessionId: "sess-9", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const loadInitial = vi.fn().mockResolvedValue(undefined);
    const liveLoadInitial = vi.fn().mockResolvedValue(undefined);
    const loadOlder = vi.fn().mockResolvedValue(undefined);
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-9": [{ role: "assistant", text: "Newest reply" }],
        },
        offsetsBySessionId: { "sess-9": 1 },
        hasOlderBySessionId: { "sess-9": true },
        olderBeforeBySessionId: { "sess-9": 5 },
        loadingOlderBySessionId: { "sess-9": false },
        loading: false,
      },
      { loadInitial, poll: () => Promise.resolve(), loadOlder },
    );
    const liveSessionStore = createStaticStore(
      { offsetsBySessionId: {}, requestsBySessionId: {}, requestVersionsBySessionId: {}, busyBySessionId: {}, loadingBySessionId: {} },
      { loadInitial: liveLoadInitial, poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders liveSessionStore={liveSessionStore as any} sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const loadOlderButton = Array.from(root.querySelectorAll("button")).find((button) => button.textContent === "Load older") as HTMLButtonElement | undefined;
    const jumpButton = Array.from(root.querySelectorAll("button")).find((button) => button.textContent === "Jump to latest") as HTMLButtonElement | undefined;

    expect(loadOlderButton).toBeDefined();
    expect(jumpButton).toBeDefined();

    const initialLegacyLoadCalls = loadInitial.mock.calls.length;

    loadOlderButton?.click();
    jumpButton?.click();
    await Promise.resolve();

    expect(loadOlder).toHaveBeenCalledWith("sess-9");
    expect(loadInitial).toHaveBeenCalledTimes(initialLegacyLoadCalls);
    expect(liveLoadInitial).toHaveBeenCalledWith("sess-9");
  });

  it("renders one assistant bubble for a streamed pi reply", () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-stream", agent_backend: "pi" }], activeSessionId: "sess-stream", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-stream": [
            { role: "assistant", text: "hello", streaming: true, stream_id: "pi-stream:turn-001", turn_id: "turn-001" },
          ],
        },
        offsetsBySessionId: { "sess-stream": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const assistantRows = Array.from(root.querySelectorAll(".messageRow")).filter((row) => row.textContent?.includes("hello"));
    expect(assistantRows).toHaveLength(1);
  });

  it("keeps the working indicator visible when live session state is still busy", () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-live-busy", agent_backend: "pi", busy: false }], activeSessionId: "sess-live-busy", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-live-busy": [{ role: "assistant", text: "Still thinking" }],
        },
        offsetsBySessionId: { "sess-live-busy": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );
    const liveSessionStore = createStaticStore(
      {
        offsetsBySessionId: { "sess-live-busy": 1 },
        liveOffsetsBySessionId: { "sess-live-busy": 1 },
        requestsBySessionId: {},
        requestVersionsBySessionId: {},
        busyBySessionId: { "sess-live-busy": true },
        loadingBySessionId: {},
        errorBySessionId: {},
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders
        sessionsStore={sessionsStore as any}
        messagesStore={messagesStore as any}
        liveSessionStore={liveSessionStore as any}
      >
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.textContent).toContain("Working");
  });

  it("shows a visible warning when live pi-rpc polling fails", () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-rpc-error", agent_backend: "pi", busy: false }], activeSessionId: "sess-rpc-error", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-rpc-error": [{ role: "assistant", text: "Last visible reply" }],
        },
        offsetsBySessionId: { "sess-rpc-error": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );
    const liveSessionStore = createStaticStore(
      {
        offsetsBySessionId: { "sess-rpc-error": 1 },
        liveOffsetsBySessionId: { "sess-rpc-error": 1 },
        requestsBySessionId: {},
        requestVersionsBySessionId: {},
        busyBySessionId: {},
        loadingBySessionId: {},
        errorBySessionId: { "sess-rpc-error": "broker unavailable" },
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders
        sessionsStore={sessionsStore as any}
        messagesStore={messagesStore as any}
        liveSessionStore={liveSessionStore as any}
      >
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.textContent).toContain("Pi RPC warning");
    expect(root.textContent).toContain("broker unavailable");
  });

  it("shows a floating previous-user button only after scrolling above an earlier user message", async () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-jump", agent_backend: "pi" }], activeSessionId: "sess-jump", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-jump": [
            { role: "user", text: "First question" },
            { role: "assistant", text: "First answer" },
            { role: "user", text: "Second question" },
            { role: "assistant", text: "Second answer" },
            { role: "assistant", text: "Newest answer" },
          ],
        },
        offsetsBySessionId: { "sess-jump": 5 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const pane = root.querySelector(".conversationPane") as HTMLDivElement | null;
    const rows = Array.from(root.querySelectorAll<HTMLElement>(".messageRow"));

    expect(pane).not.toBeNull();
    expect(root.querySelector("[data-testid='jump-to-previous-user']")).toBeNull();

    rows.forEach((row, index) => {
      Object.defineProperty(row, "offsetTop", { configurable: true, value: index * 180 });
    });
    Object.defineProperty(pane!, "clientHeight", { configurable: true, value: 360 });
    Object.defineProperty(pane!, "scrollTop", { configurable: true, writable: true, value: 190 });

    await act(async () => {
      pane?.dispatchEvent(new Event("scroll"));
      await Promise.resolve();
      await Promise.resolve();
    });

    const jumpButton = root.querySelector("[data-testid='jump-to-previous-user']") as HTMLButtonElement | null;
    expect(jumpButton).not.toBeNull();
    expect(jumpButton?.textContent).toBe("");
    expect(jumpButton?.getAttribute("aria-label")).toBe("Jump to previous user message");
  });

  it("jumps to the nearest earlier rendered user message and can step backward again", async () => {
    const sessionsStore = createStaticStore(
      {
        items: [{ session_id: "sess-jump-target", agent_backend: "pi" }],
        activeSessionId: "sess-jump-target",
        loading: false,
        newSessionDefaults: null,
      },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const loadOlder = vi.fn().mockResolvedValue(undefined);
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-jump-target": [
            { role: "user", text: "Question 1" },
            { role: "assistant", text: "Answer 1" },
            { role: "user", text: "Question 2" },
            { role: "assistant", text: "Answer 2" },
            { role: "assistant", text: "Answer 3" },
          ],
        },
        offsetsBySessionId: { "sess-jump-target": 5 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder },
    );

    const scrollTo = vi.fn();

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const pane = root.querySelector(".conversationPane") as HTMLDivElement | null;
    Object.defineProperty(pane!, "scrollTo", { configurable: true, value: scrollTo });
    const rows = Array.from(root.querySelectorAll<HTMLElement>(".messageRow"));
    rows.forEach((row, index) => {
      Object.defineProperty(row, "offsetTop", { configurable: true, value: index * 240 });
    });
    Object.defineProperty(pane!, "clientHeight", { configurable: true, value: 360 });
    Object.defineProperty(pane!, "scrollTop", { configurable: true, writable: true, value: 700 });

    await act(async () => {
      pane?.dispatchEvent(new Event("scroll"));
      await Promise.resolve();
      await Promise.resolve();
    });

    const jumpButton = root.querySelector("[data-testid='jump-to-previous-user']") as HTMLButtonElement | null;
    expect(jumpButton).not.toBeNull();
    await act(async () => {
      jumpButton?.click();
      await Promise.resolve();
    });

    expect(scrollTo).toHaveBeenCalledWith({ top: 464, behavior: "smooth" });
    expect(loadOlder).not.toHaveBeenCalled();

    pane!.scrollTop = 464;
    await act(async () => {
      pane?.dispatchEvent(new Event("scroll"));
      await Promise.resolve();
      await Promise.resolve();
    });

    const secondJumpButton = root.querySelector("[data-testid='jump-to-previous-user']") as HTMLButtonElement | null;
    expect(secondJumpButton).not.toBeNull();
    await act(async () => {
      secondJumpButton?.click();
      await Promise.resolve();
    });

    expect(scrollTo).toHaveBeenCalledWith({ top: 0, behavior: "smooth" });
  });

  it("clears the previous-user jump button when switching to a session without earlier user rows", async () => {
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-a": [
            { role: "user", text: "Question A" },
            { role: "assistant", text: "Answer A" },
            { role: "user", text: "Question A2" },
            { role: "assistant", text: "Answer A2" },
            { role: "assistant", text: "Newest answer" },
          ],
          "sess-b": [{ role: "assistant", text: "Only answer" }],
        },
        offsetsBySessionId: { "sess-a": 5, "sess-b": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders
        sessionsStore={createStaticStore(
          {
            items: [
              { session_id: "sess-a", agent_backend: "pi" },
              { session_id: "sess-b", agent_backend: "pi" },
            ],
            activeSessionId: "sess-a",
            loading: false,
            newSessionDefaults: null,
          },
          { refresh: () => Promise.resolve(), select: () => undefined },
        ) as any}
        messagesStore={messagesStore as any}
      >
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const pane = root.querySelector(".conversationPane") as HTMLDivElement | null;
    const rows = Array.from(root.querySelectorAll<HTMLElement>(".messageRow"));
    rows.forEach((row, index) => {
      Object.defineProperty(row, "offsetTop", { configurable: true, value: index * 180 });
    });
    Object.defineProperty(pane!, "clientHeight", { configurable: true, value: 360 });
    Object.defineProperty(pane!, "scrollTop", { configurable: true, writable: true, value: 410 });

    await act(async () => {
      pane?.dispatchEvent(new Event("scroll"));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(root.querySelector("[data-testid='jump-to-previous-user']")).not.toBeNull();

    await act(async () => {
      render(
        <AppProviders
          sessionsStore={createStaticStore(
            {
              items: [
                { session_id: "sess-a", agent_backend: "pi" },
                { session_id: "sess-b", agent_backend: "pi" },
              ],
              activeSessionId: "sess-b",
              loading: false,
              newSessionDefaults: null,
            },
            { refresh: () => Promise.resolve(), select: () => undefined },
          ) as any}
          messagesStore={messagesStore as any}
        >
          <ConversationPane />
        </AppProviders>,
        root!,
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(root.querySelector("[data-testid='jump-to-previous-user']")).toBeNull();
  });

  it("shows a scroll-to-bottom button when the pane is away from the latest content", async () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-bottom", agent_backend: "pi" }], activeSessionId: "sess-bottom", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-bottom": [
            { role: "user", text: "Question 1" },
            { role: "assistant", text: "Answer 1" },
            { role: "assistant", text: "Answer 2" },
            { role: "assistant", text: "Answer 3" },
          ],
        },
        offsetsBySessionId: { "sess-bottom": 4 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    await act(async () => {
      await Promise.resolve();
    });

    const scrollHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
    const clientHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
    Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
      configurable: true,
      get() {
        return this.classList?.contains("conversationPane") ? 1500 : (scrollHeightDescriptor?.get?.call(this) ?? 0);
      },
    });
    Object.defineProperty(HTMLElement.prototype, "clientHeight", {
      configurable: true,
      get() {
        return this.classList?.contains("conversationPane") ? 360 : (clientHeightDescriptor?.get?.call(this) ?? 0);
      },
    });

    const pane = root.querySelector(".conversationPane") as HTMLDivElement | null;
    Object.defineProperty(pane!, "scrollTop", { configurable: true, writable: true, value: 620 });
    const rows = Array.from(root.querySelectorAll<HTMLElement>(".messageRow"));
    rows.forEach((row, index) => {
      Object.defineProperty(row, "offsetTop", { configurable: true, value: index * 400 });
    });

    await act(async () => {
      pane?.dispatchEvent(new Event("scroll"));
      await Promise.resolve();
      await Promise.resolve();
    });

    const bottomButton = root.querySelector("[data-testid='scroll-to-bottom']") as HTMLButtonElement | null;
    expect(bottomButton).not.toBeNull();
    expect(bottomButton?.textContent).toBe("");
    expect(bottomButton?.getAttribute("aria-label")).toBe("Scroll to conversation bottom");

    if (scrollHeightDescriptor) {
      Object.defineProperty(HTMLElement.prototype, "scrollHeight", scrollHeightDescriptor);
    }
    if (clientHeightDescriptor) {
      Object.defineProperty(HTMLElement.prototype, "clientHeight", clientHeightDescriptor);
    }
  });

  it("does not yank the pane to the bottom when tool updates arrive while reading older content", async () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-stay-put", agent_backend: "pi" }], activeSessionId: "sess-stay-put", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createMutableStore(
      {
        bySessionId: {
          "sess-stay-put": [
            { role: "user", text: "Question 1" },
            { role: "assistant", text: "Answer 1" },
            { role: "assistant", text: "Answer 2" },
          ],
        },
        offsetsBySessionId: { "sess-stay-put": 3 },
        loading: false,
      },
      (_getState, _setState) => ({ loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() }),
    );

    const scrollTo = vi.fn();
    const scrollHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
    const clientHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
    Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
      configurable: true,
      get() {
        return this.classList?.contains("conversationPane") ? 1200 : (scrollHeightDescriptor?.get?.call(this) ?? 0);
      },
    });
    Object.defineProperty(HTMLElement.prototype, "clientHeight", {
      configurable: true,
      get() {
        return this.classList?.contains("conversationPane") ? 240 : (clientHeightDescriptor?.get?.call(this) ?? 0);
      },
    });

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const pane = root.querySelector(".conversationPane") as HTMLDivElement | null;
    Object.defineProperty(pane!, "scrollTo", { configurable: true, value: scrollTo });
    Object.defineProperty(pane!, "scrollTop", { configurable: true, writable: true, value: 360 });

    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    scrollTo.mockClear();

    await act(async () => {
      pane?.dispatchEvent(new Event("scroll"));
      await Promise.resolve();
    });

    await act(async () => {
      (messagesStore as any).setState({
        ...(messagesStore as any).getState(),
        bySessionId: {
          "sess-stay-put": [
            { role: "user", text: "Question 1" },
            { role: "assistant", text: "Answer 1" },
            { role: "assistant", text: "Answer 2" },
            { type: "tool", name: "read", text: "package.json" },
          ],
        },
        offsetsBySessionId: { "sess-stay-put": 4 },
      });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(scrollTo).not.toHaveBeenCalled();
    expect(root.querySelector("[data-testid='scroll-to-bottom']")).not.toBeNull();

    if (scrollHeightDescriptor) {
      Object.defineProperty(HTMLElement.prototype, "scrollHeight", scrollHeightDescriptor);
    }
    if (clientHeightDescriptor) {
      Object.defineProperty(HTMLElement.prototype, "clientHeight", clientHeightDescriptor);
    }
  });

  it("shows both floating buttons together and scrolls to the bottom when requested", async () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-both", agent_backend: "pi" }], activeSessionId: "sess-both", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-both": [
            { role: "user", text: "Question 1" },
            { role: "assistant", text: "Answer 1" },
            { role: "user", text: "Question 2" },
            { role: "assistant", text: "Answer 2" },
            { role: "assistant", text: "Answer 3" },
            { role: "assistant", text: "Newest answer" },
          ],
        },
        offsetsBySessionId: { "sess-both": 6 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    const scrollTo = vi.fn();

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    await act(async () => {
      await Promise.resolve();
    });

    const scrollHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
    const clientHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
    Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
      configurable: true,
      get() {
        return this.classList?.contains("conversationPane") ? 1800 : (scrollHeightDescriptor?.get?.call(this) ?? 0);
      },
    });
    Object.defineProperty(HTMLElement.prototype, "clientHeight", {
      configurable: true,
      get() {
        return this.classList?.contains("conversationPane") ? 360 : (clientHeightDescriptor?.get?.call(this) ?? 0);
      },
    });

    const pane = root.querySelector(".conversationPane") as HTMLDivElement | null;
    Object.defineProperty(pane!, "scrollTo", { configurable: true, value: scrollTo });
    Object.defineProperty(pane!, "scrollTop", { configurable: true, writable: true, value: 700 });
    const rows = Array.from(root.querySelectorAll<HTMLElement>(".messageRow"));
    rows.forEach((row, index) => {
      Object.defineProperty(row, "offsetTop", { configurable: true, value: index * 350 });
    });

    await act(async () => {
      pane?.dispatchEvent(new Event("scroll"));
      await Promise.resolve();
      await Promise.resolve();
    });

    const previousButton = root.querySelector("[data-testid='jump-to-previous-user']") as HTMLButtonElement | null;
    const bottomButton = root.querySelector("[data-testid='scroll-to-bottom']") as HTMLButtonElement | null;

    expect(previousButton).not.toBeNull();
    expect(bottomButton).not.toBeNull();

    await act(async () => {
      bottomButton?.click();
      await Promise.resolve();
    });

    expect(scrollTo).toHaveBeenCalledWith({ top: 1800, behavior: "smooth" });

    if (scrollHeightDescriptor) {
      Object.defineProperty(HTMLElement.prototype, "scrollHeight", scrollHeightDescriptor);
    }
    if (clientHeightDescriptor) {
      Object.defineProperty(HTMLElement.prototype, "clientHeight", clientHeightDescriptor);
    }
  });

  it("keeps existing messages visible while a background refresh is in flight", () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-10", agent_backend: "pi" }], activeSessionId: "sess-10", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-10": [{ role: "assistant", text: "Keep this visible" }],
        },
        offsetsBySessionId: { "sess-10": 1 },
        hasOlderBySessionId: {},
        olderBeforeBySessionId: {},
        loadingOlderBySessionId: {},
        loading: true,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.textContent).toContain("Keep this visible");
    expect(root.querySelector("[data-testid='message-surface'][data-kind='loading']")).toBeNull();
  });

  it("renders optimistic pending user messages from the composer store", () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-pending", agent_backend: "pi" }], activeSessionId: "sess-pending", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-pending": [{ role: "assistant", text: "Working on it" }],
        },
        offsetsBySessionId: { "sess-pending": 1 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );
    const composerStore = createStaticStore(
      {
        draft: "",
        sending: true,
        pendingBySessionId: {
          "sess-pending": [{ role: "user", text: "Please continue", pending: true, localId: "local-1" }],
        },
      },
      { setDraft: () => undefined, submit: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any} composerStore={composerStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const text = root.textContent || "";
    expect(text).toContain("Working on it");
    expect(text).toContain("Please continue");
    expect(text).toContain("Queued");
    expect(root.querySelectorAll("[data-testid='message-surface'][data-kind='user']")).toHaveLength(1);
  });

  it("renders server-backed bridge pseudo user messages and hides them after durable ack", () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-pending", agent_backend: "pi" }], activeSessionId: "sess-pending", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const pendingMessagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-pending": [
            { role: "user", text: "Please continue", pending: true, bridge_pseudo: true, event_id: "bridge-outbound:req-1", request_id: "req-1", request_state: "queued" },
            { role: "assistant", text: "Working on it" },
          ],
        },
        offsetsBySessionId: { "sess-pending": 2 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={pendingMessagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.textContent).toContain("Please continue");
    expect(root.textContent).toContain("Queued");

    const ackedMessagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-pending": [
            { role: "user", text: "Please continue" },
            { role: "user", text: "Please continue", pending: true, bridge_pseudo: true, event_id: "bridge-outbound:req-1", request_id: "req-1", request_state: "queued" },
            { role: "assistant", text: "Working on it" },
          ],
        },
        offsetsBySessionId: { "sess-pending": 3 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={ackedMessagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    const userSurfaces = root.querySelectorAll("[data-testid='message-surface'][data-kind='user']");
    expect(userSurfaces).toHaveLength(1);
    expect(root.textContent).not.toContain("Queued");
  });

  it("inserts day separators when consecutive messages cross calendar days", () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-days", agent_backend: "pi" }], activeSessionId: "sess-days", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-days": [
            { role: "assistant", ts: 1_744_000_000, text: "Day one" },
            { role: "assistant", ts: 1_744_086_500, text: "Day two" },
          ],
        },
        offsetsBySessionId: { "sess-days": 2 },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.querySelectorAll(".daySeparator").length).toBe(2);
    expect(root.textContent).toContain("Day one");
    expect(root.textContent).toContain("Day two");
  });

  it("does not keep showing the skeleton when a different session is still loading", () => {
    const sessionsStore = createStaticStore(
      { items: [{ session_id: "sess-1", agent_backend: "pi" }], activeSessionId: "sess-1", loading: false, newSessionDefaults: null },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const messagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-1": [{ role: "assistant", text: "Loaded message" }],
        },
        offsetsBySessionId: { "sess-1": 1 },
        hasOlderBySessionId: {},
        olderBeforeBySessionId: {},
        loadingOlderBySessionId: {},
        loadingBySessionId: { "sess-2": true },
        loadedBySessionId: { "sess-1": true },
        loading: true,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.textContent).toContain("Loaded message");
    expect(root.querySelector("[data-kind='loading']")).toBeNull();
  });

  it("shows a loading skeleton immediately when switching to an unloaded session", async () => {
    const initialSessionsStore = createStaticStore(
      {
        items: [
          { session_id: "sess-1", agent_backend: "pi" },
          { session_id: "sess-2", agent_backend: "pi" },
        ],
        activeSessionId: "sess-1",
        loading: false,
        newSessionDefaults: null,
      },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const initialMessagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-1": [{ role: "assistant", text: "Loaded message" }],
        },
        offsetsBySessionId: { "sess-1": 1 },
        hasOlderBySessionId: {},
        olderBeforeBySessionId: {},
        loadingOlderBySessionId: {},
        loadingBySessionId: {},
        loadedBySessionId: { "sess-1": true },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={initialSessionsStore as any} messagesStore={initialMessagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.textContent).toContain("Loaded message");

    const switchedSessionsStore = createStaticStore(
      {
        items: [
          { session_id: "sess-1", agent_backend: "pi" },
          { session_id: "sess-2", agent_backend: "pi" },
        ],
        activeSessionId: "sess-2",
        loading: false,
        newSessionDefaults: null,
      },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const switchedMessagesStore = createStaticStore(
      {
        bySessionId: {
          "sess-1": [{ role: "assistant", text: "Loaded message" }],
        },
        offsetsBySessionId: { "sess-1": 1 },
        hasOlderBySessionId: {},
        olderBeforeBySessionId: {},
        loadingOlderBySessionId: {},
        loadingBySessionId: {},
        loadedBySessionId: { "sess-1": true },
        loading: false,
      },
      { loadInitial: () => Promise.resolve(), poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    await act(async () => {
      render(
        <AppProviders sessionsStore={switchedSessionsStore as any} messagesStore={switchedMessagesStore as any}>
          <ConversationPane />
        </AppProviders>,
        root!,
      );
      await Promise.resolve();
    });

    expect(root.textContent).not.toContain("Loaded message");
    expect(root.querySelector("[data-kind='loading']")).not.toBeNull();
  });

  it("loads historical pi messages when the synthetic history session becomes active", async () => {
    const sessionsStore = createStaticStore(
      {
        items: [{ session_id: "history:pi:resume-hist", agent_backend: "pi", historical: true }],
        activeSessionId: "history:pi:resume-hist",
        loading: false,
        newSessionDefaults: null,
      },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    const loadInitial = vi.fn().mockResolvedValue(undefined);
    const messagesStore = createStaticStore(
      {
        bySessionId: {},
        offsetsBySessionId: {},
        hasOlderBySessionId: {},
        olderBeforeBySessionId: {},
        loadingOlderBySessionId: {},
        loadingBySessionId: {},
        loadedBySessionId: {},
        loading: false,
      },
      { loadInitial, poll: () => Promise.resolve(), loadOlder: () => Promise.resolve() },
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    await act(async () => {
      render(
        <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
          <ConversationPane />
        </AppProviders>,
        root!,
      );
      await Promise.resolve();
    });

    expect(loadInitial).toHaveBeenCalledWith("history:pi:resume-hist");
  });

  it("shows the loading skeleton immediately for an unloaded historical pi session", async () => {
    const sessionsStore = createStaticStore(
      {
        items: [{ session_id: "history:pi:resume-empty", agent_backend: "pi", historical: true }],
        activeSessionId: "history:pi:resume-empty",
        loading: false,
        newSessionDefaults: null,
      },
      { refresh: () => Promise.resolve(), select: () => undefined },
    );
    let resolveLoad: (() => void) | null = null;
    const messagesStore = createMutableStore(
      {
        bySessionId: {},
        offsetsBySessionId: {},
        hasOlderBySessionId: {},
        olderBeforeBySessionId: {},
        loadingOlderBySessionId: {},
        loadingBySessionId: {},
        loadedBySessionId: {},
        loading: false,
      },
      (getState, setState) => ({
        loadInitial: vi.fn(async (sessionId: string) => {
          setState({
            ...getState(),
            loadingBySessionId: { ...getState().loadingBySessionId, [sessionId]: true },
            loading: true,
          });
          await new Promise<void>((resolve) => {
            resolveLoad = resolve;
          });
          setState({
            ...getState(),
            loadingBySessionId: { ...getState().loadingBySessionId, [sessionId]: false },
            loadedBySessionId: { ...getState().loadedBySessionId, [sessionId]: true },
            bySessionId: { ...getState().bySessionId, [sessionId]: [] },
            loading: false,
          });
        }),
        poll: () => Promise.resolve(),
        loadOlder: () => Promise.resolve(),
      }),
    );

    root = document.createElement("div");
    document.body.appendChild(root);
    render(
      <AppProviders sessionsStore={sessionsStore as any} messagesStore={messagesStore as any}>
        <ConversationPane />
      </AppProviders>,
      root,
    );

    expect(root.querySelector("[data-kind='loading']")).not.toBeNull();
    expect(root.textContent).not.toContain("No conversation events yet.");

    await act(async () => {
      resolveLoad?.();
      await Promise.resolve();
    });
  });
});
