package app

import (
	"bufio"
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"actrail/internal/domain/message"
	"actrail/internal/domain/pi"
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
	DisplayName      string  `json:"display_name,omitempty"`
	CWD              string  `json:"cwd,omitempty"`
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
	OK                bool                  `json:"ok"`
	Session           *CreatedSession       `json:"session,omitempty"`
	SessionID         string                `json:"session_id,omitempty"`
	RuntimeID         string                `json:"runtime_id,omitempty"`
	PreviousRuntimeID string                `json:"previous_runtime_id,omitempty"`
	Restarted         bool                  `json:"restarted,omitempty"`
	WSAttach          *SessionAttachRequest `json:"ws_attach,omitempty"`
}

type HandoffSessionRequest struct {
	SessionID session.SessionID
}

type HandoffSessionResponse struct {
	OK                bool                  `json:"ok"`
	Session           *CreatedSession       `json:"session,omitempty"`
	SessionID         string                `json:"session_id,omitempty"`
	RuntimeID         string                `json:"runtime_id,omitempty"`
	PreviousSessionID string                `json:"previous_session_id,omitempty"`
	HistoryPath       string                `json:"history_path,omitempty"`
	WSAttach          *SessionAttachRequest `json:"ws_attach,omitempty"`
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
		if !sameSessionCWD(record.cwd, cwd) {
			continue
		}
		if backend != "" && record.identity.Backend().String() != backend {
			continue
		}
		candidates = append(candidates, record)
	}
	ordered := sortSessionsForDisplay(candidates, s.registry.now())
	resumeItems := make([]SessionResumeCandidate, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, item := range ordered {
		if item.record.identity.Backend() != session.BackendPI || strings.TrimSpace(item.record.importedSourcePath) == "" {
			continue
		}
		candidate := sessionResumeCandidateFromRecord(item.record, item.updatedAt)
		resumeItems = append(resumeItems, candidate)
		seen[candidate.SessionID] = struct{}{}
	}
	if backend == "" || backend == session.BackendPI.String() {
		for _, candidate := range scanPIResumeCandidates(cwd) {
			if _, ok := seen[candidate.SessionID]; ok {
				continue
			}
			seen[candidate.SessionID] = struct{}{}
			resumeItems = append(resumeItems, candidate)
		}
	}
	sort.SliceStable(resumeItems, func(i, j int) bool {
		if resumeItems[i].UpdatedTS != resumeItems[j].UpdatedTS {
			return resumeItems[i].UpdatedTS > resumeItems[j].UpdatedTS
		}
		return resumeItems[i].SessionID < resumeItems[j].SessionID
	})
	start, end := paginate(len(resumeItems), req.Offset, req.Limit)
	payload := SessionResumeCandidatesResponse{
		OK:         true,
		Exists:     exists,
		WillCreate: willCreate,
		GitRepo:    false,
		Offset:     req.Offset,
		Limit:      req.Limit,
		Remaining:  len(resumeItems) - end,
		Sessions:   append([]SessionResumeCandidate(nil), resumeItems[start:end]...),
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
	removed, ok, err := s.registry.Delete(req.SessionID)
	if err != nil {
		return DeleteSessionResponse{}, err
	}
	if err := s.setRuntimeAgentRunning(req.SessionID, false); err != nil {
		return DeleteSessionResponse{}, err
	}
	if !ok {
		return DeleteSessionResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	s.helpers.Remove(req.SessionID)
	if err := s.helperBindings.Delete(req.SessionID); err != nil {
		return DeleteSessionResponse{}, err
	}
	return DeleteSessionResponse{OK: true, SessionID: removed.identity.SessionID().String(), Removed: true}, nil
}

func (s *Stub) RestartSession(ctx context.Context, req RestartSessionRequest) (RestartSessionResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return RestartSessionResponse{}, err
	}
	if record.identity.Historical() {
		return RestartSessionResponse{}, Unsupported("historical sessions cannot be restarted")
	}
	identity, _, ok, err := s.registry.ReserveRestartIdentity(req.SessionID)
	if err != nil {
		return RestartSessionResponse{}, err
	}
	if !ok {
		return RestartSessionResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	sourcePath := strings.TrimSpace(record.importedSourcePath)
	if record.identity.Backend() == session.BackendPI && sourcePath == "" {
		var err error
		sourcePath, err = newPISessionSourcePath(record.cwd, identity.SessionID(), s.registry.now())
		if err != nil {
			return RestartSessionResponse{}, err
		}
	}
	newRuntime, err := s.launcher.Launch(ctx, runtimeLaunchRequest{
		SessionID:       identity.SessionID(),
		Backend:         record.identity.Backend(),
		CWD:             record.cwd,
		Provider:        record.provider,
		Model:           record.model,
		ReasoningEffort: record.reasoningEffort,
		SessionPath:     sourcePath,
	})
	if err != nil {
		_ = newRuntime.CleanupHelperArtifacts()
		return RestartSessionResponse{}, err
	}
	previousBinding, err := record.runtime.CurrentHelperBinding(record.identity.SessionID())
	if err != nil {
		_ = newRuntime.Kill(context.Background())
		_ = newRuntime.CleanupHelperArtifacts()
		return RestartSessionResponse{}, err
	}
	if err := s.bindRuntimeCurrentGeneration(identity.SessionID(), newRuntime); err != nil {
		_ = newRuntime.Kill(context.Background())
		_ = newRuntime.CleanupHelperArtifacts()
		return RestartSessionResponse{}, err
	}
	restoreBinding := func() {
		if previousBinding != nil {
			_ = s.bindCurrentGeneration(helperGenerationBinding{
				SessionID:        record.identity.SessionID(),
				GenerationID:     previousBinding.GenerationID,
				LastReplayOffset: previousBinding.LastReplayOffset,
			})
			return
		}
		_ = s.helperBindings.Delete(record.identity.SessionID())
	}
	if err := s.setRuntimeAgentRunning(req.SessionID, false); err != nil {
		restoreBinding()
		_ = newRuntime.Kill(context.Background())
		_ = newRuntime.CleanupHelperArtifacts()
		return RestartSessionResponse{}, err
	}
	updated, ok, err := s.registry.SwapRuntime(req.SessionID, identity, newRuntime, sourcePath)
	if err != nil {
		restoreBinding()
		_ = newRuntime.Kill(context.Background())
		_ = newRuntime.CleanupHelperArtifacts()
		return RestartSessionResponse{}, err
	}
	if !ok {
		restoreBinding()
		_ = newRuntime.Kill(context.Background())
		_ = newRuntime.CleanupHelperArtifacts()
		return RestartSessionResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	s.startRuntimeIngest(updated.identity.SessionID(), updated.identity.Backend(), newRuntime)
	_ = record.runtime.Kill(context.Background())
	_ = record.runtime.CleanupHelperArtifacts()
	queue := queueSnapshotFromState(updated.state)
	s.emitQueueState(updated.identity.SessionID(), queue)
	s.emitSessionState(updated.identity.SessionID())
	if updated.state.Queue().Len() > 0 {
		s.scheduleQueuedDispatch(updated.identity.SessionID())
	}
	previousRuntimeID, _ := record.identity.RuntimeID()
	currentRuntimeID, _ := updated.identity.RuntimeID()
	var wsAttach *SessionAttachRequest
	if stream, err := session.MainStream(updated.identity); err == nil {
		wsAttach = &SessionAttachRequest{
			SessionID:            updated.identity.SessionID().String(),
			SuggestSubscriptions: []string{stream.String()},
		}
	}
	return RestartSessionResponse{
		OK:                true,
		Session:           createdSessionFromRecord(updated),
		SessionID:         updated.identity.SessionID().String(),
		RuntimeID:         currentRuntimeID.String(),
		PreviousRuntimeID: previousRuntimeID.String(),
		Restarted:         true,
		WSAttach:          wsAttach,
	}, nil
}

func (s *Stub) HandoffSession(ctx context.Context, req HandoffSessionRequest) (HandoffSessionResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return HandoffSessionResponse{}, err
	}
	if record.identity.Historical() {
		return HandoffSessionResponse{}, Unsupported("historical sessions cannot be handed off")
	}
	if record.identity.Backend() != session.BackendPI {
		return HandoffSessionResponse{}, Unsupported("session handoff is only implemented for pi backend")
	}

	stringPtr := func(value string) *string {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil
		}
		return &trimmed
	}
	title := strings.TrimSpace(displayAlias(record))
	if title == "" {
		title = strings.TrimSpace(record.title)
	}
	created, err := s.CreateSession(ctx, CreateSessionRequest{
		AgentBackend:    record.identity.Backend().String(),
		CWD:             record.cwd,
		Provider:        stringPtr(record.provider),
		Model:           stringPtr(record.model),
		ReasoningEffort: stringPtr(record.reasoningEffort),
		Title:           stringPtr(title),
	})
	if err != nil {
		return HandoffSessionResponse{}, err
	}
	if created.Session == nil {
		return HandoffSessionResponse{}, fmt.Errorf("handoff launch returned no session")
	}
	newSessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		return HandoffSessionResponse{}, err
	}
	if _, err := s.DeleteSession(ctx, DeleteSessionRequest{SessionID: req.SessionID}); err != nil {
		_, _ = s.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: newSessionID})
		return HandoffSessionResponse{}, err
	}
	return HandoffSessionResponse{
		OK:                true,
		Session:           created.Session,
		SessionID:         created.Session.SessionID,
		RuntimeID:         created.Session.RuntimeID,
		PreviousSessionID: record.identity.SessionID().String(),
		HistoryPath:       strings.TrimSpace(record.importedSourcePath),
		WSAttach:          created.WSAttach,
	}, nil
}

func sessionResumeCandidateFromRecord(record sessionRecord, updatedAt time.Time) SessionResumeCandidate {
	return SessionResumeCandidate{
		SessionID:        record.identity.SessionID().String(),
		Title:            record.title,
		Alias:            displayAlias(record),
		DisplayName:      sessionDisplayName(record),
		CWD:              record.cwd,
		FirstUserMessage: firstUserMessageForRecord(record),
		UpdatedTS:        timestampSeconds(updatedAt),
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

func canonicalSessionCWD(raw string) string {
	cleaned := normalizeSessionCWD(raw)
	if cleaned == "" {
		return ""
	}
	if eval, err := filepath.EvalSymlinks(cleaned); err == nil && strings.TrimSpace(eval) != "" {
		return filepath.Clean(eval)
	}
	return cleaned
}

func sameSessionCWD(a, b string) bool {
	left := normalizeSessionCWD(a)
	right := normalizeSessionCWD(b)
	if left == "" || right == "" {
		return false
	}
	return left == right || canonicalSessionCWD(left) == canonicalSessionCWD(right)
}

func scanPIResumeCandidates(cwd string) []SessionResumeCandidate {
	roots := piSessionHistoryRoots(cwd)
	if len(roots) == 0 {
		return nil
	}
	seenPaths := make(map[string]struct{})
	candidates := make([]SessionResumeCandidate, 0)
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry == nil || entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
				return nil
			}
			cleaned := filepath.Clean(path)
			if _, ok := seenPaths[cleaned]; ok {
				return nil
			}
			seenPaths[cleaned] = struct{}{}
			candidate, ok := piResumeCandidateFromSourcePath(cwd, cleaned)
			if ok {
				candidates = append(candidates, candidate)
			}
			return nil
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].UpdatedTS != candidates[j].UpdatedTS {
			return candidates[i].UpdatedTS > candidates[j].UpdatedTS
		}
		return candidates[i].SessionID < candidates[j].SessionID
	})
	return candidates
}

func piResumeCandidateFromSourcePath(cwd, sourcePath string) (SessionResumeCandidate, bool) {
	backendSessionID, sourceCWD, sessionName, firstUser, ok := piResumeCandidateMetaFromSourcePath(sourcePath)
	if !ok {
		return SessionResumeCandidate{}, false
	}
	if sourceCWD != "" && !sameSessionCWD(sourceCWD, cwd) {
		return SessionResumeCandidate{}, false
	}
	durableID, err := session.NewDurableID(backendSessionID)
	if err != nil {
		return SessionResumeCandidate{}, false
	}
	sessionID, err := session.NewHistoricalSessionID(session.BackendPI, durableID)
	if err != nil {
		return SessionResumeCandidate{}, false
	}
	info, err := os.Stat(sourcePath)
	if err != nil || info.IsDir() {
		return SessionResumeCandidate{}, false
	}
	title := strings.TrimSpace(sessionName)
	if title == "" {
		title = truncateResumeTitle(firstUser)
	}
	if title == "" {
		title = backendSessionID
	}
	return SessionResumeCandidate{
		SessionID:        sessionID.String(),
		Title:            title,
		Alias:            title,
		DisplayName:      title,
		CWD:              sourceCWD,
		FirstUserMessage: firstUser,
		UpdatedTS:        timestampSeconds(info.ModTime()),
	}, true
}

func piResumeCandidateMetaFromSourcePath(sourcePath string) (sessionID string, cwd string, sessionName string, firstUser string, ok bool) {
	file, err := os.Open(sourcePath)
	if err != nil {
		return "", "", "", "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRuntimeLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if name := piSessionInfoNameFromLine(line); name != "" {
			sessionName = name
		}
		material, err := pi.ParseObjectJSON([]byte(line))
		if err != nil {
			return "", "", "", "", false
		}
		if material.Header != nil {
			sessionID = strings.TrimSpace(material.Header.SessionID)
			cwd = strings.TrimSpace(material.Header.CWD)
			continue
		}
		for _, event := range material.Events {
			if firstUser == "" && event.Message != nil && event.Message.Role == pi.MessageRoleUser {
				firstUser = strings.TrimSpace(event.Message.Text)
			}
		}
	}
	return sessionID, cwd, sessionName, firstUser, sessionID != ""
}

func piSessionInfoNameFromLine(line string) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return ""
	}
	if strings.TrimSpace(jsonStringValue(raw["type"])) != "session_info" {
		return ""
	}
	return strings.TrimSpace(jsonStringValue(raw["name"]))
}

func jsonStringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func truncateResumeTitle(value string) string {
	trimmed := strings.TrimSpace(value)
	runes := []rune(trimmed)
	if len(runes) <= 80 {
		return trimmed
	}
	return string(runes[:80])
}

func piSourcePathForHistoricalSession(cwd string, sessionID session.SessionID) (string, string, bool) {
	ref, err := session.ParseHistoricalSessionID(sessionID.String())
	if err != nil || ref.Backend != session.BackendPI {
		return "", "", false
	}
	paths := discoverPISessionSourcesByID(cwd, ref.Durable.String())
	if len(paths) == 0 {
		return "", "", false
	}
	return paths[0], ref.Durable.String(), true
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
