package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/piagentgrpc"
	"actrail/internal/domain/session"
)

const (
	piRPCStateIdlePollInterval = time.Minute
	piRPCStateFastPollInterval = time.Second
	piRPCStateBusyPollInterval = 10 * time.Second
	piRPCStatePollTimeout      = 2 * time.Second
	piRPCStateMaxFailures      = 3
	piRPCBusyHoldDuration      = 30 * time.Second
)

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
