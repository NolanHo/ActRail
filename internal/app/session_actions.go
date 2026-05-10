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

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/domain/message"
	"actrail/internal/domain/session"
)

type SessionResumeCandidatesRequest struct {
	CWD          string
	AgentBackend string
	Offset       int
	Limit        int
	ScanOffset   int
	ScanLimit    int
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
	OK            bool                     `json:"ok"`
	Exists        bool                     `json:"exists"`
	WillCreate    bool                     `json:"will_create"`
	GitRepo       bool                     `json:"git_repo"`
	GitRoot       string                   `json:"git_root,omitempty"`
	GitBranch     string                   `json:"git_branch,omitempty"`
	Offset        int                      `json:"offset"`
	Limit         int                      `json:"limit"`
	Remaining     int                      `json:"remaining"`
	ScanOffset    int                      `json:"scan_offset,omitempty"`
	ScanLimit     int                      `json:"scan_limit,omitempty"`
	Scanned       int                      `json:"scanned,omitempty"`
	ScanRemaining int                      `json:"scan_remaining,omitempty"`
	ScanComplete  bool                     `json:"scan_complete,omitempty"`
	Sessions      []SessionResumeCandidate `json:"sessions"`
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
	IODMode             StringPatch
}

type EditSessionResponse struct {
	OK                  bool               `json:"ok"`
	Alias               string             `json:"alias,omitempty"`
	PriorityOffset      float64            `json:"priority_offset,omitempty"`
	SnoozeUntil         *int64             `json:"snooze_until,omitempty"`
	DependencySessionID *string            `json:"dependency_session_id,omitempty"`
	Focused             bool               `json:"focused,omitempty"`
	IOD                 *IODRuntimeSummary `json:"iod,omitempty"`
}

type SwitchSessionModelRequest struct {
	SessionID       session.SessionID
	Model           StringPatch
	Provider        StringPatch
	ReasoningEffort StringPatch
}

type SwitchSessionModelResponse struct {
	OK              bool           `json:"ok"`
	Model           string         `json:"model,omitempty"`
	Provider        string         `json:"provider,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	ApplyStatus     string         `json:"apply_status,omitempty"`
	RestartRequired bool           `json:"restart_required,omitempty"`
	Message         string         `json:"message,omitempty"`
	Data            map[string]any `json:"data,omitempty"`
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
	SidecarPath       string                `json:"sidecar_path,omitempty"`
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
	if req.ScanOffset <= 0 {
		for _, item := range ordered {
			if strings.TrimSpace(item.record.importedSourcePath) == "" && strings.TrimSpace(item.record.importedBackendSessionID) == "" {
				continue
			}
			candidate := sessionResumeCandidateFromRecord(item.record, item.updatedAt)
			resumeItems = append(resumeItems, candidate)
			seen[candidate.SessionID] = struct{}{}
		}
	}
	scanOffset, scanLimit, scanned, scanRemaining, scanComplete := 0, 0, 0, 0, true
	if backend == "" || backend == session.BackendPI.String() {
		scannedCandidates := s.scanPIResumeCandidates(cwd, req.ScanOffset, req.ScanLimit)
		scanOffset = scannedCandidates.Offset
		scanLimit = scannedCandidates.Limit
		scanned = scannedCandidates.Scanned
		scanRemaining = scannedCandidates.Remaining
		scanComplete = scannedCandidates.Complete
		for _, candidate := range scannedCandidates.Sessions {
			if _, ok := seen[candidate.SessionID]; ok {
				continue
			}
			seen[candidate.SessionID] = struct{}{}
			resumeItems = append(resumeItems, candidate)
		}
	}
	if backend == session.BackendCodex.String() {
		scannedCandidates := scanCodexResumeCandidates(cwd, req.ScanOffset, req.ScanLimit)
		scanOffset = scannedCandidates.Offset
		scanLimit = scannedCandidates.Limit
		scanned = scannedCandidates.Scanned
		scanRemaining = scannedCandidates.Remaining
		scanComplete = scannedCandidates.Complete
		for _, candidate := range scannedCandidates.Sessions {
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
		OK:            true,
		Exists:        exists,
		WillCreate:    willCreate,
		GitRepo:       false,
		Offset:        req.Offset,
		Limit:         req.Limit,
		Remaining:     len(resumeItems) - end,
		ScanOffset:    scanOffset,
		ScanLimit:     scanLimit,
		Scanned:       scanned,
		ScanRemaining: scanRemaining,
		ScanComplete:  scanComplete,
		Sessions:      append([]SessionResumeCandidate(nil), resumeItems[start:end]...),
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

func (s *Stub) EditSession(ctx context.Context, req EditSessionRequest) (EditSessionResponse, error) {
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
	if req.IODMode.Present {
		if req.IODMode.Value == nil {
			return EditSessionResponse{}, Invalid("iod_mode", "iod_mode required")
		}
		mode := strings.TrimSpace(*req.IODMode.Value)
		if mode != "grpc" && mode != "std" {
			return EditSessionResponse{}, Invalid("iod_mode", "iod_mode must be grpc or std")
		}
		if err := s.withSessionInputLock(req.SessionID, func(locked sessionRecord) error {
			target = locked
			if mode == currentIODMode(locked) {
				return nil
			}
			if locked.state.Busy() {
				return Conflict("iod mode can only be changed while runtime is idle")
			}
			_, err := s.switchSessionIODMode(ctx, locked, mode)
			return err
		}); err != nil {
			return EditSessionResponse{}, err
		}
	}
	record, ok, err := s.registry.Update(target.identity.SessionID(), false, func(record *sessionRecord) error {
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

const (
	sessionRuntimeSettingApplyStatusUnchanged       = "unchanged"
	sessionRuntimeSettingApplyStatusRestartRequired = "restart_required"
)

func (s *Stub) SwitchSessionModel(_ context.Context, req SwitchSessionModelRequest) (SwitchSessionModelResponse, error) {
	if !req.Model.Present && !req.Provider.Present && !req.ReasoningEffort.Present {
		return SwitchSessionModelResponse{}, Invalid("model", "model, provider, or reasoning_effort required")
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
	var nextReasoning *string
	if req.ReasoningEffort.Present {
		if req.ReasoningEffort.Value != nil {
			value := strings.TrimSpace(*req.ReasoningEffort.Value)
			if value != "" && !validSessionReasoningEffort(value) {
				return SwitchSessionModelResponse{}, Invalid("reasoning_effort", "reasoning_effort must be one of off, minimal, low, medium, high, xhigh")
			}
			nextReasoning = &value
		}
	}
	existing, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SwitchSessionModelResponse{}, err
	}
	if req.ReasoningEffort.Present && nextReasoning != nil && *nextReasoning != "" && existing.identity.Backend() == session.BackendCodex {
		return SwitchSessionModelResponse{}, UnsupportedBackend("codex sessions do not support reasoning_effort changes")
	}
	changed := false
	record, ok, err := s.registry.Update(req.SessionID, false, func(record *sessionRecord) error {
		if nextModel != nil {
			if record.model != *nextModel {
				changed = true
			}
			record.model = *nextModel
		}
		if req.Provider.Present {
			next := ""
			if nextProvider == nil {
				next = ""
			} else {
				next = *nextProvider
			}
			if record.provider != next {
				changed = true
			}
			record.provider = next
		}
		if req.ReasoningEffort.Present {
			next := ""
			if nextReasoning != nil {
				next = *nextReasoning
			}
			if record.reasoningEffort != next {
				changed = true
			}
			record.reasoningEffort = next
		}
		return nil
	})
	if err != nil {
		return SwitchSessionModelResponse{}, err
	}
	if !ok {
		return SwitchSessionModelResponse{}, NotFound(fmt.Sprintf("session %q not found", req.SessionID))
	}
	if !changed {
		return SwitchSessionModelResponse{
			OK:              true,
			Model:           record.model,
			Provider:        record.provider,
			ReasoningEffort: record.reasoningEffort,
			ApplyStatus:     sessionRuntimeSettingApplyStatusUnchanged,
			Message:         "settings unchanged",
		}, nil
	}
	return SwitchSessionModelResponse{
		OK:              true,
		Model:           record.model,
		Provider:        record.provider,
		ReasoningEffort: record.reasoningEffort,
		ApplyStatus:     sessionRuntimeSettingApplyStatusRestartRequired,
		RestartRequired: true,
		Message:         "settings saved; restart or handoff the session to apply them to the runtime",
	}, nil
}

func validSessionReasoningEffort(value string) bool {
	switch strings.TrimSpace(value) {
	case "off", "minimal", "low", "medium", "high", "xhigh":
		return true
	default:
		return false
	}
}

func (s *Stub) DeleteSession(ctx context.Context, req DeleteSessionRequest) (DeleteSessionResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return DeleteSessionResponse{}, err
	}
	if err := record.runtime.Kill(ctx); err != nil {
		return DeleteSessionResponse{}, err
	}
	if err := s.orphanActiveWaits(ctx, &req.SessionID); err != nil {
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
	var updated sessionRecord
	var previousRuntimeID session.RuntimeID
	if err := s.withSessionInputLock(req.SessionID, func(record sessionRecord) error {
		if record.identity.Historical() {
			return Unsupported("historical sessions cannot be restarted")
		}
		var err error
		updated, previousRuntimeID, err = s.replaceSessionRuntime(ctx, req.SessionID, record, restartSessionUsesPIAgentGRPC(record))
		return err
	}); err != nil {
		return RestartSessionResponse{}, err
	}
	queue := queueSnapshotFromState(updated.state)
	s.emitQueueState(updated.identity.SessionID(), queue)
	s.emitSessionState(updated.identity.SessionID())
	if updated.state.Queue().Len() > 0 {
		s.scheduleQueuedDispatch(updated.identity.SessionID())
	}
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
		Session:           s.createdSessionFromRecord(updated),
		SessionID:         updated.identity.SessionID().String(),
		RuntimeID:         currentRuntimeID.String(),
		PreviousRuntimeID: previousRuntimeID.String(),
		Restarted:         true,
		WSAttach:          wsAttach,
	}, nil
}

func (s *Stub) switchSessionIODMode(ctx context.Context, record sessionRecord, mode string) (sessionRecord, error) {
	if record.identity.Historical() {
		return sessionRecord{}, Unsupported("historical sessions cannot switch iod mode")
	}
	if record.identity.Backend() != session.BackendPI {
		return sessionRecord{}, Unsupported("iod mode is only available for pi backend")
	}
	updated, _, err := s.replaceSessionRuntime(ctx, record.identity.SessionID(), record, mode == "grpc")
	return updated, err
}

func restartSessionUsesPIAgentGRPC(record sessionRecord) bool {
	return record.identity.Backend() == session.BackendPI
}

func (s *Stub) replaceSessionRuntime(ctx context.Context, routeID session.SessionID, record sessionRecord, usePIAgentGRPC bool) (sessionRecord, session.RuntimeID, error) {
	identity, _, ok, err := s.registry.ReserveRestartIdentity(routeID)
	if err != nil {
		return sessionRecord{}, "", err
	}
	if !ok {
		return sessionRecord{}, "", NotFound(fmt.Sprintf("session %q not found", routeID))
	}
	sourcePath := strings.TrimSpace(record.importedSourcePath)
	if record.identity.Backend() == session.BackendPI && sourcePath != "" {
		if _, err := os.Stat(sourcePath); err != nil {
			if !stdErrors.Is(err, os.ErrNotExist) {
				return sessionRecord{}, "", fmt.Errorf("stat pi session source %q: %w", sourcePath, err)
			}
			sourcePath = ""
		}
	}
	if record.identity.Backend() == session.BackendPI && sourcePath == "" {
		var err error
		sourcePath, err = newPISessionSourcePath(record.cwd, identity.SessionID(), s.registry.now())
		if err != nil {
			return sessionRecord{}, "", err
		}
	}
	codexThreadID, err := s.codexThreadIDForRuntimeRestart(ctx, record)
	if err != nil {
		return sessionRecord{}, "", err
	}
	if record.identity.Backend() == session.BackendCodex {
		if owner, ok := s.registry.FindCodexRuntimeOwner(codexThreadID, sourcePath); ok && owner.identity.SessionID() != record.identity.SessionID() {
			return sessionRecord{}, "", Conflict(fmt.Sprintf("codex session file is already attached to session %q", owner.identity.SessionID()))
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
		CodexThreadID:   codexThreadID,
		PIAgentGRPC:     usePIAgentGRPC,
	})
	if sourcePath != "" && sourcePath == strings.TrimSpace(record.importedSourcePath) {
		sourcePath = ""
	}
	if err != nil {
		_ = newRuntime.CleanupHelperArtifacts()
		return sessionRecord{}, "", err
	}
	previousBinding, err := record.runtime.CurrentHelperBinding(record.identity.SessionID())
	if err != nil {
		_ = newRuntime.Kill(context.Background())
		_ = newRuntime.CleanupHelperArtifacts()
		return sessionRecord{}, "", err
	}
	if err := s.bindRuntimeCurrentGeneration(identity.SessionID(), newRuntime); err != nil {
		_ = newRuntime.Kill(context.Background())
		_ = newRuntime.CleanupHelperArtifacts()
		return sessionRecord{}, "", err
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
	if err := s.setRuntimeAgentRunning(routeID, false); err != nil {
		restoreBinding()
		_ = newRuntime.Kill(context.Background())
		_ = newRuntime.CleanupHelperArtifacts()
		return sessionRecord{}, "", err
	}
	updated, ok, err := s.registry.SwapRuntime(routeID, identity, newRuntime, sourcePath)
	if err != nil {
		restoreBinding()
		_ = newRuntime.Kill(context.Background())
		_ = newRuntime.CleanupHelperArtifacts()
		return sessionRecord{}, "", err
	}
	if !ok {
		restoreBinding()
		_ = newRuntime.Kill(context.Background())
		_ = newRuntime.CleanupHelperArtifacts()
		return sessionRecord{}, "", NotFound(fmt.Sprintf("session %q not found", routeID))
	}
	s.shutdownPreviousHelperGeneration(identity.SessionID(), previousBinding)
	s.startRuntimeIngest(updated.identity.SessionID(), updated.identity.Backend(), newRuntime)
	s.startPIAgentGRPCReadyTransition(updated.identity.SessionID(), newRuntime)
	s.startCodexThreadBootstrap(updated.identity.SessionID(), newRuntime)
	_ = record.runtime.Kill(context.Background())
	if record.runtime.helper != nil && s.helpers != nil {
		s.helpers.Remove(record.identity.SessionID())
	}
	_ = record.runtime.CleanupHelperArtifacts()
	previousRuntimeID, _ := record.identity.RuntimeID()
	return updated, previousRuntimeID, nil
}

func (s *Stub) shutdownPreviousHelperGeneration(sessionID session.SessionID, binding *RuntimeHelperBinding) {
	if s == nil || binding == nil || binding.GenerationID == "" {
		return
	}
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(s.cfg.Storage.DataDir), sessionID, binding.GenerationID)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return
	}
	var manifest iod.GenerationManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return
	}
	if manifest.SessionID != sessionID || manifest.GenerationID != binding.GenerationID {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), helperStopTimeout)
	defer cancel()
	_ = shutdownIODManifest(ctx, manifest)
}

func (s *Stub) waitForHandoffRuntimeReady(ctx context.Context, sessionID session.SessionID) error {
	deadline := time.Now().Add(defaultHelperReadyTimeout)
	seenStarting := false
	for {
		record, err := s.lookupSession(sessionID)
		if err != nil {
			return err
		}
		if record.runtime.PendingPIAgentGRPCReady() {
			readyCtx, cancel := context.WithDeadline(ctx, deadline)
			err := record.runtime.WaitForPIAgentGRPCReady(readyCtx)
			cancel()
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				if time.Now().After(deadline) {
					return Conflict("session runtime is starting")
				}
				return err
			}
			record.runtime.piAgentGRPCReady = nil
			updated, ok, err := s.registry.SetRuntimeTransport(sessionID, record.runtime, transportSnapshotPIAgentGRPCAttached())
			if err != nil {
				return err
			}
			if ok {
				s.emitSessionState(updated.identity.SessionID())
			}
			return nil
		}
		transport := sessionTransportSnapshot(record)
		if err := transportControlError(transport); err == nil {
			if !record.runtime.PendingPIAgentGRPCReady() && (!seenStarting || transport.State != SessionTransportStateStarting) {
				return nil
			}
		} else if transport.State != SessionTransportStateStarting {
			return err
		} else {
			seenStarting = true
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return Conflict("session runtime is starting")
		}
		time.Sleep(helperReadyPollInterval)
	}
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
	sidecarPath, err := s.writeSessionHandoffSidecar(record)
	if err != nil {
		return HandoffSessionResponse{}, err
	}
	title := strings.TrimSpace(displayAlias(record))
	if title == "" {
		title = strings.TrimSpace(record.title)
	}
	useGRPC := record.runtime.UsesPIAgentGRPC()
	created, err := s.CreateSession(ctx, CreateSessionRequest{
		AgentBackend:    record.identity.Backend().String(),
		CWD:             record.cwd,
		Provider:        stringPtr(record.provider),
		Model:           stringPtr(record.model),
		ReasoningEffort: stringPtr(record.reasoningEffort),
		Title:           stringPtr(title),
		PIAgentGRPC:     &useGRPC,
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
	if err := s.waitForHandoffRuntimeReady(ctx, newSessionID); err != nil {
		_, _ = s.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: newSessionID})
		return HandoffSessionResponse{}, err
	}
	if _, err := s.Send(ctx, SendRequest{SessionID: newSessionID, Text: handoffPrompt(sidecarPath)}); err != nil {
		_, _ = s.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: newSessionID})
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
		SidecarPath:       sidecarPath,
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
		IOD:                 iodSummaryForRuntime(record.runtime),
	}
}

func currentIODMode(record sessionRecord) string {
	if record.runtime.UsesPIAgentGRPC() {
		return "grpc"
	}
	return "std"
}

func iodSummaryForRuntime(runtime sessionRuntime) *IODRuntimeSummary {
	if runtime.piAgentGRPC != nil {
		return grpcIODSummary()
	}
	if runtime.helper != nil {
		return runtime.helper.iodSummary()
	}
	return nil
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

type piResumeCandidateScan struct {
	Sessions  []SessionResumeCandidate
	Offset    int
	Limit     int
	Scanned   int
	Remaining int
	Complete  bool
}

func (s *Stub) scanPIResumeCandidates(cwd string, offset, limit int) piResumeCandidateScan {
	paths := s.piResumePaths.paths(cwd)
	start, end := paginate(len(paths), offset, limit)
	candidates := make([]SessionResumeCandidate, 0, end-start)
	for _, path := range paths[start:end] {
		candidate, ok := piResumeCandidateFromSourcePath(cwd, path)
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].UpdatedTS != candidates[j].UpdatedTS {
			return candidates[i].UpdatedTS > candidates[j].UpdatedTS
		}
		return candidates[i].SessionID < candidates[j].SessionID
	})
	return piResumeCandidateScan{
		Sessions:  candidates,
		Offset:    offset,
		Limit:     limit,
		Scanned:   end - start,
		Remaining: len(paths) - end,
		Complete:  end >= len(paths),
	}
}

func piResumeSourcePaths(cwd string) []string {
	return listPIResumeSourcePaths(cwd)
}

func listPIResumeSourcePaths(cwd string) []string {
	roots := piSessionHistoryRoots(cwd)
	if len(roots) == 0 {
		return nil
	}
	seenPaths := make(map[string]struct{})
	entries := make([]piResumePathEntry, 0)
	for _, root := range roots {
		items, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.IsDir() || filepath.Ext(item.Name()) != ".jsonl" {
				continue
			}
			cleaned := filepath.Clean(filepath.Join(root, item.Name()))
			if _, ok := seenPaths[cleaned]; ok {
				continue
			}
			info, err := item.Info()
			if err != nil {
				continue
			}
			seenPaths[cleaned] = struct{}{}
			entries = append(entries, piResumePathEntry{Path: cleaned, ModTime: info.ModTime()})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].ModTime.Equal(entries[j].ModTime) {
			return entries[i].ModTime.After(entries[j].ModTime)
		}
		return entries[i].Path < entries[j].Path
	})
	paths := make([]string, len(entries))
	for i, entry := range entries {
		paths[i] = entry.Path
	}
	return paths
}

type piResumePathEntry struct {
	Path    string
	ModTime time.Time
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
		entry, ok := piResumeMetaEntryFromLine(line)
		if !ok {
			return "", "", "", "", false
		}
		if entry.sessionID != "" {
			sessionID = entry.sessionID
		}
		if entry.cwd != "" {
			cwd = entry.cwd
		}
		if entry.sessionName != "" {
			sessionName = entry.sessionName
		}
		if firstUser == "" && entry.firstUser != "" {
			firstUser = entry.firstUser
		}
	}
	return sessionID, cwd, sessionName, firstUser, sessionID != ""
}

type piResumeMetaEntry struct {
	sessionID   string
	cwd         string
	sessionName string
	firstUser   string
}

func piResumeMetaEntryFromLine(line string) (piResumeMetaEntry, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return piResumeMetaEntry{}, false
	}
	switch strings.TrimSpace(stringValue(raw["type"])) {
	case "session":
		return piResumeMetaEntry{
			sessionID: firstNonEmptyString(stringValue(raw["id"]), stringValue(raw["session_id"]), stringValue(raw["sessionId"])),
			cwd:       strings.TrimSpace(stringValue(raw["cwd"])),
		}, true
	case "session_info":
		return piResumeMetaEntry{sessionName: strings.TrimSpace(stringValue(raw["name"]))}, true
	case "message":
		message, _ := raw["message"].(map[string]any)
		if strings.TrimSpace(stringValue(message["role"])) != "user" {
			return piResumeMetaEntry{}, true
		}
		return piResumeMetaEntry{firstUser: piResumeMessageText(message)}, true
	default:
		return piResumeMetaEntry{}, true
	}
}

func piResumeMessageText(message map[string]any) string {
	content, _ := message["content"].([]any)
	parts := make([]string, 0, len(content))
	for _, item := range content {
		obj, _ := item.(map[string]any)
		switch strings.TrimSpace(stringValue(obj["type"])) {
		case "text", "input_text", "output_text":
			if text := stringValue(obj["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) > 0 {
		return strings.TrimSpace(strings.Join(parts, ""))
	}
	return strings.TrimSpace(stringValue(message["text"]))
}

func truncateResumeTitle(value string) string {
	trimmed := strings.TrimSpace(value)
	runes := []rune(trimmed)
	if len(runes) <= 80 {
		return trimmed
	}
	return string(runes[:80])
}

func scanCodexResumeCandidates(cwd string, offset, limit int) piResumeCandidateScan {
	paths := listCodexResumeSourcePaths()
	candidates := make([]SessionResumeCandidate, 0, len(paths))
	for _, path := range paths {
		candidate, ok := codexResumeCandidateFromSourcePath(cwd, path)
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].UpdatedTS != candidates[j].UpdatedTS {
			return candidates[i].UpdatedTS > candidates[j].UpdatedTS
		}
		return candidates[i].SessionID < candidates[j].SessionID
	})
	start, end := paginate(len(candidates), offset, limit)
	return piResumeCandidateScan{
		Sessions:  append([]SessionResumeCandidate(nil), candidates[start:end]...),
		Offset:    offset,
		Limit:     limit,
		Scanned:   len(paths),
		Remaining: len(candidates) - end,
		Complete:  true,
	}
}

func listCodexResumeSourcePaths() []string {
	root := codexSessionRoot()
	entries := make([]piResumePathEntry, 0)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" || !strings.HasPrefix(entry.Name(), "rollout-") {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.IsDir() {
			return nil
		}
		entries = append(entries, piResumePathEntry{Path: filepath.Clean(path), ModTime: info.ModTime()})
		return nil
	})
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].ModTime.Equal(entries[j].ModTime) {
			return entries[i].ModTime.After(entries[j].ModTime)
		}
		return entries[i].Path < entries[j].Path
	})
	paths := make([]string, len(entries))
	for i, entry := range entries {
		paths[i] = entry.Path
	}
	return paths
}

func codexResumeCandidateFromSourcePath(cwd, sourcePath string) (SessionResumeCandidate, bool) {
	backendSessionID, sourceCWD, firstUser, ok := codexResumeCandidateMetaFromSourcePath(sourcePath)
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
	sessionID, err := session.NewHistoricalSessionID(session.BackendCodex, durableID)
	if err != nil {
		return SessionResumeCandidate{}, false
	}
	info, err := os.Stat(sourcePath)
	if err != nil || info.IsDir() {
		return SessionResumeCandidate{}, false
	}
	title := truncateResumeTitle(firstUser)
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

func codexResumeCandidateMetaFromSourcePath(sourcePath string) (sessionID string, cwd string, firstUser string, ok bool) {
	file, err := os.Open(sourcePath)
	if err != nil {
		return "", "", "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), codexSessionFileMaxLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry codexSessionLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return "", "", "", false
		}
		switch strings.TrimSpace(entry.Type) {
		case "session_meta":
			if entry.Payload != nil {
				sessionID = firstNonEmptyString(sessionID, strings.TrimSpace(stringValue(entry.Payload["id"])))
				cwd = firstNonEmptyString(cwd, strings.TrimSpace(stringValue(entry.Payload["cwd"])))
			}
		case "event_msg":
			if firstUser == "" && strings.TrimSpace(stringValue(entry.Payload["type"])) == "user_message" {
				firstUser = strings.TrimSpace(firstStringValue(entry.Payload["message"], entry.Payload["text"]))
			}
		case "response_item":
			if firstUser == "" &&
				strings.TrimSpace(stringValue(entry.Payload["type"])) == "message" &&
				strings.TrimSpace(stringValue(entry.Payload["role"])) == "user" {
				firstUser = strings.TrimSpace(codexContentText(entry.Payload["content"]))
				if firstUser == "" {
					firstUser = strings.TrimSpace(stringValue(entry.Payload["text"]))
				}
			}
		}
	}
	if scanner.Err() != nil {
		return "", "", "", false
	}
	return sessionID, cwd, firstUser, sessionID != ""
}

func codexSourcePathForHistoricalSession(cwd string, sessionID session.SessionID) (string, string, bool) {
	ref, err := session.ParseHistoricalSessionID(sessionID.String())
	if err != nil || ref.Backend != session.BackendCodex {
		return "", "", false
	}
	if path, ok := discoverCodexSessionFileByID(context.Background(), ref.Durable.String()); ok {
		candidate, candidateOK := codexResumeCandidateFromSourcePath(cwd, path)
		if candidateOK && candidate.SessionID == sessionID.String() {
			return path, ref.Durable.String(), true
		}
	}
	return "", "", false
}

func sourcePathForHistoricalSession(cwd string, sessionID session.SessionID, backend session.Backend) (string, string, bool) {
	switch backend {
	case session.BackendPI:
		return piSourcePathForHistoricalSession(cwd, sessionID)
	case session.BackendCodex:
		return codexSourcePathForHistoricalSession(cwd, sessionID)
	default:
		return "", "", false
	}
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
