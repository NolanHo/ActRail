import { Button } from "@/components/ui/button";
import { SessionsPane } from "../../components/sessions/SessionsPane";
import { TeamsRail } from "../../components/teams/TeamsView";
import type { TeamsData } from "../../components/teams/TeamsView";

export type DesktopGlobalView = "sessions" | "codex_sessions" | "ask_user" | "teams" | "scheduler";

interface GlobalNavRailProps {
  activeView: DesktopGlobalView;
  onBrandClick(): void;
  onViewChange(view: DesktopGlobalView): void;
}

function GlobalSessionsIcon() {
  return (
    <svg className="globalNavIcon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="5" y="4" width="14" height="5" rx="1.8" />
      <rect x="5" y="10" width="14" height="5" rx="1.8" />
      <rect x="5" y="16" width="14" height="4" rx="1.8" />
    </svg>
  );
}

function GlobalSchedulerIcon() {
  return (
    <svg className="globalNavIcon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="12" cy="12" r="7" />
      <path d="M12 8v4l3 2" />
      <path d="M7 4 5 6" />
      <path d="m17 4 2 2" />
    </svg>
  );
}

function GlobalCodexSessionsIcon() {
  return (
    <svg className="globalNavIcon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M6 4.5h8.5l3.5 3.5v11.5H6z" />
      <path d="M14.5 4.5V8H18" />
      <path d="M8.5 11h7" />
      <path d="M8.5 14h7" />
      <path d="M8.5 17h4.5" />
    </svg>
  );
}

function GlobalAskUserIcon() {
  return (
    <svg className="globalNavIcon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M5 6.5A3.5 3.5 0 0 1 8.5 3h7A3.5 3.5 0 0 1 19 6.5v4A3.5 3.5 0 0 1 15.5 14H12l-4 4v-4A3.5 3.5 0 0 1 5 10.5z" />
      <path d="M11.9 8.7c0-1.1.8-1.8 1.9-1.8 1 0 1.8.7 1.8 1.7 0 1.5-1.7 1.5-1.7 2.8" />
      <path d="M13.9 13h.01" />
    </svg>
  );
}

function GlobalTeamsIcon() {
  return (
    <svg className="globalNavIcon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="12" cy="6" r="2.3" />
      <circle cx="7" cy="16" r="2.3" />
      <circle cx="17" cy="16" r="2.3" />
      <path d="M11 8.1 8.1 13.9" />
      <path d="M13 8.1 15.9 13.9" />
      <path d="M9.4 16h5.2" />
    </svg>
  );
}

export function GlobalNavRail({
  activeView,
  onBrandClick,
  onViewChange,
}: GlobalNavRailProps) {
  return (
    <nav className="globalNavRail" aria-label="Global views">
      <Button type="button" variant="ghost" className="globalNavBrand" aria-label="ActRail home" onClick={onBrandClick}>AR</Button>
      <div className="globalNavPrimary">
        <Button
          type="button"
          variant={activeView === "sessions" ? "default" : "outline"}
          className="globalNavButton"
          aria-label="Sessions view"
          title="Sessions"
          onClick={() => onViewChange("sessions")}
        >
          <GlobalSessionsIcon />
        </Button>
        <Button
          type="button"
          variant={activeView === "codex_sessions" ? "default" : "outline"}
          className="globalNavButton"
          aria-label="Codex Sessions view"
          title="Codex Sessions"
          onClick={() => onViewChange("codex_sessions")}
        >
          <GlobalCodexSessionsIcon />
        </Button>
        <Button
          type="button"
          variant={activeView === "ask_user" ? "default" : "outline"}
          className="globalNavButton"
          aria-label="AskUser view"
          title="AskUser"
          onClick={() => onViewChange("ask_user")}
        >
          <GlobalAskUserIcon />
        </Button>
        <Button
          type="button"
          variant={activeView === "teams" ? "default" : "outline"}
          className="globalNavButton"
          aria-label="Teams view"
          title="Teams"
          onClick={() => onViewChange("teams")}
        >
          <GlobalTeamsIcon />
        </Button>
        <Button
          type="button"
          variant={activeView === "scheduler" ? "default" : "outline"}
          className="globalNavButton"
          aria-label="Inbox view"
          title="Inbox"
          onClick={() => onViewChange("scheduler")}
        >
          <GlobalSchedulerIcon />
        </Button>
      </div>
    </nav>
  );
}

interface AppShellSidebarProps {
  activeView: DesktopGlobalView;
  activeTeamId: string;
  teamsData: TeamsData;
  onNewSession(): void;
  onOpenSettings(): void;
  onLogout(): void;
  onTeamSelect(actorId: string): void;
}

export function AppShellSidebar({
  activeView,
  activeTeamId,
  teamsData,
  onNewSession,
  onOpenSettings,
  onLogout,
  onTeamSelect,
}: AppShellSidebarProps) {
  const sidebarContent = activeView === "sessions" ? (
    <SessionsPane onNewSession={onNewSession} />
  ) : activeView === "codex_sessions" ? (
    <div className="sessionsPane">
      <header className="sessionsPaneHeader">
        <div>
          <p className="sectionEyebrow">Codex</p>
          <h2>Session Files</h2>
        </div>
      </header>
      <p className="text-sm text-muted-foreground">Browse indexed Codex sessions by workspace or across all history.</p>
    </div>
  ) : activeView === "ask_user" ? (
    <div className="sessionsPane"><header className="sessionsPaneHeader"><div><p className="sectionEyebrow">Runtime waits</p><h2>AskUser</h2></div></header><p className="text-sm text-muted-foreground">Answer blocking runtime questions without leaving the global context.</p></div>
  ) : activeView === "teams" ? (
    <TeamsRail selectedActorId={activeTeamId} data={teamsData} onSelect={onTeamSelect} />
  ) : (
    <div className="sessionsPane"><header className="sessionsPaneHeader"><div><p className="sectionEyebrow">Global</p><h2>Inbox</h2></div></header><p className="text-sm text-muted-foreground">Manage session inbox delivery, self-reminders, and supervisor activity.</p></div>
  );

  return (
    <>
      {sidebarContent}
      <footer className="sidebarFooter">
        <Button type="button" variant="outline" className="footerAction"><span className="buttonGlyph">?</span><span>Help</span></Button>
        <Button type="button" variant="outline" className="footerAction" onClick={onOpenSettings}><span className="buttonGlyph">ST</span><span>Settings</span></Button>
        <Button type="button" variant="outline" className="footerAction" onClick={onLogout}><span className="buttonGlyph">LO</span><span>Log out</span></Button>
      </footer>
    </>
  );
}
