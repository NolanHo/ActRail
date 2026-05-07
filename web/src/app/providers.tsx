import { createContext } from "preact";
import { useContext, useRef } from "preact/hooks";
import { useSyncExternalStore } from "preact/compat";
import { createMessagesStore, type MessagesStore } from "../domains/messages/store";
import { createLiveSessionStore, type LiveSessionStore } from "../domains/live-session/store";
import { createSessionsStore, type SessionsStore } from "../domains/sessions/store";
import { createComposerStore, type ComposerStore } from "../domains/composer/store";
import { createSessionUiStore, type SessionUiStore } from "../domains/session-ui/store";
import { createWaitsStore, type WaitsStore } from "../domains/waits/store";

const defaultSessionsStore = createSessionsStore();
const defaultMessagesStore = createMessagesStore();
const defaultLiveSessionStore = createLiveSessionStore(defaultMessagesStore);
const defaultComposerStore = createComposerStore();
const defaultSessionUiStore = createSessionUiStore();
const defaultWaitsStore = createWaitsStore();

const SessionsStoreContext = createContext<SessionsStore>(defaultSessionsStore);
const MessagesStoreContext = createContext<MessagesStore>(defaultMessagesStore);
const LiveSessionStoreContext = createContext<LiveSessionStore>(defaultLiveSessionStore);
const ComposerStoreContext = createContext<ComposerStore>(defaultComposerStore);
const SessionUiStoreContext = createContext<SessionUiStore>(defaultSessionUiStore);
const WaitsStoreContext = createContext<WaitsStore>(defaultWaitsStore);

interface AppProvidersProps {
  children: preact.ComponentChildren;
  sessionsStore?: SessionsStore;
  messagesStore?: MessagesStore;
  liveSessionStore?: LiveSessionStore;
  composerStore?: ComposerStore;
  sessionUiStore?: SessionUiStore;
  waitsStore?: WaitsStore;
}

export function AppProviders({
  children,
  sessionsStore = defaultSessionsStore,
  messagesStore = defaultMessagesStore,
  liveSessionStore = defaultLiveSessionStore,
  composerStore = defaultComposerStore,
  sessionUiStore = defaultSessionUiStore,
  waitsStore = defaultWaitsStore,
}: AppProvidersProps) {
  return (
    <SessionsStoreContext.Provider value={sessionsStore}>
      <MessagesStoreContext.Provider value={messagesStore}>
        <LiveSessionStoreContext.Provider value={liveSessionStore}>
          <ComposerStoreContext.Provider value={composerStore}>
            <SessionUiStoreContext.Provider value={sessionUiStore}>
              <WaitsStoreContext.Provider value={waitsStore}>{children}</WaitsStoreContext.Provider>
            </SessionUiStoreContext.Provider>
          </ComposerStoreContext.Provider>
        </LiveSessionStoreContext.Provider>
      </MessagesStoreContext.Provider>
    </SessionsStoreContext.Provider>
  );
}

function useStoreSelector<TState, TSelected>(
  store: { subscribe(listener: () => void): () => void; getState(): TState },
  selector: (state: TState) => TSelected,
  isEqual: (left: TSelected, right: TSelected) => boolean = Object.is,
) {
  const snapshotRef = useRef<{ selected: TSelected } | null>(null);
  const getSnapshot = () => {
    const selected = selector(store.getState());
    const prior = snapshotRef.current;
    if (prior && isEqual(prior.selected, selected)) {
      return prior.selected;
    }
    snapshotRef.current = { selected };
    return selected;
  };
  return useSyncExternalStore(store.subscribe, getSnapshot);
}

export function shallowEqual<TSelected extends Record<string, unknown>>(left: TSelected, right: TSelected) {
  if (Object.is(left, right)) {
    return true;
  }
  const leftKeys = Object.keys(left);
  const rightKeys = Object.keys(right);
  if (leftKeys.length !== rightKeys.length) {
    return false;
  }
  return leftKeys.every((key) => Object.is(left[key], right[key]));
}

export function useSessionsStore() {
  const store = useContext(SessionsStoreContext);
  return useSyncExternalStore(store.subscribe, store.getState);
}

export function useSessionsStoreSelector<TSelected>(selector: (state: ReturnType<SessionsStore["getState"]>) => TSelected, isEqual?: (left: TSelected, right: TSelected) => boolean) {
  return useStoreSelector(useContext(SessionsStoreContext), selector, isEqual);
}

export function useMessagesStore() {
  const store = useContext(MessagesStoreContext);
  return useSyncExternalStore(store.subscribe, store.getState);
}

export function useMessagesStoreSelector<TSelected>(selector: (state: ReturnType<MessagesStore["getState"]>) => TSelected, isEqual?: (left: TSelected, right: TSelected) => boolean) {
  return useStoreSelector(useContext(MessagesStoreContext), selector, isEqual);
}

export function useLiveSessionStore() {
  const store = useContext(LiveSessionStoreContext);
  return useSyncExternalStore(store.subscribe, store.getState);
}

export function useLiveSessionStoreSelector<TSelected>(selector: (state: ReturnType<LiveSessionStore["getState"]>) => TSelected, isEqual?: (left: TSelected, right: TSelected) => boolean) {
  return useStoreSelector(useContext(LiveSessionStoreContext), selector, isEqual);
}

export function useComposerStore() {
  const store = useContext(ComposerStoreContext);
  return useSyncExternalStore(store.subscribe, store.getState);
}

export function useComposerStoreSelector<TSelected>(selector: (state: ReturnType<ComposerStore["getState"]>) => TSelected, isEqual?: (left: TSelected, right: TSelected) => boolean) {
  return useStoreSelector(useContext(ComposerStoreContext), selector, isEqual);
}

export function useSessionUiStore() {
  const store = useContext(SessionUiStoreContext);
  return useSyncExternalStore(store.subscribe, store.getState);
}

export function useSessionUiStoreSelector<TSelected>(selector: (state: ReturnType<SessionUiStore["getState"]>) => TSelected, isEqual?: (left: TSelected, right: TSelected) => boolean) {
  return useStoreSelector(useContext(SessionUiStoreContext), selector, isEqual);
}

export function useWaitsStore() {
  const store = useContext(WaitsStoreContext);
  return useSyncExternalStore(store.subscribe, store.getState);
}

export function useWaitsStoreSelector<TSelected>(selector: (state: ReturnType<WaitsStore["getState"]>) => TSelected, isEqual?: (left: TSelected, right: TSelected) => boolean) {
  return useStoreSelector(useContext(WaitsStoreContext), selector, isEqual);
}

export function useSessionsStoreApi() {
  return useContext(SessionsStoreContext);
}

export function useMessagesStoreApi() {
  return useContext(MessagesStoreContext);
}

export function useLiveSessionStoreApi() {
  return useContext(LiveSessionStoreContext);
}

export function useComposerStoreApi() {
  return useContext(ComposerStoreContext);
}

export function useSessionUiStoreApi() {
  return useContext(SessionUiStoreContext);
}

export function useWaitsStoreApi() {
  return useContext(WaitsStoreContext);
}
