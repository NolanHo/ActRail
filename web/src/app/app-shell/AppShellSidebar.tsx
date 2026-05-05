import { Button } from "@/components/ui/button";
import { SessionsPane } from "../../components/sessions/SessionsPane";
import { SubagentsRail } from "../../components/subagents/SubagentsView";
import type { SubagentsData } from "../../components/subagents/SubagentsView";

export type DesktopGlobalView = "sessions" | "subagents" | "scheduler";

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

function GlobalSubagentsIcon() {
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
          variant={activeView === "subagents" ? "default" : "outline"}
          className="globalNavButton"
          aria-label="Teams view"
          title="Teams"
          onClick={() => onViewChange("subagents")}
        >
          <GlobalSubagentsIcon />
        </Button>
        <Button
          type="button"
          variant={activeView === "scheduler" ? "default" : "outline"}
          className="globalNavButton"
          aria-label="Scheduler view"
          title="Scheduler"
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
  activeSubagentId: string;
  subagentsData: SubagentsData;
  onNewSession(): void;
  onOpenSettings(): void;
  onLogout(): void;
  onSubagentSelect(actorId: string): void;
}

export function AppShellSidebar({
  activeView,
  activeSubagentId,
  subagentsData,
  onNewSession,
  onOpenSettings,
  onLogout,
  onSubagentSelect,
}: AppShellSidebarProps) {
  return (
    <>
      {activeView === "sessions" ? <SessionsPane onNewSession={onNewSession} /> : activeView === "subagents" ? <SubagentsRail selectedActorId={activeSubagentId} data={subagentsData} onSelect={onSubagentSelect} /> : <div className="sessionsPane"><header className="sessionsPaneHeader"><div><p className="sectionEyebrow">Global</p><h2>Scheduler</h2></div></header><p className="text-sm text-muted-foreground">Manage alarms, supervisor preset activity, and inbox delivery.</p></div>}
      <footer className="sidebarFooter">
        <Button type="button" variant="outline" className="footerAction"><span className="buttonGlyph">?</span><span>Help</span></Button>
        <Button type="button" variant="outline" className="footerAction" onClick={onOpenSettings}><span className="buttonGlyph">ST</span><span>Settings</span></Button>
        <Button type="button" variant="outline" className="footerAction" onClick={onLogout}><span className="buttonGlyph">LO</span><span>Log out</span></Button>
      </footer>
    </>
  );
}
