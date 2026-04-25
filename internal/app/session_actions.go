package app

import (
	"context"
	stdErrors "errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"actrail/internal/domain/message"
	"actrail/internal/domain/session"
)

type SessionResumeCandidatesRequest struct {
	CWD          string
	AgentBackend string
	Offset       int
	Limit        int
}

type SessionResumeCandidate struct {
	SessionID        string  `json:"session_id"`
	Title            string  `json:"title,omitempty"`
	Alias            string  `json:"alias,omitempty"`
	FirstUserMessage string  `json:"first_user_message,omitempty"`
	UpdatedTS        float64 `json:"updated_ts,omitempty"`
	GitBranch        string  `json:"git_branch,omitempty"`
}

type SessionResumeCandidatesResponse struct {
	OK         bool                     `json:"ok"`
	Exists     bool                     `json:"exists"`
	WillCreate bool                     `json:"will_create"`
	GitRepo    bool                     `json:"git_repo"`
	GitRoot    string                   `json:"git_root,omitempty"`
	GitBranch  string                   `json:"git_branch,omitempty"`
	Offset     int                      `json:"offset"`
	Limit      int                      `json:"limit"`
	Remaining  int                      `json:"remaining"`
	Sessions   []SessionResumeCandidate `json:"sessions"`
}

type RenameSessionRequest struct {
	SessionID session.SessionID
	Name      string
}

type RenameSessionResponse struct {
	OK    bool   `json:"ok"`
	Alias string `json:"alias,omitempty"`
}

type FocusSessionRequest struct {
	SessionID session.SessionID
	Focused   bool
}

type FocusSessionResponse struct {
	OK      bool `json:"ok"`
	Focused bool `json:"focused"`
}

type StringPatch struct {
	Present bool
	Value   *string
}

type Float64Patch struct {
	Present bool
	Value   *float64
}

type Int64Patch struct {
	Present bool
	Value   *int64
}

type EditSessionRequest struct {
	SessionID           session.SessionID
	Name                StringPatch
	PriorityOffset      Float64Patch
	SnoozeUntil         Int64Patch
	DependencySessionID StringPatch
}

type EditSessionResponse struct {
	OK                  bool    `json:"ok"`
	Alias               string  `json:"alias,omitempty"`
	PriorityOffset      float64 `json:"priority_offset,omitempty"`
	SnoozeUntil         *int64  `json:"snooze_until,omitempty"`
	DependencySessionID *string `json:"dependency_session_id,omitempty"`
	Focused             bool    `json:"focused,omitempty"`
}

type SwitchSessionModelRequest struct {
	SessionID session.SessionID
	Model     StringPatch
	Provider  StringPatch
}

type SwitchSessionModelResponse struct {
	OK       bool           `json:"ok"`
	Model    string         `json:"model,omitempty"`
	Provider string         `json:"provider,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

type DeleteSessionRequest struct {
	SessionID session.SessionID
}

type DeleteSessionResponse struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id,omitempty"`
	Removed   bool   `json:"removed"`
}

type RestartSessionRequest struct {
	SessionID session.SessionID
}

type RestartSessionResponse struct {
	OK bool `json:"ok"`
}

type HandoffSessionRequest struct {
	SessionID session.SessionID
}

type HandoffSessionResponse struct {
	OK bool `json:"ok"`
}

func (s *Stub) SessionResumeCandidates(_ context.Context, req SessionResumeCandidatesRequest) (SessionResumeCandidatesResponse, error) {
	cwd := normalizeSessionCWD(req.CWD)
	if cwd == "" {
		return SessionResumeCandidatesResponse{}, Invalid("cwd", "cwd required")
	}
	backend := ""
	if raw := strings.TrimSpace(req.AgentBackend); raw != "" {
		parsed, err := session.ParseBackend(raw)
		if err != nil {
			return SessionResumeCandidatesResponse{}, Invalid("backend", err.Error())
		}
		backend = parsed.String()
	}
	exists, willCreate := pathExists(cwd)
	items := s.registry.List()
	candidates := make([]sessionRecord, 0, len(items))
	for _, record := range items {
		if normalizeSessionCWD(record.cwd) != cwd {
			continue
		}
		if backend != "" && record.identity.Backend().String() != backend {
			continue
		}
		candidates = append(candidates, record)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].updatedAt.Equal(candidates[j].updatedAt) {
			return candidates[i].identity.SessionID().String() > candidates[j].identity.SessionID().String()
		}
		return candidates[i].updatedAt.After(candidates[j].updatedAt)
	})
	start, end := paginate(len(candidates), req.Offset, req.Limit)
	payload := SessionResumeCandidatesResponse{
		OK:         true,
		Exists:     exists,
		WillCreate: willCreate,
		GitRepo:    false,
		Offset:     req.Offset,
		Limit:      req.Limit,
		Remaining:  len(candidates) - end,
		Sessions:   make([]SessionResumeCandidate, 0, end-start),
	}
	for _, record := range candidates[start:end] {
		payload.Sessions = append(payload.Sessions, sessionResumeCandidateFromRecord(record))
	}
	return payload, nil
}

func (s *Stub) RenameSession(_ context.Context, req RenameSessionRequest) (RenameSessionResponse, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return RenameSessionResponse{}, Invalid("name", "name required")
	}
	record, ok, err := s.registry.Update(req.SessionID, false, func(record *sessionRecord) error {
		record.title = name
		record.alias = name
		return nil
	})
	if err != nil {
		return RenameSessionResponse{}, err
	}
	if !ok {
		return RenameSessionResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	return RenameSessionResponse{OK: true, Alias: displayAlias(record)}, nil
}

func (s *Stub) FocusSession(_ context.Context, req FocusSessionRequest) (FocusSessionResponse, error) {
	record, ok, err := s.registry.Update(req.SessionID, false, func(record *sessionRecord) error {
		record.focused = req.Focused
		return nil
	})
	if err != nil {
		return FocusSessionResponse{}, err
	}
	if !ok {
		return FocusSessionResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	return FocusSessionResponse{OK: true, Focused: record.focused}, nil
}

func (s *Stub) EditSession(_ context.Context, req EditSessionRequest) (EditSessionResponse, error) {
	target, err := s.lookupSession(req.SessionID)
	if err != nil {
		return EditSessionResponse{}, err
	}
	var nextName *string
	if req.Name.Present {
		if req.Name.Value == nil || strings.TrimSpace(*req.Name.Value) == "" {
			return EditSessionResponse{}, Invalid("name", "name required")
		}
		value := strings.TrimSpace(*req.Name.Value)
		nextName = &value
	}
	var nextPriority *float64
	if req.PriorityOffset.Present {
		if req.PriorityOffset.Value == nil {
			return EditSessionResponse{}, Invalid("priority_offset", "priority_offset required")
		}
		if math.IsNaN(*req.PriorityOffset.Value) || math.IsInf(*req.PriorityOffset.Value, 0) {
			return EditSessionResponse{}, Invalid("priority_offset", "priority_offset must be finite")
		}
		value := *req.PriorityOffset.Value
		nextPriority = &value
	}
	var nextSnooze *time.Time
	if req.SnoozeUntil.Present && req.SnoozeUntil.Value != nil && *req.SnoozeUntil.Value > 0 {
		value := time.Unix(*req.SnoozeUntil.Value, 0).UTC()
		nextSnooze = &value
	}
	var nextDependency *session.SessionID
	if req.DependencySessionID.Present && req.DependencySessionID.Value != nil {
		parsed, err := session.ParseSessionID(*req.DependencySessionID.Value)
		if err != nil {
			return EditSessionResponse{}, Invalid("dependency_session_id", err.Error())
		}
		dependency, err := s.lookupSession(parsed)
		if err != nil {
			return EditSessionResponse{}, NotFound(fmt.Sprintf("session %q not found", parsed))
		}
		resolved := dependency.identity.SessionID()
		if resolved == target.identity.SessionID() {
			return EditSessionResponse{}, Invalid("dependency_session_id", "dependency_session_id cannot reference the same session")
		}
		nextDependency = &resolved
	}
	record, ok, err := s.registry.Update(req.SessionID, false, func(record *sessionRecord) error {
		if nextName != nil {
			record.title = *nextName
			record.alias = *nextName
		}
		if nextPriority != nil {
			record.priorityOffset = *nextPriority
		}
		if req.SnoozeUntil.Present {
			record.snoozeUntil = copyTimePtr(nextSnooze)
		}
		if req.DependencySessionID.Present {
			record.dependencySessionID = copySessionIDPtr(nextDependency)
		}
		return nil
	})
	if err != nil {
		return EditSessionResponse{}, err
	}
	if !ok {
		return EditSessionResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	return editSessionResponseFromRecord(record), nil
}

func (s *Stub) SwitchSessionModel(_ context.Context, req SwitchSessionModelRequest) (SwitchSessionModelResponse, error) {
	if !req.Model.Present && !req.Provider.Present {
		return SwitchSessionModelResponse{}, Invalid("model", "model or provider required")
	}
	var nextModel *string
	if req.Model.Present {
		if req.Model.Value == nil || strings.TrimSpace(*req.Model.Value) == "" {
			return SwitchSessionModelResponse{}, Invalid("model", "model required")
		}
		value := strings.TrimSpace(*req.Model.Value)
		nextModel = &value
	}
	var nextProvider *string
	if req.Provider.Present {
		if req.Provider.Value != nil {
			value := strings.TrimSpace(*req.Provider.Value)
			nextProvider = &value
		}
	}
	record, ok, err := s.registry.Update(req.SessionID, false, func(record *sessionRecord) error {
		if nextModel != nil {
			record.model = *nextModel
		}
		if req.Provider.Present {
			if nextProvider == nil {
				record.provider = ""
			} else {
				record.provider = *nextProvider
			}
		}
		return nil
	})
	if err != nil {
		return SwitchSessionModelResponse{}, err
	}
	if !ok {
		return SwitchSessionModelResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	return SwitchSessionModelResponse{OK: true, Model: record.model, Provider: record.provider}, nil
}

func (s *Stub) DeleteSession(ctx context.Context, req DeleteSessionRequest) (DeleteSessionResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return DeleteSessionResponse{}, err
	}
	if err := record.runtime.Kill(ctx); err != nil {
		return DeleteSessionResponse{}, err
	}
	removed, ok := s.registry.Delete(req.SessionID)
	if !ok {
		return DeleteSessionResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	return DeleteSessionResponse{OK: true, SessionID: removed.identity.SessionID().String(), Removed: true}, nil
}

func (s *Stub) RestartSession(_ context.Context, req RestartSessionRequest) (RestartSessionResponse, error) {
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return RestartSessionResponse{}, err
	}
	return RestartSessionResponse{}, Unsupported("session restart not implemented")
}

func (s *Stub) HandoffSession(_ context.Context, req HandoffSessionRequest) (HandoffSessionResponse, error) {
	if _, err := s.lookupSession(req.SessionID); err != nil {
		return HandoffSessionResponse{}, err
	}
	return HandoffSessionResponse{}, Unsupported("session handoff not implemented")
}

func sessionResumeCandidateFromRecord(record sessionRecord) SessionResumeCandidate {
	return SessionResumeCandidate{
		SessionID:        record.identity.SessionID().String(),
		Title:            record.title,
		Alias:            displayAlias(record),
		FirstUserMessage: firstUserMessage(record.transcript),
		UpdatedTS:        timestampSeconds(record.updatedAt),
	}
}

func editSessionResponseFromRecord(record sessionRecord) EditSessionResponse {
	return EditSessionResponse{
		OK:                  true,
		Alias:               displayAlias(record),
		PriorityOffset:      record.priorityOffset,
		SnoozeUntil:         unixSecondsPtr(record.snoozeUntil),
		DependencySessionID: sessionIDPtrString(record.dependencySessionID),
		Focused:             record.focused,
	}
}

func firstUserMessage(transcript message.Transcript) string {
	for _, item := range transcript.Items() {
		if item.Role() == message.RoleUser {
			return item.Text()
		}
	}
	return ""
}

func displayAlias(record sessionRecord) string {
	if value := strings.TrimSpace(record.alias); value != "" {
		return value
	}
	return record.title
}

func normalizeSessionCWD(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}

func pathExists(path string) (bool, bool) {
	_, err := os.Stat(path)
	if err == nil {
		return true, false
	}
	if stdErrors.Is(err, os.ErrNotExist) {
		return false, true
	}
	return false, false
}

func unixSecondsPtr(ts *time.Time) *int64 {
	if ts == nil || ts.IsZero() {
		return nil
	}
	value := ts.UTC().Unix()
	return &value
}

func sessionIDPtrString(id *session.SessionID) *string {
	if id == nil {
		return nil
	}
	value := id.String()
	return &value
}
