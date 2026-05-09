package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"actrail/internal/adapters/iod"
	"actrail/internal/domain/codex"
	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

type iodTerminalOutputPayload struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type runtimeEventDecoder struct {
	backend     session.Backend
	helperLines runtimeLineBuffer
}

type runtimeLineBuffer struct {
	pending bytes.Buffer
}

type runtimeProjection struct {
	events            []pi.Event
	waitRequests      []pi.Event
	codexThreadID     string
	codexSessionPath  string
	codexTurnID       string
	clearCodexTurn    bool
	probeCodexTurn    bool
	codexBusy         *bool
	codexInitialized  bool
	model             string
	provider          string
	contextUsage      *SessionContextUsageSnapshot
	turnTiming        *SessionTurnTimingSnapshot
	piRPCState        *piRPCStateSnapshot
	piRPCStateFailure *piRPCStateFailure
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
	if strings.TrimSpace(src.codexSessionPath) != "" {
		dst.codexSessionPath = strings.TrimSpace(src.codexSessionPath)
	}
	if strings.TrimSpace(src.codexTurnID) != "" {
		dst.codexTurnID = strings.TrimSpace(src.codexTurnID)
	}
	if src.clearCodexTurn {
		dst.clearCodexTurn = true
	}
	if src.probeCodexTurn {
		dst.probeCodexTurn = true
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
		codexSessionPath: projection.SessionPath,
		codexTurnID:      projection.TurnID,
		clearCodexTurn:   projection.ClearTurn,
		probeCodexTurn:   projection.ProbeTurn,
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
