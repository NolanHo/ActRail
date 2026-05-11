import type { ComponentChildren } from "preact";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { NewSessionDialog } from "../../components/new-session/NewSessionDialog";
import { FileViewerDialog } from "../../components/workspace/FileViewerDialog";
import type { FileViewMode } from "../../components/workspace/FileViewerDialog";
import { InboxDialog } from "../../components/workspace/InboxDialog";

interface AppShellWorkspaceOverlaysProps {
  activeSessionId: string | null;
  activeSessionRuntimeId?: string | null;
  fileViewerLine: number | null;
  fileViewerMode: FileViewMode | null;
  fileViewerOpen: boolean;
  fileViewerPath: string;
  fileViewerRequestKey: number;
  inboxOpen: boolean;
  newSessionOpen: boolean;
  sessionsRail: ComponentChildren;
  sidebarOpen: boolean;
  voiceSettingsDialog: ComponentChildren;
  workspaceDetails: ComponentChildren;
  workspaceOpen: boolean;
  onCloseFileViewer(): void;
  onCloseInbox(): void;
  onCloseNewSession(): void;
  onCloseSidebar(): void;
  onCloseWorkspace(): void;
}

export function AppShellWorkspaceOverlays({
  activeSessionId,
  activeSessionRuntimeId,
  fileViewerLine,
  fileViewerMode,
  fileViewerOpen,
  fileViewerPath,
  fileViewerRequestKey,
  inboxOpen,
  newSessionOpen,
  sessionsRail,
  sidebarOpen,
  voiceSettingsDialog,
  workspaceDetails,
  workspaceOpen,
  onCloseFileViewer,
  onCloseInbox,
  onCloseNewSession,
  onCloseSidebar,
  onCloseWorkspace,
}: AppShellWorkspaceOverlaysProps) {
  return (
    <>
      <div data-testid="mobile-sessions-sheet">
        <Sheet open={sidebarOpen}>
          <button type="button" className="sheetBackdropButton" aria-label="Close sessions panel" onClick={onCloseSidebar} />
          <SheetContent side="left" className="mobileSheetContent" titleId="mobile-sessions-title">
            <div className="mobileSheetRail">
              <header className="mobileSheetHeader">
                <h2 id="mobile-sessions-title">Sessions</h2>
                <Button type="button" variant="ghost" size="sm" onClick={onCloseSidebar}>Close</Button>
              </header>
              {sessionsRail}
            </div>
          </SheetContent>
        </Sheet>
      </div>
      <Dialog open={workspaceOpen} onOpenChange={(open) => {
        if (!open) {
          onCloseWorkspace();
        }
      }}>
        <DialogContent className="workspaceDialog mobileDetailDialog max-w-none" titleId="workspace-dialog-title">
          <div data-testid="workspace-dialog" className="workspaceDialogBody">
            <DialogHeader className="workspaceDialogHeader">
              <div className="flex items-center justify-between gap-3">
                <div className="space-y-1">
                  <DialogTitle id="workspace-dialog-title">Metadata</DialogTitle>
                  <p className="text-sm text-muted-foreground">Inspect session metadata, inbox state, tracked files, diagnostics, and UI requests.</p>
                </div>
                <Button type="button" variant="ghost" size="sm" onClick={onCloseWorkspace}>Close</Button>
              </div>
            </DialogHeader>
            <div className="min-h-0 flex-1 overflow-y-auto p-5">
              {workspaceDetails}
            </div>
          </div>
        </DialogContent>
      </Dialog>
      {voiceSettingsDialog}
      <FileViewerDialog
        open={fileViewerOpen}
        sessionId={activeSessionId}
        runtimeId={activeSessionRuntimeId}
        initialPath={fileViewerPath}
        initialLine={fileViewerLine}
        initialMode={fileViewerMode}
        openRequestKey={fileViewerRequestKey}
        onClose={onCloseFileViewer}
      />
      <InboxDialog open={inboxOpen} sessionId={activeSessionId} onClose={onCloseInbox} />
      <NewSessionDialog open={newSessionOpen} onClose={onCloseNewSession} />
    </>
  );
}
