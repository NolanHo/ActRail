package app

import (
	"context"
	"fmt"

	adaptergit "actrail/internal/adapters/git"
)

func (s *Stub) GitFileVersions(ctx context.Context, req GitFileVersionsRequest) (GitFileVersionsResponse, error) {
	root, err := s.sessionWorkspaceRoot(req.SessionID)
	if err != nil {
		return GitFileVersionsResponse{}, err
	}
	rel, err := workspaceFilePath(req.Path)
	if err != nil {
		return GitFileVersionsResponse{}, err
	}
	gitSvc, err := adaptergit.New(adaptergit.Options{})
	if err != nil {
		return GitFileVersionsResponse{}, fmt.Errorf("init git adapter: %w", err)
	}
	versions, err := gitSvc.FileVersions(ctx, root, rel)
	if err != nil {
		return GitFileVersionsResponse{}, mapWorkspaceAccessError(err)
	}
	items := make([]GitFileVersion, 0, len(versions.Items))
	for _, item := range versions.Items {
		items = append(items, GitFileVersion{
			VersionID:  item.VersionID,
			Label:      item.Label,
			CommitHash: item.CommitHash,
			Author:     item.Author,
			CommitTS:   timestampSeconds(item.CommitAt),
			Message:    item.Message,
			Current:    item.Current,
		})
	}
	return GitFileVersionsResponse{
		Path:           versions.Path.String(),
		FallbackReason: versions.FallbackReason,
		Items:          items,
	}, nil
}
