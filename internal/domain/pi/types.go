package pi

import "actrail/internal/domain/runtimeevent"

// Material is parsed Pi session material from one JSONL source.
type Material struct {
	Header *Header
	Events []Event
}

// Header is the session header row emitted by Pi session logs.
type Header struct {
	SessionID     string
	Version       int
	CWD           string
	Timestamp     float64
	Provider      string
	Model         string
	ThinkingLevel string
}

// The normalized event model is owned by runtimeevent. These aliases keep
// existing Pi parser callers source-compatible while newer backends depend on
// the backend-neutral package directly.
type EventKind = runtimeevent.EventKind

const (
	EventKindMessage      = runtimeevent.EventKindMessage
	EventKindMessageDelta = runtimeevent.EventKindMessageDelta
	EventKindTool         = runtimeevent.EventKindTool
	EventKindError        = runtimeevent.EventKindError
	EventKindUIRequest    = runtimeevent.EventKindUIRequest
	EventKindUIResolved   = runtimeevent.EventKindUIResolved
	EventKindBoundary     = runtimeevent.EventKindBoundary
)

type MessageRole = runtimeevent.MessageRole

const (
	MessageRoleUser      = runtimeevent.MessageRoleUser
	MessageRoleAssistant = runtimeevent.MessageRoleAssistant
)

type MessageClass = runtimeevent.MessageClass

const (
	MessageClassUserPrompt = runtimeevent.MessageClassUserPrompt
	MessageClassNarration  = runtimeevent.MessageClassNarration
	MessageClassFinal      = runtimeevent.MessageClassFinal
	MessageClassCommitted  = runtimeevent.MessageClassCommitted
)

type UIRequestSource = runtimeevent.UIRequestSource

const (
	UIRequestSourceAskUserTool  = runtimeevent.UIRequestSourceAskUserTool
	UIRequestSourceExtensionRPC = runtimeevent.UIRequestSourceExtensionRPC
)

type UIRequestKind = runtimeevent.UIRequestKind

const (
	UIRequestKindAskUser = runtimeevent.UIRequestKindAskUser
	UIRequestKindDialog  = runtimeevent.UIRequestKindDialog
)

type UIMethod = runtimeevent.UIMethod

const (
	UIMethodSelect  = runtimeevent.UIMethodSelect
	UIMethodConfirm = runtimeevent.UIMethodConfirm
	UIMethodInput   = runtimeevent.UIMethodInput
	UIMethodEditor  = runtimeevent.UIMethodEditor
)

type BoundaryKind = runtimeevent.BoundaryKind

const (
	BoundaryKindAgentStarted   = runtimeevent.BoundaryKindAgentStarted
	BoundaryKindAgentCompleted = runtimeevent.BoundaryKindAgentCompleted
	BoundaryKindTurnStarted    = runtimeevent.BoundaryKindTurnStarted
	BoundaryKindTurnCompleted  = runtimeevent.BoundaryKindTurnCompleted
	BoundaryKindTurnAborted    = runtimeevent.BoundaryKindTurnAborted
	BoundaryKindCommitted      = runtimeevent.BoundaryKindCommitted
)

type Event = runtimeevent.Event
type Message = runtimeevent.Message
type MessageDelta = runtimeevent.MessageDelta
type ToolEvent = runtimeevent.ToolEvent
type ErrorMessage = runtimeevent.ErrorMessage
type UIRequest = runtimeevent.UIRequest
type UIOption = runtimeevent.UIOption
type UIQuestion = runtimeevent.UIQuestion
type UIResolution = runtimeevent.UIResolution
type CompactionEvent = runtimeevent.CompactionEvent
type Boundary = runtimeevent.Boundary
