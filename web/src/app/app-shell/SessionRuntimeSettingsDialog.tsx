import type { JSX } from "preact";
import { useEffect, useMemo, useState } from "preact/hooks";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";

import { api } from "../../lib/api";
import { backendSupportsReasoningEffort, normalizeLaunchBackend } from "../../lib/launch";
import {
  defaultModelFor,
  defaultPiModelForProvider,
  defaultProviderFor,
  defaultReasoningFor,
  modelChoicesForDefaults,
  providerChoicesForDefaults,
  reasoningChoicesForDefaults,
  uniqueStrings,
} from "../../lib/launch-options";
import { getSessionRuntimeId } from "../../lib/session-identity";
import type { LaunchBackendDefaults, NewSessionDefaults, SessionSummary, SwitchSessionModelResponse } from "../../lib/types";

interface SessionRuntimeSettingsDialogProps {
  defaults: NewSessionDefaults | null;
  open: boolean;
  session: SessionSummary | null;
  onClose(): void;
  onRefreshDefaults?(): Promise<void>;
  onSaved(response: SwitchSessionModelResponse): void | Promise<void>;
}

const EMPTY_BACKEND_DEFAULTS: LaunchBackendDefaults = {};

function SelectField(props: JSX.SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
      {...props}
    />
  );
}

function trimValue(value: string | null | undefined) {
  return String(value || "").trim();
}

function responseStatusMessage(response: SwitchSessionModelResponse) {
  const message = trimValue(response.message);
  if (message) {
    return message;
  }
  if (response.restart_required) {
    return "Settings saved for the next restart or handoff.";
  }
  if (response.apply_status === "unchanged") {
    return "Settings unchanged.";
  }
  return "Settings saved.";
}

export function SessionRuntimeSettingsDialog({
  defaults,
  open,
  session,
  onClose,
  onRefreshDefaults,
  onSaved,
}: SessionRuntimeSettingsDialogProps) {
  const [providerChoice, setProviderChoice] = useState("");
  const [model, setModel] = useState("");
  const [reasoningEffort, setReasoningEffort] = useState("");
  const [refreshingDefaults, setRefreshingDefaults] = useState(false);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState("");
  const [savedSettings, setSavedSettings] = useState<{ provider: string; model: string; reasoningEffort: string } | null>(null);

  const backend = normalizeLaunchBackend(session?.agent_backend);
  const backendDefaults = useMemo(
    () => defaults?.backends?.[backend] || EMPTY_BACKEND_DEFAULTS,
    [backend, defaults],
  );
  const supportsReasoningEffort = backendSupportsReasoningEffort(backend, defaults);
  const providerChoices = useMemo(() => uniqueStrings([
    ...providerChoicesForDefaults(backendDefaults),
    providerChoice,
  ]), [backendDefaults, providerChoice]);
  const modelChoices = useMemo(() => uniqueStrings([
    ...modelChoicesForDefaults(backendDefaults, backend, providerChoice),
    model,
  ]), [backend, backendDefaults, model, providerChoice]);
  const reasoningChoices = useMemo(() => uniqueStrings([
    ...reasoningChoicesForDefaults(backendDefaults, backend),
    reasoningEffort,
  ]), [backend, backendDefaults, reasoningEffort]);
  const sessionProvider = trimValue(session?.provider_choice) || defaultProviderFor(backendDefaults);
  const initialProvider = savedSettings?.provider ?? sessionProvider;
  const initialModel = savedSettings?.model ?? (trimValue(session?.model) || defaultModelFor(backendDefaults, backend, initialProvider));
  const initialReasoningEffort = supportsReasoningEffort
    ? savedSettings?.reasoningEffort ?? (trimValue(session?.reasoning_effort) || defaultReasoningFor(backendDefaults, backend))
    : "";
  const providerChanged = providerChoice !== initialProvider;
  const modelChanged = model.trim() !== initialModel;
  const reasoningChanged = supportsReasoningEffort && reasoningEffort !== initialReasoningEffort;
  const hasChanges = providerChanged || modelChanged || reasoningChanged;
  const canSave = Boolean(session && model.trim() && hasChanges && !saving);
  const datalistId = session ? `session-runtime-models-${session.session_id}` : "session-runtime-models";

  useEffect(() => {
    if (!open || !session) {
      setSaving(false);
      setRefreshingDefaults(false);
      setStatus("");
      setSavedSettings(null);
      return;
    }
    const nextProvider = trimValue(session.provider_choice) || defaultProviderFor(backendDefaults);
    setProviderChoice(nextProvider);
    setModel(trimValue(session.model) || defaultModelFor(backendDefaults, backend, nextProvider));
    setReasoningEffort(supportsReasoningEffort
      ? trimValue(session.reasoning_effort) || defaultReasoningFor(backendDefaults, backend)
      : "");
    setSaving(false);
    setRefreshingDefaults(false);
    setStatus("");
    setSavedSettings(null);
  }, [backend, backendDefaults, open, session?.session_id, supportsReasoningEffort]);

  if (!open || !session) {
    return null;
  }

  const applyProviderChoice = (nextProviderChoice: string) => {
    setProviderChoice(nextProviderChoice);
    if (backend === "pi" && !model.trim()) {
      setModel(defaultPiModelForProvider(backendDefaults, nextProviderChoice));
    }
  };

  const save = async () => {
    if (!canSave) {
      return;
    }
    setSaving(true);
    setStatus("");
    try {
      const payload: { model?: string; provider?: string; reasoning_effort?: string } = {};
      if (modelChanged) {
        payload.model = model.trim();
      }
      if (providerChanged) {
        payload.provider = providerChoice.trim();
      }
      if (reasoningChanged) {
        payload.reasoning_effort = reasoningEffort.trim();
      }
      const response = await api.switchSessionModel(session.session_id, payload, getSessionRuntimeId(session));
      const nextProvider = trimValue(response.provider) || providerChoice;
      const nextModel = trimValue(response.model) || model.trim();
      const nextReasoningEffort = trimValue(response.reasoning_effort) || reasoningEffort;
      setProviderChoice(nextProvider);
      setModel(nextModel);
      setReasoningEffort(nextReasoningEffort);
      setSavedSettings({ provider: nextProvider, model: nextModel, reasoningEffort: nextReasoningEffort });
      setStatus(responseStatusMessage(response));
      await onSaved(response);
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Unable to save runtime settings");
    } finally {
      setSaving(false);
    }
  };

  const refreshDefaults = async () => {
    if (!onRefreshDefaults || refreshingDefaults) {
      return;
    }
    setRefreshingDefaults(true);
    setStatus("");
    try {
      await onRefreshDefaults();
      setStatus("Model list refreshed.");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Unable to refresh model list");
    } finally {
      setRefreshingDefaults(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (!nextOpen && !saving) {
        onClose();
      }
    }}>
      <div data-testid="session-runtime-settings-dialog" className="w-full max-w-2xl">
        <DialogContent titleId="session-runtime-settings-title" className="sessionRuntimeSettingsDialog max-h-[88dvh] overflow-hidden border-border/70 bg-card/95 p-0 shadow-2xl shadow-primary/10">
          <DialogHeader className="p-5 pb-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="space-y-2">
                <DialogTitle id="session-runtime-settings-title">Runtime settings</DialogTitle>
                <p className="text-sm text-muted-foreground">
                  Update the saved provider, model, and supported reasoning setting for this session.
                </p>
              </div>
              {backend === "pi" && onRefreshDefaults ? (
                <Button
                  type="button"
                  variant="outline"
                  className="h-8 px-3"
                  disabled={refreshingDefaults}
                  onClick={() => {
                    void refreshDefaults();
                  }}
                >
                  {refreshingDefaults ? "Refreshing..." : "Refresh Pi models"}
                </Button>
              ) : null}
            </div>
          </DialogHeader>

          <Separator className="bg-border/70" />

          <form
            className="sessionRuntimeSettingsForm"
            onSubmit={(event) => {
              event.preventDefault();
              void save();
            }}
          >
            <div className="sessionRuntimeSettingsBody">
              <div className="fieldGrid twoCol gap-3">
                {providerChoices.length ? (
                  <label className="fieldBlock space-y-2">
                    <span className="fieldLabel">Provider</span>
                    <SelectField
                      name="provider"
                      value={providerChoice}
                      onInput={(event) => applyProviderChoice(event.currentTarget.value)}
                      onChange={(event) => applyProviderChoice(event.currentTarget.value)}
                    >
                      {providerChoices.map((value) => (
                        <option key={value} value={value}>
                          {value}
                        </option>
                      ))}
                    </SelectField>
                  </label>
                ) : null}
                <label className="fieldBlock space-y-2">
                  <span className="fieldLabel">Model</span>
                  <Input
                    name="model"
                    value={model}
                    onInput={(event) => setModel(event.currentTarget.value)}
                    onChange={(event) => setModel(event.currentTarget.value)}
                    placeholder="model"
                    list={modelChoices.length ? datalistId : undefined}
                    data-testid="session-runtime-model-input"
                  />
                </label>
                {modelChoices.length ? (
                  <datalist id={datalistId}>
                    {modelChoices.map((modelOption) => (
                      <option key={modelOption} value={modelOption} />
                    ))}
                  </datalist>
                ) : null}
                {supportsReasoningEffort ? (
                  <label className="fieldBlock space-y-2">
                    <span className="fieldLabel">Reasoning effort</span>
                    <SelectField
                      name="reasoningEffort"
                      value={reasoningEffort}
                      onInput={(event) => setReasoningEffort(event.currentTarget.value)}
                      onChange={(event) => setReasoningEffort(event.currentTarget.value)}
                      data-testid="session-runtime-reasoning-select"
                    >
                      {reasoningChoices.map((value) => (
                        <option key={value} value={value}>
                          {value}
                        </option>
                      ))}
                    </SelectField>
                  </label>
                ) : null}
              </div>

              <p className="fieldHint text-sm text-muted-foreground">
                Current runtime keeps its launched settings until the next restart or handoff unless backend live switching is added.
              </p>
              {status ? <p className="sessionRuntimeSettingsStatus text-sm" role="status">{status}</p> : null}
            </div>

            <Separator className="bg-border/70" />

            <div className="dialogFormActions sessionRuntimeSettingsActions">
              <Button type="button" variant="outline" disabled={saving} onClick={onClose}>Cancel</Button>
              <Button type="submit" disabled={!canSave}>{saving ? "Saving..." : "Save"}</Button>
            </div>
          </form>
        </DialogContent>
      </div>
    </Dialog>
  );
}
