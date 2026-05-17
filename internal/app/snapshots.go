package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"actrail/internal/domain/message"
	"actrail/internal/domain/session"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type SessionDetailsRequest struct {
	SessionID session.SessionID
}

type SessionDetailsResponse struct {
	SessionID           string                    `json:"session_id"`
	RuntimeID           string                    `json:"runtime_id,omitempty"`
	ThreadID            string                    `json:"thread_id,omitempty"`
	Title               string                    `json:"title"`
	Alias               string                    `json:"alias,omitempty"`
	DisplayName         string                    `json:"display_name,omitempty"`
	FirstUserMessage    string                    `json:"first_user_message,omitempty"`
	CWD                 string                    `json:"cwd"`
	AgentBackend        string                    `json:"agent_backend"`
	Provider            string                    `json:"provider,omitempty"`
	Model               string                    `json:"model,omitempty"`
	ReasoningEffort     string                    `json:"reasoning_effort,omitempty"`
	Busy                bool                      `json:"busy"`
	RuntimeState        string                    `json:"runtime_state,omitempty"`
	RuntimeStateReason  string                    `json:"runtime_state_reason,omitempty"`
	Focused             bool                      `json:"focused,omitempty"`
	QueueLength         int                       `json:"queue_length"`
	PriorityOffset      float64                   `json:"priority_offset,omitempty"`
	SnoozeUntil         *int64                    `json:"snooze_until,omitempty"`
	DependencySessionID string                    `json:"dependency_session_id,omitempty"`
	SessionFilePath     string                    `json:"session_file_path,omitempty"`
	BackendSessionID    string                    `json:"backend_session_id,omitempty"`
	LastUpdatedTS       float64                   `json:"last_updated_ts"`
	LastActivityTS      float64                   `json:"last_activity_ts"`
	Historical          bool                      `json:"historical"`
	Capabilities        SessionCapabilitySnapshot `json:"capabilities"`
}

type SessionCapabilitySnapshot struct {
	WSRealtime     bool `json:"ws_realtime"`
	Voice          bool `json:"voice"`
	Harness        bool `json:"harness"`
	Notifications  bool `json:"notifications"`
	PIUI           bool `json:"pi_ui"`
	WorkspaceRead  bool `json:"workspace_read"`
	WorkspaceWrite bool `json:"workspace_write"`
}

type SessionMessagesRequest struct {
	SessionID          session.SessionID
	AfterSeq           *uint64
	BeforeSeq          *uint64
	Limit              int
	Init               bool
	Deferred           bool
	ActiveTurnStartSeq uint64
	IncludeToolDetails bool
	IncludeToolEvents  bool
	EventID            string
	ToolCallID         string
}

type SessionMessage struct {
	Seq            uint64                 `json:"seq"`
	Role           string                 `json:"role,omitempty"`
	Kind           string                 `json:"kind"`
	Type           string                 `json:"type,omitempty"`
	Text           string                 `json:"text,omitempty"`
	TS             float64                `json:"ts"`
	SessionID      string                 `json:"session_id,omitempty"`
	EventID        string                 `json:"event_id,omitempty"`
	ParentEventID  string                 `json:"parent_event_id,omitempty"`
	SourceOrder    string                 `json:"source_order,omitempty"`
	Name           string                 `json:"name,omitempty"`
	Summary        string                 `json:"summary,omitempty"`
	ToolCallID     string                 `json:"tool_call_id,omitempty"`
	IsError        bool                   `json:"is_error,omitempty"`
	Details        map[string]any         `json:"details,omitempty"`
	SupervisorRuns []SupervisorRunSummary `json:"supervisor_runs,omitempty"`
}

type SessionMessagesResponse struct {
	Items         []SessionMessage `json:"items"`
	NextBeforeSeq *uint64          `json:"next_before_seq,omitempty"`
	HasMore       bool             `json:"has_more"`
	TailSeq       uint64           `json:"tail_seq"`
}

type SessionStateRequest struct {
	SessionID session.SessionID
}

type ProbeSessionStateRequest struct {
	SessionID session.SessionID
}

type ProbeSessionStateResponse struct {
	ProbeID string               `json:"probe_id"`
	State   SessionStateResponse `json:"state"`
}

type SessionStateResponse struct {
	Busy                 bool                          `json:"busy"`
	BusyReason           string                        `json:"busy_reason,omitempty"`
	RuntimeState         string                        `json:"runtime_state,omitempty"`
	RuntimeStateReason   string                        `json:"runtime_state_reason,omitempty"`
	Queue                SessionQueueSnapshot          `json:"queue"`
	Transport            SessionTransportSnapshot      `json:"transport"`
	UIRequest            *SessionUIRequestSnapshot     `json:"ui_request,omitempty"`
	PartialAssistantTurn *PartialAssistantTurnSnapshot `json:"partial_assistant_turn,omitempty"`
	TailSeq              uint64                        `json:"tail_seq"`
	ResumeCursors        SessionResumeCursors          `json:"resume_cursors"`
	ContextUsage         *SessionContextUsageSnapshot  `json:"context_usage,omitempty"`
	TurnTiming           *SessionTurnTimingSnapshot    `json:"turn_timing,omitempty"`
	ActiveWait           *ActiveWaitSummary            `json:"active_wait,omitempty"`
}

type SessionContextUsageSnapshot struct {
	UsedTokens  *int `json:"used_tokens,omitempty"`
	TotalTokens *int `json:"total_tokens,omitempty"`
	PercentUsed *int `json:"percent_used,omitempty"`
}

type SessionTurnTimingSnapshot struct {
	StartedTS   float64  `json:"started_ts,omitempty"`
	LastEventTS *float64 `json:"last_event_ts,omitempty"`
}

type SessionQueueSnapshot struct {
	Items []QueuedPromptSnapshot `json:"items"`
}

type QueuedPromptSnapshot struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	State string `json:"state"`
}

type SessionUIRequestSnapshot struct {
	RequestID     string                      `json:"request_id"`
	Kind          string                      `json:"kind"`
	Method        string                      `json:"method,omitempty"`
	Title         string                      `json:"title,omitempty"`
	Message       string                      `json:"message,omitempty"`
	Prompt        string                      `json:"prompt"`
	Question      string                      `json:"question,omitempty"`
	Context       string                      `json:"context,omitempty"`
	AllowFreeform bool                        `json:"allow_freeform,omitempty"`
	AllowMultiple bool                        `json:"allow_multiple,omitempty"`
	Options       []SessionUIOptionSnapshot   `json:"options,omitempty"`
	Questions     []SessionUIQuestionSnapshot `json:"questions,omitempty"`
	Metadata      map[string]any              `json:"metadata,omitempty"`
}

type SessionUIOptionSnapshot struct {
	Label       string `json:"label,omitempty"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
}

type SessionUIQuestionSnapshot struct {
	Header      string                    `json:"header,omitempty"`
	Question    string                    `json:"question"`
	Options     []SessionUIOptionSnapshot `json:"options,omitempty"`
	MultiSelect bool                      `json:"multiSelect,omitempty"`
}

type PartialAssistantTurnSnapshot struct {
	TurnID string `json:"turn_id"`
	Text   string `json:"text"`
}

type SessionResumeCursors struct {
	Session   string `json:"session,omitempty"`
	UI        string `json:"ui,omitempty"`
	Transport string `json:"transport,omitempty"`
}

type SessionWorkspaceRequest struct {
	SessionID session.SessionID
}

type UpdateSessionWorkspaceRequest struct {
	SessionID    session.SessionID
	SelectedPath string                 `json:"selected_path,omitempty"`
	OpenPaths    []string               `json:"open_paths"`
	HistoryItems []WorkspaceHistoryItem `json:"history_items"`
}

type SessionWorkspaceResponse struct {
	RootPath          string                 `json:"root_path"`
	CanonicalRootPath string                 `json:"canonical_root_path,omitempty"`
	SelectedPath      string                 `json:"selected_path,omitempty"`
	OpenPaths         []string               `json:"open_paths"`
	HistoryItems      []WorkspaceHistoryItem `json:"history_items"`
}

type WorkspaceHistoryItem struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

type WorkspaceFileListRequest struct {
	SessionID session.SessionID
	Path      string
	Search    string
	Limit     int
}

type WorkspaceFileListResponse struct {
	RootPath          string               `json:"root_path"`
	CanonicalRootPath string               `json:"canonical_root_path,omitempty"`
	Path              string               `json:"path"`
	Items             []WorkspaceFileEntry `json:"items"`
	Truncated         bool                 `json:"truncated"`
}

type WorkspaceFileEntry struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type WorkspaceFileReadRequest struct {
	SessionID session.SessionID
	Path      string
}

type WorkspaceFileReadResponse struct {
	Path              string `json:"path"`
	Kind              string `json:"kind"`
	MIMEType          string `json:"mime_type,omitempty"`
	Encoding          string `json:"encoding,omitempty"`
	SizeBytes         int64  `json:"size_bytes,omitempty"`
	Text              string `json:"text,omitempty"`
	DownloadName      string `json:"download_name,omitempty"`
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
}

type GitFileVersionsRequest struct {
	SessionID session.SessionID
	Path      string
}

type GitFileVersionsResponse struct {
	Path           string           `json:"path"`
	FallbackReason string           `json:"fallback_reason,omitempty"`
	Items          []GitFileVersion `json:"items"`
}

type GitFileVersion struct {
	VersionID  string  `json:"version_id"`
	Label      string  `json:"label"`
	CommitHash string  `json:"commit_hash,omitempty"`
	Author     string  `json:"author,omitempty"`
	CommitTS   float64 `json:"commit_ts,omitempty"`
	Message    string  `json:"message,omitempty"`
	Current    bool    `json:"current"`
}

func (s *Stub) SessionDetails(_ context.Context, req SessionDetailsRequest) (SessionDetailsResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionDetailsResponse{}, err
	}
	runtimeID, _ := record.identity.RuntimeID()
	threadID, _ := record.identity.ThreadID()
	busy, _ := effectiveBusy(record)
	runtimeState, runtimeStateReason := runtimeStateFields(record)
	return SessionDetailsResponse{
		SessionID:           record.identity.SessionID().String(),
		RuntimeID:           runtimeID.String(),
		ThreadID:            threadID.String(),
		Title:               record.title,
		Alias:               displayAlias(record),
		DisplayName:         sessionDisplayName(record),
		FirstUserMessage:    firstUserMessageForRecord(record),
		CWD:                 record.cwd,
		AgentBackend:        record.identity.Backend().String(),
		Provider:            record.provider,
		Model:               record.model,
		ReasoningEffort:     record.reasoningEffort,
		Busy:                busy,
		RuntimeState:        runtimeState,
		RuntimeStateReason:  runtimeStateReason,
		Focused:             record.focused,
		QueueLength:         record.state.Queue().Len(),
		PriorityOffset:      record.priorityOffset,
		SnoozeUntil:         unixSecondsPtr(record.snoozeUntil),
		DependencySessionID: sessionIDString(record.dependencySessionID),
		SessionFilePath:     record.importedSourcePath,
		BackendSessionID:    record.importedBackendSessionID,
		LastUpdatedTS:       timestampSeconds(sessionDisplayUpdatedAt(record)),
		LastActivityTS:      timestampSeconds(record.activityAt),
		Historical:          record.identity.Historical(),
		Capabilities:        s.capabilitiesSnapshot(),
	}, nil
}

func (s *Stub) SessionMessages(ctx context.Context, req SessionMessagesRequest) (SessionMessagesResponse, error) {
	ctx, span := otel.Tracer("actrail/app").Start(ctx, "app.SessionMessages")
	defer span.End()
	span.SetAttributes(
		attribute.String("session.id", req.SessionID.String()),
		attribute.Int("messages.limit", req.Limit),
		attribute.Bool("messages.init", req.Init),
		attribute.Bool("messages.deferred", req.Deferred),
		attribute.Bool("messages.include_tool_events", req.IncludeToolEvents),
	)
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionMessagesResponse{}, err
	}
	if item, ok := s.messageByID(ctx, record, req); ok {
		return s.annotateSupervisorRuns(ctx, record.identity.SessionID(), SessionMessagesResponse{Items: []SessionMessage{item}, TailSeq: item.Seq}), nil
	}
	if sessionMessageIDRequested(req) {
		return SessionMessagesResponse{TailSeq: record.transcript.TailSeq().Uint64()}, nil
	}
	if response, ok, err := s.loadPIAuthoritativeHistory(ctx, record, s.cfg.Storage.DataDir, req); ok {
		return s.annotateSupervisorRuns(ctx, record.identity.SessionID(), response), err
	}
	if response, ok, err := s.loadDetachedImportedPIHistory(ctx, record, req); ok {
		return s.annotateSupervisorRuns(ctx, record.identity.SessionID(), response), err
	}
	if response, ok, err := s.loadCodexSessionFileHistory(ctx, record, req); ok {
		return s.annotateSupervisorRuns(ctx, record.identity.SessionID(), response), err
	}
	activeTurnStartSeq := req.ActiveTurnStartSeq
	if req.Deferred && activeTurnStartSeq == 0 {
		activeTurnStartSeq = record.transcript.LastUserSeq().Uint64()
	}
	if req.AfterSeq != nil {
		items := visibleCommittedMessages(record.transcript.HistoryAfter(message.Seq(*req.AfterSeq)).Items(), req)
		response := SessionMessagesResponse{
			Items:   make([]SessionMessage, 0, len(items)),
			TailSeq: record.transcript.TailSeq().Uint64(),
		}
		for _, item := range items {
			if item.Seq().Uint64() > *req.AfterSeq {
				msg := sessionMessageForRequest(item, req, record.transcript.TailSeq().Uint64(), activeTurnStartSeq)
				msg.SessionID = record.identity.SessionID().String()
				response.Items = append(response.Items, msg)
			}
		}
		if !req.IncludeToolEvents {
			response.Items = annotateHiddenToolActivitySummaries(response.Items, sessionMessagesFromCommittedItems(record.transcript.Items()))
		}
		return s.annotateSupervisorRuns(ctx, record.identity.SessionID(), response), nil
	}
	page := visibleTranscriptHistory(record.transcript, req)
	items := page.Items()
	response := SessionMessagesResponse{
		Items:   make([]SessionMessage, 0, len(items)),
		HasMore: page.HasMore(),
		TailSeq: record.transcript.TailSeq().Uint64(),
	}
	for _, item := range items {
		msg := sessionMessageForRequest(item, req, record.transcript.TailSeq().Uint64(), activeTurnStartSeq)
		msg.SessionID = record.identity.SessionID().String()
		response.Items = append(response.Items, msg)
	}
	if !req.IncludeToolEvents {
		response.Items = annotateHiddenToolActivitySummaries(response.Items, sessionMessagesFromCommittedItems(record.transcript.Items()))
	}
	if nextBefore, ok := page.NextBefore(); ok {
		value := nextBefore.Uint64()
		response.NextBeforeSeq = &value
	}
	return s.annotateSupervisorRuns(ctx, record.identity.SessionID(), response), nil
}

func visibleTranscriptHistory(transcript message.Transcript, req SessionMessagesRequest) message.HistoryPage {
	if req.IncludeToolEvents {
		return transcript.History(messageBeforeSeq(req.BeforeSeq), req.Limit)
	}
	return message.HistoryFromCommitted(visibleCommittedMessages(transcript.Items(), req), messageBeforeSeq(req.BeforeSeq), req.Limit)
}

func visibleCommittedMessages(items []message.CommittedMessage, req SessionMessagesRequest) []message.CommittedMessage {
	if req.IncludeToolEvents {
		return items
	}
	visible := make([]message.CommittedMessage, 0, len(items))
	for _, item := range items {
		if committedMessageIsToolEvent(item) {
			continue
		}
		visible = append(visible, item)
	}
	return visible
}

func committedMessageIsToolEvent(item message.CommittedMessage) bool {
	kind := item.Kind().String()
	return kind == "tool" || kind == "tool_result"
}

func sessionMessageForRequest(item message.CommittedMessage, req SessionMessagesRequest, tailSeq uint64, activeTurnStartSeq uint64) SessionMessage {
	return deferSessionMessageForRequest(sessionMessageFromCommitted(item), req, tailSeq, activeTurnStartSeq)
}

func sessionMessagesFromCommittedItems(items []message.CommittedMessage) []SessionMessage {
	messages := make([]SessionMessage, 0, len(items))
	for _, item := range items {
		messages = append(messages, sessionMessageFromCommitted(item))
	}
	return messages
}

func deferSessionMessageForRequest(msg SessionMessage, req SessionMessagesRequest, tailSeq uint64, activeTurnStartSeq uint64) SessionMessage {
	if !req.Deferred || !deferToolMessageBody(msg, tailSeq, activeTurnStartSeq, req.IncludeToolDetails) {
		return msg
	}
	msg.Text = ""
	msg.Details = deferredToolDetails(msg)
	return msg
}

func sessionMessageIDRequested(req SessionMessagesRequest) bool {
	return strings.TrimSpace(req.EventID) != "" || strings.TrimSpace(req.ToolCallID) != ""
}

func (s *Stub) messageByID(ctx context.Context, record sessionRecord, req SessionMessagesRequest) (SessionMessage, bool) {
	eventID := strings.TrimSpace(req.EventID)
	toolCallID := strings.TrimSpace(req.ToolCallID)
	if eventID == "" && toolCallID == "" {
		return SessionMessage{}, false
	}
	if items, ok, _, err := s.loadPIAuthoritativeHistoryItems(ctx, record, s.cfg.Storage.DataDir); ok && err == nil {
		if msg, ok := findSessionMessageByID(items, eventID, toolCallID); ok {
			msg.SessionID = record.identity.SessionID().String()
			return msg, true
		}
	}
	if record.identity.Backend() == session.BackendCodex && record.runtime.helper != nil {
		if packet, ok, err := s.codexIODHistorySnapshot(ctx, record); err == nil && ok && len(packet.Messages) > 0 {
			if msg, ok := findSessionMessageByID(sessionMessagesFromIODHistory(packet.Messages), eventID, toolCallID); ok {
				msg.SessionID = record.identity.SessionID().String()
				return msg, true
			}
		}
	}
	for _, item := range record.transcript.History(nil, 0).Items() {
		msg := sessionMessageFromCommitted(item)
		if eventID != "" && msg.EventID == eventID || toolCallID != "" && msg.ToolCallID == toolCallID {
			msg.SessionID = record.identity.SessionID().String()
			return msg, true
		}
	}
	return SessionMessage{}, false
}

func findSessionMessageByID(items []SessionMessage, eventID string, toolCallID string) (SessionMessage, bool) {
	for _, item := range items {
		if eventID != "" && item.EventID == eventID || toolCallID != "" && item.ToolCallID == toolCallID {
			return item, true
		}
	}
	return SessionMessage{}, false
}

func activeTurnStartSeqForCommitted(items []message.CommittedMessage) uint64 {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Role().String() == "user" {
			return items[i].Seq().Uint64()
		}
	}
	return 0
}

func activeTurnStartSeqForMessages(items []SessionMessage) uint64 {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Role == "user" {
			return items[i].Seq
		}
	}
	return 0
}

func deferToolMessageBody(msg SessionMessage, tailSeq uint64, activeTurnStartSeq uint64, includeToolDetails bool) bool {
	if msg.Kind != "tool" && msg.Kind != "tool_result" && msg.Type != "tool" && msg.Type != "tool_result" {
		return false
	}
	if includeToolDetails {
		return false
	}
	if activeTurnStartSeq > 0 {
		return msg.Seq < activeTurnStartSeq
	}
	return msg.Seq+4 < tailSeq
}

func deferredToolDetails(msg SessionMessage) map[string]any {
	details := map[string]any{"deferred": true}
	if msg.ToolCallID != "" {
		details["tool_call_id"] = msg.ToolCallID
	}
	if msg.EventID != "" {
		details["event_id"] = msg.EventID
	}
	return details
}

func (s *Stub) SessionState(ctx context.Context, req SessionStateRequest) (SessionStateResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionStateResponse{}, err
	}
	record = s.reconcileCodexSessionFileFinalForState(record)
	return s.sessionStateResponse(record), nil
}

func (s *Stub) ProbeSessionState(ctx context.Context, req ProbeSessionStateRequest) (ProbeSessionStateResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return ProbeSessionStateResponse{}, err
	}
	probeID := fmt.Sprintf("actrail-manual-state-%d", time.Now().UTC().UnixNano())
	if record.identity.Backend() == session.BackendCodex && record.runtime.protocol == runtimeProtocolCodexRPC {
		if err := record.runtime.RequestCodexThreadState(ctx); err != nil {
			if errors.Is(err, errCodexThreadNotReady) {
				return ProbeSessionStateResponse{ProbeID: probeID, State: s.sessionStateResponse(record)}, nil
			}
			return ProbeSessionStateResponse{}, mapRuntimeControlError(err)
		}
		return ProbeSessionStateResponse{ProbeID: probeID, State: s.sessionStateResponse(record)}, nil
	}
	if record.identity.Backend() != session.BackendPI || record.runtime.protocol != runtimeProtocolPIRPC {
		return ProbeSessionStateResponse{}, Invalid("runtime", "state probe requires Pi RPC or Codex app-server runtime")
	}
	if record.runtime.piAgentGRPC != nil {
		state, err := record.runtime.RequestPIRPCStateSnapshot(ctx)
		if err != nil {
			return ProbeSessionStateResponse{}, mapRuntimeControlError(err)
		}
		if err := s.applyRuntimeProjection(req.SessionID, runtimeProjection{piRPCState: state}); err != nil {
			return ProbeSessionStateResponse{}, err
		}
		updated, err := s.lookupSession(req.SessionID)
		if err != nil {
			return ProbeSessionStateResponse{}, err
		}
		return ProbeSessionStateResponse{ProbeID: probeID, State: s.sessionStateResponse(updated)}, nil
	}
	if record.runtime.helper != nil {
		s.notePIRPCStateProbeSent(req.SessionID, record.runtime.helper.generationID, probeID)
	}
	if err := record.runtime.RequestPIRPCState(ctx, probeID); err != nil {
		return ProbeSessionStateResponse{}, mapRuntimeControlError(err)
	}
	return ProbeSessionStateResponse{ProbeID: probeID, State: s.sessionStateResponse(record)}, nil
}

func effectiveBusy(record sessionRecord) (bool, string) {
	if record.identity.Historical() {
		return false, ""
	}
	if record.identity.Backend() == session.BackendCodex {
		activity := codexVisibleActivity(record)
		if activity.Busy {
			return true, activity.Reason
		}
		return false, ""
	}
	if record.state.Busy() {
		return true, "state_busy"
	}
	if record.runtimeAgentRunning && record.state.Tail().Live() {
		return true, "runtime_agent_running"
	}
	if record.uiRequest != nil {
		return true, "ui_request"
	}
	if _, ok := record.transcript.PartialAssistantTurn(); ok {
		return true, "partial_assistant_turn"
	}
	return false, ""
}

func runtimeStateFields(record sessionRecord) (string, string) {
	if record.identity.Backend() != session.BackendCodex || record.identity.Historical() {
		return "", ""
	}
	activity := codexVisibleActivity(record)
	return string(activity.Phase), activity.Reason
}

func (s *Stub) sessionStateResponse(record sessionRecord) SessionStateResponse {
	contextUsage := copyContextUsage(record.contextUsage)
	turnTiming := copyTurnTiming(record.turnTiming)
	busy, busyReason := effectiveBusy(record)
	runtimeState, runtimeStateReason := runtimeStateFields(record)
	tailSeq := record.transcript.TailSeq().Uint64()
	if record.identity.Backend() == session.BackendCodex && s != nil {
		if mirrored := s.codexLiveMirroredTail(record.identity.SessionID()); mirrored > tailSeq {
			tailSeq = mirrored
		}
	}
	partial := partialAssistantTurn(record.transcript)
	if !busy {
		partial = nil
	}
	return SessionStateResponse{
		Busy:                 busy,
		BusyReason:           busyReason,
		RuntimeState:         runtimeState,
		RuntimeStateReason:   runtimeStateReason,
		Queue:                queueSnapshotFromState(record.state),
		Transport:            s.sessionTransportSnapshot(record),
		UIRequest:            copySessionUIRequest(record.uiRequest),
		PartialAssistantTurn: partial,
		TailSeq:              tailSeq,
		ResumeCursors:        record.resumeCursors,
		ContextUsage:         contextUsage,
		TurnTiming:           turnTiming,
		ActiveWait:           s.activeWaitForSession(record.identity.SessionID()),
	}
}

func (s *Stub) lookupSession(sessionID session.SessionID) (sessionRecord, error) {
	record, ok := s.registry.LookupRoute(sessionID)
	if !ok {
		return sessionRecord{}, NotFound(fmt.Sprintf("session %q not found", sessionID))
	}
	record.runtime = s.runtimeForRecord(record)
	return record, nil
}

func queueSnapshotFromState(state session.State) SessionQueueSnapshot {
	items := state.Queue().Items()
	snapshot := SessionQueueSnapshot{Items: make([]QueuedPromptSnapshot, 0, len(items))}
	for _, item := range items {
		snapshot.Items = append(snapshot.Items, QueuedPromptSnapshot{
			ID:    item.ID().String(),
			Text:  item.Text(),
			State: item.State().String(),
		})
	}
	return snapshot
}

func partialAssistantTurn(transcript message.Transcript) *PartialAssistantTurnSnapshot {
	partial, ok := transcript.PartialAssistantTurn()
	if !ok {
		return nil
	}
	return &PartialAssistantTurnSnapshot{TurnID: partial.TurnID().String(), Text: partial.Text()}
}

func messageBeforeSeq(raw *uint64) *message.Seq {
	if raw == nil {
		return nil
	}
	seq := message.Seq(*raw)
	return &seq
}
