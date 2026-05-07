package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/piagentgrpc"
	"actrail/internal/domain/codex"
	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

const (
	maxRuntimeLineBytes = 1 << 20
)

var runtimeHelperProjectors sync.Map

type iodTerminalOutputPayload struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type runtimeEventDecoder struct {
	backend     session.Backend
	helperLines runtimeLineBuffer
}

type runtimeProjection struct {
	events            []pi.Event
	waitRequests      []pi.Event
	codexThreadID     string
	codexTurnID       string
	clearCodexTurn    bool
	codexBusy         *bool
	codexInitialized  bool
	model             string
	provider          string
	contextUsage      *SessionContextUsageSnapshot
	turnTiming        *SessionTurnTimingSnapshot
	piRPCState        *piRPCStateSnapshot
	piRPCStateFailure *piRPCStateFailure
}

type runtimeLineBuffer struct {
	pending bytes.Buffer
}

type runtimeHelperProjector struct {
	mu      sync.Mutex
	decoder runtimeEventDecoder
}

func mergeRuntimeProjection(dst, src runtimeProjection) runtimeProjection {
	if len(src.events) > 0 {
		dst.events = append(dst.events, src.events...)
	}
	if len(src.waitRequests) > 0 {
		dst.waitRequests = append(dst.waitRequests, src.waitRequests...)
	}
	if strings.TrimSpace(src.codexThreadID) != "" {
		dst.codexThreadID = strings.TrimSpace(src.codexThreadID)
	}
	if strings.TrimSpace(src.codexTurnID) != "" {
		dst.codexTurnID = strings.TrimSpace(src.codexTurnID)
	}
	if src.clearCodexTurn {
		dst.clearCodexTurn = true
	}
	if src.codexBusy != nil {
		busy := *src.codexBusy
		dst.codexBusy = &busy
	}
	if src.codexInitialized {
		dst.codexInitialized = true
	}
	if strings.TrimSpace(src.model) != "" {
		dst.model = strings.TrimSpace(src.model)
	}
	if strings.TrimSpace(src.provider) != "" {
		dst.provider = strings.TrimSpace(src.provider)
	}
	if src.contextUsage != nil {
		dst.contextUsage = copyContextUsage(src.contextUsage)
	}
	if src.turnTiming != nil {
		dst.turnTiming = mergeTurnTiming(dst.turnTiming, src.turnTiming)
	}
	if src.piRPCState != nil {
		state := *src.piRPCState
		dst.piRPCState = &state
	}
	if src.piRPCStateFailure != nil {
		failure := *src.piRPCStateFailure
		dst.piRPCStateFailure = &failure
	}
	return dst
}

func runtimeProjectionSupported(backend session.Backend) bool {
	switch backend {
	case session.BackendPI, session.BackendCodex:
		return true
	default:
		return false
	}
}

func (s *Stub) startRuntimeIngest(sessionID session.SessionID, backend session.Backend, runtime sessionRuntime) {
	if s == nil || !runtimeProjectionSupported(backend) {
		return
	}
	if backend == session.BackendPI {
		if record, ok := s.registry.Lookup(sessionID); ok && record.state.Busy() {
			_ = s.setRuntimeAgentRunning(sessionID, true)
		}
		s.startPIRPCStatePolling(sessionID, runtime)
	}
	if runtime.helper != nil {
		go s.readRuntimeHelper(sessionID, backend, runtime.helper)
		return
	}
	if runtime.piAgentGRPC != nil {
		go s.readPIAgentGRPC(sessionID, runtime.piAgentGRPC)
		return
	}
	if runtime.handle == nil {
		return
	}
	for _, src := range runtimeOutputSources(runtime) {
		if src == nil {
			continue
		}
		go s.readRuntimeOutput(sessionID, backend, src)
	}
}

func runtimeOutputSources(runtime sessionRuntime) []io.Reader {
	if runtime.handle == nil {
		return nil
	}
	if pty := runtime.handle.PTY(); pty != nil {
		return []io.Reader{pty}
	}
	sources := make([]io.Reader, 0, 2)
	if stdout := runtime.handle.Stdout(); stdout != nil {
		sources = append(sources, stdout)
	}
	if stderr := runtime.handle.Stderr(); stderr != nil {
		sources = append(sources, stderr)
	}
	return sources
}

func (s *Stub) readRuntimeOutput(sessionID session.SessionID, backend session.Backend, src io.Reader) {
	decoder := runtimeEventDecoder{backend: backend}
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRuntimeLineBytes)
	for scanner.Scan() {
		_ = s.applyRuntimeProjection(sessionID, decoder.decodeRuntimeLine(scanner.Bytes()))
	}
	_ = s.orphanActiveWaits(context.Background(), &sessionID)
}

func (s *Stub) readPIAgentGRPC(sessionID session.SessionID, client *piagentgrpc.Client) {
	if s == nil || client == nil {
		return
	}
	if state, err := client.GetState(context.Background()); err == nil {
		_ = s.applyRuntimeProjection(sessionID, runtimeProjection{piRPCState: piRPCStateSnapshotFromGRPC(state)})
	}
	decoder := runtimeEventDecoder{backend: session.BackendPI}
	_ = client.Subscribe(context.Background(), func(event piagentgrpc.Event) error {
		if event.SessionBoundary != nil {
			if state, err := client.GetState(context.Background()); err == nil {
				_ = s.applyRuntimeProjection(sessionID, runtimeProjection{piRPCState: piRPCStateSnapshotFromGRPC(state)})
			}
			return nil
		}
		projection := decoder.decodeRuntimeLine(event.PayloadJSON)
		return s.applyRuntimeProjection(sessionID, projection)
	})
	_ = s.orphanActiveWaits(context.Background(), &sessionID)
}

func (s *Stub) readRuntimeHelper(sessionID session.SessionID, backend session.Backend, helper *runtimeIODHelper) {
	if s == nil || helper == nil || helper.streamClient == nil {
		return
	}
	for {
		packet, err := helper.streamClient.ReadPacket(context.Background())
		if err != nil {
			return
		}
		if err := s.applyRuntimeHelperPacket(sessionID, backend, packet); err != nil {
			return
		}
	}
}

func (s *Stub) applyRuntimeHelperPacket(sessionID session.SessionID, backend session.Backend, packet any) error {
	if s == nil {
		return nil
	}
	key := struct {
		stub      *Stub
		sessionID session.SessionID
		backend   session.Backend
	}{stub: s, sessionID: sessionID, backend: backend}
	projectorAny, _ := runtimeHelperProjectors.LoadOrStore(key, &runtimeHelperProjector{decoder: runtimeEventDecoder{backend: backend}})
	projector := projectorAny.(*runtimeHelperProjector)
	projector.mu.Lock()
	defer projector.mu.Unlock()
	projection, err := projector.decoder.decodeHelperPacket(packet)
	if err != nil {
		return err
	}
	return s.applyRuntimeProjection(sessionID, projection)
}

func (d *runtimeEventDecoder) decodeHelperPacket(packet any) (runtimeProjection, error) {
	switch v := packet.(type) {
	case iod.StatePacket:
		return d.decodeHelperFact(v.Fact)
	case iod.ReplayItemPacket:
		return d.decodeHelperFact(v.Item.Fact)
	default:
		return runtimeProjection{}, nil
	}
}

func (d *runtimeEventDecoder) decodeHelperFact(fact iod.HelperFact) (runtimeProjection, error) {
	if fact.FactKind != iod.FactOutputDelta {
		return runtimeProjection{}, nil
	}
	var payload iodTerminalOutputPayload
	if err := json.Unmarshal(fact.Payload, &payload); err != nil {
		return runtimeProjection{}, fmt.Errorf("decode helper output payload: %w", err)
	}
	return d.decodeHelperOutput(payload), nil
}

func (d *runtimeEventDecoder) decodeHelperOutput(payload iodTerminalOutputPayload) runtimeProjection {
	if payload.Data == "" || payload.Stream == "stderr" {
		return runtimeProjection{}
	}
	d.helperLines.append(payload.Data)
	projection := runtimeProjection{}

	for {
		line, ok := d.helperLines.nextLine()
		if !ok {
			return projection
		}
		projection = mergeRuntimeProjection(projection, d.decodeRuntimeLine(line))
	}
}

// PI still emits legacy type-tagged JSON objects. Codex app-server emits line-delimited request/response/notification objects.
func (d *runtimeEventDecoder) decodeRuntimeLine(raw []byte) runtimeProjection {
	line := bytes.TrimSpace(raw)
	if len(line) == 0 || line[0] != '{' {
		return runtimeProjection{}
	}
	if d.backend == session.BackendCodex {
		if projection, ok := codex.DecodeAppServerLine(line); ok {
			return runtimeProjectionFromCodex(projection)
		}
	}
	metadata := decodePIRuntimeMetadata(line)
	if metadata.piRPCState != nil || metadata.piRPCStateFailure != nil {
		return metadata
	}
	material, err := pi.ParseObjectJSON(line)
	if err != nil {
		return metadata
	}
	for _, event := range material.Events {
		if event.Kind == pi.EventKindUIRequest && event.UIRequest != nil && event.UIRequest.Kind == pi.UIRequestKindAskUser {
			metadata.waitRequests = append(metadata.waitRequests, event)
			continue
		}
		metadata.events = append(metadata.events, event)
	}
	return metadata
}

func runtimeProjectionFromCodex(projection codex.Projection) runtimeProjection {
	return runtimeProjection{
		events:           projection.Events,
		codexThreadID:    projection.ThreadID,
		codexTurnID:      projection.TurnID,
		clearCodexTurn:   projection.ClearTurn,
		codexBusy:        projection.Busy,
		codexInitialized: projection.Initialized,
		model:            projection.Model,
		contextUsage:     contextUsageFromCodex(projection.Usage),
		turnTiming:       turnTimingFromCodex(projection.Timing),
	}
}

func contextUsageFromCodex(usage *codex.ContextUsage) *SessionContextUsageSnapshot {
	if usage == nil {
		return nil
	}
	return &SessionContextUsageSnapshot{
		UsedTokens:  usage.UsedTokens,
		TotalTokens: usage.TotalTokens,
		PercentUsed: usage.PercentUsed,
	}
}

func turnTimingFromCodex(timing *codex.TurnTiming) *SessionTurnTimingSnapshot {
	if timing == nil {
		return nil
	}
	return &SessionTurnTimingSnapshot{
		StartedTS:   timing.StartedTS,
		LastEventTS: timing.LastEventTS,
	}
}

func decodePIRuntimeMetadata(line []byte) runtimeProjection {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return runtimeProjection{}
	}
	if projection, ok := decodePIRPCStateResponse(raw); ok {
		return projection
	}
	message, _ := raw["message"].(map[string]any)
	if message == nil {
		return runtimeProjection{}
	}
	projection := runtimeProjection{}
	if model, ok := message["model"].(string); ok {
		projection.model = strings.TrimSpace(model)
	}
	if provider, ok := message["provider"].(string); ok {
		projection.provider = strings.TrimSpace(provider)
	}
	if ts := numericValue(message["timestamp"]); ts > 0 {
		typeName := strings.TrimSpace(stringValue(raw["type"]))
		seconds := normalizeRuntimeTimestampSeconds(ts)
		switch typeName {
		case "message_start":
			if role := strings.TrimSpace(stringValue(message["role"])); role == "user" {
				projection.turnTiming = &SessionTurnTimingSnapshot{StartedTS: seconds}
			}
		case "message_update", "message_end", "turn_end":
			projection.turnTiming = &SessionTurnTimingSnapshot{LastEventTS: &seconds}
		}
	}
	usage, _ := message["usage"].(map[string]any)
	if usage == nil {
		return projection
	}
	used := intValueFromAny(usage["totalTokens"])
	if used <= 0 {
		input := intValueFromAny(usage["input"])
		output := intValueFromAny(usage["output"])
		used = input + output
	}
	if used > 0 {
		projection.contextUsage = &SessionContextUsageSnapshot{UsedTokens: &used}
	}
	return projection
}

func decodePIRPCStateResponse(raw map[string]any) (runtimeProjection, bool) {
	if strings.TrimSpace(stringValue(raw["type"])) != "response" || strings.TrimSpace(stringValue(raw["command"])) != "get_state" {
		return runtimeProjection{}, false
	}
	probeID := strings.TrimSpace(stringValue(raw["id"]))
	success, ok := raw["success"].(bool)
	if !ok || !success {
		return runtimeProjection{piRPCStateFailure: &piRPCStateFailure{ProbeID: probeID, Reason: firstNonEmptyString(stringValue(raw["error"]), "get_state failed")}}, true
	}
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		return runtimeProjection{piRPCStateFailure: &piRPCStateFailure{ProbeID: probeID, Reason: "get_state missing data"}}, true
	}
	return runtimeProjection{piRPCState: &piRPCStateSnapshot{
		ProbeID:             probeID,
		IsStreaming:         boolValue(data["isStreaming"]),
		IsCompacting:        boolValue(data["isCompacting"]),
		PendingMessageCount: intValueFromAny(data["pendingMessageCount"]),
		IsBusy:              boolValue(data["isBusy"]),
		BusyReason:          strings.TrimSpace(stringValue(data["busyReason"])),
	}}, true
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		return strings.TrimSpace(value[:idx])
	}
	return value
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func numericValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func intValueFromAny(value any) int {
	v := numericValue(value)
	if v <= 0 {
		return 0
	}
	return int(v + 0.5)
}

func normalizeRuntimeTimestampSeconds(value float64) float64 {
	if value > 9_999_999_999 {
		return value / 1000
	}
	return value
}

func (s *Stub) applyRuntimeProjection(sessionID session.SessionID, projection runtimeProjection) error {
	if projection.codexInitialized {
		s.noteCodexInitialized(sessionID)
	}
	if strings.TrimSpace(projection.codexThreadID) != "" {
		s.noteCodexThreadID(sessionID, projection.codexThreadID)
	}
	if strings.TrimSpace(projection.codexTurnID) != "" {
		s.noteCodexTurnID(sessionID, projection.codexTurnID)
	}
	if projection.clearCodexTurn {
		s.clearCodexTurnID(sessionID, projection.codexTurnID)
	}
	if strings.TrimSpace(projection.model) != "" || strings.TrimSpace(projection.provider) != "" || projection.contextUsage != nil || projection.turnTiming != nil {
		if record, ok, err := s.registry.UpdateRuntimeMetadata(sessionID, projection.model, projection.provider, projection.contextUsage, projection.turnTiming); err == nil && ok {
			if strings.TrimSpace(projection.model) != "" || strings.TrimSpace(projection.provider) != "" {
				s.emitSessionState(record.identity.SessionID())
			}
		}
	}
	if projection.piRPCStateFailure != nil {
		if s.applyPIRPCStateFailure(sessionID, *projection.piRPCStateFailure) {
			return nil
		}
	}
	if projection.piRPCState != nil {
		if err := s.applyPIRPCState(sessionID, *projection.piRPCState); err != nil {
			return err
		}
	}
	if err := s.applyPIEvents(sessionID, projection.events); err != nil {
		return err
	}
	if projection.codexBusy != nil {
		if err := s.applyCodexBusy(sessionID, *projection.codexBusy); err != nil {
			return err
		}
	}
	for _, event := range projection.waitRequests {
		s.startRuntimeAskUserWait(sessionID, event)
	}
	return nil
}

func (b *runtimeLineBuffer) append(chunk string) {
	if b == nil || chunk == "" {
		return
	}
	_, _ = b.pending.WriteString(chunk)
}

func (b *runtimeLineBuffer) nextLine() ([]byte, bool) {
	if b == nil {
		return nil, false
	}
	data := b.pending.Bytes()
	idx := bytes.IndexByte(data, '\n')
	if idx >= 0 {
		line := append([]byte(nil), data[:idx]...)
		b.pending.Next(idx + 1)
		return line, true
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed) {
		line := append([]byte(nil), trimmed...)
		b.pending.Reset()
		return line, true
	}
	return nil, false
}
