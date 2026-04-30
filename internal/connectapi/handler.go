package connectapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"actrail/internal/app"
	"actrail/internal/domain/session"
)

const (
	connectBasePath       = "/api/connect/"
	sessionCommandService = "actrail.v1.SessionCommandService"
	eventService          = "actrail.v1.EventService"
)

type Handler struct {
	controller app.SessionController
	broker     *Broker
	now        func() time.Time
}

type SessionIdentity struct {
	SessionID string `json:"sessionId"`
	RuntimeID string `json:"runtimeId,omitempty"`
}

type commandRequest struct {
	Session       SessionIdentity `json:"session"`
	SessionRaw    SessionIdentity `json:"session_identity"`
	Text          string          `json:"text"`
	ResponseTo    string          `json:"responseTo"`
	ResponseToRaw string          `json:"response_to"`
	Value         json.RawMessage `json:"value"`
}

type commandResponse struct {
	PayloadJSON string `json:"payloadJson"`
}

type subscribeRequest struct {
	AfterEventID    uint64 `json:"afterEventId"`
	AfterEventIDRaw uint64 `json:"after_event_id"`
}

type connectError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewHandler(controller app.SessionController, broker *Broker) *Handler {
	if broker == nil {
		broker = NewBroker(defaultBrokerLimit)
	}
	return &Handler{controller: controller, broker: broker, now: time.Now}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeConnectError(w, http.StatusMethodNotAllowed, "unimplemented", "method not supported")
		return
	}
	tail := strings.TrimPrefix(req.URL.Path, connectBasePath)
	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) != 2 {
		writeConnectError(w, http.StatusNotFound, "unimplemented", "unknown service")
		return
	}
	switch parts[0] {
	case sessionCommandService:
		h.handleCommand(w, req, parts[1])
	case eventService:
		h.handleEvent(w, req, parts[1])
	default:
		writeConnectError(w, http.StatusNotFound, "unimplemented", "unknown service")
	}
}

func (h *Handler) handleCommand(w http.ResponseWriter, req *http.Request, method string) {
	started := time.Now()
	if h.controller == nil {
		writeConnectError(w, http.StatusServiceUnavailable, "unavailable", "session controller unavailable")
		return
	}
	proto := requestWantsProto(req.Header.Get("Content-Type"))
	var body commandRequest
	if proto {
		data, err := readProtoBody(req.Body)
		if err != nil {
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
		body, err = decodeCommandRequestProto(method, data)
		if err != nil {
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
	} else if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid json")
		return
	}
	sessionID, err := body.sessionID()
	if err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	payload, err := h.dispatchCommand(req.Context(), method, sessionID, body)
	if err != nil {
		status := statusForCommandError(err)
		slog.Info("connect command", "method", method, "status", status, "latency_ms", time.Since(started).Milliseconds(), "error", err.Error())
		writeConnectError(w, status, codeForCommandError(err), err.Error())
		return
	}
	slog.Info("connect command", "method", method, "status", http.StatusOK, "latency_ms", time.Since(started).Milliseconds())
	if proto {
		writeConnectProto(w, http.StatusOK, encodeCommandResponseProto(payload))
		return
	}
	writeConnectJSON(w, http.StatusOK, commandResponse{PayloadJSON: base64.StdEncoding.EncodeToString(payload)})
}

func (h *Handler) dispatchCommand(ctx context.Context, method string, sessionID session.SessionID, body commandRequest) ([]byte, error) {
	switch method {
	case "Send":
		res, err := h.controller.Send(ctx, app.SendRequest{SessionID: sessionID, Text: body.Text})
		return marshalPayload(res, err)
	case "Enqueue":
		res, err := h.controller.Enqueue(ctx, app.EnqueueRequest{SessionID: sessionID, Text: body.Text})
		return marshalPayload(res, err)
	case "CancelQueue":
		res, err := h.controller.CancelQueue(ctx, app.CancelQueueRequest{SessionID: sessionID})
		return marshalPayload(res, err)
	case "Interrupt":
		res, err := h.controller.Interrupt(ctx, app.InterruptRequest{SessionID: sessionID})
		return marshalPayload(res, err)
	case "RespondUI":
		value, err := normalizeValue(body.Value)
		if err != nil {
			return nil, err
		}
		res, err := h.controller.RespondUI(ctx, app.UIResponseRequest{SessionID: sessionID, ResponseTo: body.responseTo(), Value: value})
		return marshalPayload(res, err)
	default:
		return nil, fmt.Errorf("unknown command method %q", method)
	}
}

func (h *Handler) handleEvent(w http.ResponseWriter, req *http.Request, method string) {
	if method != "Subscribe" {
		writeConnectError(w, http.StatusNotFound, "unimplemented", "unknown event method")
		return
	}
	proto := requestWantsProto(req.Header.Get("Content-Type"))
	var body subscribeRequest
	if proto {
		data, err := readProtoBody(req.Body)
		if err != nil {
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
		body, err = decodeSubscribeRequestProto(data)
		if err != nil {
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
	} else if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid json")
		return
	}
	after := body.AfterEventID
	if after == 0 {
		after = body.AfterEventIDRaw
	}
	if proto {
		w.Header().Set("Content-Type", "application/connect+proto")
	} else {
		w.Header().Set("Content-Type", "application/connect+json")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	writeEvent := func(event EventEnvelope) bool {
		var err error
		if proto {
			err = writeConnectProtoEnvelope(w, event)
		} else {
			err = writeConnectEnvelope(w, event)
		}
		if err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	replay, _ := h.broker.Replay(after)
	for _, event := range replay {
		if !writeEvent(event) {
			return
		}
	}
	ch, unsubscribe := h.broker.Subscribe()
	defer unsubscribe()
	for {
		select {
		case <-req.Context().Done():
			return
		case event := <-ch:
			if !writeEvent(event) {
				return
			}
		}
	}
}

func (r commandRequest) sessionID() (session.SessionID, error) {
	identity := r.Session
	if strings.TrimSpace(identity.SessionID) == "" {
		identity = r.SessionRaw
	}
	if strings.TrimSpace(identity.SessionID) == "" {
		return "", fmt.Errorf("session.sessionId is required")
	}
	return session.ParseSessionID(identity.SessionID)
}

func (r commandRequest) responseTo() string {
	if strings.TrimSpace(r.ResponseTo) != "" {
		return strings.TrimSpace(r.ResponseTo)
	}
	return strings.TrimSpace(r.ResponseToRaw)
}

func normalizeValue(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", fmt.Errorf("value is required")
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return "", err
		}
		return value, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "", err
	}
	return compact.String(), nil
}

func marshalPayload(value any, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func writeConnectEnvelope(w http.ResponseWriter, event EventEnvelope) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return writeConnectFramedPayload(w, payload)
}

func writeConnectProtoEnvelope(w http.ResponseWriter, event EventEnvelope) error {
	return writeConnectFramedPayload(w, encodeEventEnvelopeProto(event))
}

func writeConnectFramedPayload(w http.ResponseWriter, payload []byte) error {
	var header [5]byte
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func writeConnectJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeConnectProto(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/connect+proto")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func writeConnectError(w http.ResponseWriter, status int, code, message string) {
	writeConnectJSON(w, status, connectError{Code: code, Message: message})
}

func statusForCommandError(err error) int {
	var appErr *app.Error
	if errors.As(err, &appErr) {
		switch appErr.Code {
		case "invalid_request":
			return http.StatusBadRequest
		case "not_found":
			return http.StatusNotFound
		case "conflict", "transport_reset_required":
			return http.StatusConflict
		case "unsupported":
			return http.StatusNotImplemented
		}
	}
	return http.StatusInternalServerError
}

func codeForCommandError(err error) string {
	var appErr *app.Error
	if errors.As(err, &appErr) && strings.TrimSpace(appErr.Code) != "" {
		return appErr.Code
	}
	return "internal"
}
