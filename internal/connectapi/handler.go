package connectapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"actrail/internal/app"
	"actrail/internal/domain/session"
	actrailv1 "actrail/proto/actrail/v1"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
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
	logger     *zap.Logger
}

type SessionIdentity struct {
	SessionID    string `json:"sessionId"`
	SessionIDRaw string `json:"session_id"`
	RuntimeID    string `json:"runtimeId,omitempty"`
	RuntimeIDRaw string `json:"runtime_id,omitempty"`
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
	TraceID     string `json:"traceId,omitempty"`
}

type listSessionsRequest struct {
	GroupKey        string `json:"groupKey"`
	GroupKeyRaw     string `json:"group_key"`
	Offset          int    `json:"offset"`
	Limit           int    `json:"limit"`
	GroupOffset     int    `json:"groupOffset"`
	GroupOffsetRaw  int    `json:"group_offset"`
	GroupLimit      int    `json:"groupLimit"`
	GroupLimitRaw   int    `json:"group_limit"`
	AgentBackend    string `json:"agentBackend"`
	AgentBackendRaw string `json:"agent_backend"`
	CWD             string `json:"cwd"`
	Title           string `json:"title"`
}

type subscribeRequest struct {
	AfterEventID    uint64 `json:"afterEventId"`
	AfterEventIDRaw uint64 `json:"after_event_id"`
}

type sessionMessagesRequest struct {
	Session               SessionIdentity `json:"session"`
	SessionRaw            SessionIdentity `json:"session_identity"`
	AfterSeq              *uint64         `json:"afterSeq"`
	AfterSeqRaw           *uint64         `json:"after_seq"`
	BeforeSeq             *uint64         `json:"beforeSeq"`
	BeforeSeqRaw          *uint64         `json:"before_seq"`
	Limit                 int             `json:"limit"`
	Init                  bool            `json:"init"`
	Deferred              bool            `json:"deferred"`
	ActiveTurnStartSeq    uint64          `json:"activeTurnStartSeq"`
	ActiveTurnStartSeqRaw uint64          `json:"active_turn_start_seq"`
	IncludeToolDetails    bool            `json:"includeToolDetails"`
	IncludeToolDetailsRaw bool            `json:"include_tool_details"`
	IncludeToolEvents     bool            `json:"includeToolEvents"`
	IncludeToolEventsRaw  bool            `json:"include_tool_events"`
	EventID               string          `json:"eventId"`
	EventIDRaw            string          `json:"event_id"`
	ToolCallID            string          `json:"toolCallId"`
	ToolCallIDRaw         string          `json:"tool_call_id"`
}

type connectError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	TraceID string `json:"traceId,omitempty"`
}

type HandlerOption func(*Handler)

func WithLogger(logger *zap.Logger) HandlerOption {
	return func(h *Handler) {
		if logger != nil {
			h.logger = logger
		}
	}
}

func NewHandler(controller app.SessionController, broker *Broker, opts ...HandlerOption) *Handler {
	if broker == nil {
		broker = NewBroker(defaultBrokerLimit)
	}
	h := &Handler{controller: controller, broker: broker, now: time.Now, logger: zap.NewNop()}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	operation := req.Method + " " + req.URL.Path
	ctx, span := otel.Tracer("actrail/connect").Start(req.Context(), operation, trace.WithAttributes(
		attribute.String("http.request.method", req.Method),
		attribute.String("url.path", req.URL.Path),
	))
	defer span.End()
	traceID := span.SpanContext().TraceID().String()
	if traceID == "00000000000000000000000000000000" {
		traceID = ""
	}
	w.Header().Set("X-Trace-Id", traceID)
	if req.Method != http.MethodPost {
		span.SetStatus(codes.Error, "method not supported")
		writeConnectError(w, http.StatusMethodNotAllowed, "unimplemented", "method not supported")
		return
	}
	tail := strings.TrimPrefix(req.URL.Path, connectBasePath)
	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) != 2 {
		span.SetStatus(codes.Error, "unknown service")
		writeConnectError(w, http.StatusNotFound, "unimplemented", "unknown service")
		return
	}
	span.SetAttributes(attribute.String("rpc.service", parts[0]), attribute.String("rpc.method", parts[1]))
	req = req.WithContext(ctx)
	switch parts[0] {
	case sessionCommandService:
		if parts[1] == "ListSessions" {
			h.handleListSessions(w, req)
			return
		}
		if parts[1] == "SessionMessages" {
			h.handleSessionMessages(w, req)
			return
		}
		h.handleCommand(w, req, parts[1])
	case eventService:
		h.handleEvent(w, req, parts[1])
	default:
		span.SetStatus(codes.Error, "unknown service")
		writeConnectError(w, http.StatusNotFound, "unimplemented", "unknown service")
	}
}

func (h *Handler) handleListSessions(w http.ResponseWriter, req *http.Request) {
	started := time.Now()
	span := trace.SpanFromContext(req.Context())
	span.SetAttributes(attribute.String("rpc.method", "ListSessions"))
	if h.controller == nil {
		span.SetStatus(codes.Error, "session controller unavailable")
		span.AddEvent("connect.error", trace.WithAttributes(attribute.String("error.code", "unavailable")))
		writeConnectError(w, http.StatusServiceUnavailable, "unavailable", "session controller unavailable")
		return
	}
	protoMode := requestWantsProto(req.Header.Get("Content-Type"))
	span.SetAttributes(attribute.Bool("connect.proto", protoMode))
	var body listSessionsRequest
	if protoMode {
		data, err := readProtoBody(req.Body)
		if err != nil {
			span.SetStatus(codes.Error, "invalid protobuf")
			span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_protobuf")))
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
		decoded, err := decodeListSessionsRequestProto(data)
		if err != nil {
			span.SetStatus(codes.Error, "invalid protobuf")
			span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_protobuf")))
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
		body = decoded
	} else if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		span.SetStatus(codes.Error, "invalid json")
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_json")))
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid json")
		return
	}
	span.AddEvent("connect.request.decoded", trace.WithAttributes(
		attribute.Bool("sessions.group_key_filter", firstString(body.GroupKey, body.GroupKeyRaw) != ""),
		attribute.Bool("sessions.agent_backend_filter", firstString(body.AgentBackend, body.AgentBackendRaw) != ""),
		attribute.Bool("sessions.cwd_filter", strings.TrimSpace(body.CWD) != ""),
		attribute.Bool("sessions.title_filter", strings.TrimSpace(body.Title) != ""),
		attribute.Int("sessions.limit", body.Limit),
	))
	payload, err := h.controller.ListSessions(req.Context(), app.ListSessionsRequest{
		GroupKey:     firstString(body.GroupKey, body.GroupKeyRaw),
		Offset:       body.Offset,
		Limit:        body.Limit,
		GroupOffset:  firstNonZeroInt(body.GroupOffset, body.GroupOffsetRaw),
		GroupLimit:   firstNonZeroInt(body.GroupLimit, body.GroupLimitRaw),
		AgentBackend: firstString(body.AgentBackend, body.AgentBackendRaw),
		CWD:          body.CWD,
		Title:        body.Title,
	})
	if err != nil {
		status := statusForCommandError(err)
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.error", trace.WithAttributes(attribute.Int("http.response.status_code", status), attribute.String("error.code", codeForCommandError(err))))
		h.logger.Info("connect command", zap.String("method", "ListSessions"), zap.Int("status", status), zap.Int64("latency_ms", time.Since(started).Milliseconds()), zap.Error(err))
		writeConnectError(w, status, codeForCommandError(err), err.Error())
		return
	}
	encoded, err := marshalPayload(payload, nil)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.error", trace.WithAttributes(attribute.String("error.code", "internal")))
		writeConnectError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	span.SetAttributes(attribute.Int("sessions.result_count", len(payload.Items)), attribute.Int("connect.response_bytes", len(encoded)), attribute.Int64("connect.latency_ms", time.Since(started).Milliseconds()))
	span.AddEvent("connect.response.encoded")
	h.logger.Info("connect command", zap.String("method", "ListSessions"), zap.Int("status", http.StatusOK), zap.Int64("latency_ms", time.Since(started).Milliseconds()))
	traceID := traceIDFromContext(req.Context())
	if protoMode {
		writeConnectProto(w, http.StatusOK, encodeCommandResponseProto(encoded, traceID))
		return
	}
	writeConnectJSON(w, http.StatusOK, commandResponse{PayloadJSON: base64.StdEncoding.EncodeToString(encoded), TraceID: traceID})
}

func (h *Handler) handleCommand(w http.ResponseWriter, req *http.Request, method string) {
	started := time.Now()
	span := trace.SpanFromContext(req.Context())
	span.SetAttributes(attribute.String("rpc.method", method))
	if h.controller == nil {
		span.SetStatus(codes.Error, "session controller unavailable")
		span.AddEvent("connect.error", trace.WithAttributes(attribute.String("error.code", "unavailable")))
		writeConnectError(w, http.StatusServiceUnavailable, "unavailable", "session controller unavailable")
		return
	}
	proto := requestWantsProto(req.Header.Get("Content-Type"))
	span.SetAttributes(attribute.Bool("connect.proto", proto))
	var body commandRequest
	if proto {
		data, err := readProtoBody(req.Body)
		if err != nil {
			span.SetStatus(codes.Error, "invalid protobuf")
			span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_protobuf")))
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
		body, err = decodeCommandRequestProto(method, data)
		if err != nil {
			span.SetStatus(codes.Error, "invalid protobuf")
			span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_protobuf")))
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
	} else if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		span.SetStatus(codes.Error, "invalid json")
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_json")))
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid json")
		return
	}
	sessionID, err := body.sessionID()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_session_id")))
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	span.SetAttributes(attribute.String("session.id", sessionID.String()))
	span.AddEvent("connect.request.decoded")
	payload, err := h.dispatchCommand(req.Context(), method, sessionID, body)
	if err != nil {
		status := statusForCommandError(err)
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.error", trace.WithAttributes(attribute.Int("http.response.status_code", status), attribute.String("error.code", codeForCommandError(err))))
		h.logger.Info("connect command", zap.String("method", method), zap.Int("status", status), zap.Int64("latency_ms", time.Since(started).Milliseconds()), zap.Error(err))
		writeConnectError(w, status, codeForCommandError(err), err.Error())
		return
	}
	span.SetAttributes(attribute.Int("connect.response_bytes", len(payload)), attribute.Int64("connect.latency_ms", time.Since(started).Milliseconds()))
	span.AddEvent("connect.response.encoded")
	h.logger.Info("connect command", zap.String("method", method), zap.Int("status", http.StatusOK), zap.Int64("latency_ms", time.Since(started).Milliseconds()))
	traceID := traceIDFromContext(req.Context())
	if proto {
		writeConnectProto(w, http.StatusOK, encodeCommandResponseProto(payload, traceID))
		return
	}
	writeConnectJSON(w, http.StatusOK, commandResponse{PayloadJSON: base64.StdEncoding.EncodeToString(payload), TraceID: traceID})
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
	case "SessionState":
		res, err := h.controller.SessionState(ctx, app.SessionStateRequest{SessionID: sessionID})
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

func (r sessionMessagesRequest) sessionID() (session.SessionID, error) {
	identity := r.Session
	if strings.TrimSpace(identity.SessionID) == "" && strings.TrimSpace(identity.SessionIDRaw) == "" {
		identity = r.SessionRaw
	}
	value := firstString(identity.SessionID, identity.SessionIDRaw)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("session.sessionId is required")
	}
	return session.ParseSessionID(value)
}

func (r commandRequest) sessionID() (session.SessionID, error) {
	identity := r.Session
	if strings.TrimSpace(identity.SessionID) == "" && strings.TrimSpace(identity.SessionIDRaw) == "" {
		identity = r.SessionRaw
	}
	value := firstString(identity.SessionID, identity.SessionIDRaw)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("session.sessionId is required")
	}
	return session.ParseSessionID(value)
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

func withConnectTraceID(payload any, traceID string) any {
	if traceID == "" || payload == nil {
		return payload
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return payload
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return payload
	}
	if _, ok := obj["traceId"]; !ok {
		encodedTraceID, err := json.Marshal(traceID)
		if err != nil {
			return payload
		}
		obj["traceId"] = encodedTraceID
	}
	return obj
}

func writeConnectProto(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/proto")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func writeConnectError(w http.ResponseWriter, status int, code, message string) {
	writeConnectJSON(w, status, connectError{Code: code, Message: message, TraceID: w.Header().Get("X-Trace-Id")})
}

func traceIDFromContext(ctx context.Context) string {
	span := trace.SpanContextFromContext(ctx)
	if !span.IsValid() {
		return ""
	}
	traceID := span.TraceID().String()
	if traceID == "00000000000000000000000000000000" {
		return ""
	}
	return traceID
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

func (h *Handler) handleSessionMessages(w http.ResponseWriter, req *http.Request) {
	ctx, span := otel.Tracer("actrail/connect").Start(req.Context(), "connect.SessionMessages")
	defer span.End()
	started := time.Now()
	if h.controller == nil {
		span.SetStatus(codes.Error, "session controller unavailable")
		span.AddEvent("connect.error", trace.WithAttributes(attribute.String("error.code", "unavailable")))
		writeConnectError(w, http.StatusServiceUnavailable, "unavailable", "session controller unavailable")
		return
	}
	protoMode := requestWantsProto(req.Header.Get("Content-Type"))
	span.SetAttributes(attribute.Bool("connect.proto", protoMode))
	body, err := decodeSessionMessagesRequest(req, protoMode)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "session_messages")))
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	sessionID, err := body.sessionID()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_session_id")))
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	span.SetAttributes(
		attribute.String("session.id", sessionID.String()),
		attribute.Int("messages.limit", body.Limit),
		attribute.Bool("messages.init", body.Init),
		attribute.Bool("messages.deferred", body.Deferred),
		attribute.Bool("messages.include_tool_details", body.IncludeToolDetails || body.IncludeToolDetailsRaw),
		attribute.Bool("messages.include_tool_events", body.IncludeToolEvents || body.IncludeToolEventsRaw),
		attribute.String("messages.event_id", firstString(body.EventID, body.EventIDRaw)),
		attribute.String("messages.tool_call_id", firstString(body.ToolCallID, body.ToolCallIDRaw)),
	)
	span.AddEvent("connect.request.decoded")
	payload, err := h.controller.SessionMessages(ctx, app.SessionMessagesRequest{
		SessionID:          sessionID,
		AfterSeq:           firstUint64(body.AfterSeq, body.AfterSeqRaw),
		BeforeSeq:          firstUint64(body.BeforeSeq, body.BeforeSeqRaw),
		Limit:              body.Limit,
		Init:               body.Init,
		Deferred:           body.Deferred,
		ActiveTurnStartSeq: firstNonZeroUint64(body.ActiveTurnStartSeq, body.ActiveTurnStartSeqRaw),
		IncludeToolDetails: body.IncludeToolDetails || body.IncludeToolDetailsRaw,
		IncludeToolEvents:  body.IncludeToolEvents || body.IncludeToolEventsRaw,
		EventID:            firstString(body.EventID, body.EventIDRaw),
		ToolCallID:         firstString(body.ToolCallID, body.ToolCallIDRaw),
	})
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.error", trace.WithAttributes(attribute.Int("http.response.status_code", statusForCommandError(err)), attribute.String("error.code", codeForCommandError(err))))
		writeConnectError(w, statusForCommandError(err), codeForCommandError(err), err.Error())
		return
	}
	span.SetAttributes(attribute.Int("messages.result_count", len(payload.Items)), attribute.Int64("connect.latency_ms", time.Since(started).Milliseconds()))
	span.AddEvent("connect.response.encoded")
	traceID := traceIDFromContext(req.Context())
	if protoMode {
		writeConnectProto(w, http.StatusOK, encodeSessionMessagesResponseProto(payload, traceID))
		return
	}
	writeConnectJSON(w, http.StatusOK, withConnectTraceID(payload, traceID))
}

func decodeSessionMessagesRequest(req *http.Request, protoMode bool) (sessionMessagesRequest, error) {
	if !protoMode {
		var body sessionMessagesRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return sessionMessagesRequest{}, fmt.Errorf("invalid json")
		}
		return body, nil
	}
	data, err := readProtoBody(req.Body)
	if err != nil {
		return sessionMessagesRequest{}, fmt.Errorf("invalid protobuf")
	}
	var msg actrailv1.SessionMessagesRequest
	if err := proto.Unmarshal(data, &msg); err != nil {
		return sessionMessagesRequest{}, fmt.Errorf("invalid protobuf")
	}
	body := sessionMessagesRequest{
		Session:            SessionIdentity{SessionID: msg.SessionId},
		Limit:              int(msg.Limit),
		Init:               msg.Init,
		Deferred:           msg.Deferred,
		ActiveTurnStartSeq: msg.ActiveTurnStartSeq,
		IncludeToolDetails: msg.IncludeToolDetails,
		IncludeToolEvents:  msg.IncludeToolEvents,
		EventID:            msg.EventId,
		ToolCallID:         msg.ToolCallId,
	}
	if msg.AfterSeq != nil {
		v := msg.GetAfterSeq()
		body.AfterSeq = &v
	}
	if msg.BeforeSeq != nil {
		v := msg.GetBeforeSeq()
		body.BeforeSeq = &v
	}
	return body, nil
}

func encodeSessionMessagesResponseProto(response app.SessionMessagesResponse, traceID string) []byte {
	msg := &actrailv1.SessionMessagesResponse{
		TailSeq: response.TailSeq,
		HasMore: response.HasMore,
		TraceId: traceID,
	}
	if response.NextBeforeSeq != nil {
		v := *response.NextBeforeSeq
		msg.NextBeforeSeq = &v
	}
	for _, event := range response.Items {
		data, err := json.Marshal(event)
		if err == nil {
			msg.EventsJson = append(msg.EventsJson, data)
		}
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil
	}
	return data
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstUint64(a, b *uint64) *uint64 {
	if a != nil {
		return a
	}
	return b
}

func firstNonZeroUint64(a, b uint64) uint64 {
	if a != 0 {
		return a
	}
	return b
}

func firstNonZeroInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}
