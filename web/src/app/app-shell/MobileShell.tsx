import { useEffect, useState } from "preact/hooks";
import { Button } from "@/components/ui/button";
import { SessionsPane } from "@/components/sessions/SessionsPane";
import { Composer } from "@/components/composer/Composer";
import { ConversationPane } from "@/components/conversation/ConversationPane";
import { SessionFileView } from "@/components/session-files/SessionFileView";
import { MetadataIcon, StopIcon } from "./icons";
import { SessionStatusStrip, type ConversationStatusItem } from "./AppShellToolbar";

export type MobileRoute =
  | { screen: "sessions" }
  | { screen: "codex_sessions" }
  | { screen: "read"; sessionId: string }
  | { screen: "chat"; sessionId: string }
  | { screen: "settings" };

interface MobileShellProps {
  activeSessionId: string | null;
  activeCwd?: string;
  activeTitle: string;
  announcementEnabled: boolean;
  announcementLabel: string;
  canInterrupt: boolean;
  statusItems?: ConversationStatusItem[];
  notificationLabel: string;
  notificationsEnabled: boolean;
  onInterrupt(): void;
  onLogout(): void;
  onNewSession(): void;
  onCodexSessionRenamed?(): void;
  onOpenFilePath(path: string, line?: number | null): void;
  onOpenRuntimeSettings(): void;
  onOpenSettings(): void;
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

function sessionRoute(screen: "read" | "chat", sessionId: string | null): MobileRoute {
  return sessionId ? { screen, sessionId } : { screen: "sessions" };
}

function routeLabel(route: MobileRoute) {
  switch (route.screen) {
    case "sessions":
      return "Sessions";
    case "codex_sessions":
      return "Codex";
    case "read":
      return "Read";
    case "chat":
      return "Chat";
    case "settings":
      return "Settings";
  }
}

interface MobileTopBarProps {
  activeTitle: string;
  canInterrupt: boolean;
  compact?: boolean;
  route: MobileRoute;
  statusItems?: ConversationStatusItem[];
  onBack(): void;
  onInterrupt(): void;
}

function MobileTopBar({ activeTitle, canInterrupt, compact = false, route, statusItems = [], onBack, onInterrupt }: MobileTopBarProps) {
  const showStatus = route.screen !== "sessions" && statusItems.length > 0;
  return (
    <header className={compact ? "mobileRouteHeader compact" : "mobileRouteHeader"}>
      {route.screen !== "sessions" ? (
        <Button type="button" variant="outline" size="sm" className="mobileBackButton" onClick={onBack}>Back</Button>
      ) : null}
      <div className="mobileChatHeading">
        <p className="mobileSectionEyebrow">{routeLabel(route)}</p>
        <h1 className="mobileChatTitle">{route.screen === "sessions" ? "Sessions" : activeTitle}</h1>
        {showStatus ? <SessionStatusStrip items={statusItems} className="mobileSessionStatusStrip" /> : null}
      </div>
      {canInterrupt ? (
        <Button type="button" variant="outline" size="sm" className="mobileInterruptButton" onClick={onInterrupt}>
          <StopIcon />
          <span>Interrupt</span>
        </Button>
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
  onOpenSettings,
  onOpenRuntimeSettings,
  onToggleAnnouncements,
  onToggleNotifications,
}: Pick<MobileShellProps, "announcementEnabled" | "announcementLabel" | "notificationLabel" | "notificationsEnabled" | "onLogout" | "onOpenRuntimeSettings" | "onOpenSettings" | "onToggleAnnouncements" | "onToggleNotifications">) {
  return (
    <section className="mobileToolsPage" aria-label="Settings">
      <div className="mobileToolsGrid">
        <Button type="button" variant="outline" className="mobileToolCard" onClick={onOpenRuntimeSettings}>
          <span className="mobileToolCardIcon" aria-hidden="true"><MetadataIcon /></span>
          <span className="mobileToolCardText">
            <strong>Runtime settings</strong>
            <span>Provider, model, effort.</span>
          </span>
        </Button>
        <Button type="button" variant="outline" className="mobileToolCard" onClick={onOpenSettings}>
          <span className="mobileToolCardIcon" aria-hidden="true">S</span>
          <span className="mobileToolCardText">
            <strong>Display and voice</strong>
            <span>Theme, audio, composer.</span>
          </span>
        </Button>
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
  activeCwd = "",
  activeTitle,
  announcementEnabled,
  announcementLabel,
  canInterrupt,
  statusItems = [],
  notificationLabel,
  notificationsEnabled,
  onInterrupt,
  onLogout,
  onNewSession,
  onCodexSessionRenamed,
  onOpenFilePath,
  onOpenRuntimeSettings,
  onOpenSettings,
  onToggleAnnouncements,
  onToggleNotifications,
}: MobileShellProps) {
  const [route, setRoute] = useState<MobileRoute>(activeSessionId ? { screen: "read", sessionId: activeSessionId } : { screen: "sessions" });

  useEffect(() => {
    setRoute((current) => {
      if (!activeSessionId) {
        return { screen: "sessions" };
      }
      if (current.screen === "read" && current.sessionId !== activeSessionId) {
        return { screen: "read", sessionId: activeSessionId };
      }
      if (current.screen === "chat" && current.sessionId !== activeSessionId) {
        return { screen: "chat", sessionId: activeSessionId };
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
    setRoute({ screen: "sessions" });
  };

  const readRoute = sessionRoute("read", activeSessionId);
  const chatRoute = sessionRoute("chat", activeSessionId);

  return (
    <div className="mobileShell" data-testid="mobile-shell">
      <section className="mobileShellBody">
        {route.screen === "sessions" ? (
          <div className="mobilePane mobileSessionsPane">
            <SessionsPane
              onNewSession={onNewSession}
              onOpenSettings={() => routeTo({ screen: "settings" })}
              onSessionSelect={(session) => routeTo({ screen: "read", sessionId: session.session_id })}
            />
          </div>
        ) : null}
        {route.screen === "codex_sessions" ? (
          <div className="mobilePane mobileCodexSessionsPane">
            <MobileTopBar activeTitle="Codex Sessions" canInterrupt={canInterrupt} route={route} statusItems={statusItems} onBack={back} onInterrupt={onInterrupt} />
            <SessionFileView
              active
              activeCwd={activeCwd}
              className="mobileEmbeddedView"
              onRenamed={onCodexSessionRenamed}
            />
          </div>
        ) : null}
        {route.screen === "read" ? (
          <section className="mobilePane mobileReadPane">
            <MobileTopBar activeTitle={activeTitle} canInterrupt={canInterrupt} compact route={route} statusItems={statusItems} onBack={back} onInterrupt={onInterrupt} />
            <ConversationPane
              key={route.sessionId || "no-session"}
              onOpenFilePath={(path, line) => onOpenFilePath(path, line ?? null)}
            />
          </section>
        ) : null}
        {route.screen === "chat" ? (
          <section className="mobilePane mobileChatPane">
            <MobileTopBar activeTitle={activeTitle} canInterrupt={canInterrupt} route={route} statusItems={statusItems} onBack={back} onInterrupt={onInterrupt} />
            <ConversationPane
              key={route.sessionId || "no-session"}
              onOpenFilePath={(path, line) => onOpenFilePath(path, line ?? null)}
            />
            <Composer compactMobile />
          </section>
        ) : null}
        {route.screen === "settings" ? (
          <div className="mobilePane mobileSettingsPane">
            <MobileTopBar activeTitle={activeTitle} canInterrupt={canInterrupt} route={route} statusItems={statusItems} onBack={back} onInterrupt={onInterrupt} />
            <MobileSettingsSection
              announcementEnabled={announcementEnabled}
              announcementLabel={announcementLabel}
              notificationLabel={notificationLabel}
              notificationsEnabled={notificationsEnabled}
              onLogout={onLogout}
              onOpenRuntimeSettings={onOpenRuntimeSettings}
              onOpenSettings={onOpenSettings}
              onToggleAnnouncements={onToggleAnnouncements}
              onToggleNotifications={onToggleNotifications}
            />
          </div>
        ) : null}
      </section>
      <nav className="mobileBottomNav" aria-label="Primary">
        <Button type="button" variant={route.screen === "sessions" ? "default" : "outline"} className="mobileBottomNavButton" onClick={() => routeTo({ screen: "sessions" })}>Sessions</Button>
        <Button type="button" variant={route.screen === "codex_sessions" ? "default" : "outline"} className="mobileBottomNavButton" onClick={() => routeTo({ screen: "codex_sessions" })}>Codex</Button>
        <Button type="button" variant={route.screen === "read" ? "default" : "outline"} className="mobileBottomNavButton" onClick={() => routeTo(readRoute)}>Read</Button>
        <Button type="button" variant={route.screen === "chat" ? "default" : "outline"} className="mobileBottomNavButton" onClick={() => routeTo(chatRoute)}>Chat</Button>
      </nav>
    </div>
  );
}
