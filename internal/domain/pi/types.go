package pi

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

// EventKind is the normalized event category extracted from Pi session material.
type EventKind string

const (
	EventKindMessage      EventKind = "message"
	EventKindMessageDelta EventKind = "message_delta"
	EventKindTool         EventKind = "tool"
	EventKindError        EventKind = "error"
	EventKindUIRequest    EventKind = "ui_request"
	EventKindUIResolved   EventKind = "ui_resolved"
	EventKindBoundary     EventKind = "turn_boundary"
)

// MessageRole is the user-visible chat role.
type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

// MessageClass distinguishes durable assistant responses from provisional narration.
type MessageClass string

const (
	MessageClassUserPrompt MessageClass = "user_prompt"
	MessageClassNarration  MessageClass = "narration"
	MessageClassFinal      MessageClass = "final_response"
	MessageClassCommitted  MessageClass = "committed_response"
)

// UIRequestSource identifies where a UI request came from.
type UIRequestSource string

const (
	UIRequestSourceAskUserTool  UIRequestSource = "ask_user_tool"
	UIRequestSourceExtensionRPC UIRequestSource = "extension_ui_request"
)

// UIRequestKind groups compatible request families.
type UIRequestKind string

const (
	UIRequestKindAskUser UIRequestKind = "ask_user"
	UIRequestKindDialog  UIRequestKind = "dialog"
)

// UIMethod is the extension UI method for interactive dialog requests.
type UIMethod string

const (
	UIMethodSelect  UIMethod = "select"
	UIMethodConfirm UIMethod = "confirm"
	UIMethodInput   UIMethod = "input"
	UIMethodEditor  UIMethod = "editor"
)

// BoundaryKind captures turn lifecycle boundaries.
type BoundaryKind string

const (
	BoundaryKindAgentStarted   BoundaryKind = "agent_started"
	BoundaryKindAgentCompleted BoundaryKind = "agent_completed"
	BoundaryKindTurnStarted    BoundaryKind = "turn_started"
	BoundaryKindTurnCompleted  BoundaryKind = "turn_completed"
	BoundaryKindTurnAborted    BoundaryKind = "turn_aborted"
	BoundaryKindCommitted      BoundaryKind = "message_committed"
)

// Event is one normalized event extracted from Pi session material.
type Event struct {
	Kind       EventKind
	Timestamp  float64
	RawType    string
	RawID      string
	ParentID   string
	SessionID  string
	ThreadID   string
	TurnID     string
	Message    *Message
	Delta      *MessageDelta
	Tool       *ToolEvent
	Error      *ErrorMessage
	UIRequest  *UIRequest
	UIResolved *UIResolution
	Compaction *CompactionEvent
	Boundary   *Boundary
}

// Message is one normalized user-visible message.
type Message struct {
	ID            string
	Role          MessageRole
	Text          string
	Class         MessageClass
	StopReason    string
	ToolCallCount int
	ThinkingCount int
	CommitLike    bool
}

// MessageDelta is one live assistant text delta.
type MessageDelta struct {
	Role MessageRole
	Text string
}

// ToolEvent is one completed tool call or tool result.
type ToolEvent struct {
	CallID      string
	Name        string
	Text        string
	Arguments   map[string]any
	Result      bool
	ResultIndex int
	IsError     bool
}

// ErrorMessage is a terminal runtime error surfaced separately from chat roles.
type ErrorMessage struct {
	Message    string
	Source     string
	StopReason string
}

// UIRequest is one interactive request that later maps to ui.request frames.
type UIRequest struct {
	RequestID     string
	Source        UIRequestSource
	Kind          UIRequestKind
	Method        UIMethod
	Title         string
	Message       string
	Prompt        string
	Context       string
	Options       []UIOption
	Questions     []UIQuestion
	AllowFreeform bool
	AllowMultiple bool
	TimeoutMS     *int
	Metadata      map[string]any
	Interactive   bool
}

// UIOption is one normalized request option.
type UIOption struct {
	Label       string
	Value       string
	Description string
	Raw         map[string]any
}

// UIQuestion is one normalized ask-user question.
type UIQuestion struct {
	Header      string
	Prompt      string
	Options     []UIOption
	MultiSelect bool
}

// UIResolution is one typed resolution of an earlier UI request.
type UIResolution struct {
	RequestID               string
	Cancelled               bool
	WasCustom               bool
	AnswerText              string
	AnswerValues            []string
	AnswersByQuestion       map[string]string
	PromptFallbackAvailable bool
}

// CompactionEvent is one Pi RPC compaction lifecycle event.
type CompactionEvent struct {
	Phase        string
	Reason       string
	InputTokens  int
	InputTokensK float64
	TokensBefore int
	TokensAfter  int
	TokensAfterK float64
	DurationMS   int
	Model        map[string]any
	Result       map[string]any
	Aborted      bool
	WillRetry    bool
	ErrorMessage string
}

// Boundary is one turn lifecycle boundary.
type Boundary struct {
	Kind       BoundaryKind
	Inferred   bool
	CommitLike bool
	Reason     string
}
