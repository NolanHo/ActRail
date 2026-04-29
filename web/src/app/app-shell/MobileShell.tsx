import { useEffect, useState } from "preact/hooks";
import { Button } from "@/components/ui/button";
import { SessionsPane } from "@/components/sessions/SessionsPane";
import { Composer } from "@/components/composer/Composer";
import { ConversationPane } from "@/components/conversation/ConversationPane";
import { SessionWorkspace } from "@/components/workspace/SessionWorkspace";
import { useComposerStoreApi } from "../providers";
import { FileIcon, HarnessIcon, InsightIcon, StopIcon, WorkspaceIcon } from "./icons";

export type MobileRoute =
  | { screen: "sessions" }
  | { screen: "conversation"; sessionId: string }
  | { screen: "workspace"; sessionId: string }
  | { screen: "waits"; sessionId?: string }
  | { screen: "settings" };

interface MobileShellProps {
  activeSessionId: string | null;
  activeTitle: string;
  announcementEnabled: boolean;
  announcementLabel: string;
  canInterrupt: boolean;
  notificationLabel: string;
  notificationsEnabled: boolean;
  onInterrupt(): void;
  onLogout(): void;
  onNewSession(): void;
  onOpenFilePath(path: string, line?: number | null): void;
  onOpenFiles(): void;
  onOpenHarness(): void;
  onOpenSettings(): void;
  onOpenInsight(): void;
  onToggleAnnouncements(): void;
  onToggleNotifications(): void;
}

function blurActiveInteractiveElement() {
  if (typeof document === "undefined") {
    return;
  }
  const active = document.activeElement;
  if (active instanceof HTMLElement) {
    active.blur();
  }
}

function sessionRoute(screen: "conversation" | "workspace", sessionId: string | null): MobileRoute {
  return sessionId ? { screen, sessionId } : { screen: "sessions" };
}

function routeSessionId(route: MobileRoute) {
  return "sessionId" in route ? route.sessionId || null : null;
}

function routeLabel(route: MobileRoute) {
  switch (route.screen) {
    case "sessions":
      return "Sessions";
    case "conversation":
      return "Conversation";
    case "workspace":
      return "Workspace";
    case "waits":
      return "Waits";
    case "settings":
      return "Settings";
  }
}

interface MobileTopBarProps {
  activeSessionId: string | null;
  activeTitle: string;
  canInterrupt: boolean;
  route: MobileRoute;
  onBack(): void;
  onCommand(): void;
  onInterrupt(): void;
  onRoute(route: MobileRoute): void;
}

function MobileTopBar({ activeSessionId, activeTitle, canInterrupt, route, onBack, onCommand, onInterrupt, onRoute }: MobileTopBarProps) {
  const showSessionActions = route.screen === "conversation" && Boolean(activeSessionId);
  return (
    <header className="mobileRouteHeader">
      <div className="mobileRouteHeaderTop">
        {route.screen !== "sessions" ? (
          <Button type="button" variant="outline" size="sm" className="mobileBackButton" onClick={onBack}>Back</Button>
        ) : null}
        <div className="mobileChatHeading">
          <p className="mobileSectionEyebrow">{routeLabel(route)}</p>
          <h1 className="mobileChatTitle">{route.screen === "sessions" ? "Sessions" : activeSessionId ? activeTitle : "No session selected"}</h1>
        </div>
        {canInterrupt ? (
          <Button type="button" variant="outline" size="sm" className="mobileInterruptButton" onClick={onInterrupt}>
            <StopIcon />
            <span>Interrupt</span>
          </Button>
        ) : null}
      </div>
      {showSessionActions ? (
        <div className="mobileRouteActions" aria-label="Conversation actions">
          <Button type="button" variant="outline" size="sm" onClick={() => onRoute(sessionRoute("workspace", activeSessionId))}>Files</Button>
          <Button type="button" variant="outline" size="sm" onClick={() => onRoute({ screen: "waits", sessionId: activeSessionId || undefined })}>Waits</Button>
          <Button type="button" variant="outline" size="sm" onClick={onCommand}>Command</Button>
        </div>
      ) : null}
    </header>
  );
}

function MobileSettingsSection({
  announcementEnabled,
  announcementLabel,
  notificationLabel,
  notificationsEnabled,
  onLogout,
  onNewSession,
  onOpenFiles,
  onOpenHarness,
  onOpenSettings,
  onOpenInsight,
  onToggleAnnouncements,
  onToggleNotifications,
}: Omit<MobileShellProps, "activeSessionId" | "activeTitle" | "canInterrupt" | "onInterrupt" | "onOpenFilePath">) {
  return (
    <section className="mobileToolsPage" aria-label="Settings">
      <div className="mobileToolsGrid">
        <Button type="button" variant="outline" className="mobileToolCard" onClick={onNewSession}>
          <span className="mobileToolCardIcon" aria-hidden="true">+</span>
          <span className="mobileToolCardText">
            <strong>New session</strong>
            <span>Start a new browser-owned session.</span>
          </span>
        </Button>
        <Button type="button" variant="outline" className="mobileToolCard" onClick={onOpenFiles}>
          <FileIcon />
          <span className="mobileToolCardText">
            <strong>File viewer</strong>
            <span>Open direct file lookup.</span>
          </span>
        </Button>
        <Button type="button" variant="outline" className="mobileToolCard" onClick={onOpenInsight}>
          <InsightIcon />
          <span className="mobileToolCardText">
            <strong>Insight</strong>
            <span>Open context and usage diagnostics.</span>
          </span>
        </Button>
        <Button type="button" variant="outline" className="mobileToolCard" onClick={onOpenHarness}>
          <HarnessIcon />
          <span className="mobileToolCardText">
            <strong>Harness</strong>
            <span>Inspect or adjust automation controls.</span>
          </span>
        </Button>
        <Button type="button" variant="outline" className="mobileToolCard" onClick={onOpenSettings}>
          <span className="mobileToolCardIcon" aria-hidden="true">S</span>
          <span className="mobileToolCardText">
            <strong>Display and voice</strong>
            <span>Theme, audio, and composer preferences.</span>
          </span>
        </Button>
        <div className="mobileToolCard mobileToolCardStatic">
          <WorkspaceIcon />
          <span className="mobileToolCardText">
            <strong>Workspace</strong>
            <span>Use the Workspace route for session details.</span>
          </span>
        </div>
      </div>
      <div className="mobileToggleStack">
        <Button type="button" variant="outline" className="mobileToggleButton" onClick={onToggleNotifications}>
          <span className="mobileToggleLabel">Notifications</span>
          <span className="mobileToggleValue">{notificationsEnabled ? "On" : "Off"}</span>
          <span className="visuallyHidden">{notificationLabel}</span>
        </Button>
        <Button type="button" variant="outline" className="mobileToggleButton" onClick={onToggleAnnouncements}>
          <span className="mobileToggleLabel">Announcements</span>
          <span className="mobileToggleValue">{announcementEnabled ? "On" : "Off"}</span>
          <span className="visuallyHidden">{announcementLabel}</span>
        </Button>
        <Button type="button" variant="outline" className="mobileToggleButton" onClick={onLogout}>
          <span className="mobileToggleLabel">Log out</span>
          <span className="mobileToggleValue">Session</span>
        </Button>
      </div>
    </section>
  );
}

export function MobileShell({
  activeSessionId,
  activeTitle,
  announcementEnabled,
  announcementLabel,
  canInterrupt,
  notificationLabel,
  notificationsEnabled,
  onInterrupt,
  onLogout,
  onNewSession,
  onOpenFilePath,
  onOpenFiles,
  onOpenHarness,
  onOpenSettings,
  onOpenInsight,
  onToggleAnnouncements,
  onToggleNotifications,
}: MobileShellProps) {
  const composerStore = useComposerStoreApi();
  const [commandSheetRequestKey, setCommandSheetRequestKey] = useState(0);
  const [route, setRoute] = useState<MobileRoute>(activeSessionId ? { screen: "conversation", sessionId: activeSessionId } : { screen: "sessions" });

  useEffect(() => {
    setRoute((current) => {
      if (!activeSessionId) {
        return { screen: "sessions" };
      }
      if (current.screen === "sessions") {
        return current;
      }
      if ((current.screen === "conversation" || current.screen === "workspace") && current.sessionId !== activeSessionId) {
        return { screen: current.screen, sessionId: activeSessionId };
      }
      return current;
    });
  }, [activeSessionId]);

  useEffect(() => {
    blurActiveInteractiveElement();
  }, [route.screen]);

  const routeTo = (next: MobileRoute) => {
    setRoute(next);
  };

  const back = () => {
    setRoute((current) => {
      if (current.screen === "conversation") {
        return { screen: "sessions" };
      }
      if (current.screen === "workspace") {
        return sessionRoute("conversation", current.sessionId || activeSessionId);
      }
      if (current.screen === "waits") {
        return activeSessionId ? { screen: "conversation", sessionId: activeSessionId } : { screen: "sessions" };
      }
      if (current.screen === "settings") {
        return activeSessionId ? { screen: "conversation", sessionId: activeSessionId } : { screen: "sessions" };
      }
      return current;
    });
  };

  const openCommandSheet = () => {
    if (!activeSessionId) {
      return;
    }
    const currentDraft = composerStore.getState().draftBySessionId?.[activeSessionId] ?? "";
    if (!currentDraft.startsWith("/")) {
      composerStore.setDraft(activeSessionId, "/");
    }
    setCommandSheetRequestKey((value) => value + 1);
  };

  const currentSessionId = routeSessionId(route) || activeSessionId;

  return (
    <div className="mobileShell" data-testid="mobile-shell">
      <section className="mobileShellBody">
        {route.screen === "sessions" ? (
          <div className="mobilePane mobileSessionsPane">
            <SessionsPane
              onNewSession={onNewSession}
              onSessionSelect={(session) => routeTo({ screen: "conversation", sessionId: session.session_id })}
            />
          </div>
        ) : null}
        {route.screen === "conversation" ? (
          <section className="mobilePane mobileConversationPane">
            <MobileTopBar
              activeSessionId={activeSessionId}
              activeTitle={activeTitle}
              canInterrupt={canInterrupt}
              route={route}
              onBack={back}
              onCommand={openCommandSheet}
              onInterrupt={onInterrupt}
              onRoute={routeTo}
            />
            <ConversationPane
              key={route.sessionId || "no-session"}
              onOpenFilePath={(path, line) => onOpenFilePath(path, line ?? null)}
            />
            <Composer compactMobile commandSheetRequestKey={commandSheetRequestKey} />
          </section>
        ) : null}
        {route.screen === "workspace" ? (
          <section className="mobilePane mobileWorkspacePane">
            <MobileTopBar
              activeSessionId={activeSessionId}
              activeTitle={activeTitle}
              canInterrupt={canInterrupt}
              route={route}
              onBack={back}
              onCommand={openCommandSheet}
              onInterrupt={onInterrupt}
              onRoute={routeTo}
            />
            <SessionWorkspace mode="details" initialTab="overview" />
          </section>
        ) : null}
        {route.screen === "waits" ? (
          <section className="mobilePane mobileWaitsPane">
            <MobileTopBar
              activeSessionId={activeSessionId}
              activeTitle={activeTitle}
              canInterrupt={canInterrupt}
              route={route}
              onBack={back}
              onCommand={openCommandSheet}
              onInterrupt={onInterrupt}
              onRoute={routeTo}
            />
            <SessionWorkspace mode="details" initialTab={currentSessionId ? "wait" : "waiting-inbox"} />
          </section>
        ) : null}
        {route.screen === "settings" ? (
          <div className="mobilePane mobileSettingsPane">
            <MobileTopBar
              activeSessionId={activeSessionId}
              activeTitle={activeTitle}
              canInterrupt={canInterrupt}
              route={route}
              onBack={back}
              onCommand={openCommandSheet}
              onInterrupt={onInterrupt}
              onRoute={routeTo}
            />
            <MobileSettingsSection
              announcementEnabled={announcementEnabled}
              announcementLabel={announcementLabel}
              notificationLabel={notificationLabel}
              notificationsEnabled={notificationsEnabled}
              onLogout={onLogout}
              onNewSession={onNewSession}
              onOpenFiles={onOpenFiles}
              onOpenHarness={onOpenHarness}
              onOpenSettings={onOpenSettings}
              onOpenInsight={onOpenInsight}
              onToggleAnnouncements={onToggleAnnouncements}
              onToggleNotifications={onToggleNotifications}
            />
          </div>
        ) : null}
      </section>
      <nav className="mobileBottomNav" aria-label="Primary">
        <Button type="button" variant={route.screen === "sessions" ? "default" : "outline"} className="mobileBottomNavButton" onClick={() => routeTo({ screen: "sessions" })}>Sessions</Button>
        <Button type="button" variant={route.screen === "conversation" ? "default" : "outline"} className="mobileBottomNavButton" onClick={() => routeTo(sessionRoute("conversation", activeSessionId))}>Conversation</Button>
        <Button type="button" variant={route.screen === "workspace" ? "default" : "outline"} className="mobileBottomNavButton" onClick={() => routeTo(sessionRoute("workspace", activeSessionId))}>Workspace</Button>
        <Button type="button" variant={route.screen === "waits" ? "default" : "outline"} className="mobileBottomNavButton" onClick={() => routeTo({ screen: "waits", sessionId: activeSessionId || undefined })}>Waits</Button>
        <Button type="button" variant={route.screen === "settings" ? "default" : "outline"} className="mobileBottomNavButton" onClick={() => routeTo({ screen: "settings" })}>Settings</Button>
      </nav>
    </div>
  );
}
