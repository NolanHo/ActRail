package piagentgrpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	piagentv1 "actrail/proto/pi/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	DefaultSocketDir       = "/tmp/pi-agent/actrail"
	DefaultSocketPath      = "/tmp/pi-agent/agent.sock"
	DefaultTarget          = "unix://" + DefaultSocketPath
	SubscribeBatchWindowMS = 8
	SubscribeMaxBatchSize  = 64
	InlineByteLimit        = 65536
	ReconnectDelay         = 250 * time.Millisecond
)

type Dialer func(context.Context, string) (*grpc.ClientConn, error)

type Client struct {
	mu       sync.Mutex
	target   string
	dialer   Dialer
	conn     *grpc.ClientConn
	rpc      piagentv1.PiAgentClient
	lastSeen uint64
}

type Event struct {
	Type            string
	Sequence        uint64
	PayloadJSON     []byte
	SessionBoundary *SessionBoundary
}

type SessionBoundary struct {
	SessionID   string
	SessionFile string
	Reason      string
}

type SourceInfo struct {
	Path    string `json:"path,omitempty"`
	Source  string `json:"source,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Origin  string `json:"origin,omitempty"`
	BaseDir string `json:"base_dir,omitempty"`
}

type Command struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Source      string     `json:"source"`
	SourceInfo  SourceInfo `json:"source_info,omitempty"`
}

type RuntimeState string

const (
	RuntimeStateUnknown  RuntimeState = ""
	RuntimeStateStarting RuntimeState = "starting"
	RuntimeStateReady    RuntimeState = "ready"
	RuntimeStateFailed   RuntimeState = "failed"
)

type State struct {
	SessionID            string
	SessionFile          string
	SessionName          string
	ModelID              string
	Provider             string
	ThinkingLevel        string
	IsStreaming          bool
	IsCompacting         bool
	PendingMessageCount  int
	IsBusy               bool
	BusyReason           string
	RuntimeState         RuntimeState
	RuntimeStatusMessage string
}

func New(target string, dialer Dialer) *Client {
	return &Client{target: normalizeTarget(target), dialer: dialer}
}

func normalizeTarget(target string) string {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return DefaultTarget
	}
	return trimmed
}

func TargetForSession(sessionID string) string {
	resolved := strings.TrimSpace(sessionID)
	if resolved == "" {
		return DefaultTarget
	}
	return "unix://" + DefaultSocketDir + "/" + resolved + ".sock"
}

func SocketPathForTarget(target string) string {
	resolved := normalizeTarget(target)
	return strings.TrimPrefix(resolved, "unix://")
}

func (c *Client) Connect(ctx context.Context) error {
	state, err := c.GetState(ctx)
	if err != nil {
		return err
	}
	if state.RuntimeFailed() {
		return fmt.Errorf("runtime failed: %s", firstNonEmptyString(state.RuntimeMessage(), "runtime failed"))
	}
	return nil
}

func (c *Client) GetState(ctx context.Context) (State, error) {
	rpc, err := c.client(ctx)
	if err != nil {
		return State{}, err
	}
	state, err := rpc.GetState(ctx, &piagentv1.GetStateRequest{})
	if err != nil {
		return State{}, err
	}
	return stateFromProto(state), nil
}

func (c *Client) Prompt(ctx context.Context, message string) error {
	return c.prompt(ctx, message, piagentv1.StreamingBehavior_STREAMING_BEHAVIOR_UNSPECIFIED)
}

func (c *Client) FollowUp(ctx context.Context, message string) error {
	return c.prompt(ctx, message, piagentv1.StreamingBehavior_STREAMING_BEHAVIOR_FOLLOW_UP)
}

func (c *Client) prompt(ctx context.Context, message string, behavior piagentv1.StreamingBehavior) error {
	text := strings.TrimSpace(message)
	if text == "" {
		return fmt.Errorf("prompt is required")
	}
	rpc, err := c.client(ctx)
	if err != nil {
		return err
	}
	_, err = rpc.Prompt(ctx, &piagentv1.PromptRequest{Message: text, StreamingBehavior: behavior})
	return err
}

func (c *Client) Abort(ctx context.Context) error {
	rpc, err := c.client(ctx)
	if err != nil {
		return err
	}
	_, err = rpc.Abort(ctx, &piagentv1.AbortRequest{})
	return err
}

func (c *Client) ListCommands(ctx context.Context) ([]Command, error) {
	rpc, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := rpc.ListCommands(ctx, &piagentv1.ListCommandsRequest{})
	if err != nil {
		return nil, err
	}
	return commandsFromProto(resp.GetCommands()), nil
}

func (c *Client) ExecuteCommand(ctx context.Context, name, args string) error {
	command := strings.TrimPrefix(strings.TrimSpace(name), "/")
	if command == "" {
		return fmt.Errorf("command is required")
	}
	rpc, err := c.client(ctx)
	if err != nil {
		return err
	}
	_, err = rpc.ExecuteCommand(ctx, &piagentv1.ExecuteCommandRequest{Name: command, Args: strings.TrimSpace(args)})
	return err
}

func (c *Client) Subscribe(ctx context.Context, handle func(Event) error) error {
	if handle == nil {
		return fmt.Errorf("event handler is required")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.subscribeOnce(ctx, handle); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ReconnectDelay):
		}
	}
}

func (c *Client) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.rpc = nil
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

func (c *Client) client(ctx context.Context) (piagentv1.PiAgentClient, error) {
	c.mu.Lock()
	if c.rpc != nil {
		rpc := c.rpc
		c.mu.Unlock()
		return rpc, nil
	}
	target := c.target
	dialer := c.dialer
	c.mu.Unlock()

	if dialer == nil {
		dialer = defaultDialer
	}
	conn, err := dialer(ctx, target)
	if err != nil {
		return nil, err
	}
	rpc := piagentv1.NewPiAgentClient(conn)

	c.mu.Lock()
	if c.rpc != nil {
		existing := c.rpc
		c.mu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	c.conn = conn
	c.rpc = rpc
	c.mu.Unlock()
	return rpc, nil
}

func defaultDialer(ctx context.Context, target string) (*grpc.ClientConn, error) {
	return grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func (c *Client) subscribeOnce(ctx context.Context, handle func(Event) error) error {
	rpc, err := c.client(ctx)
	if err != nil {
		return err
	}
	stream, err := rpc.SubscribeEvents(ctx, &piagentv1.SubscribeEventsRequest{
		Level:           piagentv1.EventLevel_EVENT_LEVEL_SUMMARY,
		BatchWindowMs:   SubscribeBatchWindowMS,
		MaxBatchSize:    SubscribeMaxBatchSize,
		AfterSequence:   c.lastSeenSequence(),
		InlinePayloads:  true,
		InlineByteLimit: InlineByteLimit,
	})
	if err != nil {
		return err
	}
	for {
		batch, err := stream.Recv()
		if err != nil {
			return err
		}
		for _, item := range batch.GetEvents() {
			event := eventFromProto(item)
			c.observeSequence(event.Sequence)
			if err := handle(event); err != nil {
				return err
			}
		}
	}
}

func (c *Client) lastSeenSequence() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSeen
}

func (c *Client) observeSequence(seq uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq > c.lastSeen {
		c.lastSeen = seq
	}
}

func runtimeStateFromProto(state piagentv1.RuntimeState) RuntimeState {
	switch state {
	case piagentv1.RuntimeState_RUNTIME_STATE_STARTING:
		return RuntimeStateStarting
	case piagentv1.RuntimeState_RUNTIME_STATE_READY:
		return RuntimeStateReady
	case piagentv1.RuntimeState_RUNTIME_STATE_FAILED:
		return RuntimeStateFailed
	default:
		return RuntimeStateUnknown
	}
}

func (s State) RuntimeReady() bool {
	return s.RuntimeState == RuntimeStateUnknown || s.RuntimeState == RuntimeStateReady
}

func (s State) RuntimeStarting() bool {
	return s.RuntimeState == RuntimeStateStarting
}

func (s State) RuntimeFailed() bool {
	return s.RuntimeState == RuntimeStateFailed
}

func (s State) RuntimeMessage() string {
	return strings.TrimSpace(s.RuntimeStatusMessage)
}

func (s State) Busy() bool {
	if reason := strings.TrimSpace(s.BusyReason); reason != "" {
		return s.IsBusy
	}
	return s.IsStreaming || s.IsCompacting || s.PendingMessageCount > 0
}

func (s State) BusyStateReason() string {
	if reason := strings.TrimSpace(s.BusyReason); reason != "" {
		return reason
	}
	if s.IsCompacting {
		return "compacting"
	}
	if s.IsStreaming {
		return "streaming"
	}
	if s.PendingMessageCount > 0 {
		return "queued"
	}
	if s.RuntimeStarting() {
		return "starting"
	}
	if s.RuntimeFailed() {
		return "failed"
	}
	return "ready"
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func stateFromProto(state *piagentv1.SessionState) State {
	if state == nil {
		return State{}
	}
	out := State{
		SessionID:            strings.TrimSpace(state.GetSessionId()),
		SessionFile:          strings.TrimSpace(state.GetSessionFile()),
		SessionName:          strings.TrimSpace(state.GetSessionName()),
		ThinkingLevel:        strings.TrimSpace(state.GetThinkingLevel()),
		IsStreaming:          state.GetIsStreaming(),
		IsCompacting:         state.GetIsCompacting(),
		PendingMessageCount:  int(state.GetPendingMessageCount()),
		IsBusy:               state.GetIsBusy(),
		BusyReason:           strings.TrimSpace(state.GetBusyReason()),
		RuntimeState:         runtimeStateFromProto(state.GetRuntimeState()),
		RuntimeStatusMessage: strings.TrimSpace(state.GetRuntimeStatusMessage()),
	}
	if model := state.GetModel(); model != nil {
		out.ModelID = strings.TrimSpace(model.GetId())
		out.Provider = strings.TrimSpace(model.GetProvider())
	}
	return out
}

func commandsFromProto(commands []*piagentv1.SlashCommand) []Command {
	out := make([]Command, 0, len(commands))
	for _, command := range commands {
		if command == nil {
			continue
		}
		out = append(out, Command{
			Name:        strings.TrimSpace(command.GetName()),
			Description: strings.TrimSpace(command.GetDescription()),
			Source:      strings.TrimSpace(command.GetSource()),
			SourceInfo:  sourceInfoFromProto(command.GetSourceInfo()),
		})
	}
	return out
}

func sourceInfoFromProto(sourceInfo *piagentv1.SourceInfo) SourceInfo {
	if sourceInfo == nil {
		return SourceInfo{}
	}
	return SourceInfo{
		Path:    strings.TrimSpace(sourceInfo.GetPath()),
		Source:  strings.TrimSpace(sourceInfo.GetSource()),
		Scope:   strings.TrimSpace(sourceInfo.GetScope()),
		Origin:  strings.TrimSpace(sourceInfo.GetOrigin()),
		BaseDir: strings.TrimSpace(sourceInfo.GetBaseDir()),
	}
}

func eventFromProto(event *piagentv1.Event) Event {
	if event == nil {
		return Event{}
	}
	out := Event{
		Type:        strings.TrimSpace(event.GetType()),
		Sequence:    event.GetSequence(),
		PayloadJSON: append([]byte(nil), event.GetPayload().GetJson()...),
	}
	if boundary := event.GetSessionBoundary(); boundary != nil {
		out.SessionBoundary = &SessionBoundary{
			SessionID:   strings.TrimSpace(boundary.GetSessionId()),
			SessionFile: strings.TrimSpace(boundary.GetSessionFile()),
			Reason:      strings.TrimSpace(boundary.GetReason()),
		}
	}
	return out
}
