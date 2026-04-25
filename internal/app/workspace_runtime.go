package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	adapterfs "actrail/internal/adapters/filesystem"
	"actrail/internal/domain/session"
	"actrail/internal/domain/workspace"
)

func (s *Stub) SessionWorkspace(_ context.Context, req SessionWorkspaceRequest) (SessionWorkspaceResponse, error) {
	root, err := s.sessionWorkspaceRoot(req.SessionID)
	if err != nil {
		return SessionWorkspaceResponse{}, err
	}
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionWorkspaceResponse{}, err
	}
	return workspaceResponse(root.Path(), record.workspace), nil
}

func (s *Stub) UpdateSessionWorkspace(_ context.Context, req UpdateSessionWorkspaceRequest) (SessionWorkspaceResponse, error) {
	root, err := s.sessionWorkspaceRoot(req.SessionID)
	if err != nil {
		return SessionWorkspaceResponse{}, err
	}
	state, err := normalizeWorkspaceBrowserState(req.SelectedPath, req.OpenPaths, req.HistoryItems)
	if err != nil {
		return SessionWorkspaceResponse{}, err
	}
	updated, ok, err := s.registry.UpdateWorkspace(req.SessionID, state)
	if err != nil {
		return SessionWorkspaceResponse{}, err
	}
	if !ok {
		return SessionWorkspaceResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	return workspaceResponse(root.Path(), updated), nil
}

func normalizeWorkspaceBrowserState(selectedPath string, openPaths []string, historyItems []WorkspaceHistoryItem) (workspaceBrowserState, error) {
	normalizedSelectedPath, err := normalizeWorkspaceSelectedPath(selectedPath)
	if err != nil {
		return workspaceBrowserState{}, err
	}
	normalizedOpenPaths, err := normalizeWorkspaceOpenPaths(openPaths)
	if err != nil {
		return workspaceBrowserState{}, err
	}
	normalizedHistoryItems, err := normalizeWorkspaceHistoryItems(historyItems)
	if err != nil {
		return workspaceBrowserState{}, err
	}
	if normalizedSelectedPath != "" {
		normalizedOpenPaths = prependUniquePath(normalizedOpenPaths, normalizedSelectedPath)
		normalizedHistoryItems = prependUniqueHistoryItem(normalizedHistoryItems, WorkspaceHistoryItem{
			Path:  normalizedSelectedPath,
			Label: workspaceHistoryLabel(normalizedSelectedPath),
		})
	}
	return workspaceBrowserState{
		SelectedPath: normalizedSelectedPath,
		OpenPaths:    normalizedOpenPaths,
		HistoryItems: normalizedHistoryItems,
	}, nil
}

func normalizeWorkspaceSelectedPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	rel, err := workspace.ParseRelPath(trimmed)
	if err != nil {
		return "", Invalid("selected_path", err.Error())
	}
	return rel.String(), nil
}

func normalizeWorkspaceOpenPaths(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]struct{}, len(raw))
	items := make([]string, 0, len(raw))
	for _, candidate := range raw {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		rel, err := workspace.ParseRelPath(trimmed)
		if err != nil {
			return nil, Invalid("open_paths", err.Error())
		}
		normalized := rel.String()
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		items = append(items, normalized)
	}
	return items, nil
}

func normalizeWorkspaceHistoryItems(raw []WorkspaceHistoryItem) ([]WorkspaceHistoryItem, error) {
	if len(raw) == 0 {
		return []WorkspaceHistoryItem{}, nil
	}
	seen := make(map[string]struct{}, len(raw))
	items := make([]WorkspaceHistoryItem, 0, len(raw))
	for _, candidate := range raw {
		trimmedPath := strings.TrimSpace(candidate.Path)
		if trimmedPath == "" {
			continue
		}
		rel, err := workspace.ParseRelPath(trimmedPath)
		if err != nil {
			return nil, Invalid("history_items", err.Error())
		}
		normalizedPath := rel.String()
		if _, exists := seen[normalizedPath]; exists {
			continue
		}
		seen[normalizedPath] = struct{}{}
		label := strings.TrimSpace(candidate.Label)
		if label == "" {
			label = workspaceHistoryLabel(normalizedPath)
		}
		items = append(items, WorkspaceHistoryItem{Path: normalizedPath, Label: label})
	}
	return items, nil
}

func prependUniquePath(items []string, candidate string) []string {
	if candidate == "" {
		return append([]string(nil), items...)
	}
	next := []string{candidate}
	for _, existing := range items {
		if existing == candidate {
			continue
		}
		next = append(next, existing)
	}
	return next
}

func prependUniqueHistoryItem(items []WorkspaceHistoryItem, candidate WorkspaceHistoryItem) []WorkspaceHistoryItem {
	if strings.TrimSpace(candidate.Path) == "" {
		return append([]WorkspaceHistoryItem(nil), items...)
	}
	next := []WorkspaceHistoryItem{candidate}
	for _, existing := range items {
		if existing.Path == candidate.Path {
			continue
		}
		next = append(next, existing)
	}
	return next
}

func (s *Stub) WorkspaceFileList(ctx context.Context, req WorkspaceFileListRequest) (WorkspaceFileListResponse, error) {
	root, err := s.sessionWorkspaceRoot(req.SessionID)
	if err != nil {
		return WorkspaceFileListResponse{}, err
	}
	rel, err := workspaceListPath(req.Path)
	if err != nil {
		return WorkspaceFileListResponse{}, err
	}
	files, err := adapterfs.New(adapterfs.Options{})
	if err != nil {
		return WorkspaceFileListResponse{}, fmt.Errorf("init filesystem adapter: %w", err)
	}
	list, err := files.List(ctx, root, workspace.FileListOptions{Path: rel, Search: req.Search, Limit: req.Limit})
	if err != nil {
		return WorkspaceFileListResponse{}, mapWorkspaceAccessError(err)
	}
	items := make([]WorkspaceFileEntry, 0, len(list.Items))
	for _, item := range list.Items {
		items = append(items, WorkspaceFileEntry{
			Path:      item.Path.String(),
			Name:      item.Name,
			Kind:      string(item.Kind),
			SizeBytes: item.SizeBytes,
		})
	}
	return WorkspaceFileListResponse{
		RootPath:  list.RootPath,
		Path:      list.Path.String(),
		Items:     items,
		Truncated: list.Truncated,
	}, nil
}

func (s *Stub) WorkspaceFileRead(ctx context.Context, req WorkspaceFileReadRequest) (WorkspaceFileReadResponse, error) {
	root, err := s.sessionWorkspaceRoot(req.SessionID)
	if err != nil {
		return WorkspaceFileReadResponse{}, err
	}
	rel, err := workspaceFilePath(req.Path)
	if err != nil {
		return WorkspaceFileReadResponse{}, err
	}
	files, err := adapterfs.New(adapterfs.Options{})
	if err != nil {
		return WorkspaceFileReadResponse{}, fmt.Errorf("init filesystem adapter: %w", err)
	}
	item, err := files.Read(ctx, root, rel)
	if err != nil {
		return WorkspaceFileReadResponse{}, mapWorkspaceAccessError(err)
	}
	return WorkspaceFileReadResponse{
		Path:              item.Path.String(),
		Kind:              string(item.Kind),
		MIMEType:          item.MIMEType,
		Encoding:          item.Encoding,
		SizeBytes:         item.SizeBytes,
		Text:              item.Text,
		DownloadName:      item.DownloadName,
		UnsupportedReason: item.UnsupportedReason,
	}, nil
}

func (s *Stub) sessionWorkspaceRoot(sessionID session.SessionID) (workspace.Root, error) {
	record, err := s.lookupSession(sessionID)
	if err != nil {
		return workspace.Root{}, err
	}
	root, err := workspace.NewRoot(record.cwd)
	if err != nil {
		return workspace.Root{}, fmt.Errorf("session %q workspace root invalid: %w", sessionID, err)
	}
	return root, nil
}

func workspaceListPath(raw string) (workspace.RelPath, error) {
	rel, err := workspace.ParseRelPathAllowRoot(raw)
	if err != nil {
		return workspace.RelPath{}, Invalid("path", err.Error())
	}
	return rel, nil
}

func workspaceFilePath(raw string) (workspace.RelPath, error) {
	rel, err := workspace.ParseRelPath(raw)
	if err != nil {
		return workspace.RelPath{}, Invalid("path", err.Error())
	}
	return rel, nil
}

func mapWorkspaceAccessError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, os.ErrNotExist):
		return NotFound("workspace path not found")
	}
	if strings.Contains(err.Error(), " is a directory") || strings.Contains(err.Error(), " is not a directory") {
		return Invalid("path", err.Error())
	}
	return err
}
