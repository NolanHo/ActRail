import type { JSX } from "preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { cn } from "@/lib/utils";

import { useSessionsStore, useSessionsStoreApi } from "../../app/providers";
import { api } from "../../lib/api";
import { backendCapability, backendSupportsReasoningEffort } from "../../lib/launch";
import {
  defaultModelFor,
  defaultProviderFor,
  defaultReasoningFor,
  modelChoicesForDefaults,
  providerChoicesForDefaults,
  reasoningChoicesForDefaults,
  uniqueStrings,
} from "../../lib/launch-options";
import { getSessionDisplayName } from "../../lib/session-display";
import type { CreateSessionResponse, SessionResumeCandidate, SessionResumeCandidatesResponse, SessionSummary } from "../../lib/types";

interface NewSessionDialogProps {
  open: boolean;
  onClose: () => void;
}

interface SessionCwdInfo {
  exists: boolean;
  willCreate: boolean;
  gitRepo: boolean;
  gitRoot: string;
  gitBranch: string;
}

type LaunchSettingField = "backend" | "model" | "providerChoice" | "reasoningEffort" | "createInTmux" | "fastMode";
type NewSessionSurfaceTab = "start" | "resume";

const DEFAULT_NEW_SESSION_CWD = "/root/docs";

function baseName(value: string) {
  const trimmed = value.trim().replace(/[\\/]+$/, "");
  if (!trimmed) return "";
  const parts = trimmed.split(/[\\/]+/);
  return parts[parts.length - 1] || "";
}

function initialCwdForDialog(activeSessionCwd: string | null | undefined, recentCwds: string[]) {
  const active = String(activeSessionCwd || "").trim();
  if (active) return active;
  const recent = recentCwds.find((item) => String(item || "").trim().length > 0)?.trim();
  return recent || DEFAULT_NEW_SESSION_CWD;
}

function buildOptimisticCreatedSession(
  response: CreateSessionResponse,
  fallback: { backend: string; cwd: string; name?: string },
): SessionSummary | null {
  const sessionId = String(response.session_id || "").trim();
  if (!sessionId) {
    return null;
  }
  const runtimeId = String(response.runtime_id || "").trim() || null;
  const alias = String(response.alias || fallback.name || "").trim() || undefined;
  return {
    session_id: sessionId,
    runtime_id: runtimeId,
    agent_backend: String(response.backend || fallback.backend || "").trim() || fallback.backend,
    cwd: fallback.cwd,
    alias,
    focused: response.focused === true,
    pending_startup: response.pending_startup === true,
    busy: response.pending_startup === true,
  };
}

const RESUME_PAGE_SIZE = 20;
const RESUME_SCAN_BATCH_SIZE = 20;

function resumeOptionLabel(item: SessionResumeCandidate) {
  const title = getSessionDisplayName(item as any, item.session_id.slice(0, 8));
  const branch = item.git_branch?.trim();
  return branch ? `${title} (${branch})` : title;
}

function resumeUpdatedLabel(item: SessionResumeCandidate) {
  const ts = Number(item.updated_ts || 0);
  if (!Number.isFinite(ts) || ts <= 0) return "";
  return `Modified ${new Date(ts * 1000).toLocaleString()}`;
}

function SelectField(props: JSX.SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
      {...props}
    />
  );
}

function ToggleField({
  label,
  name,
  checked,
  disabled,
  description,
  onChange,
}: {
  label: string;
  name?: string;
  checked: boolean;
  disabled?: boolean;
  description?: string;
  onChange: (checked: boolean) => void;
}) {
  return (
    <label className={cn("toggleOption flex cursor-pointer items-start gap-3 rounded-2xl border border-border/70 bg-background/80 px-3 py-3 text-sm", disabled && "cursor-not-allowed opacity-60")}>
      <input
        type="checkbox"
        name={name}
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.currentTarget.checked)}
      />
      <span className="space-y-1">
        <span className="block font-medium text-foreground">{label}</span>
        {description ? <span className="block text-muted-foreground">{description}</span> : null}
      </span>
    </label>
  );
}

export function NewSessionDialog({ open, onClose }: NewSessionDialogProps) {
  const { activeSessionId, bootstrapLoaded, items, newSessionDefaults, recentCwds, tmuxAvailable } = useSessionsStore();
  const sessionsStoreApi = useSessionsStoreApi();
  const [cwd, setCwd] = useState("");
  const [backend, setBackend] = useState("codex");
  const [sessionName, setSessionName] = useState("");
  const [surfaceTab, setSurfaceTab] = useState<NewSessionSurfaceTab>("start");
  const [model, setModel] = useState("");
  const [providerChoice, setProviderChoice] = useState("");
  const [reasoningEffort, setReasoningEffort] = useState("");
  const [createInTmux, setCreateInTmux] = useState(false);
  const [fastMode, setFastMode] = useState(false);
  const [usePIAgentGRPC, setUsePIAgentGRPC] = useState(true);
  const [resumeSessionId, setResumeSessionId] = useState("");
  const [resumeCandidates, setResumeCandidates] = useState<SessionResumeCandidate[]>([]);
  const [resumeOffset, setResumeOffset] = useState(0);
  const [resumeRemaining, setResumeRemaining] = useState(0);
  const [resumeScanRemaining, setResumeScanRemaining] = useState(0);
  const [resumeLoading, setResumeLoading] = useState(false);
  const [resumeTitleFilter, setResumeTitleFilter] = useState("");
  const [refreshingPiModels, setRefreshingPiModels] = useState(false);
  const [cwdInfo, setCwdInfo] = useState<SessionCwdInfo>({
    exists: false,
    willCreate: false,
    gitRepo: false,
    gitRoot: "",
    gitBranch: "",
  });
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [lookupError, setLookupError] = useState("");
  const wasOpenRef = useRef(false);
  const hydratedDefaultsRef = useRef(false);
  const touchedLaunchSettingsRef = useRef<Record<LaunchSettingField, boolean>>({
    backend: false,
    model: false,
    providerChoice: false,
    reasoningEffort: false,
    createInTmux: false,
    fastMode: false,
  });
  const submittingRef = useRef(false);

  const backendNames = useMemo(
    () => Object.keys(newSessionDefaults?.backends || { codex: {}, pi: {} }),
    [newSessionDefaults?.backends],
  );
  const backendDefaults = newSessionDefaults?.backends?.[backend] || {};
  const activeSession = useMemo(
    () => items.find((session) => session.session_id === activeSessionId) ?? null,
    [activeSessionId, items],
  );
  const providerChoices = useMemo(() => uniqueStrings([
    ...providerChoicesForDefaults(backendDefaults),
    providerChoice,
  ]), [backendDefaults, providerChoice]);
  const reasoningChoices = useMemo(() => uniqueStrings([
    ...reasoningChoicesForDefaults(backendDefaults, backend),
    reasoningEffort,
  ]), [backend, backendDefaults, reasoningEffort]);
  const modelChoices = useMemo(() => uniqueStrings([
    ...modelChoicesForDefaults(backendDefaults, backend, providerChoice),
    model,
  ]), [backend, backendDefaults, model, providerChoice]);
  const supportsFast = !!backendDefaults.supports_fast;
  const supportsTmux = tmuxAvailable;
  const supportsResumeHistory = backendCapability(newSessionDefaults, backend)?.resume_history === true;
  const sessionNamePlaceholder = baseName(cwd) || "session-name";
  const filteredResumeCandidates = useMemo(() => {
    const query = resumeTitleFilter.trim().toLowerCase();
    if (!query) {
      return resumeCandidates;
    }
    return resumeCandidates.filter((item) => [
      item.title,
      item.alias,
      item.display_name,
      item.first_user_message,
      item.session_id,
    ].some((value) => String(value || "").toLowerCase().includes(query)));
  }, [resumeCandidates, resumeTitleFilter]);
  const selectedResumeCandidate = filteredResumeCandidates.find((item) => item.session_id === resumeSessionId) ?? null;
  const dialogTitleId = "new-session-dialog-title";

  useEffect(() => {
    if (!open) {
      wasOpenRef.current = false;
      hydratedDefaultsRef.current = false;
      touchedLaunchSettingsRef.current = {
        backend: false,
        model: false,
        providerChoice: false,
        reasoningEffort: false,
        createInTmux: false,
        fastMode: false,
      };
      return;
    }
    if (wasOpenRef.current) {
      return;
    }
    wasOpenRef.current = true;
    const initialBackend = newSessionDefaults?.default_backend || "codex";
    const initialDefaults = newSessionDefaults?.backends?.[initialBackend] || {};
    const initialProvider = defaultProviderFor(initialDefaults);
    hydratedDefaultsRef.current = Object.keys(newSessionDefaults?.backends || {}).length > 0;
    setCwd(initialCwdForDialog(activeSession?.cwd, recentCwds));
    setBackend(initialBackend);
    setSessionName("");
    setSurfaceTab("start");
    setProviderChoice(initialProvider);
    setModel(defaultModelFor(initialDefaults, initialBackend, initialProvider));
    setReasoningEffort(defaultReasoningFor(initialDefaults, initialBackend));
    setCreateInTmux(Boolean(tmuxAvailable));
    setFastMode(String(initialDefaults.service_tier || "").trim().toLowerCase() === "fast");
    setUsePIAgentGRPC(newSessionDefaults?.pi_agent_grpc_default !== false);
    setResumeSessionId("");
    setResumeCandidates([]);
    setResumeOffset(0);
    setResumeRemaining(0);
    setResumeScanRemaining(0);
    setResumeLoading(false);
    setResumeTitleFilter("");
    setRefreshingPiModels(false);
    setCwdInfo({ exists: false, willCreate: false, gitRepo: false, gitRoot: "", gitBranch: "" });
    setSubmitting(false);
    setError("");
    setLookupError("");
  }, [activeSession?.cwd, newSessionDefaults, open, recentCwds, tmuxAvailable]);

  useEffect(() => {
    if (!open || hydratedDefaultsRef.current) {
      return;
    }

    const backendNames = Object.keys(newSessionDefaults?.backends || {});
    if (!backendNames.length) {
      return;
    }

    hydratedDefaultsRef.current = true;
    const defaultBackend = newSessionDefaults?.default_backend || backendNames[0] || "codex";
    const selectedBackend = touchedLaunchSettingsRef.current.backend && backend ? backend : defaultBackend;
    const defaultValues = newSessionDefaults?.backends?.[selectedBackend] || {};
    const defaultProvider = defaultProviderFor(defaultValues);

    if (!touchedLaunchSettingsRef.current.backend) {
      setBackend(selectedBackend);
    }
    if (!touchedLaunchSettingsRef.current.model) {
      setModel(defaultModelFor(defaultValues, selectedBackend, defaultProvider));
    }
    if (!touchedLaunchSettingsRef.current.providerChoice) {
      setProviderChoice(defaultProvider);
    }
    if (!touchedLaunchSettingsRef.current.reasoningEffort) {
      setReasoningEffort(defaultReasoningFor(defaultValues, selectedBackend));
    }
    if (!touchedLaunchSettingsRef.current.createInTmux) {
      setCreateInTmux(Boolean(tmuxAvailable));
    }
    if (!touchedLaunchSettingsRef.current.fastMode) {
      setFastMode(String(defaultValues.service_tier || "").trim().toLowerCase() === "fast");
    }
  }, [backend, open, newSessionDefaults, tmuxAvailable]);

  useEffect(() => {
    if (!supportsFast && fastMode) {
      setFastMode(false);
    }
  }, [fastMode, supportsFast]);

  useEffect(() => {
    if (!supportsTmux && createInTmux) {
      setCreateInTmux(false);
    }
  }, [createInTmux, supportsTmux]);

  useEffect(() => {
    if (surfaceTab !== "resume" || supportsResumeHistory) {
      return;
    }
    setSurfaceTab("start");
    setResumeSessionId("");
    setResumeCandidates([]);
    setResumeOffset(0);
    setResumeRemaining(0);
    setResumeScanRemaining(0);
    setResumeLoading(false);
    setLookupError("");
  }, [surfaceTab, supportsResumeHistory]);

  useEffect(() => {
    if (!open || bootstrapLoaded) {
      return;
    }
    sessionsStoreApi.refreshBootstrap().catch(() => undefined);
  }, [bootstrapLoaded, open, sessionsStoreApi]);

  useEffect(() => {
    if (!open) return;
    setResumeOffset(0);
    setResumeCandidates([]);
    setResumeSessionId("");
    setResumeRemaining(0);
    setResumeScanRemaining(0);
    setLookupError("");
  }, [backend, cwd, open, surfaceTab]);

  useEffect(() => {
    if (!open) return;
    if (surfaceTab !== "resume" || !supportsResumeHistory) {
      setResumeCandidates([]);
      setResumeSessionId("");
      setResumeRemaining(0);
      setResumeScanRemaining(0);
      setResumeLoading(false);
      setLookupError("");
      return;
    }
    const rawCwd = cwd.trim();
    if (!rawCwd) {
      setResumeCandidates([]);
      setResumeSessionId("");
      setResumeRemaining(0);
      setResumeScanRemaining(0);
      setResumeLoading(false);
      setLookupError("");
      setCwdInfo({ exists: false, willCreate: false, gitRepo: false, gitRoot: "", gitBranch: "" });
      return;
    }

    let cancelled = false;
    setResumeLoading(true);
    const timeoutId = window.setTimeout(async () => {
      try {
        const result: SessionResumeCandidatesResponse = await api.getSessionResumeCandidates(rawCwd, backend, {
          offset: 0,
          limit: 0,
          scanOffset: resumeOffset,
          scanLimit: RESUME_SCAN_BATCH_SIZE,
        });
        if (cancelled) return;
        const page = Array.isArray(result.sessions) ? result.sessions : [];
        const scanRemaining = Math.max(0, Number(result.scan_remaining || 0));
        setResumeCandidates(page);
        setResumeRemaining(scanRemaining);
        setResumeScanRemaining(scanRemaining);
        setResumeSessionId((current) => {
          if (!current) return "";
          return page.some((item) => item.session_id === current) ? current : "";
        });
        setCwdInfo({
          exists: !!result.exists,
          willCreate: !!result.will_create,
          gitRepo: !!result.git_repo,
          gitRoot: result.git_root || "",
          gitBranch: result.git_branch || "",
        });
        setLookupError("");
      } catch (loadError) {
        if (cancelled) return;
        setResumeCandidates([]);
        setResumeSessionId("");
        setResumeRemaining(0);
        setResumeScanRemaining(0);
        setCwdInfo({ exists: false, willCreate: false, gitRepo: false, gitRoot: "", gitBranch: "" });
        setLookupError(loadError instanceof Error ? loadError.message : "Failed to inspect working directory");
      } finally {
        if (!cancelled) {
          setResumeLoading(false);
        }
      }
    }, 180);

    return () => {
      cancelled = true;
      window.clearTimeout(timeoutId);
    };
  }, [backend, cwd, open, resumeOffset, surfaceTab, supportsResumeHistory]);

  if (!open) return null;

  const cwdHint = cwd.trim()
    ? resumeLoading
      ? "Inspecting..."
      : cwdInfo.gitRepo
        ? `Git${cwdInfo.gitBranch ? ` · ${cwdInfo.gitBranch}` : ""}${cwdInfo.gitRoot ? ` · ${cwdInfo.gitRoot}` : ""}`
        : cwdInfo.exists
          ? "Dir exists. Start creates fresh."
          : cwdInfo.willCreate
            ? "Will create dir."
            : ""
    : "";

  const markLaunchSettingTouched = (field: LaunchSettingField) => {
    touchedLaunchSettingsRef.current[field] = true;
  };

  const applyBackend = (nextBackend: string) => {
    touchedLaunchSettingsRef.current.backend = true;
    const nextDefaults = newSessionDefaults?.backends?.[nextBackend] || {};
    const nextProvider = defaultProviderFor(nextDefaults);
    const hasLaunchDefaults = Boolean(
      nextDefaults.model
      || nextDefaults.provider_choice
      || nextDefaults.reasoning_effort
      || nextDefaults.service_tier
      || Object.keys(nextDefaults.provider_models || {}).length
      || providerChoicesForDefaults(nextDefaults).length
      || reasoningChoicesForDefaults(nextDefaults, nextBackend).length,
    );

    if (hasLaunchDefaults) {
      touchedLaunchSettingsRef.current.model = false;
      touchedLaunchSettingsRef.current.providerChoice = false;
      touchedLaunchSettingsRef.current.reasoningEffort = false;
      touchedLaunchSettingsRef.current.createInTmux = false;
      touchedLaunchSettingsRef.current.fastMode = false;
    }
    setBackend(nextBackend);
    setUsePIAgentGRPC(nextBackend === "pi" && newSessionDefaults?.pi_agent_grpc_default !== false);
    setProviderChoice(nextProvider);
    setModel(defaultModelFor(nextDefaults, nextBackend, nextProvider));
    setReasoningEffort(defaultReasoningFor(nextDefaults, nextBackend));
    setFastMode(String(nextDefaults.service_tier || "").trim().toLowerCase() === "fast");
    setCreateInTmux(Boolean(tmuxAvailable));
    setResumeSessionId("");
    setResumeOffset(0);
    setResumeRemaining(0);
    setError("");
  };

  const applyProviderChoice = (nextProviderChoice: string) => {
    markLaunchSettingTouched("providerChoice");
    setProviderChoice(nextProviderChoice);
    if (touchedLaunchSettingsRef.current.model) {
      return;
    }
    setModel(defaultModelFor(backendDefaults, backend, nextProviderChoice));
  };

  const refreshPiModels = async () => {
    if (refreshingPiModels) {
      return;
    }
    setRefreshingPiModels(true);
    setError("");
    try {
      await sessionsStoreApi.refreshBootstrap({ refreshPiModels: true });
    } catch (refreshError) {
      setError(refreshError instanceof Error ? refreshError.message : "Failed to refresh Pi models");
    } finally {
      setRefreshingPiModels(false);
    }
  };

  return (
    <Dialog open={open}>
      <div data-testid="new-session-dialog" className="w-full max-w-3xl">
        <DialogContent titleId={dialogTitleId} className="newSessionDialog max-h-[88dvh] overflow-hidden border-border/70 bg-card/95 p-0 shadow-2xl shadow-primary/10">
          <DialogHeader className="space-y-4 p-6 pb-5">
            <div className="newSessionHeaderLead">
              <div className="space-y-3">
                <div className="space-y-1">
                  <DialogTitle id={dialogTitleId}>New session</DialogTitle>
                  <p className="text-sm text-muted-foreground">Launch a backend in a project directory.</p>
                </div>
                <div className="newSessionMeta flex flex-wrap gap-2">
                  <Badge variant="secondary" className="capitalize">{backend}</Badge>
                  {supportsFast ? <Badge variant="outline">Fast available</Badge> : null}
                  {supportsTmux ? <Badge variant="outline">tmux ready</Badge> : null}
                </div>
              </div>
              <div className="agentBackendTabs grid min-w-[14rem] grid-cols-2 gap-2 rounded-2xl bg-muted/60 p-1">
                {backendNames.map((backendName) => (
                  <Button
                    key={backendName}
                    type="button"
                    variant={backend === backendName ? "default" : "ghost"}
                    data-testid={`backend-tab-${backendName}`}
                    className="backendOptionButton h-11 rounded-[1rem] capitalize"
                    onClick={() => applyBackend(backendName)}
                  >
                    {backendName}
                  </Button>
                ))}
              </div>
            </div>
            <div className="newSessionSurfaceTabs grid w-full max-w-md grid-cols-2 gap-2 rounded-2xl bg-muted/60 p-1">
              <Button
                type="button"
                variant={surfaceTab === "start" ? "default" : "ghost"}
                className="h-10 rounded-[1rem]"
                onClick={() => setSurfaceTab("start")}
              >
                Start
              </Button>
              {supportsResumeHistory ? (
                <Button
                  type="button"
                  variant={surfaceTab === "resume" ? "default" : "ghost"}
                  className="h-10 rounded-[1rem]"
                  onClick={() => setSurfaceTab("resume")}
                >
                  Resume
                </Button>
              ) : null}
            </div>
          </DialogHeader>

          <Separator className="bg-border/70" />

          <form
            className="newSessionForm flex max-h-[calc(88dvh-8.5rem)] flex-col"
            onSubmit={async (event) => {
              event.preventDefault();
              const trimmedCwd = cwd.trim();
              if (!trimmedCwd) {
                setError("Working directory is required.");
                return;
              }
              const selectedResumeId = surfaceTab === "resume" ? resumeSessionId : "";
              if (surfaceTab === "resume" && !selectedResumeId) {
                setError("Select a resume candidate first.");
                return;
              }
              if (selectedResumeId && !filteredResumeCandidates.some((item) => item.session_id === selectedResumeId)) {
                setError("Selected resume conversation is no longer available for this directory.");
                return;
              }

              if (submittingRef.current) {
                return;
              }

              submittingRef.current = true;
              setSubmitting(true);
              setError("");
              try {
                const trimmedSessionName = surfaceTab === "start" ? sessionName.trim() || undefined : undefined;
                const response = await api.createSession({
                  cwd: trimmedCwd,
                  title: trimmedSessionName,
                  agent_backend: backend,
                  resume_session_id: selectedResumeId || undefined,
                  provider: providerChoice.trim() || backendDefaults.provider_choice?.trim() || undefined,
                  model: model.trim() || undefined,
                  reasoning_effort: backendSupportsReasoningEffort(backend, newSessionDefaults) ? reasoningEffort.trim() || undefined : undefined,
                  pi_agent_grpc: backend === "pi" ? usePIAgentGRPC : undefined,
                });
                const optimisticSession = buildOptimisticCreatedSession(response, {
                  backend,
                  cwd: trimmedCwd,
                  name: trimmedSessionName,
                });
                if (optimisticSession) {
                  sessionsStoreApi.upsertSession(optimisticSession, { prepend: true, select: true });
                  void sessionsStoreApi.refreshBootstrap().catch(() => undefined);
                  onClose();
                  return;
                }
                await sessionsStoreApi.refresh();
                const returnedSessionId = String(response.session_id || "").trim();
                let createdSessionId = returnedSessionId || "";
                if (!createdSessionId) {
                  const brokerPid = typeof response.broker_pid === "number" ? response.broker_pid : null;
                  const matched = brokerPid === null
                    ? undefined
                    : sessionsStoreApi.getState().items.find((session) => session.broker_pid === brokerPid);
                  createdSessionId = matched?.session_id || "";
                }
                await sessionsStoreApi.refreshBootstrap();
                if (createdSessionId) {
                  sessionsStoreApi.select(createdSessionId);
                }
                onClose();
              } catch (submitError) {
                setError(submitError instanceof Error ? submitError.message : "Failed to create session");
              } finally {
                submittingRef.current = false;
                setSubmitting(false);
              }
            }}
          >
            <div className="newSessionFormBody space-y-5 overflow-y-auto px-6 py-5">
              {surfaceTab === "resume" ? (
                <section className="dialogSection space-y-4">
                  <div>
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <h3 className="text-sm font-semibold text-foreground">Resume backend history</h3>
                      <Badge variant="outline">{resumeLoading ? "Loading" : `${filteredResumeCandidates.length} candidates`}</Badge>
                    </div>
                    <p className="mt-1 text-sm text-muted-foreground">Create a new ActRail slot from an exact backend history identity.</p>
                  </div>
                  <div className="fieldGrid twoCol gap-3">
                    <label className="fieldBlock space-y-2">
                      <span className="fieldLabel">Working directory</span>
                      <Input
                        name="cwd"
                        value={cwd}
                        onInput={(event) => setCwd(event.currentTarget.value)}
                        onChange={(event) => setCwd(event.currentTarget.value)}
                        placeholder="/path/to/project"
                        list="new-session-recent-cwds"
                      />
                    </label>
                    <label className="fieldBlock space-y-2">
                      <span className="fieldLabel">Title contains</span>
                      <Input
                        name="resumeTitle"
                        value={resumeTitleFilter}
                        onInput={(event) => setResumeTitleFilter(event.currentTarget.value)}
                        onChange={(event) => setResumeTitleFilter(event.currentTarget.value)}
                        placeholder="Session title or first user text"
                      />
                    </label>
                  </div>
                  {recentCwds.length ? (
                    <datalist id="new-session-recent-cwds">
                      {recentCwds.map((recentCwd) => (
                        <option key={recentCwd} value={recentCwd} />
                      ))}
                    </datalist>
                  ) : null}
                  {cwdHint ? <p className="fieldHint text-sm text-muted-foreground">{cwdHint}</p> : null}
                  {lookupError ? <p className="errorText text-sm font-medium">{lookupError}</p> : null}
                  {filteredResumeCandidates.length ? (
                    <div className="focusSessionList">
                      {filteredResumeCandidates.map((item) => (
                        <button
                          key={item.session_id}
                          type="button"
                          className={cn("focusSessionItem", resumeSessionId === item.session_id && "ring-1 ring-primary")}
                          onClick={() => setResumeSessionId(item.session_id)}
                        >
                          <span className="focusSessionTitle">{resumeOptionLabel(item)}</span>
                          <span className="focusSessionMeta">
                            {item.session_id}
                            {item.cwd?.trim() ? ` · ${item.cwd.trim()}` : ""}
                            {resumeUpdatedLabel(item) ? ` · ${resumeUpdatedLabel(item)}` : ""}
                          </span>
                          {item.first_user_message?.trim() ? (
                            <span className="focusSessionMeta">{item.first_user_message.trim()}</span>
                          ) : null}
                        </button>
                      ))}
                    </div>
                  ) : (
                    <div className="focusSessionEmpty">{resumeLoading ? "Loading resumable sessions..." : "No matching backend history found for this directory."}</div>
                  )}
                  {resumeCandidates.length || resumeOffset > 0 || resumeRemaining > 0 ? (
                    <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                      <span>
                        {resumeCandidates.length
                          ? `Showing ${resumeOffset + 1}-${resumeOffset + resumeCandidates.length}`
                          : `Showing ${resumeOffset + 1}-${resumeOffset}`}
                        {resumeRemaining > 0 ? `, ${resumeRemaining} older` : ""}
                        {resumeScanRemaining > 0 ? ` (${resumeScanRemaining} uninspected)` : ""}
                      </span>
                      <div className="flex items-center gap-2">
                        <Button
                          type="button"
                          variant="ghost"
                          className="h-8 px-2"
                          disabled={resumeLoading || resumeOffset <= 0}
                          onClick={() => setResumeOffset((current) => Math.max(0, current - RESUME_PAGE_SIZE))}
                        >
                          Newer
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          className="h-8 px-2"
                          disabled={resumeLoading || resumeRemaining <= 0}
                          onClick={() => setResumeOffset((current) => current + RESUME_PAGE_SIZE)}
                        >
                          Older
                        </Button>
                      </div>
                    </div>
                  ) : null}
                  {selectedResumeCandidate ? (
                    <p className="fieldHint text-sm text-muted-foreground">Selected: {resumeOptionLabel(selectedResumeCandidate)}</p>
                  ) : null}
                </section>
              ) : (
                <>
              <section className="dialogSection space-y-3">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div>
                    <h3 className="text-sm font-semibold text-foreground">Project target</h3>
                    <p className="mt-1 text-sm text-muted-foreground">Pick the working directory for the new runtime.</p>
                  </div>
                </div>
                <label className="fieldBlock space-y-2">
                  <span className="fieldLabel">Working directory</span>
                  <Input
                    name="cwd"
                    value={cwd}
                    onInput={(event) => setCwd(event.currentTarget.value)}
                    onChange={(event) => setCwd(event.currentTarget.value)}
                    placeholder="/path/to/project"
                    list="new-session-recent-cwds"
                  />
                </label>
                {recentCwds.length ? (
                  <datalist id="new-session-recent-cwds">
                    {recentCwds.map((recentCwd) => (
                      <option key={recentCwd} value={recentCwd} />
                    ))}
                  </datalist>
                ) : null}
                {cwdHint ? <p className="fieldHint text-sm text-muted-foreground">{cwdHint}</p> : null}
                {lookupError ? <p className="errorText text-sm font-medium">{lookupError}</p> : null}
                <div className="fieldGrid twoCol gap-3">
                  <label className="fieldBlock space-y-2">
                    <span className="fieldLabel">Session name</span>
                    <Input
                      name="sessionName"
                      value={sessionName}
                      onInput={(event) => setSessionName(event.currentTarget.value)}
                      onChange={(event) => setSessionName(event.currentTarget.value)}
                      placeholder={sessionNamePlaceholder}
                    />
                  </label>
                </div>
              </section>

              <Separator className="bg-border/70" />

              <section className="dialogSection space-y-3">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div>
                    <h3 className="text-sm font-semibold text-foreground">Model settings</h3>
                    <p className="mt-1 text-sm text-muted-foreground">Tune provider, model, and reasoning without leaving the launch flow.</p>
                  </div>
                  {backend === "pi" ? (
                    <Button
                      type="button"
                      variant="outline"
                      className="h-8 px-3"
                      disabled={refreshingPiModels}
                      onClick={() => {
                        void refreshPiModels();
                      }}
                    >
                      {refreshingPiModels ? "Refreshing Pi models..." : "Refresh Pi models"}
                    </Button>
                  ) : null}
                </div>
                <div className="fieldGrid threeCol gap-3">
                  <label className="fieldBlock space-y-2">
                    <span className="fieldLabel">Model</span>
                    <Input
                      name="model"
                      value={model}
                      onInput={(event) => {
                        markLaunchSettingTouched("model");
                        setModel(event.currentTarget.value);
                      }}
                      onChange={(event) => {
                        markLaunchSettingTouched("model");
                        setModel(event.currentTarget.value);
                      }}
                      placeholder="default"
                      list="new-session-models"
                    />
                  </label>
                  {modelChoices.length ? (
                    <datalist id="new-session-models">
                      {modelChoices.map((modelOption) => (
                        <option key={modelOption} value={modelOption} />
                      ))}
                    </datalist>
                  ) : null}
                  <label className="fieldBlock space-y-2">
                    <span className="fieldLabel">Reasoning effort</span>
                    <SelectField
                      name="reasoningEffort"
                      value={reasoningEffort}
                      onInput={(event) => {
                        markLaunchSettingTouched("reasoningEffort");
                        setReasoningEffort(event.currentTarget.value);
                      }}
                      onChange={(event) => {
                        markLaunchSettingTouched("reasoningEffort");
                        setReasoningEffort(event.currentTarget.value);
                      }}
                    >
                      {reasoningChoices.map((value) => (
                        <option key={value} value={value}>
                          {value}
                        </option>
                      ))}
                    </SelectField>
                  </label>
                  <div className="fieldBlock space-y-2">
                    <span className="fieldLabel">Speed</span>
                    <ToggleField
                      label="Fast"
                      name="fastMode"
                      checked={fastMode}
                      disabled={!supportsFast}
                      description={supportsFast ? "Use the backend's faster service tier when available." : "This backend does not expose a fast tier."}
                      onChange={(checked) => {
                        markLaunchSettingTouched("fastMode");
                        setFastMode(checked);
                      }}
                    />
                  </div>
                </div>
                <div className="fieldGrid twoCol gap-3">
                  <label className="fieldBlock space-y-2">
                    <span className="fieldLabel">Provider</span>
                    <SelectField
                      name="providerChoice"
                      value={providerChoice}
                      onInput={(event) => {
                        applyProviderChoice(event.currentTarget.value);
                      }}
                      onChange={(event) => {
                        applyProviderChoice(event.currentTarget.value);
                      }}
                    >
                      {providerChoices.map((value) => (
                        <option key={value} value={value}>
                          {value}
                        </option>
                      ))}
                    </SelectField>
                  </label>
                  <div className="fieldBlock space-y-2">
                    <span className="fieldLabel">Launch mode</span>
                    <ToggleField
                      label={supportsTmux ? "Create in tmux" : "tmux unavailable"}
                      name="createInTmux"
                      checked={createInTmux}
                      disabled={!supportsTmux}
                      description={supportsTmux ? (backend === "pi" ? "Host the new Pi session in tmux while pi-rpc handles web control." : "Keep the new session attached to a tmux pane.") : "tmux is unavailable on this host."}
                      onChange={(checked) => {
                        markLaunchSettingTouched("createInTmux");
                        setCreateInTmux(checked);
                      }}
                    />
                  </div>
                  {backend === "pi" ? (
                    <div className="fieldBlock space-y-2">
                      <span className="fieldLabel">Pi transport</span>
                      <ToggleField
                        label="Use gRPC IPC"
                        name="usePIAgentGRPC"
                        checked={usePIAgentGRPC}
                        description="Launch Pi with --mode grpc and connect ActRail directly to the local Unix socket. Disable to use the existing IOD helper path."
                        onChange={setUsePIAgentGRPC}
                      />
                    </div>
                  ) : null}
                </div>
              </section>

                </>
              )}
              {error ? <p className="errorText text-sm font-medium">{error}</p> : null}
            </div>

            <Separator className="bg-border/70" />

            <div className="newSessionFooter flex items-center justify-end gap-3 px-6 py-4">
              <Button type="button" variant="outline" onClick={onClose} disabled={submitting}>
                Cancel
              </Button>
              <Button type="submit" disabled={submitting || !cwd.trim() || (surfaceTab === "resume" && !resumeSessionId)}>
                {submitting ? (surfaceTab === "resume" ? "Resuming..." : "Launching...") : (surfaceTab === "resume" ? "Resume" : "Start session")}
              </Button>
            </div>
          </form>
        </DialogContent>
      </div>
    </Dialog>
  );
}
