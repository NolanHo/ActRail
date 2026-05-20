package app

type codexTransportAxis string

const (
	codexTransportUnknown     codexTransportAxis = "unknown"
	codexTransportAttached    codexTransportAxis = "attached"
	codexTransportUnavailable codexTransportAxis = "unavailable"
	codexTransportReplacing   codexTransportAxis = "replacing"
)

type codexRuntimeActivityAxis string

const (
	codexRuntimeActivityIdle         codexRuntimeActivityAxis = "idle"
	codexRuntimeActivityRunning      codexRuntimeActivityAxis = "running"
	codexRuntimeActivityWaitingUser  codexRuntimeActivityAxis = "waiting_user"
	codexRuntimeActivityInterrupting codexRuntimeActivityAxis = "interrupting"
	codexRuntimeActivityEnded        codexRuntimeActivityAxis = "ended"
)

type codexCommandAxis string

const (
	codexCommandPending     codexCommandAxis = "pending"
	codexCommandDispatching codexCommandAxis = "dispatching"
	codexCommandAccepted    codexCommandAxis = "accepted"
	codexCommandRejected    codexCommandAxis = "rejected"
	codexCommandReflected   codexCommandAxis = "reflected"
	codexCommandCompleted   codexCommandAxis = "completed"
	codexCommandFailed      codexCommandAxis = "failed"
	codexCommandCancelled   codexCommandAxis = "cancelled"
)

type codexHistoryAxis string

const (
	codexHistoryLiveOnly   codexHistoryAxis = "live_only"
	codexHistoryReconciled codexHistoryAxis = "reconciled"
	codexHistoryUncertain  codexHistoryAxis = "uncertain"
	codexHistoryFailed     codexHistoryAxis = "failed"
)

type codexDerivedDisplayState string

const (
	codexDisplayStarting     codexDerivedDisplayState = "starting"
	codexDisplaySending      codexDerivedDisplayState = "sending"
	codexDisplayTurnStarting codexDerivedDisplayState = "turn_starting"
	codexDisplayWorking      codexDerivedDisplayState = "working"
	codexDisplayIdle         codexDerivedDisplayState = "idle"
	codexDisplayFailed       codexDerivedDisplayState = "failed"
)

type codexStateAxes struct {
	Transport       codexTransportAxis
	RuntimeActivity codexRuntimeActivityAxis
	ActiveCommand   codexCommandAxis
	History         codexHistoryAxis
	UIWaitActive    bool
	AttachInFlight  bool
}

func (s codexStateAxes) canDirectSend() bool {
	return s.Transport == codexTransportAttached &&
		s.RuntimeActivity == codexRuntimeActivityIdle &&
		!codexCommandActive(s.ActiveCommand) &&
		!s.UIWaitActive
}

func (s codexStateAxes) displayState() codexDerivedDisplayState {
	if s.Transport == codexTransportUnavailable || s.ActiveCommand == codexCommandFailed || s.ActiveCommand == codexCommandRejected {
		return codexDisplayFailed
	}
	if s.Transport == codexTransportUnknown && s.AttachInFlight {
		return codexDisplayStarting
	}
	if s.RuntimeActivity == codexRuntimeActivityRunning ||
		s.RuntimeActivity == codexRuntimeActivityWaitingUser ||
		s.RuntimeActivity == codexRuntimeActivityInterrupting {
		return codexDisplayWorking
	}
	switch s.ActiveCommand {
	case codexCommandPending, codexCommandDispatching:
		return codexDisplaySending
	case codexCommandAccepted:
		return codexDisplayTurnStarting
	}
	return codexDisplayIdle
}

func codexCommandActive(state codexCommandAxis) bool {
	switch state {
	case codexCommandPending, codexCommandDispatching, codexCommandAccepted, codexCommandReflected:
		return true
	default:
		return false
	}
}
