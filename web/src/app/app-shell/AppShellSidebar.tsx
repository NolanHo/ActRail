import { Button } from "@/components/ui/button";
import { SessionsPane } from "../../components/sessions/SessionsPane";
import { SubagentsRail } from "../../components/subagents/SubagentsView";
import { BellIcon, VolumeIcon } from "./icons";

export type DesktopGlobalView = "sessions" | "subagents";

interface AppShellSidebarProps {
  activeView: DesktopGlobalView;
  activeSubagentId: string;
  announcementEnabled: boolean;
  announcementLabel: string;
  notificationLabel: string;
  notificationsEnabled: boolean;
  onBrandClick(): void;
  onNewSession(): void;
  onOpenSettings(): void;
  onLogout(): void;
  onToggleAnnouncements(): void;
  onToggleNotifications(): void;
  onSubagentSelect(actorId: string): void;
  onViewChange(view: DesktopGlobalView): void;
}

export function AppShellSidebar({
  activeView,
  activeSubagentId,
  announcementEnabled,
  announcementLabel,
  notificationLabel,
  notificationsEnabled,
  onBrandClick,
  onNewSession,
  onOpenSettings,
  onLogout,
  onToggleAnnouncements,
  onToggleNotifications,
  onSubagentSelect,
  onViewChange,
}: AppShellSidebarProps) {
  return (
    <>
      <header className="sidebarBanner">
        <div className="sidebarBannerActions">
          <Button type="button" variant="ghost" className="brandMark" onClick={onBrandClick}>ActRail</Button>
          <div className="sidebarActionButtons">
            <Button
              type="button"
              variant="outline"
              size="icon"
              className={`iconAction legacyToggleAction${notificationsEnabled ? " isActive" : ""}`}
              aria-label={notificationLabel}
              title={notificationLabel}
              onClick={onToggleNotifications}
            >
              <BellIcon />
              <span className="visuallyHidden">{notificationLabel}</span>
            </Button>
            <Button
              type="button"
              variant="outline"
              size="icon"
              className={`iconAction legacyToggleAction${announcementEnabled ? " isActive" : ""}`}
              aria-label={announcementLabel}
              title={announcementLabel}
              onClick={onToggleAnnouncements}
            >
              <VolumeIcon />
              <span className="visuallyHidden">{announcementLabel}</span>
            </Button>
          </div>
        </div>
      </header>
      <nav className="globalViewSwitch" aria-label="Global view">
        <Button
          type="button"
          variant={activeView === "sessions" ? "default" : "outline"}
          className="globalViewSwitchButton"
          onClick={() => onViewChange("sessions")}
        >
          Sessions
        </Button>
        <Button
          type="button"
          variant={activeView === "subagents" ? "default" : "outline"}
          className="globalViewSwitchButton"
          onClick={() => onViewChange("subagents")}
        >
          Subagents
        </Button>
      </nav>
      {activeView === "sessions" ? <SessionsPane onNewSession={onNewSession} /> : <SubagentsRail selectedActorId={activeSubagentId} onSelect={onSubagentSelect} />}
      <footer className="sidebarFooter">
        <Button type="button" variant="outline" className="footerAction"><span className="buttonGlyph">?</span><span>Help</span></Button>
        <Button type="button" variant="outline" className="footerAction" onClick={onOpenSettings}><span className="buttonGlyph">⚙</span><span>Settings</span></Button>
        <Button type="button" variant="outline" className="footerAction" onClick={onLogout}><span className="buttonGlyph">→|</span><span>Log out</span></Button>
      </footer>
    </>
  );
}
