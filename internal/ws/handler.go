package ws

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"actrail/internal/config"
	"actrail/internal/httpapi/authn"

	"github.com/gorilla/websocket"
)

const maxIncomingFrameBytes int64 = 1 << 20

type Ticker interface {
	Chan() <-chan time.Time
	Stop()
}

type stdTicker struct {
	*time.Ticker
}

func (t stdTicker) Chan() <-chan time.Time {
	return t.C
}

type HandlerOption func(*Handler)

type Handler struct {
	cfg           config.Config
	upgrader      websocket.Upgrader
	registry      *Registry
	replay        *ReplayBuffer
	codec         Codec
	now           func() time.Time
	newTicker     func(time.Duration) Ticker
	connectionIDs IDSource
	frameIDs      IDSource
	initErr       error
}

type errorEnvelope struct {
	OK    bool      `json:"ok"`
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

type readResult struct {
	messageType int
	payload     []byte
	err         error
}

func NewHandler(cfg config.Config, opts ...HandlerOption) *Handler {
	replay, err := NewReplayBuffer(cfg.Protocol.ResumeBuffer)
	h := &Handler{
		cfg:           cfg,
		upgrader:      websocket.Upgrader{},
		registry:      NewRegistry(),
		replay:        replay,
		codec:         Codec{},
		now:           time.Now,
		newTicker:     func(interval time.Duration) Ticker { return stdTicker{Ticker: time.NewTicker(interval)} },
		connectionIDs: NewCounterIDSource("conn"),
		frameIDs:      NewCounterIDSource("evt"),
		initErr:       err,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	return h
}

func WithNow(now func() time.Time) HandlerOption {
	return func(h *Handler) {
		if now != nil {
			h.now = now
		}
	}
}

func WithTickerFactory(factory func(time.Duration) Ticker) HandlerOption {
	return func(h *Handler) {
		if factory != nil {
			h.newTicker = factory
		}
	}
}

func WithRegistry(registry *Registry) HandlerOption {
	return func(h *Handler) {
		if registry != nil {
			h.registry = registry
		}
	}
}

func WithReplayBuffer(replay *ReplayBuffer) HandlerOption {
	return func(h *Handler) {
		h.replay = replay
		h.initErr = nil
	}
}

func WithConnectionIDs(ids IDSource) HandlerOption {
	return func(h *Handler) {
		if ids != nil {
			h.connectionIDs = ids
		}
	}
}

func WithFrameIDs(ids IDSource) HandlerOption {
	return func(h *Handler) {
		if ids != nil {
			h.frameIDs = ids
		}
	}
}

func WithUpgrader(upgrader websocket.Upgrader) HandlerOption {
	return func(h *Handler) {
		h.upgrader = upgrader
	}
}

func (h *Handler) Registry() *Registry {
	return h.registry
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !authn.Authenticated(req, h.cfg.Auth) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid auth cookie required", "")
		return
	}
	if version := strings.TrimSpace(req.URL.Query().Get("protocol_version")); version != "" {
		want := strconv.Itoa(h.cfg.Protocol.Version)
		if version != want {
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown protocol version", "protocol_version")
			return
		}
	}
	if h.initErr != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", h.initErr.Error(), "")
		return
	}

	wsconn, err := h.upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	defer wsconn.Close()
	wsconn.SetReadLimit(maxIncomingFrameBytes)

	now := h.now()
	state, err := NewConnectionState(h.connectionIDs.Next(), h.cfg.Protocol.HeartbeatInterval, h.frameIDs, now)
	if err != nil {
		return
	}
	if err := h.registry.Add(state); err != nil {
		return
	}
	defer h.registry.Remove(state.ID())

	hello := NewHelloFrame(now, h.frameIDs.Next(), state.ID(), h.cfg.Protocol.Version, h.cfg.HeartbeatIntervalMillis(), h.cfg.Protocol.ResumeBuffer)
	if err := h.writeFrames(wsconn, state.serverFrames(now, hello)...); err != nil {
		return
	}

	ticker := h.newTicker(h.cfg.Protocol.HeartbeatInterval)
	defer ticker.Stop()

	reads := make(chan readResult, 1)
	done := make(chan struct{})
	defer close(done)
	go readLoop(wsconn, reads, done)

	for {
		select {
		case tickAt := <-ticker.Chan():
			if !state.Heartbeat().ServerHeartbeatDue(tickAt) {
				continue
			}
			if err := h.writeFrames(wsconn, state.BuildHeartbeatFrame(tickAt)); err != nil {
				return
			}
		case read := <-reads:
			if read.err != nil {
				if websocket.IsCloseError(read.err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return
				}
				if websocket.IsUnexpectedCloseError(read.err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return
				}
				return
			}
			if read.messageType != websocket.TextMessage {
				now := h.now()
				if err := h.writeFrames(wsconn, state.errorFrames(now, RawFrame{Stream: SystemStream.String()}, ErrorCodeUnsupported, "only text websocket frames are supported", "type")...); err != nil {
					return
				}
				continue
			}
			if err := h.handleIncomingFrame(wsconn, state, read.payload); err != nil {
				return
			}
		}
	}
}

func readLoop(conn *websocket.Conn, out chan<- readResult, done <-chan struct{}) {
	for {
		messageType, payload, err := conn.ReadMessage()
		result := readResult{messageType: messageType, payload: payload, err: err}
		select {
		case out <- result:
		case <-done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (h *Handler) handleIncomingFrame(conn *websocket.Conn, state *ConnectionState, payload []byte) error {
	raw, err := h.codec.Decode(payload)
	now := h.now()
	if err != nil {
		return h.writeFrames(conn, state.errorFrames(now, RawFrame{Stream: SystemStream.String()}, ErrorCodeInvalidRequest, err.Error(), "payload")...)
	}
	frames, err := state.HandleFrame(now, raw, h.replay)
	if err != nil {
		return h.writeFrames(conn, state.errorFrames(now, raw, ErrorCodeInternal, err.Error(), "payload")...)
	}
	if len(frames) == 0 {
		return nil
	}
	return h.writeFrames(conn, frames...)
}

func (h *Handler) writeFrames(conn *websocket.Conn, frames ...Frame) error {
	for _, frame := range frames {
		encoded, err := h.codec.Encode(frame)
		if err != nil {
			return err
		}
		if err := conn.WriteMessage(websocket.TextMessage, encoded); err != nil {
			return err
		}
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, message, field string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		OK: false,
		Error: errorBody{
			Code:    code,
			Message: message,
			Field:   field,
		},
	})
}
