import { useEffect, useRef, useState } from "preact/hooks";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { FileIcon, MenuIcon, MetadataIcon, ProbeIcon, SessionsIcon, StopIcon, SupervisorIcon } from "./icons";

export interface ConversationStatusItem {
  label: string;
  value: string;
  tone?: "default" | "attention" | "error" | "busy" | "success";
}

interface AppShellToolbarProps {
  activeSessionId: string | null;
  activeTitle: string;
  canInterrupt: boolean;
  canProbeRuntime?: boolean;
  probingRuntime?: boolean;
  showInterruptAction: boolean;
  statusItems?: ConversationStatusItem[];
  showMobileSessionsTrigger: boolean;
  showMobileToolbarMenu: boolean;
  onInterrupt(): void;
  onProbeRuntime?(): void;
  onOpenFiles(): void;
  onOpenHarness(): void;
  onOpenSessions(): void;
  onOpenWorkspace(): void;
}

export function AppShellToolbar({
  activeSessionId,
  activeTitle,
  canInterrupt,
  canProbeRuntime = false,
  probingRuntime = false,
  showInterruptAction,
  statusItems = [],
  showMobileSessionsTrigger,
  showMobileToolbarMenu,
  onInterrupt,
  onProbeRuntime,
  onOpenFiles,
  onOpenHarness,
  onOpenSessions,
  onOpenWorkspace,
}: AppShellToolbarProps) {
  const [mobileToolsOpen, setMobileToolsOpen] = useState(false);
  const mobileToolsMenuRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    setMobileToolsOpen(false);
  }, [activeSessionId, showMobileToolbarMenu]);

  useEffect(() => {
    if (!mobileToolsOpen) {
      return undefined;
    }

    const handleDocumentClick = (event: MouseEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }
      if (mobileToolsMenuRef.current?.contains(target)) {
        return;
      }
      setMobileToolsOpen(false);
    };

    document.addEventListener("click", handleDocumentClick);
    return () => {
      document.removeEventListener("click", handleDocumentClick);
    };
  }, [mobileToolsOpen]);

  const closeMobileToolsMenu = () => {
    setMobileToolsOpen(false);
  };

  const renderConversationActionButtons = (mobileMenu = false) => (
    <>
      {mobileMenu && showMobileSessionsTrigger ? (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="conversationMenuItem"
          aria-label="Sessions"
          title="Sessions"
          onClick={() => {
            closeMobileToolsMenu();
            onOpenSessions();
          }}
        >
          <SessionsIcon />
          <span>Sessions</span>
        </Button>
      ) : null}
      <Button
        type="button"
        variant={mobileMenu ? "ghost" : "outline"}
        size={mobileMenu ? "sm" : "icon"}
        className={mobileMenu ? "conversationMenuItem" : "toolbarButton conversationToolButton"}
        aria-label="Metadata"
        title="Metadata"
        disabled={!activeSessionId}
        onClick={() => {
          closeMobileToolsMenu();
          onOpenWorkspace();
        }}
      >
        <MetadataIcon />
        {mobileMenu ? <span>Metadata</span> : null}
      </Button>
      <Button
        type="button"
        variant={mobileMenu ? "ghost" : "outline"}
        size={mobileMenu ? "sm" : "icon"}
        className={mobileMenu ? "conversationMenuItem" : "toolbarButton conversationToolButton"}
        aria-label="Files"
        title="Files"
        disabled={!activeSessionId}
        onClick={() => {
          closeMobileToolsMenu();
          onOpenFiles();
        }}
      >
        <FileIcon />
        {mobileMenu ? <span>Files</span> : null}
      </Button>
      {canProbeRuntime ? (
        <Button
          type="button"
          variant={mobileMenu ? "ghost" : "outline"}
          size="sm"
          className={mobileMenu ? "conversationMenuItem" : "toolbarButton conversationToolButton"}
          aria-label="Probe runtime state"
          title="Probe runtime state"
          disabled={probingRuntime}
          onClick={() => {
            closeMobileToolsMenu();
            onProbeRuntime?.();
          }}
        >
          <ProbeIcon />
          {mobileMenu ? <span>{probingRuntime ? "Probing" : "Probe"}</span> : null}
        </Button>
      ) : null}
      {showInterruptAction ? (
        <Button
          type="button"
          variant={mobileMenu ? "ghost" : "outline"}
          size={mobileMenu ? "sm" : "icon"}
          className={cn(
            mobileMenu ? "conversationMenuItem conversationMenuItemDanger" : "toolbarButton conversationToolButton conversationToolButtonDanger",
          )}
          aria-label="Interrupt (Esc)"
          title="Interrupt (Esc)"
          disabled={!canInterrupt}
          onClick={() => {
            closeMobileToolsMenu();
            onInterrupt();
          }}
        >
          <StopIcon />
          {mobileMenu ? <span>Interrupt</span> : null}
        </Button>
      ) : null}
      <Button
        type="button"
        variant={mobileMenu ? "ghost" : "outline"}
        size={mobileMenu ? "sm" : "icon"}
        className={mobileMenu ? "conversationMenuItem" : "toolbarButton conversationToolButton"}
        aria-label="Supervisor"
        title="Supervisor"
        disabled={!activeSessionId}
        onClick={() => {
          closeMobileToolsMenu();
          onOpenHarness();
        }}
      >
        <SupervisorIcon />
        {mobileMenu ? <span>Supervisor</span> : null}
      </Button>
    </>
  );

  return (
    <div className="conversationToolbar">
      <div className="conversationToolbarGroup conversationToolbarGroupPrimary">
        {showMobileToolbarMenu ? (
          <div ref={mobileToolsMenuRef} className="conversationMenuWrap">
            <Button
              type="button"
              variant="outline"
              size="icon"
              className="toolbarButton mobileToolsTrigger conversationMenuButton"
              aria-label="Conversation tools"
              title="Conversation tools"
              aria-expanded={mobileToolsOpen ? "true" : "false"}
              onClick={() => setMobileToolsOpen((current) => !current)}
            >
              <MenuIcon />
            </Button>
            {mobileToolsOpen ? <div className="conversationMenuPanel">{renderConversationActionButtons(true)}</div> : null}
          </div>
        ) : null}
        <div className="conversationIdentityBlock">
          <div className="conversationTitle">{activeSessionId ? activeTitle : "No session selected"}</div>
          {activeSessionId && statusItems.length ? (
            <div className="conversationStatusStrip" aria-label="Session status">
              {statusItems.map((item) => (
                <span key={`${item.label}:${item.value}`} className={cn("conversationStatusChip", item.tone && item.tone !== "default" && item.tone)}>
                  <span>{item.label}</span>
                  <strong>{item.value}</strong>
                </span>
              ))}
            </div>
          ) : null}
        </div>
      </div>
      <div className="conversationToolbarGroup conversationToolbarGroupActions">
        {showMobileToolbarMenu ? null : renderConversationActionButtons()}
      </div>
    </div>
  );
}
