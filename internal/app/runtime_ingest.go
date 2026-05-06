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
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/piagentgrpc"
	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

const (
	maxRuntimeLineBytes        = 1 << 20
	piRPCStateIdlePollInterval = time.Minute
	piRPCStateFastPollInterval = time.Second
	piRPCStateBusyPollInterval = 10 * time.Second
	piRPCStatePollTimeout      = 2 * time.Second
	piRPCStateMaxFailures      = 3
	piRPCBusyHoldDuration      = 30 * time.Second
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
	codexInitialized  bool
	model             string
	provider          string
	contextUsage      *SessionContextUsageSnapshot
	turnTiming        *SessionTurnTimingSnapshot
	piRPCState        *piRPCStateSnapshot
	piRPCStateFailure *piRPCStateFailure
}

// piRPCStateSnapshot is the authoritative busy signal for Pi RPC sessions.
type piRPCStateSnapshot struct {
	ProbeID              string
	IsStreaming          bool
	IsCompacting         bool
	PendingMessageCount  int
	IsBusy               bool
	BusyReason           string
	RuntimeState         piagentgrpc.RuntimeState
	RuntimeStatusMessage string
}

type piRPCStateFailure struct {
	ProbeID string
	Reason  string
}

type piRPCStateCache struct {
	GenerationID         iod.GenerationID
	Polling              bool
	PendingProbeID       string
	LastAckProbeID       string
	ConsecutiveFailures  int
	LastSuccessTS        time.Time
	LastFailureTS        time.Time
	LastState            *piRPCStateSnapshot
	StalledResetRequired bool
	BusyHoldUntil        time.Time
	StartupProbeUntil    time.Time
	IdleHoldUntil        time.Time
	ActiveTurn           bool
	SettlingUntilIdle    bool
	KickSeq              uint64
}

func (s piRPCStateSnapshot) Busy() bool {
	if strings.TrimSpace(s.BusyReason) != "" {
		return s.IsBusy
	}
	return s.IsStreaming || s.IsCompacting || s.PendingMessageCount > 0
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

func (s *Stub) startPIRPCStatePolling(sessionID session.SessionID, runtime sessionRuntime) {
	if s == nil || runtime.protocol != runtimeProtocolPIRPC || runtime.helper == nil {
		return
	}
	generationID := runtime.helper.generationID
	if !s.activatePIRPCStatePoller(sessionID, generationID) {
		return
	}
	go func() {
		defer s.deactivatePIRPCStatePoller(sessionID, generationID)
		s.pollPIRPCState(sessionID, runtime, generationID)
	}()
}

func (s *Stub) pollPIRPCState(sessionID session.SessionID, runtime sessionRuntime, generationID iod.GenerationID) {
	for s.shouldPollPIRPCState(sessionID, runtime, generationID) {
		probeID := fmt.Sprintf("actrail-state-%d", time.Now().UTC().UnixNano())
		kickSeq := s.notePIRPCStateProbeSent(sessionID, generationID, probeID)
		ctx, cancel := context.WithTimeout(context.Background(), piRPCStatePollTimeout)
		err := runtime.RequestPIRPCState(ctx, probeID)
		cancel()
		if err != nil {
			if s.recordPIRPCStateTransportFailure(sessionID, generationID, err.Error()) {
				return
			}
			if s.recordPIRPCStateProbeFailure(sessionID, generationID, probeID, err.Error()) {
				return
			}
			continue
		}
		if !s.waitPIRPCStateProbeAck(sessionID, generationID, probeID) {
			if s.recordPIRPCStateProbeFailure(sessionID, generationID, probeID, "get_state timeout") {
				return
			}
			continue
		}
		if !s.waitPIRPCStatePollInterval(sessionID, generationID, kickSeq) {
			return
		}
	}
}

func (s *Stub) waitPIRPCStateProbeAck(sessionID session.SessionID, generationID iod.GenerationID, probeID string) bool {
	deadline := time.Now().UTC().Add(piRPCStatePollTimeout)
	for {
		if s.piRPCStateProbeAcked(sessionID, generationID, probeID) {
			return true
		}
		if time.Now().UTC().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *Stub) waitPIRPCStatePollInterval(sessionID session.SessionID, generationID iod.GenerationID, kickSeq uint64) bool {
	deadline := time.Now().UTC().Add(s.nextPIRPCStatePollInterval(sessionID, generationID))
	for {
		if s.piRPCStatePollKicked(sessionID, generationID, kickSeq) {
			return true
		}
		if !s.shouldPollPIRPCState(sessionID, sessionRuntime{protocol: runtimeProtocolPIRPC, helper: &runtimeIODHelper{generationID: generationID}}, generationID) {
			return false
		}
		if time.Now().UTC().After(deadline) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *Stub) nextPIRPCStatePollInterval(sessionID session.SessionID, generationID iod.GenerationID) time.Duration {
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	cache := s.piRPCStates[sessionID]
	if cache.GenerationID != generationID {
		return piRPCStateIdlePollInterval
	}
	if cache.PendingProbeID != "" || cache.ActiveTurn || cache.SettlingUntilIdle {
		return piRPCStateFastPollInterval
	}
	if cache.LastState != nil && cache.LastState.Busy() {
		return piRPCStateBusyPollInterval
	}
	return piRPCStateIdlePollInterval
}

func (s *Stub) shouldPollPIRPCState(sessionID session.SessionID, runtime sessionRuntime, generationID iod.GenerationID) bool {
	if s == nil || runtime.protocol != runtimeProtocolPIRPC || runtime.helper == nil || runtime.helper.generationID != generationID {
		return false
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendPI || record.runtime.helper == nil || record.runtime.helper.generationID != generationID {
		return false
	}
	transport := sessionTransportSnapshot(record)
	return !transport.ResetRequired && transport.State != SessionTransportStateBroken && transport.State != SessionTransportStateEnded
}

func (s *Stub) activatePIRPCStatePoller(sessionID session.SessionID, generationID iod.GenerationID) bool {
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	if s.piRPCStates == nil {
		s.piRPCStates = map[session.SessionID]piRPCStateCache{}
	}
	cache := s.piRPCStates[sessionID]
	if cache.Polling && cache.GenerationID == generationID && !cache.StalledResetRequired {
		cache.KickSeq++
		s.piRPCStates[sessionID] = cache
		return false
	}
	if cache.GenerationID != generationID {
		cache = piRPCStateCache{GenerationID: generationID}
	}
	cache.Polling = true
	cache.PendingProbeID = ""
	cache.StalledResetRequired = false
	s.piRPCStates[sessionID] = cache
	return true
}

func (s *Stub) deactivatePIRPCStatePoller(sessionID session.SessionID, generationID iod.GenerationID) {
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	cache := s.piRPCStates[sessionID]
	if cache.GenerationID != generationID {
		return
	}
	cache.Polling = false
	s.piRPCStates[sessionID] = cache
}

func (s *Stub) notePIRPCStateProbeSent(sessionID session.SessionID, generationID iod.GenerationID, probeID string) uint64 {
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	cache := s.piRPCStates[sessionID]
	if cache.GenerationID != generationID {
		cache = piRPCStateCache{GenerationID: generationID, Polling: true}
	}
	cache.PendingProbeID = probeID
	s.piRPCStates[sessionID] = cache
	return cache.KickSeq
}

func (s *Stub) piRPCStatePollKicked(sessionID session.SessionID, generationID iod.GenerationID, kickSeq uint64) bool {
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	cache := s.piRPCStates[sessionID]
	return cache.GenerationID == generationID && cache.KickSeq != kickSeq
}

func (s *Stub) piRPCStateProbeAcked(sessionID session.SessionID, generationID iod.GenerationID, probeID string) bool {
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	cache := s.piRPCStates[sessionID]
	return cache.GenerationID == generationID && cache.LastAckProbeID == probeID && cache.PendingProbeID == ""
}

func (s *Stub) recordPIRPCStateProbeFailure(sessionID session.SessionID, generationID iod.GenerationID, probeID, reason string) bool {
	s.piRPCStateMu.Lock()
	cache := s.piRPCStates[sessionID]
	if cache.GenerationID == "" {
		cache.GenerationID = generationID
	}
	if cache.GenerationID != generationID || cache.StalledResetRequired {
		s.piRPCStateMu.Unlock()
		return cache.StalledResetRequired
	}
	if probeID != "" && cache.LastAckProbeID == probeID {
		s.piRPCStateMu.Unlock()
		return false
	}
	cache.PendingProbeID = ""
	cache.ConsecutiveFailures++
	cache.LastFailureTS = time.Now().UTC()
	stalled := cache.ConsecutiveFailures >= piRPCStateMaxFailures
	s.piRPCStates[sessionID] = cache
	s.piRPCStateMu.Unlock()
	if stalled && !isPIRPCStateProbeTimeout(reason) {
		_ = s.emitPIRPCStateProbeWarning(sessionID, generationID, reason)
	}
	return false
}

func (s *Stub) recordPIRPCStateTransportFailure(sessionID session.SessionID, generationID iod.GenerationID, reason string) bool {
	if !isPIRPCControlSocketFailure(reason) {
		return false
	}
	s.piRPCStateMu.Lock()
	cache := s.piRPCStates[sessionID]
	if cache.GenerationID == "" {
		cache.GenerationID = generationID
	}
	if cache.GenerationID != generationID {
		s.piRPCStateMu.Unlock()
		return true
	}
	cache.PendingProbeID = ""
	cache.StalledResetRequired = true
	cache.LastFailureTS = time.Now().UTC()
	s.piRPCStates[sessionID] = cache
	s.piRPCStateMu.Unlock()
	_ = s.markSessionTransportResetRequired(sessionID, generationID, reason)
	return true
}

func isPIRPCControlSocketFailure(reason string) bool {
	text := strings.TrimSpace(reason)
	return strings.Contains(text, "dial iod control socket") || strings.Contains(text, "connect: no such file or directory")
}

func isPIRPCStateProbeTimeout(reason string) bool {
	return strings.TrimSpace(reason) == "get_state timeout"
}

func (s *Stub) recordPIRPCStateSuccess(sessionID session.SessionID, state piRPCStateSnapshot) {
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	cache := s.piRPCStates[sessionID]
	if cache.StalledResetRequired {
		return
	}
	cache.PendingProbeID = ""
	cache.LastAckProbeID = state.ProbeID
	cache.ConsecutiveFailures = 0
	cache.LastSuccessTS = time.Now().UTC()
	cache.LastState = &state
	s.piRPCStates[sessionID] = cache
}

func (s *Stub) holdPIRPCBusy(sessionID session.SessionID, generationID iod.GenerationID) {
	if s == nil || generationID == "" {
		return
	}
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	if s.piRPCStates == nil {
		s.piRPCStates = map[session.SessionID]piRPCStateCache{}
	}
	cache := s.piRPCStates[sessionID]
	if cache.GenerationID != "" && cache.GenerationID != generationID {
		cache = piRPCStateCache{GenerationID: generationID}
	}
	cache.GenerationID = generationID
	cache.ActiveTurn = true
	cache.SettlingUntilIdle = false
	until := time.Now().UTC().Add(piRPCBusyHoldDuration)
	if cache.BusyHoldUntil.Before(until) {
		cache.BusyHoldUntil = until
	}
	cache.IdleHoldUntil = time.Time{}
	s.piRPCStates[sessionID] = cache
}

func (s *Stub) kickPIRPCStateProbe(sessionID session.SessionID, generationID iod.GenerationID) {
	if s == nil || generationID == "" {
		return
	}
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	if s.piRPCStates == nil {
		s.piRPCStates = map[session.SessionID]piRPCStateCache{}
	}
	cache := s.piRPCStates[sessionID]
	if cache.GenerationID != "" && cache.GenerationID != generationID {
		cache = piRPCStateCache{GenerationID: generationID}
	}
	cache.GenerationID = generationID
	cache.KickSeq++
	s.piRPCStates[sessionID] = cache
}

func (s *Stub) piRPCBusyHeld(sessionID session.SessionID, generationID iod.GenerationID) bool {
	if s == nil || generationID == "" {
		return false
	}
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	cache := s.piRPCStates[sessionID]
	return cache.GenerationID == generationID && time.Now().UTC().Before(cache.BusyHoldUntil)
}

func (s *Stub) piRPCActiveTurn(sessionID session.SessionID, generationID iod.GenerationID) bool {
	if s == nil || generationID == "" {
		return false
	}
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	cache := s.piRPCStates[sessionID]
	return cache.GenerationID == generationID && cache.ActiveTurn
}

func (s *Stub) holdPIRPCIdle(sessionID session.SessionID, generationID iod.GenerationID) {
	if s == nil || generationID == "" {
		return
	}
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	if s.piRPCStates == nil {
		s.piRPCStates = map[session.SessionID]piRPCStateCache{}
	}
	cache := s.piRPCStates[sessionID]
	if cache.GenerationID != "" && cache.GenerationID != generationID {
		cache = piRPCStateCache{GenerationID: generationID}
	}
	cache.GenerationID = generationID
	cache.ActiveTurn = false
	cache.StartupProbeUntil = time.Time{}
	cache.BusyHoldUntil = time.Time{}
	cache.SettlingUntilIdle = true
	cache.KickSeq++
	s.piRPCStates[sessionID] = cache
}

func (s *Stub) piRPCIdleHeld(sessionID session.SessionID, generationID iod.GenerationID) bool {
	if s == nil || generationID == "" {
		return false
	}
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	cache := s.piRPCStates[sessionID]
	return cache.GenerationID == generationID && cache.SettlingUntilIdle
}

func (s *Stub) finishPIRPCSettling(sessionID session.SessionID, generationID iod.GenerationID) {
	if s == nil || generationID == "" {
		return
	}
	s.piRPCStateMu.Lock()
	defer s.piRPCStateMu.Unlock()
	cache := s.piRPCStates[sessionID]
	if cache.GenerationID != generationID {
		return
	}
	cache.SettlingUntilIdle = false
	cache.IdleHoldUntil = time.Time{}
	s.piRPCStates[sessionID] = cache
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

func piRPCStateSnapshotFromGRPC(state piagentgrpc.State) *piRPCStateSnapshot {
	return &piRPCStateSnapshot{
		ProbeID:              fmt.Sprintf("pi-agent-grpc-%d", time.Now().UTC().UnixNano()),
		IsStreaming:          state.IsStreaming,
		IsCompacting:         state.IsCompacting,
		PendingMessageCount:  state.PendingMessageCount,
		IsBusy:               state.Busy(),
		BusyReason:           state.BusyStateReason(),
		RuntimeState:         state.RuntimeState,
		RuntimeStatusMessage: state.RuntimeMessage(),
	}
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
		if projection, ok := decodeCodexAppServerLine(line); ok {
			return projection
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

type codexAppServerLine struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

type codexThreadEnvelope struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type codexTurnEnvelope struct {
	Turn struct {
		ID     string `json:"id"`
		Status any    `json:"status"`
		Error  any    `json:"error"`
	} `json:"turn"`
}

type codexAgentMessageDeltaParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type codexItemNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Item     struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"item"`
}

func decodeCodexAppServerLine(raw []byte) (runtimeProjection, bool) {
	var line codexAppServerLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return runtimeProjection{}, false
	}
	if strings.TrimSpace(line.Method) != "" {
		switch strings.TrimSpace(line.Method) {
		case "thread/started":
			var params codexThreadEnvelope
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return runtimeProjection{}, true
			}
			return runtimeProjection{codexThreadID: strings.TrimSpace(params.Thread.ID)}, true
		case "turn/started":
			var params codexTurnEnvelope
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return runtimeProjection{}, true
			}
			turnID := strings.TrimSpace(params.Turn.ID)
			return runtimeProjection{
				codexTurnID: turnID,
				events:      []pi.Event{{Kind: pi.EventKindBoundary, RawType: line.Method, TurnID: turnID, Boundary: &pi.Boundary{Kind: pi.BoundaryKindTurnStarted}}},
			}, true
		case "turn/completed":
			var params codexTurnEnvelope
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return runtimeProjection{}, true
			}
			turnID := strings.TrimSpace(params.Turn.ID)
			return runtimeProjection{
				clearCodexTurn: true,
				codexTurnID:    turnID,
				events:         []pi.Event{{Kind: pi.EventKindBoundary, RawType: line.Method, TurnID: turnID, Boundary: &pi.Boundary{Kind: pi.BoundaryKindTurnCompleted}}},
			}, true
		case "item/agentMessage/delta":
			var params codexAgentMessageDeltaParams
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return runtimeProjection{}, true
			}
			if strings.TrimSpace(params.Delta) == "" {
				return runtimeProjection{}, true
			}
			return runtimeProjection{events: []pi.Event{{
				Kind:    pi.EventKindMessageDelta,
				RawType: line.Method,
				RawID:   strings.TrimSpace(params.ItemID),
				TurnID:  strings.TrimSpace(params.TurnID),
				Delta:   &pi.MessageDelta{Role: pi.MessageRoleAssistant, Text: params.Delta},
			}}}, true
		case "item/completed":
			var params codexItemNotification
			if err := json.Unmarshal(line.Params, &params); err != nil {
				return runtimeProjection{}, true
			}
			if strings.TrimSpace(params.Item.Type) != "agentMessage" || strings.TrimSpace(params.Item.Text) == "" {
				return runtimeProjection{}, true
			}
			return runtimeProjection{events: []pi.Event{{
				Kind:    pi.EventKindMessage,
				RawType: line.Method,
				RawID:   strings.TrimSpace(params.Item.ID),
				TurnID:  strings.TrimSpace(params.TurnID),
				Message: &pi.Message{ID: strings.TrimSpace(params.Item.ID), Role: pi.MessageRoleAssistant, Text: params.Item.Text, Class: pi.MessageClassCommitted, CommitLike: true},
			}}}, true
		default:
			return runtimeProjection{}, true
		}
	}
	if len(line.Result) > 0 && string(line.Result) != "null" {
		var thread codexThreadEnvelope
		if err := json.Unmarshal(line.Result, &thread); err == nil && strings.TrimSpace(thread.Thread.ID) != "" {
			return runtimeProjection{codexThreadID: strings.TrimSpace(thread.Thread.ID)}, true
		}
		var turn codexTurnEnvelope
		if err := json.Unmarshal(line.Result, &turn); err == nil && strings.TrimSpace(turn.Turn.ID) != "" {
			return runtimeProjection{codexTurnID: strings.TrimSpace(turn.Turn.ID)}, true
		}
		return runtimeProjection{codexInitialized: true}, true
	}
	return runtimeProjection{}, false
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
	for _, event := range projection.waitRequests {
		s.startRuntimeAskUserWait(sessionID, event)
	}
	return nil
}

func (s *Stub) applyPIRPCStateFailure(sessionID session.SessionID, failure piRPCStateFailure) bool {
	if s == nil {
		return false
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendPI || record.runtime.helper == nil {
		return false
	}
	return s.recordPIRPCStateProbeFailure(sessionID, record.runtime.helper.generationID, failure.ProbeID, failure.Reason)
}

func (s *Stub) applyPIRPCState(sessionID session.SessionID, state piRPCStateSnapshot) error {
	if s == nil {
		return nil
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return nil
	}
	transport := sessionTransportSnapshot(record)
	if record.identity.Backend() == session.BackendPI && record.runtime.piAgentGRPC != nil {
		if state.RuntimeState == piagentgrpc.RuntimeStateStarting {
			if _, err := s.setSessionTransport(sessionID, transportSnapshotPIAgentGRPCStarting()); err != nil {
				return err
			}
			s.emitSessionState(sessionID)
			return nil
		}
		if state.RuntimeState == piagentgrpc.RuntimeStateFailed {
			if _, err := s.setSessionTransport(sessionID, transportSnapshotPIAgentGRPCFailed(firstNonEmptyString(state.RuntimeStatusMessage, "runtime failed"))); err != nil {
				return err
			}
			_ = s.setRuntimeAgentRunning(sessionID, false)
			s.emitSessionState(sessionID)
			return nil
		}
		if transport.State == SessionTransportStateStarting || transport.State == SessionTransportStateFailed {
			if _, err := s.setSessionTransport(sessionID, transportSnapshotPIAgentGRPCAttached()); err != nil {
				return err
			}
		}
	}
	if record.identity.Backend() == session.BackendPI && transport.ResetRequired {
		if !isPIRPCStateProbeTransportIssue(transport) {
			return nil
		}
		if record.runtime.helper != nil {
			if _, err := s.setSessionTransport(sessionID, transportSnapshotAttached(record.runtime.helper.generationID)); err != nil {
				return err
			}
		}
	}
	s.recordPIRPCStateSuccess(sessionID, state)
	busy := state.Busy()
	if record.identity.Backend() == session.BackendPI && record.runtime.protocol == runtimeProtocolPIRPC && record.runtime.helper != nil {
		generationID := record.runtime.helper.generationID
		if !busy && (s.piRPCActiveTurn(sessionID, generationID) || s.piRPCBusyHeld(sessionID, generationID)) {
			return nil
		}
		if busy && !s.isRuntimeAgentRunning(sessionID) && s.piRPCIdleHeld(sessionID, generationID) {
			return nil
		}
		if !busy {
			s.finishPIRPCSettling(sessionID, generationID)
		}
	}
	if err := s.setRuntimeAgentRunning(sessionID, busy); err != nil {
		return err
	}
	record, ok = s.registry.Lookup(sessionID)
	if !ok {
		return nil
	}
	if record.state.Busy() == busy {
		return nil
	}
	updated, ok, err := s.registry.SetBusy(sessionID, busy)
	if err != nil || !ok {
		return err
	}
	s.emitSessionState(sessionID)
	if !updated.Busy() {
		s.scheduleQueuedDispatch(sessionID)
	}
	return nil
}

func (s *Stub) emitPIRPCStateProbeWarning(sessionID session.SessionID, generationID iod.GenerationID, reason string) error {
	resolvedReason := firstNonEmptyString(reason, "get_state failed")
	committed, err := s.AppendSessionMessage(sessionID, "system", "pi_event", "Pi RPC state probe failed: "+resolvedReason)
	if err != nil {
		return err
	}
	committed.Role = ""
	committed.Type = "pi_event"
	committed.Summary = "Pi RPC state probe failed"
	committed.Details = map[string]any{
		"raw_type":      "actrail_state_probe_failure",
		"generation_id": strings.TrimSpace(generationID.String()),
		"reason":        resolvedReason,
	}
	s.emitMessageCommit(sessionID, "", committed)
	return nil
}

func isPIRPCStateProbeTransportIssue(transport SessionTransportSnapshot) bool {
	return transport.State == SessionTransportStateStalled && isRecoverableTransportProbeIssue(transport)
}

func (s *Stub) markPIRPCStateStalled(sessionID session.SessionID, generationID iod.GenerationID, reason string) error {
	if s == nil {
		return nil
	}
	resolvedReason := firstNonEmptyString(reason, "get_state failed")
	updated, err := s.setSessionTransport(sessionID, SessionTransportSnapshot{
		GenerationID:  strings.TrimSpace(generationID.String()),
		State:         SessionTransportStateStalled,
		ResetRequired: true,
		Reason:        resolvedReason,
	})
	if err != nil {
		return err
	}
	if updated.GenerationID != "" {
		s.emitTransportResetRequired(sessionID, updated.GenerationID, updated.Reason)
	}
	if err := s.setRuntimeAgentRunning(sessionID, false); err != nil {
		return err
	}
	if state, ok, err := s.registry.SetBusy(sessionID, false); err != nil {
		return err
	} else if ok && !state.Busy() {
		s.emitSessionState(sessionID)
	}
	return nil
}

func (s *Stub) withCodexRuntimeState(sessionID session.SessionID, apply func(*codexRuntimeState)) {
	if s == nil || apply == nil {
		return
	}
	_, _, err := s.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = s.runtimeForSession(record.identity.SessionID(), record.identity.Backend(), record.runtime)
		if record.runtime.codex != nil {
			apply(record.runtime.codex)
		}
		return nil
	})
	if err != nil {
		return
	}
}

func (s *Stub) noteCodexInitialized(sessionID session.SessionID) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.markInitialized()
	})
}

func (s *Stub) noteCodexThreadID(sessionID session.SessionID, threadID string) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.setThreadID(threadID)
	})
}

func (s *Stub) noteCodexTurnID(sessionID session.SessionID, turnID string) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.setActiveTurnID(turnID)
	})
}

func (s *Stub) clearCodexTurnID(sessionID session.SessionID, turnID string) {
	s.withCodexRuntimeState(sessionID, func(state *codexRuntimeState) {
		state.clearActiveTurnID(turnID)
	})
}

func (s *Stub) applyPIEvents(sessionID session.SessionID, events []pi.Event) error {
	for _, event := range events {
		if err := s.applyPIEvent(sessionID, event); err != nil {
			return err
		}
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

func (s *Stub) applyPIEvent(sessionID session.SessionID, event pi.Event) error {
	s.messageCache.Invalidate(sessionID)
	if event.Kind == pi.EventKindMessageDelta || event.Kind == pi.EventKindTool || event.Kind == pi.EventKindUIRequest || (event.Kind == pi.EventKindMessage && event.Message != nil && event.Message.Role == pi.MessageRoleAssistant && event.Message.ToolCallCount > 0) {
		if err := s.markRuntimeActiveFromPIEvent(sessionID); err != nil {
			return err
		}
	}
	switch event.Kind {
	case pi.EventKindMessageDelta:
		return s.applyPIDelta(sessionID, event)
	case pi.EventKindMessage:
		return s.applyPIMessage(sessionID, event)
	case pi.EventKindTool:
		return s.applyPITool(sessionID, event)
	case pi.EventKindError:
		return s.applyPIError(sessionID, event)
	case pi.EventKindUIRequest:
		return s.applyPIUIRequest(sessionID, event)
	case pi.EventKindUIResolved:
		return s.applyPIUIResolved(sessionID, event)
	case pi.EventKindBoundary:
		return s.applyPIBoundary(sessionID, event)
	}
	return nil
}

func (s *Stub) markRuntimeActiveFromPIEvent(sessionID session.SessionID) error {
	record, ok := s.registry.Lookup(sessionID)
	if !ok || record.identity.Backend() != session.BackendPI {
		return nil
	}
	if record.runtime.protocol == runtimeProtocolPIRPC && record.runtime.helper != nil {
		s.holdPIRPCBusy(sessionID, record.runtime.helper.generationID)
		s.kickPIRPCStateProbe(sessionID, record.runtime.helper.generationID)
	}
	if err := s.setRuntimeAgentRunning(sessionID, true); err != nil {
		return err
	}
	if record.state.Busy() {
		return nil
	}
	if _, _, err := s.registry.SetBusy(sessionID, true); err != nil {
		return err
	}
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) applyPIDelta(sessionID session.SessionID, event pi.Event) error {
	if event.Delta == nil {
		return nil
	}
	turnID := runtimeTurnID(event)
	if turnID == "" {
		return nil
	}
	if _, err := s.AppendAssistantDelta(sessionID, turnID, event.Delta.Text); err != nil {
		return err
	}
	s.emitMessageDelta(sessionID, turnID, string(event.Delta.Role), event.Delta.Text)
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) applyPIMessage(sessionID session.SessionID, event pi.Event) error {
	if event.Message == nil || strings.TrimSpace(event.Message.Text) == "" {
		return nil
	}
	if strings.TrimSpace(event.Message.StopReason) == "status" {
		committed, err := s.AppendSessionMessage(sessionID, "system", "pi_event", event.Message.Text)
		if err != nil {
			return err
		}
		committed.Role = ""
		committed.Type = "pi_event"
		committed.EventID = piMessageEventID(event)
		committed.ParentEventID = piParentEventID(event)
		committed.Details = map[string]any{
			"raw_type": strings.TrimSpace(event.RawType),
			"status":   true,
		}
		if event.Compaction != nil {
			committed.Details["compaction"] = compactionEventDetails(*event.Compaction)
			committed.Summary = compactionEventSummary(*event.Compaction)
		}
		s.emitMessageCommit(sessionID, runtimeTurnID(event), committed)
		s.emitSessionState(sessionID)
		return nil
	}
	role := strings.TrimSpace(string(event.Message.Role))
	if role == "" {
		return nil
	}
	if event.Message.Role == pi.MessageRoleUser {
		committed, err := s.AppendSessionMessage(sessionID, role, "message", event.Message.Text)
		if err != nil {
			return err
		}
		committed.EventID = piMessageEventID(event)
		committed.ParentEventID = piParentEventID(event)
		s.emitMessageCommit(sessionID, runtimeTurnID(event), committed)
		s.emitSessionState(sessionID)
		return nil
	}
	if !event.Message.CommitLike {
		if event.Message.Role != pi.MessageRoleAssistant {
			return nil
		}
		turnID := runtimeTurnID(event)
		if turnID == "" {
			return nil
		}
		if _, err := s.AppendAssistantDelta(sessionID, turnID, event.Message.Text); err != nil {
			return err
		}
		s.emitMessageDelta(sessionID, turnID, role, event.Message.Text)
		s.emitSessionState(sessionID)
		return nil
	}

	turnID := runtimeTurnID(event)
	committed, committedNew, err := s.commitRuntimeMessage(sessionID, turnID, role, event.Message.Text)
	if err != nil {
		return err
	}
	if event.Message.Role == pi.MessageRoleAssistant && event.Message.CommitLike && strings.TrimSpace(event.Message.StopReason) != "status" && event.Message.ToolCallCount == 0 {
		if record, ok := s.registry.Lookup(sessionID); ok && record.identity.Backend() == session.BackendPI {
			if record.runtime.protocol == runtimeProtocolPIRPC && record.runtime.helper != nil {
				s.holdPIRPCIdle(sessionID, record.runtime.helper.generationID)
			}
			if err := s.setRuntimeAgentRunning(sessionID, false); err != nil {
				return err
			}
			if _, _, err := s.registry.SetBusy(sessionID, false); err != nil {
				return err
			}
		} else if err := s.setRuntimeAgentRunning(sessionID, false); err != nil {
			return err
		}
	}
	if !committedNew {
		s.emitSessionState(sessionID)
		return nil
	}
	committed.EventID = piMessageEventID(event)
	committed.ParentEventID = piParentEventID(event)
	s.emitMessageCommit(sessionID, turnID, committed)
	s.emitAssistantFinalNotification(sessionID, committed)
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) emitAssistantFinalNotification(sessionID session.SessionID, msg SessionMessage) {
	if msg.Role != "assistant" || strings.TrimSpace(msg.Text) == "" {
		return
	}
	title := "Session"
	if record, ok := s.registry.Lookup(sessionID); ok {
		title = firstNonEmptyString(record.alias, record.title, record.cwd, sessionID.String())
	}
	messageID := strings.TrimSpace(msg.EventID)
	if messageID == "" && msg.Seq > 0 {
		messageID = fmt.Sprintf("%s:%d", sessionID, msg.Seq)
	}
	s.emitNotification(NotificationEvent{SessionID: sessionID.String(), Title: title, Body: msg.Text, MessageID: messageID, Kind: "assistant_final"})
}

func (s *Stub) commitRuntimeMessage(sessionID session.SessionID, turnID, role, text string) (SessionMessage, bool, error) {
	record, err := s.lookupSession(sessionID)
	if err != nil {
		return SessionMessage{}, false, err
	}
	if partial, ok := record.transcript.PartialAssistantTurn(); ok {
		resolvedTurnID := strings.TrimSpace(turnID)
		if resolvedTurnID == "" {
			resolvedTurnID = partial.TurnID().String()
		}
		if partial.TurnID().String() == resolvedTurnID {
			msg, err := s.CommitAssistantTurn(sessionID, resolvedTurnID, text)
			return msg, true, err
		}
	}
	trimmedText := strings.TrimSpace(text)
	if role == "assistant" && trimmedText != "" {
		items := record.transcript.Items()
		if len(items) > 0 {
			last := items[len(items)-1]
			if last.Role().String() == role && last.Kind().String() == "message" && strings.TrimSpace(last.Text()) == trimmedText {
				return sessionMessageFromCommitted(last), false, nil
			}
		}
	}
	msg, err := s.AppendSessionMessage(sessionID, role, "message", text)
	return msg, true, err
}

func (s *Stub) applyPITool(sessionID session.SessionID, event pi.Event) error {
	if event.Tool == nil {
		return nil
	}
	kind := "tool"
	if event.Tool.Result {
		kind = "tool_result"
	}
	text := strings.TrimSpace(event.Tool.Text)
	if text == "" {
		text = strings.TrimSpace(event.Tool.Name)
	}
	if text == "" {
		text = kind
	}
	committed, err := s.AppendSessionMessage(sessionID, "system", kind, text)
	if err != nil {
		return err
	}
	committed.Role = ""
	committed.Type = kind
	committed.EventID = piToolEventID(event)
	committed.ParentEventID = piParentEventID(event)
	committed.Name = strings.TrimSpace(event.Tool.Name)
	committed.Summary = strings.TrimSpace(event.Tool.Name)
	committed.ToolCallID = strings.TrimSpace(event.Tool.CallID)
	committed.IsError = event.Tool.IsError
	committed.Details = map[string]any{}
	if committed.Name != "" {
		committed.Details["name"] = committed.Name
	}
	if committed.ToolCallID != "" {
		committed.Details["tool_call_id"] = committed.ToolCallID
	}
	if len(event.Tool.Arguments) > 0 {
		committed.Details["arguments"] = event.Tool.Arguments
	}
	s.emitMessageCommit(sessionID, runtimeTurnID(event), committed)
	s.emitSessionState(sessionID)
	return nil
}

func compactionEventSummary(event pi.CompactionEvent) string {
	if event.Phase == "start" {
		return "Compaction started"
	}
	if event.ErrorMessage != "" {
		return "Compaction failed"
	}
	if event.Aborted {
		return "Compaction aborted"
	}
	if event.WillRetry {
		return "Compaction ended, retrying"
	}
	return "Compaction ended"
}

func compactionEventDetails(event pi.CompactionEvent) map[string]any {
	details := map[string]any{
		"phase":     event.Phase,
		"reason":    event.Reason,
		"aborted":   event.Aborted,
		"willRetry": event.WillRetry,
	}
	if event.InputTokens > 0 {
		details["inputTokens"] = event.InputTokens
	}
	if event.InputTokensK > 0 {
		details["inputTokensK"] = event.InputTokensK
	}
	if event.TokensBefore > 0 {
		details["tokensBefore"] = event.TokensBefore
	}
	if event.TokensAfter > 0 {
		details["tokensAfter"] = event.TokensAfter
	}
	if event.TokensAfterK > 0 {
		details["tokensAfterK"] = event.TokensAfterK
	}
	if event.DurationMS > 0 {
		details["durationMs"] = event.DurationMS
	}
	if event.ErrorMessage != "" {
		details["errorMessage"] = event.ErrorMessage
	}
	if event.Model != nil {
		details["model"] = event.Model
	}
	if event.Result != nil {
		details["result"] = event.Result
	}
	return details
}

func (s *Stub) applyPIError(sessionID session.SessionID, event pi.Event) error {
	if event.Error == nil || strings.TrimSpace(event.Error.Message) == "" {
		return nil
	}
	committed, err := s.AppendSessionMessage(sessionID, "system", "error", event.Error.Message)
	if err != nil {
		return err
	}
	committed.Type = "error"
	committed.IsError = true
	committed.Details = map[string]any{
		"source":      strings.TrimSpace(event.Error.Source),
		"stop_reason": strings.TrimSpace(event.Error.StopReason),
	}
	s.emitMessageCommit(sessionID, runtimeTurnID(event), committed)
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) applyPIUIRequest(sessionID session.SessionID, event pi.Event) error {
	if event.UIRequest == nil {
		return nil
	}
	snapshot := SessionUIRequestSnapshot{
		RequestID:     strings.TrimSpace(event.UIRequest.RequestID),
		Kind:          strings.TrimSpace(string(event.UIRequest.Kind)),
		Method:        strings.TrimSpace(string(event.UIRequest.Method)),
		Title:         strings.TrimSpace(event.UIRequest.Title),
		Message:       strings.TrimSpace(event.UIRequest.Message),
		Prompt:        runtimeUIPrompt(*event.UIRequest),
		Question:      strings.TrimSpace(event.UIRequest.Prompt),
		Context:       strings.TrimSpace(event.UIRequest.Context),
		AllowFreeform: event.UIRequest.AllowFreeform,
		AllowMultiple: event.UIRequest.AllowMultiple,
		Options:       runtimeUIOptionsSnapshot(*event.UIRequest),
		Questions:     runtimeUIQuestionsSnapshot(*event.UIRequest),
		Metadata:      copyAnyMap(event.UIRequest.Metadata),
	}
	if err := s.SetSessionUIRequest(sessionID, snapshot); err != nil {
		return err
	}
	s.emitUIRequest(UIRequestEvent{SessionID: sessionID, Request: snapshot})
	s.emitSessionState(sessionID)
	return nil
}

func (s *Stub) applyPIUIResolved(sessionID session.SessionID, event pi.Event) error {
	if event.UIResolved == nil {
		return nil
	}
	requestID := strings.TrimSpace(event.UIResolved.RequestID)
	if requestID == "" {
		return nil
	}
	if err := s.ClearSessionUIRequest(sessionID, requestID); err != nil {
		return err
	}
	s.emitUIResolved(sessionID, requestID)
	s.emitSessionState(sessionID)
	s.scheduleQueuedDispatch(sessionID)
	return nil
}

func (s *Stub) applyPIBoundary(sessionID session.SessionID, event pi.Event) error {
	if event.Boundary == nil {
		return nil
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return nil
	}
	piRPCSession := record.identity.Backend() == session.BackendPI && record.runtime.protocol == runtimeProtocolPIRPC
	if record.identity.Backend() == session.BackendPI && !piRPCSession {
		return nil
	}
	switch event.Boundary.Kind {
	case pi.BoundaryKindAgentStarted:
		if piRPCSession && record.runtime.helper != nil {
			s.holdPIRPCBusy(sessionID, record.runtime.helper.generationID)
		}
		if err := s.setRuntimeAgentRunning(sessionID, true); err != nil {
			return err
		}
		if _, _, err := s.registry.SetBusy(sessionID, true); err != nil {
			return err
		}
		s.emitSessionState(sessionID)
	case pi.BoundaryKindAgentCompleted:
		if piRPCSession && record.runtime.helper != nil {
			s.holdPIRPCIdle(sessionID, record.runtime.helper.generationID)
		}
		if err := s.setRuntimeAgentRunning(sessionID, false); err != nil {
			return err
		}
		state, ok, err := s.registry.SetBusy(sessionID, false)
		if err != nil {
			return err
		}
		if ok {
			s.emitSessionState(sessionID)
			if !state.Busy() {
				s.scheduleQueuedDispatch(sessionID)
			}
		}
	case pi.BoundaryKindTurnStarted:
		if piRPCSession {
			if record.runtime.helper != nil {
				s.holdPIRPCBusy(sessionID, record.runtime.helper.generationID)
			}
			if err := s.setRuntimeAgentRunning(sessionID, true); err != nil {
				return err
			}
		}
		if _, _, err := s.registry.SetBusy(sessionID, true); err != nil {
			return err
		}
		s.emitSessionState(sessionID)
	case pi.BoundaryKindTurnCompleted, pi.BoundaryKindTurnAborted:
		if event.Boundary.Kind == pi.BoundaryKindTurnCompleted && !event.Boundary.CommitLike && event.Boundary.Reason != "turn_end" {
			return nil
		}
		if piRPCSession {
			if record.runtime.helper != nil {
				s.holdPIRPCIdle(sessionID, record.runtime.helper.generationID)
			}
			if err := s.setRuntimeAgentRunning(sessionID, false); err != nil {
				return err
			}
			if _, _, err := s.registry.DiscardPartialAssistantTurn(sessionID); err != nil {
				return err
			}
			state, ok, err := s.registry.SetBusy(sessionID, false)
			if err != nil {
				return err
			}
			if ok {
				s.emitSessionState(sessionID)
				if !state.Busy() {
					s.scheduleQueuedDispatch(sessionID)
				}
			}
			return nil
		}
		if s.isRuntimeAgentRunning(sessionID) {
			if _, _, err := s.registry.SetBusy(sessionID, true); err != nil {
				return err
			}
			s.emitSessionState(sessionID)
			return nil
		}
		if _, _, err := s.registry.DiscardPartialAssistantTurn(sessionID); err != nil {
			return err
		}
		state, ok, err := s.registry.SetBusy(sessionID, false)
		if err != nil {
			return err
		}
		if ok {
			s.emitSessionState(sessionID)
			if !state.Busy() {
				s.scheduleQueuedDispatch(sessionID)
			}
		}
	}
	return nil
}

func (s *Stub) isPISession(sessionID session.SessionID) bool {
	if s == nil {
		return false
	}
	record, ok := s.registry.Lookup(sessionID)
	return ok && record.identity.Backend() == session.BackendPI
}

func runtimeTurnID(event pi.Event) string {
	for _, candidate := range []string{event.TurnID, event.RawID, event.ParentID} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	if event.Timestamp <= 0 {
		return ""
	}
	return fmt.Sprintf("turn_%d", int64(event.Timestamp*1000))
}

func runtimeUIPrompt(request pi.UIRequest) string {
	for _, candidate := range []string{request.Prompt, request.Message, request.Title, request.Context} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return "ui request"
}

func runtimeUIOptions(request pi.UIRequest) []string {
	options := make([]string, 0, len(request.Options))
	appendOption := func(option pi.UIOption) {
		label := strings.TrimSpace(option.Label)
		if label == "" {
			label = strings.TrimSpace(option.Value)
		}
		if label != "" {
			options = append(options, label)
		}
	}
	for _, option := range request.Options {
		appendOption(option)
	}
	if len(options) > 0 {
		return options
	}
	for _, question := range request.Questions {
		for _, option := range question.Options {
			appendOption(option)
		}
	}
	return options
}

func runtimeUIOptionsSnapshot(request pi.UIRequest) []SessionUIOptionSnapshot {
	options := make([]SessionUIOptionSnapshot, 0, len(request.Options))
	for _, option := range request.Options {
		options = append(options, SessionUIOptionSnapshot{
			Label:       strings.TrimSpace(option.Label),
			Value:       strings.TrimSpace(option.Value),
			Description: strings.TrimSpace(option.Description),
		})
	}
	if len(options) > 0 {
		return options
	}
	for _, question := range request.Questions {
		for _, option := range question.Options {
			options = append(options, SessionUIOptionSnapshot{
				Label:       strings.TrimSpace(option.Label),
				Value:       strings.TrimSpace(option.Value),
				Description: strings.TrimSpace(option.Description),
			})
		}
	}
	return options
}

func runtimeUIQuestionsSnapshot(request pi.UIRequest) []SessionUIQuestionSnapshot {
	if len(request.Questions) == 0 {
		return nil
	}
	questions := make([]SessionUIQuestionSnapshot, 0, len(request.Questions))
	for _, question := range request.Questions {
		questions = append(questions, SessionUIQuestionSnapshot{
			Header:      strings.TrimSpace(question.Header),
			Question:    strings.TrimSpace(question.Prompt),
			Options:     runtimeUIQuestionOptionsSnapshot(question.Options),
			MultiSelect: question.MultiSelect,
		})
	}
	return questions
}

func runtimeUIQuestionOptionsSnapshot(raw []pi.UIOption) []SessionUIOptionSnapshot {
	if len(raw) == 0 {
		return nil
	}
	options := make([]SessionUIOptionSnapshot, 0, len(raw))
	for _, option := range raw {
		options = append(options, SessionUIOptionSnapshot{
			Label:       strings.TrimSpace(option.Label),
			Value:       strings.TrimSpace(option.Value),
			Description: strings.TrimSpace(option.Description),
		})
	}
	return options
}

func (s *Stub) startRuntimeAskUserWait(sessionID session.SessionID, event pi.Event) {
	if s == nil || event.UIRequest == nil {
		return
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok {
		return
	}
	runtime := s.runtimeForSession(sessionID, record.identity.Backend(), record.runtime)
	request := *event.UIRequest
	go s.runRuntimeAskUserWait(sessionID, runtime, request)
}

func (s *Stub) runRuntimeAskUserWait(sessionID session.SessionID, runtime sessionRuntime, request pi.UIRequest) {
	question := strings.TrimSpace(request.Prompt)
	if question == "" {
		question = strings.TrimSpace(request.Message)
	}
	if question == "" {
		question = strings.TrimSpace(request.Title)
	}
	if question == "" {
		question = "Runtime requested user input"
	}
	blockingReason := strings.TrimSpace(stringValueFromMap(request.Metadata, "blocking_reason", "blockingReason"))
	if blockingReason == "" {
		blockingReason = "runtime requested ask_user input"
	}
	attempted := strings.TrimSpace(stringValueFromMap(request.Metadata, "attempted"))
	if attempted == "" {
		attempted = "runtime emitted ask_user"
	}
	fallback := strings.TrimSpace(stringValueFromMap(request.Metadata, "default_if_no_reply", "defaultIfNoReply"))
	if fallback == "" {
		fallback = "No reply received. Continue with the safest reversible assumption and state the assumption."
	}
	result, err := s.AskUserWait(context.Background(), RuntimeWaitRequest{
		SessionID:           sessionID,
		RequestID:           strings.TrimSpace(request.RequestID),
		Question:            question,
		Context:             strings.TrimSpace(request.Context),
		BlockingReason:      blockingReason,
		Attempted:           attempted,
		DefaultIfNoReply:    fallback,
		TimeoutAfterMinutes: timeoutMinutes(request.TimeoutMS),
	})
	if err != nil {
		return
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return
	}
	value := string(payload)
	requestID := strings.TrimSpace(request.RequestID)
	if requestID == "" {
		requestID = result.WaitID
	}
	if err := runtime.RespondUI(context.Background(), requestID, value); err != nil {
		_ = s.emitRuntimeControlDiagnostic(sessionID, "ask_user_wait_response", err)
	}
	if state, ok, err := s.registry.SetBusy(sessionID, false); err == nil && ok {
		s.emitSessionState(sessionID)
		if !state.Busy() {
			s.scheduleQueuedDispatch(sessionID)
		}
	}
}

func stringValueFromMap(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw == nil {
			return ""
		}
		if value, ok := raw[key]; ok {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

func timeoutMinutes(timeoutMS *int) *int {
	if timeoutMS == nil || *timeoutMS <= 0 {
		return nil
	}
	minutes := (*timeoutMS + int(time.Minute/time.Millisecond) - 1) / int(time.Minute/time.Millisecond)
	if minutes < 1 {
		minutes = 1
	}
	return &minutes
}
