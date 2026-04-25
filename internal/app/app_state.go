package app

import (
	"context"
	"strings"
)

func (s *Stub) bootstrapAppStateSnapshot() ([]string, map[string]CwdGroupMeta) {
	if s == nil {
		return []string{}, map[string]CwdGroupMeta{}
	}
	s.appStateMu.RLock()
	defer s.appStateMu.RUnlock()
	return append([]string(nil), s.recentCwds...), copyCwdGroups(s.cwdGroups)
}

func (s *Stub) recordRecentCWD(cwd string) error {
	if s == nil {
		return nil
	}
	return s.updateAppState(func(recentCwds []string, cwdGroups map[string]CwdGroupMeta) ([]string, map[string]CwdGroupMeta, error) {
		return pushRecentCwd(recentCwds, cwd), cwdGroups, nil
	})
}

func (s *Stub) EditCwdGroup(_ context.Context, req EditCwdGroupRequest) (EditCwdGroupResponse, error) {
	cwd := normalizeSessionCWD(req.CWD)
	if cwd == "" {
		return EditCwdGroupResponse{}, Invalid("cwd", "cwd required")
	}
	var response EditCwdGroupResponse
	err := s.updateAppState(func(recentCwds []string, cwdGroups map[string]CwdGroupMeta) ([]string, map[string]CwdGroupMeta, error) {
		meta := cwdGroups[cwd]
		if req.Label != nil {
			meta.Label = strings.TrimSpace(*req.Label)
		}
		if req.Collapsed != nil {
			meta.Collapsed = *req.Collapsed
		}
		if meta.Label == "" && !meta.Collapsed {
			delete(cwdGroups, cwd)
		} else {
			cwdGroups[cwd] = meta
		}
		response = EditCwdGroupResponse{OK: true, CWD: cwd, Label: meta.Label, Collapsed: meta.Collapsed}
		return recentCwds, cwdGroups, nil
	})
	if err != nil {
		return EditCwdGroupResponse{}, err
	}
	return response, nil
}

func (s *Stub) updateAppState(apply func([]string, map[string]CwdGroupMeta) ([]string, map[string]CwdGroupMeta, error)) error {
	if s == nil {
		return nil
	}
	s.appStateMu.Lock()
	defer s.appStateMu.Unlock()
	nextRecent, nextGroups, err := apply(append([]string(nil), s.recentCwds...), copyCwdGroups(s.cwdGroups))
	if err != nil {
		return err
	}
	normalizedRecent := normalizeRecentCwdList(nextRecent)
	if nextGroups == nil {
		nextGroups = map[string]CwdGroupMeta{}
	}
	if err := s.persistAppStateLocked(normalizedRecent, nextGroups); err != nil {
		return err
	}
	s.recentCwds = normalizedRecent
	s.cwdGroups = nextGroups
	return nil
}

func (s *Stub) persistAppStateLocked(recentCwds []string, cwdGroups map[string]CwdGroupMeta) error {
	if s == nil || s.appStore == nil {
		return nil
	}
	return s.appStore.ReplaceAppState(context.Background(), durableAppStateFromSnapshot(recentCwds, cwdGroups))
}
