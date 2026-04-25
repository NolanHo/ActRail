import { api } from "../../lib/api";

export interface SessionUiState {
  sessionId: string | null;
  runtimeId: string | null;
  diagnostics: Record<string, unknown> | null;
  queue: Record<string, unknown> | null;
  files: string[];
  loading: boolean;
}

export interface SessionUiRefreshOptions {
  agentBackend?: string;
  runtimeId?: string | null;
}

export interface SessionUiStore {
  getState(): SessionUiState;
  subscribe(listener: () => void): () => void;
  refresh(sessionId: string, options?: SessionUiRefreshOptions): Promise<void>;
}

export function createSessionUiStore(): SessionUiStore {
  let state: SessionUiState = {
    sessionId: null,
    runtimeId: null,
    diagnostics: null,
    queue: null,
    files: [],
    loading: false,
  };
  const listeners = new Set<() => void>();
  let currentRefreshId = 0;
  let inFlightRefresh: { key: string; promise: Promise<void> } | null = null;

  const emit = () => {
    for (const listener of listeners) {
      listener();
    }
  };

  return {
    getState: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    async refresh(sessionId: string, _options?: SessionUiRefreshOptions) {
      const refreshKey = `${sessionId}:${String(_options?.runtimeId || "")}`;
      if (inFlightRefresh && inFlightRefresh.key === refreshKey) {
        return inFlightRefresh.promise;
      }

      const refreshId = ++currentRefreshId;
      const preserveCurrentState = state.sessionId === sessionId;
      state = {
        sessionId,
        runtimeId: preserveCurrentState ? state.runtimeId : (_options?.runtimeId ?? null),
        diagnostics: preserveCurrentState ? state.diagnostics : null,
        queue: preserveCurrentState ? state.queue : null,
        files: preserveCurrentState ? state.files : [],
        loading: true,
      };
      emit();

      let request: Promise<void> | null = null;
      request = (async () => {
        try {
          const workspace = _options?.runtimeId
            ? await api.getWorkspace(sessionId, undefined, _options.runtimeId)
            : await api.getWorkspace(sessionId);
          if (refreshId !== currentRefreshId) {
            return;
          }

          const openPaths = Array.isArray(workspace.open_paths)
            ? workspace.open_paths.filter((path): path is string => typeof path === "string" && path.trim().length > 0)
            : [];
          const historyPaths = Array.isArray(workspace.history_items)
            ? workspace.history_items.flatMap((item) => {
                if (!item || typeof item !== "object") {
                  return [] as string[];
                }
                const path = typeof item.path === "string" ? item.path.trim() : "";
                return path ? [path] : [];
              })
            : [];
          const files = Array.from(new Set([...openPaths, ...historyPaths]));
          const diagnosticsEntries: Array<[string, unknown]> = [];
          if (typeof workspace.root_path === "string" && workspace.root_path.trim()) {
            diagnosticsEntries.push(["root_path", workspace.root_path.trim()]);
          }
          if (typeof workspace.selected_path === "string" && workspace.selected_path.trim()) {
            diagnosticsEntries.push(["selected_path", workspace.selected_path.trim()]);
          }
          if (Array.isArray(workspace.history_items) && workspace.history_items.length > 0) {
            diagnosticsEntries.push(["history_items", workspace.history_items]);
          }

          state = {
            sessionId,
            runtimeId: typeof workspace.runtime_id === "string" && workspace.runtime_id.trim()
              ? workspace.runtime_id
              : (_options?.runtimeId ?? null),
            diagnostics: diagnosticsEntries.length ? Object.fromEntries(diagnosticsEntries) : null,
            queue: null,
            files,
            loading: false,
          };
          emit();
        } catch (error) {
          if (refreshId === currentRefreshId) {
            state = { ...state, loading: false };
            emit();
            throw error;
          }
        } finally {
          if (inFlightRefresh?.promise === request) {
            inFlightRefresh = null;
          }
        }
      })();

      inFlightRefresh = { key: refreshKey, promise: request };
      return request;
    },
  };
}
