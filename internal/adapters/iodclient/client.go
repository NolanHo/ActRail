package iodclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"actrail/internal/adapters/iod"
)

// Dialer opens the local helper control socket.
type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// Client speaks the frozen actrail-iod packet protocol over one local control connection.
type Client struct {
	conn net.Conn
	dec  *json.Decoder
	enc  *json.Encoder
	mu   sync.Mutex
}

// HelperError is a helper-returned iod.error packet surfaced as a Go error.
type HelperError struct {
	Packet iod.ErrorPacket
}

func (e HelperError) Error() string {
	return fmt.Sprintf("helper error %s: %s", e.Packet.Code, e.Packet.Message)
}

// CommandResult is one durable current-generation command outcome.
type CommandResult struct {
	Accepted *iod.CommandAcceptedPacket
	Rejected *iod.CommandRejectedPacket
}

func NewClient(conn net.Conn) *Client {
	return &Client{
		conn: conn,
		dec:  json.NewDecoder(conn),
		enc:  json.NewEncoder(conn),
	}
}

func DialContext(ctx context.Context, socketPath string, dialer Dialer) (*Client, error) {
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial iod control socket %q: %w", socketPath, err)
	}
	return NewClient(conn), nil
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) Hello(ctx context.Context) (iod.HelloPacket, error) {
	packet, err := c.readPacket(ctx)
	if err != nil {
		return iod.HelloPacket{}, err
	}
	hello, ok := packet.(iod.HelloPacket)
	if !ok {
		return iod.HelloPacket{}, fmt.Errorf("expected %q, got %T", iod.PacketHello, packet)
	}
	return hello, nil
}

func (c *Client) Command(ctx context.Context, packet iod.CommandPacket) (CommandResult, error) {
	if err := packet.Validate(); err != nil {
		return CommandResult{}, err
	}
	if err := c.writePacket(ctx, packet); err != nil {
		return CommandResult{}, err
	}
	for {
		response, err := c.readPacket(ctx)
		if err != nil {
			return CommandResult{}, err
		}
		switch v := response.(type) {
		case iod.StatePacket, iod.GenerationBreakPacket:
			continue
		case iod.CommandAcceptedPacket:
			if v.SessionID != packet.SessionID || v.GenerationID != packet.GenerationID || v.CommandID != packet.CommandID {
				return CommandResult{}, fmt.Errorf("command accepted packet does not match command %q", packet.CommandID)
			}
			return CommandResult{Accepted: &v}, nil
		case iod.CommandRejectedPacket:
			if v.SessionID != packet.SessionID || v.GenerationID != packet.GenerationID || v.CommandID != packet.CommandID {
				return CommandResult{}, fmt.Errorf("command rejected packet does not match command %q", packet.CommandID)
			}
			return CommandResult{Rejected: &v}, nil
		case iod.ErrorPacket:
			return CommandResult{}, HelperError{Packet: v}
		default:
			return CommandResult{}, fmt.Errorf("unexpected command response %T", response)
		}
	}
}

func (c *Client) SessionHistory(ctx context.Context, request iod.SessionHistoryRequestPacket) (iod.SessionHistoryResponsePacket, error) {
	if err := request.Validate(); err != nil {
		return iod.SessionHistoryResponsePacket{}, err
	}
	if err := c.writePacket(ctx, request); err != nil {
		return iod.SessionHistoryResponsePacket{}, err
	}
	for {
		packet, err := c.readPacket(ctx)
		if err != nil {
			return iod.SessionHistoryResponsePacket{}, err
		}
		switch v := packet.(type) {
		case iod.StatePacket, iod.GenerationBreakPacket:
			continue
		case iod.SessionHistoryResponsePacket:
			if v.SessionID != request.SessionID || v.GenerationID != request.GenerationID {
				return iod.SessionHistoryResponsePacket{}, fmt.Errorf("session history response does not match %q/%q", request.SessionID, request.GenerationID)
			}
			return v, nil
		case iod.ErrorPacket:
			return iod.SessionHistoryResponsePacket{}, HelperError{Packet: v}
		default:
			return iod.SessionHistoryResponsePacket{}, fmt.Errorf("unexpected session history response %T", packet)
		}
	}
}

func (c *Client) Replay(ctx context.Context, request iod.ReplayRequestPacket, visit func(iod.ReplayItemPacket) error) (iod.ReplayDonePacket, error) {
	if err := request.Validate(); err != nil {
		return iod.ReplayDonePacket{}, err
	}
	if err := c.writePacket(ctx, request); err != nil {
		return iod.ReplayDonePacket{}, err
	}
	for {
		packet, err := c.readPacket(ctx)
		if err != nil {
			return iod.ReplayDonePacket{}, err
		}
		switch v := packet.(type) {
		case iod.StatePacket, iod.GenerationBreakPacket:
			continue
		case iod.ReplayItemPacket:
			if v.SessionID != request.SessionID || v.GenerationID != request.GenerationID {
				return iod.ReplayDonePacket{}, fmt.Errorf("replay item does not match %q/%q", request.SessionID, request.GenerationID)
			}
			if visit != nil {
				if err := visit(v); err != nil {
					return iod.ReplayDonePacket{}, err
				}
			}
		case iod.ReplayDonePacket:
			if v.SessionID != request.SessionID || v.GenerationID != request.GenerationID {
				return iod.ReplayDonePacket{}, fmt.Errorf("replay done does not match %q/%q", request.SessionID, request.GenerationID)
			}
			if v.AfterOffset != request.AfterOffset {
				return iod.ReplayDonePacket{}, fmt.Errorf("replay done after offset = %d, want %d", v.AfterOffset, request.AfterOffset)
			}
			return v, nil
		case iod.ErrorPacket:
			return iod.ReplayDonePacket{}, HelperError{Packet: v}
		default:
			return iod.ReplayDonePacket{}, fmt.Errorf("unexpected replay response %T", packet)
		}
	}
}

func VerifyHelloProof(manifest iod.GenerationManifest, hello iod.HelloPacket) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if err := hello.Validate(); err != nil {
		return err
	}
	if hello.SessionID != manifest.SessionID {
		return fmt.Errorf("hello session id = %q, want %q", hello.SessionID, manifest.SessionID)
	}
	if hello.GenerationID != manifest.GenerationID {
		return fmt.Errorf("hello generation id = %q, want %q", hello.GenerationID, manifest.GenerationID)
	}
	if hello.HelperPID != manifest.HelperPID {
		return fmt.Errorf("hello helper pid = %d, want %d", hello.HelperPID, manifest.HelperPID)
	}
	if !equalChildPID(hello.ChildPID, manifest.ChildPID) {
		return fmt.Errorf("hello child pid does not match manifest")
	}
	if hello.WALPath != manifest.WALPath {
		return fmt.Errorf("hello wal path = %q, want %q", hello.WALPath, manifest.WALPath)
	}
	if hello.ControlSocketPath != manifest.ControlSocketPath {
		return fmt.Errorf("hello control socket path = %q, want %q", hello.ControlSocketPath, manifest.ControlSocketPath)
	}
	if hello.StartTS != manifest.StartTS {
		return fmt.Errorf("hello start ts = %v, want %v", hello.StartTS, manifest.StartTS)
	}
	return nil
}

func equalChildPID(left, right *int) bool {
	switch {
	case left == nil && right == nil:
		return true
	case left == nil || right == nil:
		return false
	default:
		return *left == *right
	}
}

func (c *Client) writePacket(ctx context.Context, packet any) error {
	if err := validatePacket(packet); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := setConnDeadline(ctx, c.conn); err != nil {
		return err
	}
	defer clearConnDeadline(c.conn)
	if err := c.enc.Encode(packet); err != nil {
		return fmt.Errorf("write iod packet: %w", err)
	}
	return nil
}

func (c *Client) ReadPacket(ctx context.Context) (any, error) {
	if err := setConnDeadline(ctx, c.conn); err != nil {
		return nil, err
	}
	defer clearConnDeadline(c.conn)
	var raw json.RawMessage
	if err := c.dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("read iod packet: %w", err)
	}
	packet, err := decodePacket(raw)
	if err != nil {
		return nil, err
	}
	return packet, nil
}

func (c *Client) readPacket(ctx context.Context) (any, error) {
	return c.ReadPacket(ctx)
}

func decodePacket(raw json.RawMessage) (any, error) {
	var env iod.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode iod envelope: %w", err)
	}
	switch env.Kind {
	case iod.PacketHello:
		var packet iod.HelloPacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketState:
		var packet iod.StatePacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketCommandSend, iod.PacketCommandEnqueue, iod.PacketCommandInterrupt, iod.PacketCommandUIResponseSubmit:
		var packet iod.CommandPacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketCommandAccepted:
		var packet iod.CommandAcceptedPacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketCommandRejected:
		var packet iod.CommandRejectedPacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketReplayRequest:
		var packet iod.ReplayRequestPacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketReplayItem:
		var packet iod.ReplayItemPacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketReplayDone:
		var packet iod.ReplayDonePacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketSessionHistoryRequest:
		var packet iod.SessionHistoryRequestPacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketSessionHistoryResponse:
		var packet iod.SessionHistoryResponsePacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketGenerationBreak:
		var packet iod.GenerationBreakPacket
		return packet, decodeInto(raw, &packet)
	case iod.PacketError:
		var packet iod.ErrorPacket
		return packet, decodeInto(raw, &packet)
	default:
		if err := env.Kind.Validate(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unsupported iod packet kind %q", env.Kind)
	}
}

func decodeInto(raw json.RawMessage, dst interface{ Validate() error }) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode iod packet: %w", err)
	}
	if err := dst.Validate(); err != nil {
		return err
	}
	return nil
}

func validatePacket(packet any) error {
	switch v := packet.(type) {
	case iod.CommandPacket:
		return v.Validate()
	case iod.ReplayRequestPacket:
		return v.Validate()
	case iod.HelloPacket:
		return v.Validate()
	case iod.CommandAcceptedPacket:
		return v.Validate()
	case iod.CommandRejectedPacket:
		return v.Validate()
	case iod.ReplayItemPacket:
		return v.Validate()
	case iod.ReplayDonePacket:
		return v.Validate()
	case iod.SessionHistoryRequestPacket:
		return v.Validate()
	case iod.SessionHistoryResponsePacket:
		return v.Validate()
	case iod.StatePacket:
		return v.Validate()
	case iod.GenerationBreakPacket:
		return v.Validate()
	case iod.ErrorPacket:
		return v.Validate()
	default:
		return fmt.Errorf("unsupported iod packet type %T", packet)
	}
}

func setConnDeadline(ctx context.Context, conn net.Conn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if conn == nil {
		return fmt.Errorf("iod connection is nil")
	}
	if deadline, ok := ctx.Deadline(); ok {
		return conn.SetDeadline(deadline)
	}
	return conn.SetDeadline(time.Time{})
}

func clearConnDeadline(conn net.Conn) {
	if conn == nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
}
