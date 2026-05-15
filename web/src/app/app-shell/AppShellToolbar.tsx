import { useEffect, useRef, useState } from "preact/hooks";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { FileIcon, InboxIcon, MenuIcon, MetadataIcon, ProbeIcon, SessionsIcon, StopIcon } from "./icons";

export interface ConversationStatusItem {
  label: string;
  value: string;
  tone?: "default" | "attention" | "error" | "busy" | "success";
  actionLabel?: string;
  onActivate?: () => void;
}

export function SessionStatusStrip({ items, className }: { items: ConversationStatusItem[]; className?: string }) {
  if (!items.length) {
    return null;
  }
  return (
    <div className={cn("conversationStatusStrip", className)} aria-label="Session status">
      {items.map((item) => {
        const className = cn(
          "conversationStatusChip",
          item.onActivate && "actionable",
          item.tone && item.tone !== "default" && item.tone,
        );
        const content = (
          <>
            <span>{item.label}</span>
            <strong>{item.value}</strong>
          </>
        );
        return item.onActivate ? (
          <button
            key={`${item.label}:${item.value}`}
            type="button"
            className={className}
            aria-label={item.actionLabel || item.label}
            title={item.actionLabel || item.label}
            onClick={item.onActivate}
          >
            {content}
          </button>
        ) : (
          <span key={`${item.label}:${item.value}`} className={className}>
            {content}
          </span>
        );
      })}
    </div>
  );
}

interface AppShellToolbarProps {
  activeSessionId: string | null;
  activeTitle: string;
  canInterrupt: boolean;
  canProbeRuntime?: boolean;
  inboxCount?: number;
  probingRuntime?: boolean;
  showInterruptAction: boolean;
  statusItems?: ConversationStatusItem[];
  showMobileSessionsTrigger: boolean;
  showMobileToolbarMenu: boolean;
  onInterrupt(): void;
  onProbeRuntime?(): void;
  onOpenFiles(): void;
  onOpenInbox(): void;
  onOpenSessions(): void;
  onOpenWorkspace(): void;
}

export function AppShellToolbar({
  activeSessionId,
  activeTitle,
  canInterrupt,
  canProbeRuntime = false,
  inboxCount = 0,
  probingRuntime = false,
  showInterruptAction,
  statusItems = [],
  showMobileSessionsTrigger,
  showMobileToolbarMenu,
  onInterrupt,
  onProbeRuntime,
  onOpenFiles,
  onOpenInbox,
  onOpenSessions,
  onOpenWorkspace,
}: AppShellToolbarProps) {
  const [mobileToolsOpen, setMobileToolsOpen] = useState(false);
  const mobileToolsMenuRef = useRef<HTMLDivElement | null>(null);
  const sessionInboxLabel = inboxCount > 0 ? `Session Inbox ${inboxCount}` : "Session Inbox";

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

  const renderConversationActionButtons = (mobileMenu = false) => {
    if (mobileMenu) {
      return (
        <>
          {showMobileSessionsTrigger ? (
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
            variant="ghost"
            size="sm"
            className="conversationMenuItem"
            aria-label="Metadata"
            title="Metadata"
            disabled={!activeSessionId}
            onClick={() => {
              closeMobileToolsMenu();
              onOpenWorkspace();
            }}
          >
            <MetadataIcon />
            <span>Metadata</span>
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="conversationMenuItem"
            aria-label="Files"
            title="Files"
            disabled={!activeSessionId}
            onClick={() => {
              closeMobileToolsMenu();
              onOpenFiles();
            }}
          >
            <FileIcon />
            <span>Files</span>
          </Button>
          {canProbeRuntime ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="conversationMenuItem"
              aria-label="Probe runtime state"
              title="Probe runtime state"
              disabled={probingRuntime}
              onClick={() => {
                closeMobileToolsMenu();
                onProbeRuntime?.();
              }}
            >
              <ProbeIcon />
              <span>{probingRuntime ? "Probing" : "Probe"}</span>
            </Button>
          ) : null}
          {showInterruptAction ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="conversationMenuItem conversationMenuItemDanger"
              aria-label="Interrupt (Esc)"
              title="Interrupt (Esc)"
              disabled={!canInterrupt}
              onClick={() => {
                closeMobileToolsMenu();
                onInterrupt();
              }}
            >
              <StopIcon />
              <span>Interrupt</span>
            </Button>
          ) : null}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="conversationMenuItem"
            aria-label="Session Inbox"
            title="Session Inbox"
            disabled={!activeSessionId}
            onClick={() => {
              closeMobileToolsMenu();
              onOpenInbox();
            }}
          >
            <InboxIcon />
            <span>{sessionInboxLabel}</span>
          </Button>
        </>
      );
    }
    return (
      <>
        <div className="conversationToolCluster" aria-label="Session inspection tools">
          <Button
            type="button"
            variant="outline"
            size="icon"
            className="toolbarButton conversationToolButton"
            aria-label="Metadata"
            title="Metadata"
            disabled={!activeSessionId}
            onClick={onOpenWorkspace}
          >
            <MetadataIcon />
          </Button>
          <Button
            type="button"
            variant="outline"
            size="icon"
            className="toolbarButton conversationToolButton"
            aria-label="Files"
            title="Files"
            disabled={!activeSessionId}
            onClick={onOpenFiles}
          >
            <FileIcon />
          </Button>
        </div>
        <div className="conversationToolCluster" aria-label="Runtime controls">
          {canProbeRuntime ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="toolbarButton conversationToolButton"
              aria-label="Probe runtime state"
              title="Probe runtime state"
              disabled={probingRuntime}
              onClick={() => onProbeRuntime?.()}
            >
              <ProbeIcon />
            </Button>
          ) : null}
          {showInterruptAction ? (
            <Button
              type="button"
              variant="outline"
              size="icon"
              className="toolbarButton conversationToolButton conversationToolButtonDanger"
              aria-label="Interrupt (Esc)"
              title="Interrupt (Esc)"
              disabled={!canInterrupt}
              onClick={onInterrupt}
            >
              <StopIcon />
            </Button>
          ) : null}
        </div>
        <div className="conversationToolCluster" aria-label="Session workflow">
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="toolbarButton conversationToolButton sessionInboxButton"
            aria-label="Session Inbox"
            title="Session Inbox"
            disabled={!activeSessionId}
            onClick={onOpenInbox}
          >
            <InboxIcon />
            <span>{sessionInboxLabel}</span>
          </Button>
        </div>
      </>
    );
  };

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
          {activeSessionId ? <SessionStatusStrip items={statusItems} /> : null}
        </div>
      </div>
      <div className="conversationToolbarGroup conversationToolbarGroupActions">
        {showMobileToolbarMenu ? null : renderConversationActionButtons()}
      </div>
    </div>
  );
}
