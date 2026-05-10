import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "preact/hooks";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

import {
  shallowEqual,
  useComposerStoreApi,
  useLiveSessionStoreApi,
  useComposerStoreSelector,
  useLiveSessionStoreSelector,
  useSessionUiStoreApi,
  useSessionUiStoreSelector,
  useSessionsStoreApi,
  useSessionsStoreSelector,
  useWaitsStoreApi,
  useWaitsStoreSelector,
} from "../../app/providers";
import { getRealtimeConnectionState } from "../../domains/realtime/client";
import { api } from "../../lib/api";
import { getSessionRuntimeId } from "../../lib/session-identity";
import type {
  ActiveWaitSummary,
  SessionCommand,
  SessionSummary,
} from "../../lib/types";
import { WaitAnswerForm } from "@/components/waits/WaitAnswerForm";
import { WaitJustification } from "@/components/waits/WaitJustification";
import { WaitStateBadge } from "@/components/waits/WaitStateBadge";
import { getDisplayableTodoSnapshot, TodoComposerPanel } from "./TodoComposerPanel";

function enterToSendEnabled() {
  return window.localStorage.getItem("actrail.enterToSend") === "1";
}

function getSlashDraftQuery(draft: string) {
  const trimmed = draft.trimStart();
  if (!trimmed.startsWith("/")) {
    return null;
  }

  return trimmed.slice(1).trimStart().split(/\s+/, 1)[0].toLowerCase();
}

function isSlashCommandDraft(draft: string) {
  return draft.trimStart().startsWith("/");
}

function commandMenuNode({
  commandsLoading,
  highlightedCommandIndex,
  menuRef,
  visibleCommands,
  onApply,
}: {
  commandsLoading: boolean;
  highlightedCommandIndex: number;
  menuRef?: preact.Ref<HTMLDivElement>;
  visibleCommands: SessionCommand[];
  onApply: (command: SessionCommand) => void;
}) {
  return (
    <div ref={menuRef} className="composerCommandMenu" data-testid="composer-command-menu" onWheel={(event) => event.stopPropagation()}>
      {commandsLoading ? <div className="composerCommandHint">Loading Pi commands...</div> : null}
      {!commandsLoading
        ? visibleCommands.map((command, index) => (
          <button
            key={command.name}
            type="button"
            className={cn("composerCommandItem", index === highlightedCommandIndex && "is-active")}
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => onApply(command)}
          >
            <span className="composerCommandName">/{command.name}</span>
            {command.description ? <span className="composerCommandDescription">{command.description}</span> : null}
            {command.source ? <span className="composerCommandSource">{command.source}</span> : null}
          </button>
        ))
        : null}
    </div>
  );
}

function formatSlashCommandValue(commandName: string) {
  const normalized = commandName.startsWith("/") ? commandName : `/${commandName}`;

  return `${normalized} `;
}

function sessionBackendUnavailable(session: SessionSummary | null) {
  if (!session) {
    return false;
  }
  const state = String(session.transport_state || "").trim();
  return session.reset_required === true || state === "ended" || state === "broken" || state === "failed";
}

function sessionBackendUnavailableLabel(session: SessionSummary | null) {
	const state = String(session?.transport_state || "").trim();
	if (session?.reset_required === true) {
		return "Session backend requires restart before sending.";
  }
  if (state === "ended") {
    return "Session backend has ended. Restart or create a new session before sending.";
  }
  if (state === "broken") {
    return "Session backend is broken. Restart or create a new session before sending.";
  }
  if (state === "failed") {
    return "Session backend failed to start. Restart or create a new session before sending.";
  }
	return "Session backend is unavailable.";
}

function sessionRuntimeTerminal(session: SessionSummary | null) {
	const state = String(session?.runtime_state || "").trim();

	return state === "failed" || state === "ended";
}

function composerActionErrorMessage(error: unknown, fallback: string) {
  if (error instanceof Error && error.message.trim()) {
    return error.message.trim();
  }
  if (typeof error === "string" && error.trim()) {
    return error.trim();
  }
  return fallback;
}

const MOBILE_COMPOSER_QUERY = "(max-width: 880px)";
const MOBILE_COMPOSER_MIN_HEIGHT_PX = 56;
const MOBILE_COMPOSER_MAX_HEIGHT_PX = 112;
const POST_SEND_REFRESH_DELAYS_MS = [1500, 4000, 8000];

export type ComposerMode = "idle" | "typing" | "slash_menu" | "attachment" | "sending" | "busy" | "waiting_user" | "disabled";

export interface ComposerModeInputs {
  waitingUser?: boolean;
  disabled?: boolean;
  sending?: boolean;
  slashMenu?: boolean;
  attachment?: boolean;
  busy?: boolean;
  typing?: boolean;
}

export function deriveComposerMode(input: ComposerModeInputs): ComposerMode {
  if (input.waitingUser) {
    return "waiting_user";
  }
  if (input.disabled) {
    return "disabled";
  }
  if (input.sending) {
    return "sending";
  }
  if (input.slashMenu) {
    return "slash_menu";
  }
  if (input.attachment) {
    return "attachment";
  }
  if (input.busy) {
    return "busy";
  }
  if (input.typing) {
    return "typing";
  }
  return "idle";
}

function shouldUseMobileComposerAutosize() {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return false;
  }

  return window.matchMedia(MOBILE_COMPOSER_QUERY).matches;
}

function isCompactMobileViewport() {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return false;
  }

  return window.matchMedia("(max-width: 880px), (pointer: coarse)").matches;
}

function syncComposerTextareaHeight(textarea: HTMLTextAreaElement | null, enabled: boolean) {
  if (!textarea) {
    return;
  }

  if (!enabled) {
    textarea.style.height = "";
    textarea.style.minHeight = "";
    textarea.style.maxHeight = "";
    textarea.style.overflowY = "";
    return;
  }

  textarea.style.minHeight = `${MOBILE_COMPOSER_MIN_HEIGHT_PX}px`;
  textarea.style.maxHeight = `${MOBILE_COMPOSER_MAX_HEIGHT_PX}px`;
  textarea.style.height = "auto";

  const nextHeight = Math.min(
    Math.max(textarea.scrollHeight, MOBILE_COMPOSER_MIN_HEIGHT_PX),
    MOBILE_COMPOSER_MAX_HEIGHT_PX,
  );

  textarea.style.height = `${nextHeight}px`;
  textarea.style.overflowY = textarea.scrollHeight > MOBILE_COMPOSER_MAX_HEIGHT_PX ? "auto" : "hidden";
}

const MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024;

function safeStem(name: string) {
  const base = String(name || "file").split("/").pop() || "file";
  const dot = base.lastIndexOf(".");

  return (dot > 0 ? base.slice(0, dot) : base).replace(/[^a-zA-Z0-9._-]+/g, "_").slice(0, 80) || "file";
}

function extLower(name: string) {
  const dot = String(name || "").lastIndexOf(".");

  return dot >= 0 ? String(name).slice(dot + 1).toLowerCase() : "";
}

function isLikelyHeic(file: File) {
  const type = String(file.type || "").toLowerCase();
  const ext = extLower(file.name);

  return type.includes("heic") || type.includes("heif") || ext === "heic" || ext === "heif";
}

function looksLikeImage(file: File) {
  const type = String(file.type || "").toLowerCase();
  const ext = extLower(file.name);

  return type.startsWith("image/") || ["png", "jpg", "jpeg", "webp", "gif", "bmp", "svg", "avif", "heic", "heif"].includes(ext);
}

function bytesToBase64(bytes: Uint8Array) {
  let binary = "";
  const chunkSize = 0x8000;

  for (let index = 0; index < bytes.length; index += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(index, index + chunkSize));
  }

  return btoa(binary);
}

async function blobToArrayBuffer(blob: Blob) {
  if (typeof blob.arrayBuffer === "function") {
    return blob.arrayBuffer();
  }

  return new Promise<ArrayBuffer>((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(reader.error ?? new Error("read failed"));
    reader.onload = () => {
      if (reader.result instanceof ArrayBuffer) {
        resolve(reader.result);
        return;
      }

      reject(new Error("read failed"));
    };
    reader.readAsArrayBuffer(blob);
  });
}

async function toJpegBlob(file: File, options: { maxDim: number; quality: number }) {
  const url = URL.createObjectURL(file);

  try {
    const image = new Image();
    image.decoding = "async";
    image.src = url;
    if (image.decode) {
      await image.decode();
    } else {
      await new Promise((resolve, reject) => {
        image.onload = resolve;
        image.onerror = () => reject(new Error("decode failed"));
      });
    }

    const naturalWidth = image.naturalWidth || image.width || 0;
    const naturalHeight = image.naturalHeight || image.height || 0;
    if (!naturalWidth || !naturalHeight) {
      throw new Error("invalid image dimensions");
    }

    const scale = Math.min(1, options.maxDim / Math.max(naturalWidth, naturalHeight));
    const width = Math.max(1, Math.round(naturalWidth * scale));
    const height = Math.max(1, Math.round(naturalHeight * scale));
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const context = canvas.getContext("2d", { alpha: false });
    if (!context) {
      throw new Error("no canvas");
    }
    context.drawImage(image, 0, 0, width, height);

    const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/jpeg", options.quality));
    if (!blob) {
      throw new Error("jpeg encode failed");
    }

    return blob;
  } finally {
    URL.revokeObjectURL(url);
  }
}

interface MobileWaitActionPanelProps {
  wait: ActiveWaitSummary;
  disabled?: boolean;
  onClaim(): void;
  onCancel(): void;
  onAnswer(answer: string): void;
}

function MobileWaitActionPanel({ wait, disabled, onClaim, onCancel, onAnswer }: MobileWaitActionPanelProps) {
  return (
    <Card data-testid="mobile-wait-composer-panel" className="mobileWaitComposerPanel rounded-[1.2rem] border-border/70 bg-card/95 shadow-lg shadow-primary/5">
      <CardContent className="space-y-3 p-4">
        <div className="waitPanelHeader compact">
          <WaitStateBadge state={wait.state} />
          <h3>{wait.question}</h3>
        </div>
        <WaitJustification wait={wait} />
        {wait.state === "pending_unread" ? (
          <div className="waitPanelActions">
            <Button type="button" disabled={disabled} onClick={onClaim}>Claim</Button>
            <Button type="button" variant="outline" disabled={disabled} onClick={onCancel}>Cancel wait</Button>
          </div>
        ) : null}
        {wait.state === "claimed" ? (
          <div className="waitPanelActionsVertical">
            <WaitAnswerForm disabled={disabled} submitting={disabled} onSubmit={onAnswer} />
            <Button type="button" variant="outline" disabled={disabled} onClick={onCancel}>Cancel wait</Button>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

interface ComposerProps {
  compactMobile?: boolean;
  commandSheetRequestKey?: number;
}

export function Composer({ compactMobile = false, commandSheetRequestKey = 0 }: ComposerProps = {}) {
  const waitsStoreApi = useWaitsStoreApi();
  const activeSessionId = useSessionsStoreSelector((state) => state.activeSessionId);
  const activeSession = useSessionsStoreSelector(
    (state) => state.items.find((session) => session.session_id === state.activeSessionId) ?? null,
  );
  const { hasActiveSessionLiveBusy, activeSessionLiveBusy, activeSessionLiveRuntimeState } = useLiveSessionStoreSelector((state) => {
    if (!activeSessionId) {
      return { hasActiveSessionLiveBusy: false, activeSessionLiveBusy: false, activeSessionLiveRuntimeState: undefined };
    }

    return {
      hasActiveSessionLiveBusy: Object.prototype.hasOwnProperty.call(state.busyBySessionId ?? {}, activeSessionId),
      activeSessionLiveBusy: state.busyBySessionId?.[activeSessionId] === true,
      activeSessionLiveRuntimeState: state.runtimeStateBySessionId?.[activeSessionId],
    };
  }, shallowEqual);
  const sending = useComposerStoreSelector((state) => state.sending);
  const draft = useComposerStoreSelector((state) => activeSessionId ? state.draftBySessionId?.[activeSessionId] ?? "" : "");
  const activeWait = useWaitsStoreSelector((state) => activeSessionId ? state.activeBySessionId?.[activeSessionId] ?? null : null);
  const { sessionId: sessionUiSessionId, diagnostics } = useSessionUiStoreSelector((state) => ({
    sessionId: state.sessionId,
    diagnostics: state.diagnostics,
  }), shallowEqual);
  const sessionsStoreApi = useSessionsStoreApi();
  const composerStoreApi = useComposerStoreApi();
  const liveSessionStoreApi = useLiveSessionStoreApi();
  const sessionUiStoreApi = useSessionUiStoreApi();
  const [todoExpandedBySessionId, setTodoExpandedBySessionId] = useState<Record<string, boolean>>({});
  const [commandsBySessionId, setCommandsBySessionId] = useState<Record<string, SessionCommand[]>>({});
  const [commandsLoadedBySessionId, setCommandsLoadedBySessionId] = useState<Record<string, boolean>>({});
  const [commandsLoadingBySessionId, setCommandsLoadingBySessionId] = useState<Record<string, boolean>>({});
  const [highlightedCommandIndex, setHighlightedCommandIndex] = useState(0);
  const [slashMenuDismissed, setSlashMenuDismissed] = useState(false);
  const commandMenuRef = useRef<HTMLDivElement | null>(null);
  const [attachedFilesBySessionId, setAttachedFilesBySessionId] = useState<Record<string, number>>({});
  const [attachmentUploading, setAttachmentUploading] = useState(false);
  const [mobileWaitSubmitting, setMobileWaitSubmitting] = useState(false);
  const [mobileWaitError, setMobileWaitError] = useState("");
  const [composerActionError, setComposerActionError] = useState("");
  const [mobileComposerAutosize, setMobileComposerAutosize] = useState(() => shouldUseMobileComposerAutosize());
  const fileInputRef = useRef<HTMLInputElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const postSendRefreshTimeoutsRef = useRef<number[]>([]);
  const activeSessionRuntimeId = getSessionRuntimeId(activeSession);
  const activeSessionPending = activeSession?.pending_startup === true;
  const visibleActiveWait = activeWait ?? activeSession?.active_wait ?? null;
  const activeSessionBackendUnavailable = sessionBackendUnavailable(activeSession);
	const supervisorEnabled = activeSession?.supervisor?.enabled === true;
	const supervisorBlockReason = "Supervisor is controlling this session. Disable Supervisor to send manually.";
	const activeSessionSendBlocked = activeSessionPending || activeSessionBackendUnavailable || Boolean(visibleActiveWait) || supervisorEnabled;
	const activeSessionSendBlockReason = supervisorEnabled ? supervisorBlockReason : visibleActiveWait ? "Answer the active wait in Details before sending a normal message." : activeSessionBackendUnavailable ? sessionBackendUnavailableLabel(activeSession) : activeSessionPending ? "Session runtime is starting." : "";
	const listedRuntimeState = typeof activeSession?.runtime_state === "string" ? activeSession.runtime_state.trim() : "";
	const liveRuntimeState = typeof activeSessionLiveRuntimeState === "string" ? activeSessionLiveRuntimeState.trim() : "";
	const activeSessionRuntimeState = listedRuntimeState === "failed" || listedRuntimeState === "ended"
		? listedRuntimeState
		: liveRuntimeState || listedRuntimeState;
	const activeSessionTerminalRuntime = sessionRuntimeTerminal(activeSession) || activeSessionRuntimeState === "failed" || activeSessionRuntimeState === "ended";
	const activeSessionBusy = Boolean(
		activeSession
		&& !activeSessionTerminalRuntime
		&& (hasActiveSessionLiveBusy ? activeSessionLiveBusy : activeSession.busy === true),
	);
  const activeQueueCount = typeof activeSession?.queue_len === "number" && Number.isFinite(activeSession.queue_len)
		? Math.max(0, Math.round(activeSession.queue_len))
		: 0;
  const queueButtonLabel = activeSessionBusy
    ? activeQueueCount > 0 ? `Queue next (${activeQueueCount})` : "Queue next"
    : activeQueueCount > 0 ? `Queue ${activeQueueCount}` : "Queue";
  const queueButtonTitle = supervisorEnabled
    ? supervisorBlockReason
    : activeSessionBackendUnavailable
      ? "Queue this draft until the session backend is restarted."
      : activeSessionBusy
        ? "Queue this draft after the current turn."
        : "Queue this draft instead of sending it now.";
  const sendButtonLabel = sending ? "Sending" : activeSessionBusy ? "Send now" : "Send";
  const sendButtonTitle = activeSessionSendBlockReason || (activeSessionBusy ? "Send immediately to the busy session." : undefined);
  const activeSessionIsPi = activeSession?.agent_backend === "pi";
  const activeSessionIsCodex = activeSession?.agent_backend === "codex";
  const activeSessionIsHistoricalPi = activeSessionIsPi && activeSession?.historical === true;
  const activeAttachmentCount = activeSessionId ? attachedFilesBySessionId[activeSessionId] ?? 0 : 0;
  const attachmentsSupported = Boolean(activeSessionId && activeSession?.agent_backend !== "pi" && activeSession?.agent_backend !== "codex" && !supervisorEnabled);
  const slashQuery = getSlashDraftQuery(draft);
  const slashCommandDraft = isSlashCommandDraft(draft);
  const todoSnapshot = useMemo(() => {
    if (!activeSessionId || activeSession?.agent_backend !== "pi") {
      return null;
    }

    if (sessionUiSessionId !== activeSessionId) {
      return null;
    }

    if (!diagnostics || typeof diagnostics !== "object") {
      return null;
    }

    const snapshot = (diagnostics as { todo_snapshot?: unknown }).todo_snapshot;

    return getDisplayableTodoSnapshot(snapshot);
  }, [activeSession?.agent_backend, activeSessionId, diagnostics, sessionUiSessionId]);

  const visibleTodoExpanded = activeSessionId ? Boolean(todoExpandedBySessionId[activeSessionId]) : false;
  const visibleCommands = useMemo(() => {
    if (!activeSessionId || !activeSessionIsPi || slashQuery === null) {
      return [];
    }

    const commands = commandsBySessionId[activeSessionId] ?? [];

    return commands
      .filter((command) => command.name.toLowerCase().startsWith(slashQuery))
      .slice(0, 8);
  }, [activeSessionId, activeSessionIsPi, commandsBySessionId, slashQuery]);
  const commandsLoaded = activeSessionId ? Boolean(commandsLoadedBySessionId[activeSessionId]) : false;
  const commandsLoading = activeSessionId ? Boolean(commandsLoadingBySessionId[activeSessionId]) : false;
  const commandMenuOpen = Boolean(
    activeSessionIsPi && slashQuery !== null && !slashMenuDismissed && (commandsLoading || visibleCommands.length > 0),
  );
  const composerMode = deriveComposerMode({
    waitingUser: Boolean(visibleActiveWait),
    disabled: activeSessionBackendUnavailable,
    sending,
    slashMenu: commandMenuOpen,
    attachment: attachmentUploading,
    busy: activeSessionBusy,
    typing: draft.trim().length > 0,
  });
  const commandMenuVisible = composerMode === "slash_menu";

  useEffect(() => {
    setComposerActionError("");
  }, [activeSessionId]);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return;
    }

    const mediaQuery = window.matchMedia(MOBILE_COMPOSER_QUERY);
    const update = () => {
      setMobileComposerAutosize(mediaQuery.matches);
    };

    update();
    if (typeof mediaQuery.addEventListener === "function") {
      mediaQuery.addEventListener("change", update);
    } else {
      mediaQuery.addListener?.(update);
    }

    return () => {
      if (typeof mediaQuery.removeEventListener === "function") {
        mediaQuery.removeEventListener("change", update);
      } else {
        mediaQuery.removeListener?.(update);
      }
    };
  }, []);

  useEffect(() => () => {
    for (const timeoutId of postSendRefreshTimeoutsRef.current) {
      window.clearTimeout(timeoutId);
    }
    postSendRefreshTimeoutsRef.current = [];
  }, []);

  useLayoutEffect(() => {
    syncComposerTextareaHeight(textareaRef.current, mobileComposerAutosize);
  }, [draft, mobileComposerAutosize]);

  useEffect(() => {
    const textarea = textareaRef.current;
    if (!textarea) {
      return;
    }

    if (document.activeElement === textarea) {
      textarea.blur();
    }
    syncComposerTextareaHeight(textarea, mobileComposerAutosize);
  }, [activeSessionId, mobileComposerAutosize]);

  useEffect(() => {
    const textarea = textareaRef.current;
    if (!textarea || typeof window === "undefined") {
      return;
    }

    const alignIntoView = () => {
      if (document.activeElement !== textarea || !isCompactMobileViewport()) {
        return;
      }
      window.requestAnimationFrame(() => {
        textarea.scrollIntoView({ block: "nearest", inline: "nearest" });
      });
    };

    const handleFocus = () => {
      alignIntoView();
      window.setTimeout(alignIntoView, 120);
    };
    const handleViewportResize = () => {
      syncComposerTextareaHeight(textarea, mobileComposerAutosize);
      alignIntoView();
    };

    textarea.addEventListener("focus", handleFocus);
    window.visualViewport?.addEventListener("resize", handleViewportResize);
    window.addEventListener("resize", handleViewportResize);
    return () => {
      textarea.removeEventListener("focus", handleFocus);
      window.visualViewport?.removeEventListener("resize", handleViewportResize);
      window.removeEventListener("resize", handleViewportResize);
    };
  }, [activeSessionId, mobileComposerAutosize]);

  useEffect(() => {
    if (!activeSessionId || !activeSessionIsPi || activeSessionPending || slashQuery === null) {
      return;
    }
    if (commandsLoaded || commandsLoading) {
      return;
    }

    let cancelled = false;

    setCommandsLoadingBySessionId((value) => ({
      ...value,
      [activeSessionId]: true,
    }));

    (activeSessionRuntimeId
      ? api.getSessionCommands(activeSessionId, undefined, activeSessionRuntimeId)
      : api.getSessionCommands(activeSessionId))
      .then((response) => {
        if (cancelled) {
          return;
        }

        setCommandsBySessionId((value) => ({
          ...value,
          [activeSessionId]: Array.isArray(response.commands) ? response.commands : [],
        }));
        setCommandsLoadedBySessionId((value) => ({
          ...value,
          [activeSessionId]: true,
        }));
      })
      .catch(() => {
        if (cancelled) {
          return;
        }

        setCommandsBySessionId((value) => ({
          ...value,
          [activeSessionId]: [],
        }));
        setCommandsLoadedBySessionId((value) => ({
          ...value,
          [activeSessionId]: true,
        }));
      })
      .finally(() => {
        if (cancelled) {
          return;
        }

        setCommandsLoadingBySessionId((value) => ({
          ...value,
          [activeSessionId]: false,
        }));
      });

    return () => {
      cancelled = true;
    };
  }, [activeSessionId, activeSessionIsPi, activeSessionPending, activeSessionRuntimeId, slashQuery]);

  useEffect(() => {
    setHighlightedCommandIndex(0);
  }, [activeSessionId, slashQuery]);

  useEffect(() => {
    setSlashMenuDismissed(false);
  }, [activeSessionId, slashCommandDraft, slashQuery]);

  useEffect(() => {
    if (!compactMobile || !commandSheetRequestKey || !activeSessionId) {
      return;
    }
    setSlashMenuDismissed(false);
    const currentDraft = composerStoreApi.getState().draftBySessionId?.[activeSessionId] ?? "";
    if (!currentDraft.startsWith("/")) {
      composerStoreApi.setDraft(activeSessionId, "/");
    }
  }, [activeSessionId, commandSheetRequestKey, compactMobile, composerStoreApi]);

  useEffect(() => {
    if (!visibleCommands.length) {
      setHighlightedCommandIndex(0);
      return;
    }

    setHighlightedCommandIndex((value) => Math.min(value, visibleCommands.length - 1));
  }, [visibleCommands.length]);

  const applySlashCommand = (command: SessionCommand | undefined) => {
    if (!command) {
      return;
    }

    composerStoreApi.setDraft(activeSessionId, formatSlashCommandValue(command.name));
    setHighlightedCommandIndex(0);
  };
  useEffect(() => {
    const menu = commandMenuRef.current;
    if (!menu || !visibleCommands.length) {
      return;
    }
    const active = menu.querySelector<HTMLElement>(".composerCommandItem.is-active");
    if (typeof active?.scrollIntoView === "function") {
      active.scrollIntoView({ block: "nearest" });
    }
  }, [highlightedCommandIndex, visibleCommands.length]);

  const commandMenu = commandMenuVisible && !compactMobile
    ? commandMenuNode({ commandsLoading, highlightedCommandIndex, menuRef: commandMenuRef, visibleCommands, onApply: applySlashCommand })
    : null;

  const clearAttachmentCount = (sessionId: string) => {
    setAttachedFilesBySessionId((value) => {
      if (!value[sessionId]) {
        return value;
      }

      return {
        ...value,
        [sessionId]: 0,
      };
    });
  };

  const refreshSessionAfterSend = (sessionId: string, runtimeId?: string | null, agentBackend?: string) => {
    const refreshLiveSnapshot = () => (
      runtimeId ? liveSessionStoreApi.loadInitial(sessionId, runtimeId) : liveSessionStoreApi.loadInitial(sessionId)
    );
    const refreshWorkspace = () => (
      runtimeId ? sessionUiStoreApi.refresh(sessionId, { agentBackend, runtimeId }) : sessionUiStoreApi.refresh(sessionId, { agentBackend })
    );
    const refreshNow = () => Promise.allSettled([
      refreshLiveSnapshot(),
      refreshWorkspace(),
      sessionsStoreApi.refresh(),
    ]);

    for (const timeoutId of postSendRefreshTimeoutsRef.current) {
      window.clearTimeout(timeoutId);
    }
    postSendRefreshTimeoutsRef.current = [];

    if (getRealtimeConnectionState() === "open") {
      return Promise.allSettled([
        refreshWorkspace(),
        sessionsStoreApi.refresh(),
      ]);
    }

    for (const delayMs of POST_SEND_REFRESH_DELAYS_MS) {
      const timeoutId = window.setTimeout(() => {
        void Promise.allSettled([
          runtimeId ? liveSessionStoreApi.poll(sessionId, runtimeId) : liveSessionStoreApi.poll(sessionId),
          refreshWorkspace(),
          sessionsStoreApi.refresh(),
        ]);
      }, delayMs);
      postSendRefreshTimeoutsRef.current.push(timeoutId);
    }

    return refreshNow();
  };

  const resolvePostSendSessionIdentity = async (response: unknown, fallbackSessionId: string, fallbackRuntimeId?: string | null) => {
    const nextSessionId = response && typeof response === "object"
      ? String((response as { session_id?: unknown }).session_id || "").trim()
      : "";
    const nextRuntimeId = response && typeof response === "object"
      ? String((response as { runtime_id?: unknown }).runtime_id || "").trim()
      : "";
    if (!nextSessionId || nextSessionId === fallbackSessionId) {
      return { sessionId: fallbackSessionId, runtimeId: fallbackRuntimeId || null };
    }

    await sessionsStoreApi.refresh();
    sessionsStoreApi.select(nextSessionId);
    return { sessionId: nextSessionId, runtimeId: nextRuntimeId || null };
  };

  const submitCurrentDraft = () => {
    if (!activeSessionId || !draft.trim() || sending || activeSessionSendBlocked) {
      return;
    }

    setComposerActionError("");
    (activeSessionRuntimeId
      ? composerStoreApi.submit(activeSessionId, activeSessionRuntimeId)
      : composerStoreApi.submit(activeSessionId))
      .then(async (response) => {
        clearAttachmentCount(activeSessionId);
        setComposerActionError("");
        const target = await resolvePostSendSessionIdentity(response, activeSessionId, activeSessionRuntimeId);
        await refreshSessionAfterSend(target.sessionId, target.runtimeId, activeSession?.agent_backend);
      })
      .catch((error) => {
        setComposerActionError(composerActionErrorMessage(error, "Failed to send message"));
      });
  };

  const queueCurrentDraft = () => {
    if (!activeSessionId || !draft.trim() || sending || visibleActiveWait || supervisorEnabled) {
      return;
    }

    const queuedText = draft;
    setComposerActionError("");
    composerStoreApi.setDraft(activeSessionId, "");
    (activeSessionRuntimeId
      ? api.enqueueMessage(activeSessionId, queuedText, activeSessionRuntimeId)
      : api.enqueueMessage(activeSessionId, queuedText))
      .then(async (response) => {
        clearAttachmentCount(activeSessionId);
        const target = await resolvePostSendSessionIdentity(response, activeSessionId, activeSessionRuntimeId);
        if (activeSessionIsHistoricalPi && target.sessionId !== activeSessionId) {
          return refreshSessionAfterSend(target.sessionId, target.runtimeId, activeSession?.agent_backend);
        }
        return target.runtimeId
          ? sessionUiStoreApi.refresh(target.sessionId, { agentBackend: activeSession?.agent_backend, runtimeId: target.runtimeId })
          : sessionUiStoreApi.refresh(target.sessionId, { agentBackend: activeSession?.agent_backend });
      })
      .catch((error) => {
        composerStoreApi.setDraft(activeSessionId, queuedText);
        setComposerActionError(composerActionErrorMessage(error, "Failed to queue message"));
      });
  };

  const interruptCurrentLoop = () => {
    if (!activeSessionId || !activeSessionBusy) {
      return;
    }

    const interruptRequest = activeSessionRuntimeId
      ? api.interruptSession(activeSessionId, activeSessionRuntimeId)
      : api.interruptSession(activeSessionId);

    interruptRequest
      .then(() => Promise.allSettled([
        sessionsStoreApi.refresh(),
        activeSessionRuntimeId
          ? liveSessionStoreApi.loadInitial(activeSessionId, activeSessionRuntimeId)
          : liveSessionStoreApi.loadInitial(activeSessionId),
        activeSessionRuntimeId
          ? sessionUiStoreApi.refresh(activeSessionId, { agentBackend: activeSession?.agent_backend, runtimeId: activeSessionRuntimeId })
          : sessionUiStoreApi.refresh(activeSessionId, { agentBackend: activeSession?.agent_backend }),
      ]))
      .catch(() => undefined);
  };

  const handleAttachClick = () => {
    if (!attachmentsSupported || attachmentUploading || sending || supervisorEnabled) {
      return;
    }

    const input = fileInputRef.current;
    if (!input) {
      return;
    }

    input.value = "";
    input.click();
  };

  const handleAttachChange = async (event: Event) => {
    if (!attachmentsSupported || !activeSessionId || attachmentUploading || sending || supervisorEnabled) {
      return;
    }

    const input = event.currentTarget as HTMLInputElement | null;
    const file = input?.files?.[0];
    if (input) {
      input.value = "";
    }
    if (!file) {
      return;
    }

    const attachmentIndex = activeAttachmentCount + 1;
    setAttachmentUploading(true);

    try {
      let uploadBlob: Blob = file;
      let uploadName = file.name || "file";

      if (looksLikeImage(file) && (file.size > MAX_ATTACHMENT_BYTES || isLikelyHeic(file))) {
        uploadName = `${safeStem(file.name)}.jpg`;
        const attempts = [
          { maxDim: 2048, quality: 0.86 },
          { maxDim: 1600, quality: 0.82 },
          { maxDim: 1600, quality: 0.72 },
          { maxDim: 1280, quality: 0.68 },
          { maxDim: 1280, quality: 0.58 },
        ];

        let compressedBlob: Blob | null = null;
        for (const attempt of attempts) {
          compressedBlob = await toJpegBlob(file, attempt);
          if (compressedBlob.size <= MAX_ATTACHMENT_BYTES) {
            break;
          }
        }
        if (!compressedBlob || compressedBlob.size > MAX_ATTACHMENT_BYTES) {
          throw new Error("image too large");
        }

        uploadBlob = compressedBlob;
      }

      const buffer = await blobToArrayBuffer(uploadBlob);
      if (buffer.byteLength > MAX_ATTACHMENT_BYTES) {
        throw new Error("file too large");
      }

      if (activeSessionRuntimeId) {
        await api.attachSessionFile(activeSessionId, {
          filename: uploadName,
          data_b64: bytesToBase64(new Uint8Array(buffer)),
          attachment_index: attachmentIndex,
        }, activeSessionRuntimeId);
      } else {
        await api.attachSessionFile(activeSessionId, {
          filename: uploadName,
          data_b64: bytesToBase64(new Uint8Array(buffer)),
          attachment_index: attachmentIndex,
        });
      }

      setAttachedFilesBySessionId((value) => ({
        ...value,
        [activeSessionId]: attachmentIndex,
      }));
    } catch (error) {
      console.error("Failed to attach file", error);
    } finally {
      setAttachmentUploading(false);
    }
  };

  const runMobileWaitAction = async (action: () => Promise<void>) => {
    if (mobileWaitSubmitting) {
      return;
    }
    setMobileWaitSubmitting(true);
    setMobileWaitError("");
    try {
      await action();
      await sessionsStoreApi.refresh();
    } catch (error) {
      setMobileWaitError(error instanceof Error ? error.message : "wait action failed");
    } finally {
      setMobileWaitSubmitting(false);
    }
  };

  const attachButtonTitle = !activeSessionId
    ? "Select a session first"
    : supervisorEnabled
      ? supervisorBlockReason
      : activeSessionIsPi
        ? "Attachments are not available for Pi sessions"
        : activeSessionIsCodex
          ? "Attachments are not available for Codex sessions"
          : attachmentUploading
            ? "Uploading attachment..."
            : "Attach file";

  return (
    <div className="composerStack space-y-3">
      {todoSnapshot ? (
        <TodoComposerPanel
          snapshot={todoSnapshot}
          expanded={visibleTodoExpanded}
          onToggle={() => {
            const currentSessionId = sessionsStoreApi.getState().activeSessionId;

            if (!currentSessionId) {
              return;
            }

            setTodoExpandedBySessionId((value) => ({
              ...value,
              [currentSessionId]: !value[currentSessionId],
            }));
          }}
        />
      ) : null}
      {compactMobile && visibleActiveWait && activeSessionId ? (
        <>
          <MobileWaitActionPanel
            wait={visibleActiveWait}
            disabled={mobileWaitSubmitting}
            onClaim={() => void runMobileWaitAction(() => waitsStoreApi.claimWait(activeSessionId, visibleActiveWait.wait_id, activeSessionRuntimeId))}
            onCancel={() => void runMobileWaitAction(() => waitsStoreApi.cancelWait(activeSessionId, visibleActiveWait.wait_id, activeSessionRuntimeId))}
            onAnswer={(answer) => void runMobileWaitAction(() => waitsStoreApi.answerWait(activeSessionId, visibleActiveWait.wait_id, answer, activeSessionRuntimeId))}
          />
          {mobileWaitError ? <p className="text-sm font-medium text-destructive">{mobileWaitError}</p> : null}
        </>
      ) : (
      <Card
        data-testid="composer-card"
        className="composerCard rounded-[1.5rem] border-border/70 bg-card/95 shadow-lg shadow-primary/5 backdrop-blur-sm"
      >
        <CardContent className="p-3 sm:p-4 space-y-2">
          {commandMenu}
          {activeSessionSendBlocked && activeSessionSendBlockReason ? <div className="composerModelError">{activeSessionSendBlockReason}</div> : null}
          {composerActionError ? <div className="composerActionError" role="alert">{composerActionError}</div> : null}
          <form
            className={cn("composer composerShell flex items-end gap-2 border-t-0", draft.includes("\n") && "multiline")}
            onSubmit={(event) => {
              event.preventDefault();
              submitCurrentDraft();
            }}
          >
            {!compactMobile ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="composerAttachButton"
                aria-label="Attach file"
                title={attachButtonTitle}
                disabled={!attachmentsSupported || attachmentUploading || sending || Boolean(visibleActiveWait)}
                onClick={handleAttachClick}
              >
                <span>Attach</span>
                {activeAttachmentCount > 0 ? <span className="composerAttachBadge">{activeAttachmentCount}</span> : null}
              </Button>
            ) : null}
            <input ref={fileInputRef} type="file" hidden tabIndex={-1} onChange={handleAttachChange} />
            <div className={cn("composerInputWrap flex-1", slashCommandDraft && "is-command") }>
              <Textarea
                textareaRef={textareaRef}
                value={draft}
                rows={mobileComposerAutosize ? 2 : undefined}
                placeholder={visibleActiveWait ? "Answer the active wait in Details" : "Enter your instructions here"}
                className="composerTextarea"
                onInput={(event) => {
                  syncComposerTextareaHeight(event.currentTarget, mobileComposerAutosize);
                  setComposerActionError("");
                  composerStoreApi.setDraft(activeSessionId, event.currentTarget.value);
                }}
                onKeyDown={(event) => {
                  if (commandMenuVisible) {
                    if (event.key === "ArrowDown") {
                      event.preventDefault();
                      setHighlightedCommandIndex((value) => (visibleCommands.length ? (value + 1) % visibleCommands.length : 0));
                      return;
                    }
                    if (event.key === "ArrowUp") {
                      event.preventDefault();
                      setHighlightedCommandIndex((value) => {
                        if (!visibleCommands.length) {
                          return 0;
                        }

                        return value <= 0 ? visibleCommands.length - 1 : value - 1;
                      });
                      return;
                    }
                    if (event.key === "Escape") {
                      event.preventDefault();
                      setSlashMenuDismissed(true);
                      return;
                    }
                    if (event.key === "Enter" && !event.shiftKey && !event.isComposing && visibleCommands.length) {
                      event.preventDefault();
                      applySlashCommand(visibleCommands[highlightedCommandIndex] ?? visibleCommands[0]);
                      return;
                    }
                  }
                  if (event.key !== "Enter" || event.isComposing) {
                    return;
                  }
                  if (event.shiftKey) {
                    return;
                  }
                  if (!enterToSendEnabled() && !event.ctrlKey && !event.metaKey) {
                    return;
                  }
                  if (!activeSessionId) {
                    return;
                  }
                  event.preventDefault();
                  submitCurrentDraft();
                }}
                disabled={sending || Boolean(visibleActiveWait)}
              />
            </div>
            <div className={cn("composerControlsColumn", compactMobile && "compactMobile") }>
              <div className={cn("composerControlsRow", compactMobile && "compactMobile")}>
                {activeSessionBusy ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="composerInterruptButton"
                    aria-label="Cancel current loop"
                    onClick={interruptCurrentLoop}
                  >
                    Cancel loop
                  </Button>
                ) : null}
                {!compactMobile ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="composerQueueButton"
                    aria-label="Queue message"
                    disabled={sending || Boolean(visibleActiveWait) || supervisorEnabled || !draft.trim()}
                    title={queueButtonTitle}
                    onClick={queueCurrentDraft}
                  >
                    {queueButtonLabel}
                  </Button>
                ) : null}
                <Button
                  type="submit"
                  className="sendButton"
                  aria-label={sendButtonLabel}
                  disabled={sending || activeSessionSendBlocked || !draft.trim()}
                  title={sendButtonTitle}
                >
                  <span className="buttonGlyph">➤</span>
                  <span className="visuallyHidden">{sending ? "Sending..." : sendButtonLabel}</span>
                </Button>
              </div>
            </div>
          </form>
          {commandMenuVisible && compactMobile ? (
            <div className="composerCommandSheetBackdrop" data-testid="composer-command-menu" onMouseDown={() => setSlashMenuDismissed(true)}>
              <div className="composerCommandSheet" role="dialog" aria-label="Slash commands" onMouseDown={(event) => event.stopPropagation()}>
                <div className="composerCommandSheetHeader">
                  <div>
                    <strong>Slash commands</strong>
                    <span>Filtering by /{slashQuery ?? ""}</span>
                  </div>
                  <Button type="button" variant="outline" size="sm" onClick={() => setSlashMenuDismissed(true)}>Close</Button>
                </div>
                {commandsLoading ? <div className="composerCommandHint">Loading Pi commands...</div> : null}
                {!commandsLoading ? (
                  <div className="composerCommandSheetList">
                    {visibleCommands.map((command, index) => (
                      <button
                        key={command.name}
                        type="button"
                        className={cn("composerCommandItem", index === highlightedCommandIndex && "is-active")}
                        onClick={() => applySlashCommand(command)}
                      >
                        <span className="composerCommandName">/{command.name}</span>
                        {command.description ? <span className="composerCommandDescription">{command.description}</span> : null}
                        {command.source ? <span className="composerCommandSource">{command.source}</span> : null}
                      </button>
                    ))}
                  </div>
                ) : null}
              </div>
            </div>
          ) : null}
        </CardContent>
      </Card>
      )}
    </div>
  );
}
