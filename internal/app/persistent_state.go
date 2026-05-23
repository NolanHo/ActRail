package app

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	sqlitestore "actrail/internal/adapters/sqlite"
)

type appStateStore interface {
	LoadAppState(context.Context) (sqlitestore.AppStateRow, error)
	ReplaceAppState(context.Context, sqlitestore.AppStateRow) error
}

type sessionReadStateStore interface {
	ListSessionReadStates(context.Context) ([]sqlitestore.SessionReadStateRow, error)
	UpsertSessionReadState(context.Context, sqlitestore.SessionReadStateRow) error
}

type workspaceBrowserState struct {
	SelectedPath string
	OpenPaths    []string
	HistoryItems []WorkspaceHistoryItem
}

func copyWorkspaceBrowserState(raw workspaceBrowserState) workspaceBrowserState {
	copied := workspaceBrowserState{
		SelectedPath: strings.TrimSpace(raw.SelectedPath),
		OpenPaths:    append([]string(nil), raw.OpenPaths...),
		HistoryItems: append([]WorkspaceHistoryItem(nil), raw.HistoryItems...),
	}
	return copied
}

func workspaceResponse(rootPath string, state workspaceBrowserState) SessionWorkspaceResponse {
	copied := copyWorkspaceBrowserState(state)
	return SessionWorkspaceResponse{
		RootPath:          rootPath,
		CanonicalRootPath: canonicalWorkspaceRootPath(rootPath),
		SelectedPath:      copied.SelectedPath,
		OpenPaths:         copied.OpenPaths,
		HistoryItems:      copied.HistoryItems,
	}
}

func canonicalWorkspaceRootPath(rootPath string) string {
	cleaned := strings.TrimSpace(rootPath)
	if cleaned == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil || strings.TrimSpace(resolved) == "" || filepath.Clean(resolved) == filepath.Clean(cleaned) {
		return ""
	}
	return filepath.Clean(resolved)
}

func durableAppStateFromSnapshot(recentCwds []string, cwdGroups map[string]CwdGroupMeta) sqlitestore.AppStateRow {
	rows := make([]sqlitestore.CwdGroupRow, 0, len(cwdGroups))
	keys := make([]string, 0, len(cwdGroups))
	for cwd := range cwdGroups {
		keys = append(keys, cwd)
	}
	sort.Strings(keys)
	for _, cwd := range keys {
		meta := cwdGroups[cwd]
		rows = append(rows, sqlitestore.CwdGroupRow{
			CWD:       cwd,
			Label:     strings.TrimSpace(meta.Label),
			Collapsed: meta.Collapsed,
		})
	}
	return sqlitestore.AppStateRow{
		RecentCwds: append([]string(nil), recentCwds...),
		CwdGroups:  rows,
	}
}

func loadPersistedAppState(store appStateStore) ([]string, map[string]CwdGroupMeta, error) {
	if store == nil {
		return []string{}, map[string]CwdGroupMeta{}, nil
	}
	state, err := store.LoadAppState(context.Background())
	if err != nil {
		return nil, nil, err
	}
	recentCwds := append([]string(nil), state.RecentCwds...)
	cwdGroups := make(map[string]CwdGroupMeta, len(state.CwdGroups))
	for _, row := range state.CwdGroups {
		cwd := normalizeSessionCWD(row.CWD)
		if cwd == "" {
			continue
		}
		cwdGroups[cwd] = CwdGroupMeta{Label: strings.TrimSpace(row.Label), Collapsed: row.Collapsed}
	}
	return recentCwds, cwdGroups, nil
}

func copyCwdGroups(raw map[string]CwdGroupMeta) map[string]CwdGroupMeta {
	if len(raw) == 0 {
		return map[string]CwdGroupMeta{}
	}
	copied := make(map[string]CwdGroupMeta, len(raw))
	for cwd, meta := range raw {
		copied[cwd] = CwdGroupMeta{Label: meta.Label, Collapsed: meta.Collapsed}
	}
	return copied
}

func normalizeRecentCwdList(raw []string) []string {
	if len(raw) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(raw))
	items := make([]string, 0, len(raw))
	for _, candidate := range raw {
		cwd := normalizeSessionCWD(candidate)
		if cwd == "" {
			continue
		}
		if _, exists := seen[cwd]; exists {
			continue
		}
		seen[cwd] = struct{}{}
		items = append(items, cwd)
	}
	return items
}

func pushRecentCwd(raw []string, cwd string) []string {
	normalized := normalizeSessionCWD(cwd)
	if normalized == "" {
		return normalizeRecentCwdList(raw)
	}
	items := []string{normalized}
	for _, existing := range raw {
		next := normalizeSessionCWD(existing)
		if next == "" || next == normalized {
			continue
		}
		items = append(items, next)
	}
	return items
}

func workspaceHistoryLabel(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	if base == "." || base == string(filepath.Separator) {
		return strings.TrimSpace(path)
	}
	return base
}
