package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"actrail/internal/adapters/iod"
	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

const (
	sourceConfidenceExact       = "exact"
	sourceConfidenceHelper      = "helper"
	sourceConfidenceInferred    = "inferred"
	sourceConfidenceProvisional = "provisional"
)

func normalizeSourceConfidence(value string) string {
	switch strings.TrimSpace(value) {
	case sourceConfidenceExact, sourceConfidenceHelper, sourceConfidenceInferred:
		return strings.TrimSpace(value)
	default:
		return sourceConfidenceProvisional
	}
}

func applyImportedSourceRefs(records []sessionRecord, refs []sqlitestore.SessionSourceRefRow) []sessionRecord {
	if len(records) == 0 || len(refs) == 0 {
		return records
	}
	bySessionID := make(map[string]sqlitestore.SessionSourceRefRow, len(refs))
	for _, row := range refs {
		bySessionID[strings.TrimSpace(row.SessionID)] = row
	}
	for i := range records {
		row, ok := bySessionID[records[i].identity.SessionID().String()]
		if !ok {
			continue
		}
		records[i].importedSourcePath = strings.TrimSpace(row.SourcePath)
		records[i].importedBackendSessionID = strings.TrimSpace(row.BackendSessionID)
		records[i].importedSourceConfidence = normalizeSourceConfidence(row.SourceConfidence)
		records[i].importedFirstUserMessage = strings.TrimSpace(row.FirstUserMessage)
		records[i].importedHasLegacySessionUIState = row.HasLegacySessionUIState
	}
	return records
}

func firstUserMessageForRecord(record sessionRecord) string {
	if text := firstUserMessage(record.transcript); text != "" {
		return text
	}
	return strings.TrimSpace(record.importedFirstUserMessage)
}

func (s *Stub) loadDetachedImportedPIHistory(ctx context.Context, record sessionRecord, req SessionMessagesRequest) (SessionMessagesResponse, bool, error) {
	if record.transcript.Len() > 0 {
		return SessionMessagesResponse{}, false, nil
	}
	if _, ok := record.transcript.PartialAssistantTurn(); ok {
		return SessionMessagesResponse{}, false, nil
	}
	sourcePath := strings.TrimSpace(record.importedSourcePath)
	if sourcePath == "" || record.identity.Backend().String() != "pi" {
		return SessionMessagesResponse{}, false, nil
	}
	if info, err := os.Stat(sourcePath); err != nil || info.IsDir() {
		return SessionMessagesResponse{}, false, nil
	}
	if backendSessionID := strings.TrimSpace(record.importedBackendSessionID); backendSessionID != "" && !piSourcePathMatchesSessionID(sourcePath, backendSessionID) {
		return SessionMessagesResponse{}, false, nil
	}
	if items, ok := s.messageCache.GetSession(record.identity.SessionID()); ok {
		return paginateSessionMessagesForRequest(items, req), true, nil
	}
	signature, ok := piSourceSignature(sourcePath)
	if !ok {
		return SessionMessagesResponse{}, false, nil
	}
	cacheKey := "detached:" + signature
	if items, ok := s.messageCache.Get(record.identity.SessionID(), cacheKey); ok {
		return paginateSessionMessagesForRequest(items, req), true, nil
	}
	if page, ok, err := loadSourceHistoryPage(sourcePath, req); ok {
		if err != nil {
			return SessionMessagesResponse{}, true, err
		}
		s.rememberPISourceBinding(record, sourcePath, sourceConfidenceExact)
		return sourceHistorySessionMessagesResponse(page, req), true, nil
	}
	items, err := importedSessionMessagesFromSourcePath(sourcePath)
	if err != nil {
		return SessionMessagesResponse{}, true, err
	}
	s.rememberPISourceBinding(record, sourcePath, sourceConfidenceExact)
	s.messageCache.Put(record.identity.SessionID(), cacheKey, items)
	return paginateSessionMessagesForRequest(items, req), true, nil
}

func importedSessionMessagesFromSourcePath(sourcePath string) ([]SessionMessage, error) {
	body, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("read imported pi session source %q: %w", sourcePath, err)
	}
	return importedSessionMessagesFromJSONLBytes(sourcePath, body)
}

func importedSessionMessagesFromJSONLLines(sourcePath string, lines []string) ([]SessionMessage, error) {
	body := []byte(strings.Join(lines, "\n"))
	if len(body) > 0 {
		body = append(body, '\n')
	}
	return importedSessionMessagesFromJSONLBytes(sourcePath, body)
}

func importedSessionMessagesFromJSONLBytes(sourcePath string, body []byte) ([]SessionMessage, error) {
	material, err := pi.ParseJSONLBytes(body)
	if err != nil {
		return nil, fmt.Errorf("parse imported pi session source %q: %w", sourcePath, err)
	}
	return sessionMessagesFromPIEvents(material.Events), nil
}

func sessionMessagesFromPIEvents(events []pi.Event) []SessionMessage {
	items := make([]SessionMessage, 0, len(events))
	for eventIndex, event := range events {
		if msg, ok := sessionMessageFromPIEvent(event); ok {
			if msg.SourceOrder == "" {
				msg.SourceOrder = fmt.Sprintf("pi:%06d", eventIndex)
			}
			items = append(items, msg)
		}
	}
	for i := range items {
		items[i].Seq = uint64(i + 1)
	}
	return items
}

func sessionMessageFromPIEvent(event pi.Event) (SessionMessage, bool) {
	switch event.Kind {
	case pi.EventKindMessage:
		if event.Message == nil || strings.TrimSpace(event.Message.Text) == "" {
			return SessionMessage{}, false
		}
		if strings.TrimSpace(event.Message.StopReason) == "status" {
			return SessionMessage{
				Kind:          "pi_event",
				Type:          "pi_event",
				Text:          event.Message.Text,
				TS:            event.Timestamp,
				EventID:       piMessageEventID(event),
				ParentEventID: piParentEventID(event),
				Details: map[string]any{
					"raw_type": strings.TrimSpace(event.RawType),
					"status":   true,
				},
			}, true
		}
		if event.Message.Role != pi.MessageRoleUser && event.Message.Role != pi.MessageRoleAssistant {
			return SessionMessage{}, false
		}
		if event.Message.Role == pi.MessageRoleAssistant && !event.Message.CommitLike {
			return SessionMessage{}, false
		}
		return SessionMessage{
			Role:          string(event.Message.Role),
			Kind:          "message",
			Text:          event.Message.Text,
			TS:            event.Timestamp,
			EventID:       piMessageEventID(event),
			ParentEventID: piParentEventID(event),
		}, true
	case pi.EventKindTool:
		if event.Tool == nil {
			return SessionMessage{}, false
		}
		kind := "tool"
		if event.Tool.Result {
			kind = "tool_result"
		}
		details := map[string]any{}
		if event.Tool.Name != "" {
			details["name"] = event.Tool.Name
		}
		if event.Tool.CallID != "" {
			details["tool_call_id"] = event.Tool.CallID
		}
		if len(event.Tool.Arguments) > 0 {
			details["arguments"] = event.Tool.Arguments
		}
		return SessionMessage{
			Kind:          kind,
			Type:          kind,
			Text:          strings.TrimSpace(event.Tool.Text),
			TS:            event.Timestamp,
			EventID:       piToolEventID(event),
			ParentEventID: piParentEventID(event),
			Name:          strings.TrimSpace(event.Tool.Name),
			Summary:       strings.TrimSpace(event.Tool.Name),
			ToolCallID:    strings.TrimSpace(event.Tool.CallID),
			IsError:       event.Tool.IsError,
			Details:       details,
		}, true
	case pi.EventKindError:
		if event.Error == nil || strings.TrimSpace(event.Error.Message) == "" {
			return SessionMessage{}, false
		}
		return SessionMessage{
			Kind:          "error",
			Type:          "error",
			Text:          strings.TrimSpace(event.Error.Message),
			TS:            event.Timestamp,
			EventID:       piErrorEventID(event),
			ParentEventID: piParentEventID(event),
			IsError:       true,
			Details: map[string]any{
				"source":      strings.TrimSpace(event.Error.Source),
				"stop_reason": strings.TrimSpace(event.Error.StopReason),
			},
		}, true
	default:
		return SessionMessage{}, false
	}
}

func piMessageEventID(event pi.Event) string {
	if id := strings.TrimSpace(event.RawID); id != "" {
		return "pi:message:" + id
	}
	return ""
}

func piToolEventID(event pi.Event) string {
	if event.Tool == nil {
		return ""
	}
	callID := strings.TrimSpace(event.Tool.CallID)
	if callID != "" {
		if event.Tool.Result {
			return fmt.Sprintf("pi:tool_result:%s:%d", callID, event.Tool.ResultIndex)
		}
		return "pi:tool_call:" + callID
	}
	if id := strings.TrimSpace(event.RawID); id != "" {
		if event.Tool.Result {
			return fmt.Sprintf("pi:tool_result:%s:%d", id, event.Tool.ResultIndex)
		}
		return "pi:tool_call:" + id
	}
	return ""
}

func piErrorEventID(event pi.Event) string {
	if id := strings.TrimSpace(event.RawID); id != "" {
		return "pi:error:" + id
	}
	return ""
}

func piParentEventID(event pi.Event) string {
	if id := strings.TrimSpace(event.ParentID); id != "" {
		return "pi:message:" + id
	}
	return ""
}

func paginateSessionMessagesForRequest(items []SessionMessage, req SessionMessagesRequest) SessionMessagesResponse {
	rawTailSeq := uint64(0)
	if len(items) > 0 {
		rawTailSeq = items[len(items)-1].Seq
	}
	visibleItems := filterSessionMessagesForRequest(items, req)
	response := paginateSessionMessages(visibleItems, req.AfterSeq, req.BeforeSeq, req.Limit)
	if rawTailSeq > response.TailSeq {
		response.TailSeq = rawTailSeq
	}
	if !req.Deferred {
		return response
	}
	activeTurnStartSeq := req.ActiveTurnStartSeq
	if activeTurnStartSeq == 0 {
		activeTurnStartSeq = activeTurnStartSeqForMessages(visibleItems)
	}
	for i := range response.Items {
		response.Items[i] = deferSessionMessageForRequest(response.Items[i], req, response.TailSeq, activeTurnStartSeq)
	}
	return response
}

func filterSessionMessagesForRequest(items []SessionMessage, req SessionMessagesRequest) []SessionMessage {
	if req.IncludeToolEvents {
		return items
	}
	visible := make([]SessionMessage, 0, len(items))
	for _, item := range items {
		if sessionMessageIsToolEvent(item) {
			continue
		}
		visible = append(visible, item)
	}
	return visible
}

func sessionMessageIsToolEvent(item SessionMessage) bool {
	return item.Kind == "tool" || item.Kind == "tool_result" || item.Type == "tool" || item.Type == "tool_result"
}

func paginateSessionMessages(items []SessionMessage, after *uint64, before *uint64, limit int) SessionMessagesResponse {
	if after != nil {
		page := make([]SessionMessage, 0, len(items))
		for _, item := range items {
			if item.Seq > *after {
				page = append(page, item)
			}
		}
		response := SessionMessagesResponse{Items: page}
		if len(items) > 0 {
			response.TailSeq = items[len(items)-1].Seq
		}
		return response
	}
	upper := len(items)
	if before != nil {
		upper = 0
		for idx, item := range items {
			if item.Seq >= *before {
				upper = idx
				break
			}
			upper = idx + 1
		}
	}
	start := 0
	if limit > 0 && upper > limit {
		start = upper - limit
	}
	page := append([]SessionMessage(nil), items[start:upper]...)
	response := SessionMessagesResponse{
		Items:   page,
		HasMore: start > 0,
	}
	if len(items) > 0 {
		response.TailSeq = items[len(items)-1].Seq
	}
	if response.HasMore && len(page) > 0 {
		next := page[0].Seq
		response.NextBeforeSeq = &next
	}
	return response
}

func (s *Stub) loadPIAuthoritativeHistory(ctx context.Context, record sessionRecord, dataDir string, req SessionMessagesRequest) (SessionMessagesResponse, bool, error) {
	if record.identity.Backend().String() != "pi" {
		return SessionMessagesResponse{}, false, nil
	}
	signatureParts := []string{fmt.Sprintf("transcript:%d", record.transcript.TailSeq().Uint64())}
	var helperItems []SessionMessage
	paths := piHistorySourcePaths(record)
	for _, path := range paths {
		if sourceSig, ok := piSourceSignature(path); ok {
			signatureParts = append(signatureParts, "source:"+sourceSig)
		}
	}
	cacheKey := "pi-authoritative:" + strings.Join(signatureParts, "|")
	if response, ok := s.messageCache.GetPage(record.identity.SessionID(), cacheKey, req); ok {
		return response, true, nil
	}
	if record.runtime.helper != nil {
		historyCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		packet, err := record.runtime.helper.sessionHistory(historyCtx)
		if err == nil && len(packet.Lines) > 0 {
			if packet.Complete || len(paths) == 0 {
				helperItems, err = importedSessionMessagesFromJSONLLines(packet.SourcePath, packet.Lines)
				if err != nil {
					return SessionMessagesResponse{}, true, err
				}
			}
			if sourceSig, ok := piSourceSignature(packet.SourcePath); ok {
				signatureParts = append(signatureParts, "helper:"+sourceSig)
			} else {
				signatureParts = append(signatureParts, fmt.Sprintf("helper:%s:%t:%t:%d", strings.TrimSpace(packet.SourcePath), packet.Warmed, packet.Complete, len(packet.Lines)))
			}
			if !packet.Complete {
				signatureParts = append(signatureParts, "incomplete")
			}
		}
	}

	items := make([]SessionMessage, 0)
	if len(paths) == 1 && len(helperItems) == 0 && record.transcript.Len() == 0 {
		if page, ok, err := loadSourceHistoryPage(paths[0], req); ok {
			if err != nil {
				return SessionMessagesResponse{}, true, err
			}
			if len(paths) > 0 {
				s.rememberPISourceBinding(record, paths[0], sourceConfidenceForPath(record, paths[0]))
			}
			return sourceHistorySessionMessagesResponse(page, req), true, nil
		}
	}
	for _, path := range paths {
		sourceItems, err := importedSessionMessagesFromSourcePath(path)
		if err != nil {
			return SessionMessagesResponse{}, true, err
		}
		appendDedupedMessages(&items, sourceItems)
	}
	appendDedupedMessages(&items, helperItems)
	appendTranscriptMessages(&items, record)
	if len(items) == 0 {
		return SessionMessagesResponse{}, false, nil
	}
	s.reconcilePIAuthoritativeFinal(record, items)
	if len(paths) > 0 {
		s.rememberPISourceBinding(record, paths[0], sourceConfidenceForPath(record, paths[0]))
	}
	for i := range items {
		items[i].Seq = uint64(i + 1)
	}
	s.messageCache.Put(record.identity.SessionID(), cacheKey, items)
	return paginateSessionMessagesForRequest(items, req), true, nil
}

func (s *Stub) rememberPISourceBinding(record sessionRecord, sourcePath, confidence string) {
	backendSessionID, ok, err := piSessionIDFromSourcePath(sourcePath)
	if err != nil || !ok || backendSessionID == "" {
		return
	}
	if record.importedBackendSessionID == backendSessionID && filepath.Clean(strings.TrimSpace(record.importedSourcePath)) == filepath.Clean(strings.TrimSpace(sourcePath)) && strings.TrimSpace(record.importedSourceConfidence) == strings.TrimSpace(confidence) {
		return
	}
	_, _, _ = s.registry.SetSourceBinding(record.identity.SessionID(), backendSessionID, sourcePath, confidence)
}

func sourceConfidenceForPath(record sessionRecord, sourcePath string) string {
	if strings.TrimSpace(record.importedBackendSessionID) != "" {
		return sourceConfidenceExact
	}
	if filepath.Clean(strings.TrimSpace(record.importedSourcePath)) == filepath.Clean(strings.TrimSpace(sourcePath)) && !looksActRailGeneratedPISource(sourcePath) {
		return sourceConfidenceExact
	}
	return sourceConfidenceInferred
}

func (s *Stub) loadPIAuthoritativeHistoryItems(ctx context.Context, record sessionRecord, dataDir string) ([]SessionMessage, bool, string, error) {
	if record.identity.Backend().String() != "pi" {
		return nil, false, "", nil
	}
	items := make([]SessionMessage, 0)
	signatureParts := []string{fmt.Sprintf("transcript:%d", record.transcript.TailSeq().Uint64())}
	if record.runtime.helper != nil {
		historyCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		packet, err := record.runtime.helper.sessionHistory(historyCtx)
		if err == nil && len(packet.Lines) > 0 {
			historyItems, err := importedSessionMessagesFromJSONLLines(packet.SourcePath, packet.Lines)
			if err != nil {
				return nil, true, "", err
			}
			appendDedupedMessages(&items, historyItems)
			if sourceSig, ok := piSourceSignature(packet.SourcePath); ok {
				signatureParts = append(signatureParts, "helper:"+sourceSig)
			} else {
				signatureParts = append(signatureParts, fmt.Sprintf("helper:%s:%t:%t:%d", strings.TrimSpace(packet.SourcePath), packet.Warmed, packet.Complete, len(packet.Lines)))
			}
			if !packet.Complete {
				signatureParts = append(signatureParts, "incomplete")
			}
		}
	}
	if len(items) == 0 {
		paths := piHistorySourcePaths(record)
		for _, path := range paths {
			if sourceSig, ok := piSourceSignature(path); ok {
				signatureParts = append(signatureParts, "source:"+sourceSig)
			}
			sourceItems, err := importedSessionMessagesFromSourcePath(path)
			if err != nil {
				return nil, true, "", err
			}
			appendDedupedMessages(&items, sourceItems)
		}
	}
	appendTranscriptMessages(&items, record)
	if len(items) == 0 {
		return nil, false, "", nil
	}
	for i := range items {
		items[i].Seq = uint64(i + 1)
	}
	return items, true, strings.Join(signatureParts, "|"), nil
}

func (s *Stub) reconcilePersistedBusySessions(ctx context.Context) {
	if s == nil {
		return
	}
	for _, record := range s.registry.List() {
		if !record.state.Busy() || record.identity.Backend().String() != "pi" {
			continue
		}
		_, _, _, _ = s.loadPIAuthoritativeHistoryItems(ctx, record, s.cfg.Storage.DataDir)
	}
}

func (s *Stub) reconcilePIAuthoritativeFinal(record sessionRecord, items []SessionMessage) {
	if s == nil || record.identity.Backend().String() != "pi" || !record.state.Busy() || len(items) == 0 {
		return
	}
	last := items[len(items)-1]
	if last.Role != "assistant" || strings.TrimSpace(last.Text) == "" {
		return
	}
	state, ok, err := s.registry.MarkRuntimeCompleted(record.identity.SessionID())
	if err != nil || !ok {
		return
	}
	if err := s.setRuntimeAgentRunning(record.identity.SessionID(), false); err != nil {
		return
	}
	s.emitSessionState(record.identity.SessionID())
	if !state.Busy() {
		s.scheduleQueuedDispatch(record.identity.SessionID())
	}
}

func piSourceSignature(path string) (string, bool) {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return "", false
	}
	info, err := os.Stat(cleaned)
	if err != nil || info.IsDir() {
		return "", false
	}
	return fmt.Sprintf("%s:%d:%d", filepath.Clean(cleaned), info.Size(), info.ModTime().UnixNano()), true
}

func appendTranscriptMessages(items *[]SessionMessage, record sessionRecord) {
	if record.transcript.Len() == 0 {
		return
	}
	incoming := make([]SessionMessage, 0, record.transcript.Len())
	for _, item := range record.transcript.Items() {
		incoming = append(incoming, sessionMessageFromCommitted(item))
	}
	appendDedupedMessages(items, incoming)
}

func appendDedupedMessages(items *[]SessionMessage, incoming []SessionMessage) {
	for _, item := range incoming {
		item.Seq = 0
		if duplicateWALMessage(*items, item) {
			continue
		}
		*items = append(*items, item)
	}
}

func (s *Stub) loadCodexIODHistory(ctx context.Context, record sessionRecord, req SessionMessagesRequest) (SessionMessagesResponse, bool, error) {
	if record.identity.Backend() != session.BackendCodex || record.transcript.Len() > 0 {
		return SessionMessagesResponse{}, false, nil
	}
	if _, ok := record.transcript.PartialAssistantTurn(); ok {
		return SessionMessagesResponse{}, false, nil
	}
	sessionID := record.identity.SessionID()
	cacheKey, err := codexIODHistoryCacheKey(s.cfg.Storage.IODRuntimeRoot(), sessionID)
	if err != nil {
		return SessionMessagesResponse{}, false, err
	}
	if cacheKey == "" {
		return SessionMessagesResponse{}, false, nil
	}
	if items, ok := s.messageCache.Get(sessionID, cacheKey); ok {
		return paginateSessionMessagesForRequest(items, req), true, nil
	}
	items, err := s.codexIODHistoryMessages(ctx, sessionID)
	if err != nil {
		return SessionMessagesResponse{}, true, err
	}
	if len(items) == 0 {
		return SessionMessagesResponse{}, false, nil
	}
	s.messageCache.Put(sessionID, cacheKey, items)
	return paginateSessionMessagesForRequest(items, req), true, nil
}

func (s *Stub) codexIODHistoryMessages(ctx context.Context, sessionID session.SessionID) ([]SessionMessage, error) {
	generations, err := codexIODHistoryGenerations(s.cfg.Storage.IODRuntimeRoot(), sessionID)
	if err != nil {
		return nil, err
	}
	items := make([]SessionMessage, 0)
	for _, generation := range generations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		replay, err := iod.ReplayWAL(generation.WALPath, sessionID, generation.GenerationID, 0)
		if err != nil {
			return nil, fmt.Errorf("replay codex iod wal %q: %w", generation.WALPath, err)
		}
		decoder := runtimeEventDecoder{backend: session.BackendCodex}
		threadID := ""
		for _, record := range replay.Records {
			projection, err := decoder.decodeHelperFact(helperFactFromWALRecord(record))
			if err != nil {
				continue
			}
			if strings.TrimSpace(projection.codexThreadID) != "" && threadID == "" {
				threadID = strings.TrimSpace(projection.codexThreadID)
			}
			for _, event := range projection.events {
				if !codexHistoryEventInMainThread(threadID, event) && !codexSubagentMessageEvent(event) {
					continue
				}
				if msg, ok := codexHistorySessionMessage(threadID, event); ok {
					appendDedupedMessages(&items, []SessionMessage{msg})
				}
			}
		}
	}
	for i := range items {
		items[i].Seq = uint64(i + 1)
	}
	return items, nil
}

func helperFactFromWALRecord(record iod.WALRecord) iod.HelperFact {
	return iod.HelperFact{
		FactKind: record.Header.Class.FactKind(),
		Seq:      record.Header.Seq,
		Payload:  append(json.RawMessage(nil), record.Payload...),
	}
}

func codexHistoryEventInMainThread(mainThreadID string, event pi.Event) bool {
	eventThreadID := strings.TrimSpace(event.ThreadID)
	if eventThreadID == "" || strings.TrimSpace(mainThreadID) == "" {
		return true
	}
	return eventThreadID == strings.TrimSpace(mainThreadID)
}

func codexHistorySessionMessage(mainThreadID string, event pi.Event) (SessionMessage, bool) {
	if event.Message != nil && !codexHistoryEventInMainThread(mainThreadID, event) {
		if !codexSubagentMessageEvent(event) {
			return SessionMessage{}, false
		}
		payload := codexSubagentMessagePayload{
			Role:     strings.TrimSpace(string(event.Message.Role)),
			Text:     strings.TrimSpace(event.Message.Text),
			ThreadID: strings.TrimSpace(event.ThreadID),
			TurnID:   strings.TrimSpace(event.TurnID),
			ItemID:   strings.TrimSpace(event.RawID),
		}
		encoded, err := encodeCodexSubagentMessage(payload)
		if err != nil {
			return SessionMessage{}, false
		}
		msg := SessionMessage{
			Kind:          "custom_message",
			Type:          "custom_message",
			Text:          encoded,
			TS:            event.Timestamp,
			EventID:       piMessageEventID(event),
			ParentEventID: piParentEventID(event),
		}
		applyCodexSubagentMessageFields(&msg, payload)
		return msg, true
	}
	if msg, ok := sessionMessageFromPIEvent(event); ok {
		return msg, true
	}
	return SessionMessage{}, false
}

type codexIODHistoryGeneration struct {
	GenerationID iod.GenerationID
	WALPath      string
	Signature    string
	StartTS      float64
}

func codexIODHistoryGenerations(root string, sessionID session.SessionID) ([]codexIODHistoryGeneration, error) {
	sessionDir := filepath.Join(strings.TrimSpace(root), sessionID.String())
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read codex iod session dir %q: %w", sessionDir, err)
	}
	generations := make([]codexIODHistoryGeneration, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		generationID, err := iod.NewGenerationID(entry.Name())
		if err != nil {
			continue
		}
		paths, err := iod.NewGenerationPaths(root, sessionID, generationID)
		if err != nil {
			continue
		}
		manifest, err := iod.ReadGenerationManifest(paths.ManifestPath)
		if err != nil {
			continue
		}
		info, err := os.Stat(paths.WALPath)
		if err != nil || info.IsDir() {
			continue
		}
		generations = append(generations, codexIODHistoryGeneration{
			GenerationID: generationID,
			WALPath:      paths.WALPath,
			Signature:    fmt.Sprintf("%s:%d:%d", filepath.Clean(paths.WALPath), info.Size(), info.ModTime().UnixNano()),
			StartTS:      manifest.StartTS,
		})
	}
	sort.Slice(generations, func(i, j int) bool {
		if generations[i].StartTS != generations[j].StartTS {
			return generations[i].StartTS < generations[j].StartTS
		}
		return generations[i].GenerationID.String() < generations[j].GenerationID.String()
	})
	return generations, nil
}

func codexIODHistoryCacheKey(root string, sessionID session.SessionID) (string, error) {
	generations, err := codexIODHistoryGenerations(root, sessionID)
	if err != nil {
		return "", err
	}
	if len(generations) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(generations))
	for _, generation := range generations {
		parts = append(parts, generation.Signature)
	}
	return "codex-iod-history:" + strings.Join(parts, "|"), nil
}

func piSessionIDFromSourcePath(path string) (string, bool, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRuntimeLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		material, err := pi.ParseObjectJSON(line)
		if err != nil {
			return "", false, err
		}
		if material.Header == nil {
			continue
		}
		id := strings.TrimSpace(material.Header.SessionID)
		return id, id != "", nil
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	return "", false, nil
}

func piSourcePathMatchesSessionID(path, sessionID string) bool {
	want := strings.TrimSpace(sessionID)
	if want == "" || strings.TrimSpace(path) == "" {
		return false
	}
	got, ok, err := piSessionIDFromSourcePath(path)
	return err == nil && ok && got == want
}

func piHistorySourcePaths(record sessionRecord) []string {
	if backendSessionID := strings.TrimSpace(record.importedBackendSessionID); backendSessionID != "" {
		return strictPISessionSourcePaths(record, backendSessionID)
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0, 4)
	add := func(path string) {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			return
		}
		if info, err := os.Stat(cleaned); err != nil || info.IsDir() {
			return
		}
		seen[cleaned] = struct{}{}
		paths = append(paths, cleaned)
	}
	add(record.importedSourcePath)
	return paths
}

func strictPISessionSourcePaths(record sessionRecord, backendSessionID string) []string {
	seen := make(map[string]struct{})
	paths := make([]string, 0, 2)
	add := func(path string) {
		cleaned := filepath.Clean(strings.TrimSpace(path))
		if cleaned == "." || cleaned == "" {
			return
		}
		if _, ok := seen[cleaned]; ok {
			return
		}
		if !piSourcePathMatchesSessionID(cleaned, backendSessionID) {
			return
		}
		seen[cleaned] = struct{}{}
		paths = append(paths, cleaned)
	}
	add(record.importedSourcePath)
	if len(paths) > 0 && strings.TrimSpace(record.importedSourceConfidence) == sourceConfidenceExact {
		return paths
	}
	for _, path := range discoverPISessionSourcesByID(record.cwd, backendSessionID) {
		add(path)
	}
	return paths
}

func discoverPISessionSourcesByID(cwd, backendSessionID string) []string {
	backendSessionID = strings.TrimSpace(backendSessionID)
	if backendSessionID == "" {
		return nil
	}
	roots := piSessionHistoryRoots(cwd)
	if len(roots) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0, 1)
	add := func(path string) {
		cleaned := filepath.Clean(strings.TrimSpace(path))
		if cleaned == "." || cleaned == "" {
			return
		}
		if _, ok := seen[cleaned]; ok {
			return
		}
		if !piSourcePathMatchesSessionID(cleaned, backendSessionID) {
			return
		}
		seen[cleaned] = struct{}{}
		paths = append(paths, cleaned)
	}
	for _, root := range roots {
		matches, err := filepath.Glob(filepath.Join(root, "*"+backendSessionID+"*.jsonl"))
		if err == nil {
			for _, path := range matches {
				add(path)
			}
		}
	}
	if len(paths) > 0 {
		return paths
	}
	for _, root := range roots {
		matches, err := filepath.Glob(filepath.Join(root, "*.jsonl"))
		if err != nil {
			continue
		}
		for _, path := range matches {
			add(path)
		}
	}
	return paths
}

func looksActRailGeneratedPISource(path string) bool {
	base := filepath.Base(strings.TrimSpace(path))
	return strings.Contains(base, "_actrail_")
}

func piSessionHistoryRoots(cwd string) []string {
	raw := strings.TrimSpace(cwd)
	if raw == "" {
		return nil
	}
	cwdValues := []string{raw}
	if eval, err := filepath.EvalSymlinks(raw); err == nil && strings.TrimSpace(eval) != "" && filepath.Clean(eval) != filepath.Clean(raw) {
		cwdValues = append(cwdValues, eval)
	}
	baseRoots := []string{}
	if piHome := strings.TrimSpace(os.Getenv("PI_HOME")); piHome != "" {
		baseRoots = append(baseRoots, filepath.Join(piHome, "agent", "sessions"))
	}
	baseRoots = append(baseRoots, "/root/.pi/agent/sessions")
	roots := make([]string, 0, len(baseRoots)*len(cwdValues))
	seen := map[string]struct{}{}
	for _, cwdValue := range cwdValues {
		dirName := piSessionDirName(cwdValue)
		if dirName == "" {
			continue
		}
		for _, base := range baseRoots {
			root := filepath.Join(base, dirName)
			cleaned := filepath.Clean(root)
			if _, ok := seen[cleaned]; ok {
				continue
			}
			if info, err := os.Stat(cleaned); err == nil && info.IsDir() {
				seen[cleaned] = struct{}{}
				roots = append(roots, cleaned)
			}
		}
	}
	return roots
}

func piSessionDirName(cwd string) string {
	cleaned := filepath.Clean(strings.TrimSpace(cwd))
	if cleaned == "." || cleaned == "" {
		return ""
	}
	trimmed := strings.Trim(cleaned, string(filepath.Separator))
	if trimmed == "" {
		return "--root--"
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool { return r == '/' || r == '\\' })
	return "--" + strings.Join(parts, "-") + "--"
}

func duplicateWALMessage(items []SessionMessage, candidate SessionMessage) bool {
	candidateEventID := strings.TrimSpace(candidate.EventID)
	if candidateEventID != "" {
		for i := len(items) - 1; i >= 0; i-- {
			if strings.TrimSpace(items[i].EventID) == candidateEventID {
				return true
			}
		}
		return false
	}
	window := 6
	text := strings.TrimSpace(candidate.Text)
	if candidate.Role == "user" && candidate.Kind == "message" && text != "" {
		for i := len(items) - 1; i >= 0; i-- {
			item := items[i]
			if item.Role == candidate.Role && item.Kind == candidate.Kind && item.Text == candidate.Text && closeTimestamp(item.TS, candidate.TS) {
				return true
			}
		}
		return false
	}
	if candidate.Role == "assistant" && candidate.Kind == "message" && len([]rune(text)) >= 80 {
		window = len(items)
	} else if candidate.Kind != "message" && len([]rune(text)) >= 32 {
		window = len(items)
	}
	for i := len(items) - 1; i >= 0 && i >= len(items)-window; i-- {
		item := items[i]
		if item.Role == candidate.Role && item.Kind == candidate.Kind && item.Type == candidate.Type && item.Text == candidate.Text {
			return true
		}
	}
	return false
}

func closeTimestamp(a, b float64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	return math.Abs(a-b) <= 120
}
