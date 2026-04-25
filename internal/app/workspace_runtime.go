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
	return SessionWorkspaceResponse{
		RootPath:     root.Path(),
		OpenPaths:    []string{},
		HistoryItems: []WorkspaceHistoryItem{},
	}, nil
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
