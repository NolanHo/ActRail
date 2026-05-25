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
	streamHeartbeatEvery  = 15 * time.Second
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
	AfterEventID         uint64                   `json:"afterEventId"`
	AfterEventIDRaw      uint64                   `json:"after_event_id"`
	Streams              []string                 `json:"streams"`
	StreamsRaw           []string                 `json:"stream_names"`
	Subscriptions        []subscribeStreamRequest `json:"subscriptions"`
	SubscriptionsRaw     []subscribeStreamRequest `json:"stream_subscriptions"`
	SuppressDeltaStreams map[string]bool          `json:"suppressMessageDeltaStreams"`
	SuppressDeltaRaw     map[string]bool          `json:"suppress_message_delta_streams"`
}

type subscribeStreamRequest struct {
	Name                     string `json:"name"`
	NameRaw                  string `json:"stream"`
	ResumeFrom               uint64 `json:"resumeFrom"`
	ResumeFromRaw            uint64 `json:"resume_from"`
	SuppressMessageDeltas    bool   `json:"suppressMessageDeltas"`
	SuppressMessageDeltasRaw bool   `json:"suppress_message_deltas"`
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

func (r subscribeRequest) afterEventID() uint64 {
	if r.AfterEventID != 0 {
		return r.AfterEventID
	}
	return r.AfterEventIDRaw
}

func (r subscribeRequest) streamFilters() map[string]struct{} {
	out := map[string]struct{}{}
	add := func(value string) {
		if value = strings.TrimSpace(value); value != "" {
			out[value] = struct{}{}
		}
	}
	for _, stream := range r.Streams {
		add(stream)
	}
	for _, stream := range r.StreamsRaw {
		add(stream)
	}
	for _, subscription := range append(r.Subscriptions, r.SubscriptionsRaw...) {
		add(firstString(subscription.Name, subscription.NameRaw))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r subscribeRequest) suppressMessageDeltaFilters() map[string]struct{} {
	out := map[string]struct{}{}
	add := func(stream string, suppress bool) {
		if !suppress {
			return
		}
		if stream = strings.TrimSpace(stream); stream != "" {
			out[stream] = struct{}{}
		}
	}
	for stream, suppress := range r.SuppressDeltaStreams {
		add(stream, suppress)
	}
	for stream, suppress := range r.SuppressDeltaRaw {
		add(stream, suppress)
	}
	for _, subscription := range append(r.Subscriptions, r.SubscriptionsRaw...) {
		add(firstString(subscription.Name, subscription.NameRaw), subscription.SuppressMessageDeltas || subscription.SuppressMessageDeltasRaw)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r subscribeRequest) minResumeFrom() uint64 {
	var min uint64
	consider := func(value uint64) {
		if value == 0 {
			return
		}
		if min == 0 || value < min {
			min = value
		}
	}
	for _, subscription := range append(r.Subscriptions, r.SubscriptionsRaw...) {
		consider(subscription.ResumeFrom)
		consider(subscription.ResumeFromRaw)
	}
	return min
}

func connectEventAllowed(event EventEnvelope, streams, suppressDelta map[string]struct{}) bool {
	if len(streams) > 0 {
		if _, ok := streams[event.Stream]; !ok {
			return false
		}
	}
	if event.Type == "message.delta" && len(suppressDelta) > 0 {
		if _, ok := suppressDelta[event.Stream]; ok {
			return false
		}
	}
	return true
}

func shouldLogConnectStreamEvent(event EventEnvelope) bool {
	switch event.Type {
	case "stream.heartbeat", "session.state", "message.delta", "message.generating", "message.commit":
		return false
	default:
		return true
	}
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
	requestID := connectRequestID(req)
	wireFormat := connectWireFormat(req)
	operation := req.Method + " " + req.URL.Path
	ctx, span := otel.Tracer("actrail/connect").Start(req.Context(), operation, trace.WithAttributes(
		attribute.String("http.request.method", req.Method),
		attribute.String("url.path", req.URL.Path),
		attribute.String("connect.wire_format", wireFormat),
	))
	defer span.End()
	if requestID != "" {
		span.SetAttributes(attribute.String("request.id", requestID))
		w.Header().Set("X-Request-Id", requestID)
	}
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
	wireFormat := connectWireFormat(req)
	span.SetAttributes(attribute.String("rpc.method", "ListSessions"), attribute.String("connect.wire_format", wireFormat))
	if h.controller == nil {
		span.SetStatus(codes.Error, "session controller unavailable")
		span.AddEvent("connect.error", trace.WithAttributes(attribute.String("error.code", "unavailable")))
		h.logConnect(req, "connect command", "ListSessions", "", "", wireFormat, http.StatusServiceUnavailable, started, "unavailable", errors.New("session controller unavailable"))
		writeConnectError(w, http.StatusServiceUnavailable, "unavailable", "session controller unavailable")
		return
	}
	protoMode := wireFormat == "proto"
	span.SetAttributes(attribute.Bool("connect.proto", protoMode))
	var body listSessionsRequest
	if protoMode {
		data, err := readProtoBody(req.Body)
		if err != nil {
			span.SetStatus(codes.Error, "invalid protobuf")
			span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_protobuf")))
			h.logConnect(req, "connect command", "ListSessions", "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
		decoded, err := decodeListSessionsRequestProto(data)
		if err != nil {
			span.SetStatus(codes.Error, "invalid protobuf")
			span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_protobuf")))
			h.logConnect(req, "connect command", "ListSessions", "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
		body = decoded
	} else if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		span.SetStatus(codes.Error, "invalid json")
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_json")))
		h.logConnect(req, "connect command", "ListSessions", "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
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
		h.logConnect(req, "connect command", "ListSessions", "", "", wireFormat, status, started, codeForCommandError(err), err)
		writeConnectError(w, status, codeForCommandError(err), err.Error())
		return
	}
	encoded, err := marshalPayload(payload, nil)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.error", trace.WithAttributes(attribute.String("error.code", "internal")))
		h.logConnect(req, "connect command", "ListSessions", "", "", wireFormat, http.StatusInternalServerError, started, "internal", err)
		writeConnectError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	span.SetAttributes(attribute.Int("sessions.result_count", len(payload.Items)), attribute.Int("connect.response_bytes", len(encoded)), attribute.Int64("connect.latency_ms", time.Since(started).Milliseconds()))
	span.AddEvent("connect.response.encoded")
	h.logConnect(req, "connect command", "ListSessions", "", "", wireFormat, http.StatusOK, started, "", nil)
	traceID := traceIDFromContext(req.Context())
	if protoMode {
		writeConnectProto(w, http.StatusOK, encodeCommandResponseProto(encoded, traceID))
		return
	}
	writeConnectJSON(w, http.StatusOK, withConnectTraceID(payload, traceID))
}

func (h *Handler) handleCommand(w http.ResponseWriter, req *http.Request, method string) {
	started := time.Now()
	span := trace.SpanFromContext(req.Context())
	wireFormat := connectWireFormat(req)
	span.SetAttributes(attribute.String("rpc.method", method), attribute.String("connect.wire_format", wireFormat))
	if h.controller == nil {
		span.SetStatus(codes.Error, "session controller unavailable")
		span.AddEvent("connect.error", trace.WithAttributes(attribute.String("error.code", "unavailable")))
		h.logConnect(req, "connect command", method, "", "", wireFormat, http.StatusServiceUnavailable, started, "unavailable", errors.New("session controller unavailable"))
		writeConnectError(w, http.StatusServiceUnavailable, "unavailable", "session controller unavailable")
		return
	}
	protoMode := wireFormat == "proto"
	span.SetAttributes(attribute.Bool("connect.proto", protoMode))
	var body commandRequest
	if protoMode {
		data, err := readProtoBody(req.Body)
		if err != nil {
			span.SetStatus(codes.Error, "invalid protobuf")
			span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_protobuf")))
			h.logConnect(req, "connect command", method, "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
		body, err = decodeCommandRequestProto(method, data)
		if err != nil {
			span.SetStatus(codes.Error, "invalid protobuf")
			span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_protobuf")))
			h.logConnect(req, "connect command", method, "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
	} else if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		span.SetStatus(codes.Error, "invalid json")
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_json")))
		h.logConnect(req, "connect command", method, "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid json")
		return
	}
	sessionID, err := body.sessionID()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_session_id")))
		h.logConnect(req, "connect command", method, "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
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
		h.logConnect(req, "connect command", method, sessionID.String(), "", wireFormat, status, started, codeForCommandError(err), err)
		writeConnectError(w, status, codeForCommandError(err), err.Error())
		return
	}
	span.SetAttributes(attribute.Int("connect.response_bytes", len(payload)), attribute.Int64("connect.latency_ms", time.Since(started).Milliseconds()))
	span.AddEvent("connect.response.encoded")
	h.logConnect(req, "connect command", method, sessionID.String(), "", wireFormat, http.StatusOK, started, "", nil)
	traceID := traceIDFromContext(req.Context())
	if protoMode {
		writeConnectProto(w, http.StatusOK, encodeCommandResponseProto(payload, traceID))
		return
	}
	if method == "SessionState" {
		var state app.SessionStateResponse
		if err := json.Unmarshal(payload, &state); err == nil {
			writeConnectJSON(w, http.StatusOK, withConnectTraceID(state, traceID))
			return
		}
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
	started := time.Now()
	span := trace.SpanFromContext(req.Context())
	wireFormat := connectWireFormat(req)
	span.SetAttributes(attribute.String("rpc.method", method), attribute.String("connect.wire_format", wireFormat))
	if method != "Subscribe" {
		span.SetStatus(codes.Error, "unknown event method")
		span.AddEvent("connect.error", trace.WithAttributes(attribute.String("error.code", "unimplemented")))
		h.logConnect(req, "connect stream", method, "", "", wireFormat, http.StatusNotFound, started, "unimplemented", fmt.Errorf("unknown event method %q", method))
		writeConnectError(w, http.StatusNotFound, "unimplemented", "unknown event method")
		return
	}
	protoMode := wireFormat == "proto"
	var body subscribeRequest
	if protoMode {
		data, err := readProtoBody(req.Body)
		if err != nil {
			span.SetStatus(codes.Error, "invalid protobuf")
			span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_protobuf")))
			h.logConnect(req, "connect stream", method, "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
		body, err = decodeSubscribeRequestProto(data)
		if err != nil {
			span.SetStatus(codes.Error, "invalid protobuf")
			span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_protobuf")))
			h.logConnect(req, "connect stream", method, "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
			writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid protobuf")
			return
		}
	} else if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		span.SetStatus(codes.Error, "invalid json")
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_json")))
		h.logConnect(req, "connect stream", method, "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", "invalid json")
		return
	}
	after := body.afterEventID()
	if minResume := body.minResumeFrom(); minResume > 0 && (after == 0 || minResume < after) {
		after = minResume
	}
	span.SetAttributes(attribute.Int64("connect.stream.after_event_id", int64(after)))
	streamFilters := body.streamFilters()
	suppressDeltaFilters := body.suppressMessageDeltaFilters()
	if len(streamFilters) > 0 {
		span.SetAttributes(attribute.Int("connect.stream.subscription_count", len(streamFilters)))
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	if protoMode {
		w.Header().Set("Content-Type", "application/connect+proto")
	} else {
		w.Header().Set("Content-Type", "application/connect+json")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	writeEvent := func(event EventEnvelope) bool {
		if !connectEventAllowed(event, streamFilters, suppressDeltaFilters) {
			return true
		}
		var err error
		eventSessionID := sessionIDFromStream(event.Stream)
		eventLagMS := h.now().UnixMilli() - event.UnixMillis
		if eventLagMS < 0 {
			eventLagMS = 0
		}
		if shouldLogConnectStreamEvent(event) {
			span.AddEvent("connect.stream.event", trace.WithAttributes(
				attribute.Int64("event.id", int64(event.ID)),
				attribute.String("event.type", event.Type),
				attribute.String("event.stream", event.Stream),
				attribute.Int64("event.lag_ms", eventLagMS),
				attribute.String("session.id", eventSessionID),
			))
			h.logConnectEvent(req, method, eventSessionID, event.Stream, wireFormat, event.ID, event.Type, eventLagMS, started)
		}
		if protoMode {
			err = writeConnectProtoEnvelope(w, event)
		} else {
			err = writeConnectEnvelope(w, event)
		}
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.AddEvent("connect.error", trace.WithAttributes(attribute.String("error.code", "stream_write"), attribute.String("event.stream", event.Stream), attribute.String("connect.stream.name", event.Stream), attribute.String("session.id", eventSessionID)))
			h.logConnect(req, "connect stream", method, eventSessionID, event.Stream, wireFormat, http.StatusInternalServerError, started, "stream_write", err)
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	replay, ch, unsubscribe := h.broker.SubscribeAfter(after)
	defer unsubscribe()
	heartbeat := time.NewTicker(streamHeartbeatEvery)
	defer heartbeat.Stop()
	for _, event := range replay {
		if !writeEvent(event) {
			return
		}
	}
	for {
		select {
		case <-req.Context().Done():
			h.logConnect(req, "connect stream closed", method, "", "", wireFormat, http.StatusOK, started, "", nil)
			return
		case event, ok := <-ch:
			if !ok {
				span.AddEvent("connect.stream.closed", trace.WithAttributes(attribute.String("error.code", "subscriber_slow")))
				h.logConnect(req, "connect stream closed", method, "", "", wireFormat, http.StatusOK, started, "subscriber_slow", nil)
				return
			}
			if !writeEvent(event) {
				return
			}
		case <-heartbeat.C:
			if !writeEvent(EventEnvelope{
				Type:        "stream.heartbeat",
				Stream:      "system",
				UnixMillis:  h.now().UnixMilli(),
				PayloadJSON: base64.StdEncoding.EncodeToString([]byte(`{}`)),
			}) {
				return
			}
		}
	}
}

func (r sessionMessagesRequest) sessionID() (session.SessionID, error) {
	identity := r.Session
	if identity.isZero() {
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
	if identity.isZero() {
		identity = r.SessionRaw
	}
	value := firstString(identity.RuntimeID, identity.RuntimeIDRaw, identity.SessionID, identity.SessionIDRaw)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("session.sessionId is required")
	}
	return session.ParseSessionID(value)
}

func (i SessionIdentity) isZero() bool {
	return strings.TrimSpace(i.SessionID) == "" &&
		strings.TrimSpace(i.SessionIDRaw) == "" &&
		strings.TrimSpace(i.RuntimeID) == "" &&
		strings.TrimSpace(i.RuntimeIDRaw) == ""
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

func connectWireFormat(req *http.Request) string {
	if requestWantsProto(req.Header.Get("Content-Type")) {
		return "proto"
	}
	return "json"
}

func connectRequestID(req *http.Request) string {
	for _, name := range []string{"X-Request-Id", "X-Request-ID", "X-Correlation-Id"} {
		if value := strings.TrimSpace(req.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func sessionIDFromStream(stream string) string {
	if !strings.HasPrefix(stream, "session:") {
		return ""
	}
	rest := strings.TrimPrefix(stream, "session:")
	if idx := strings.IndexByte(rest, ':'); idx >= 0 {
		rest = rest[:idx]
	}
	return strings.TrimSpace(rest)
}

func (h *Handler) logConnect(req *http.Request, message, method, sessionID, stream, wireFormat string, status int, started time.Time, errorCode string, err error) {
	fields := []zap.Field{
		zap.String("rpc.method", method),
		zap.String("connect.wire_format", wireFormat),
		zap.Int("http.response.status_code", status),
		zap.Int64("connect.latency_ms", time.Since(started).Milliseconds()),
	}
	if requestID := connectRequestID(req); requestID != "" {
		fields = append(fields, zap.String("request.id", requestID))
	}
	if traceID := traceIDFromContext(req.Context()); traceID != "" {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	if sessionID != "" {
		fields = append(fields, zap.String("session.id", sessionID))
	}
	if stream != "" {
		fields = append(fields, zap.String("event.stream", stream))
	}
	if errorCode != "" {
		fields = append(fields, zap.String("error.code", errorCode))
	}
	if err != nil {
		fields = append(fields, zap.Error(err))
	}
	h.logger.Info(message, fields...)
}

func (h *Handler) logConnectEvent(req *http.Request, method, sessionID, stream, wireFormat string, eventID uint64, eventType string, eventLagMS int64, started time.Time) {
	fields := []zap.Field{
		zap.String("rpc.method", method),
		zap.String("connect.wire_format", wireFormat),
		zap.Int64("connect.latency_ms", time.Since(started).Milliseconds()),
		zap.Uint64("event.id", eventID),
		zap.String("event.type", eventType),
		zap.String("event.stream", stream),
		zap.Int64("event.lag_ms", eventLagMS),
	}
	if requestID := connectRequestID(req); requestID != "" {
		fields = append(fields, zap.String("request.id", requestID))
	}
	if traceID := traceIDFromContext(req.Context()); traceID != "" {
		fields = append(fields, zap.String("trace_id", traceID))
	}
	if sessionID != "" {
		fields = append(fields, zap.String("session.id", sessionID))
	}
	h.logger.Info("connect stream event", fields...)
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
	if strings.Contains(err.Error(), "session runtime changed before send") {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func codeForCommandError(err error) string {
	var appErr *app.Error
	if errors.As(err, &appErr) && strings.TrimSpace(appErr.Code) != "" {
		return appErr.Code
	}
	if strings.Contains(err.Error(), "session runtime changed before send") {
		return "conflict"
	}
	return "internal"
}

func (h *Handler) handleSessionMessages(w http.ResponseWriter, req *http.Request) {
	ctx, span := otel.Tracer("actrail/connect").Start(req.Context(), "connect.SessionMessages")
	defer span.End()
	started := time.Now()
	wireFormat := connectWireFormat(req)
	if requestID := connectRequestID(req); requestID != "" {
		span.SetAttributes(attribute.String("request.id", requestID))
	}
	span.SetAttributes(attribute.String("rpc.method", "SessionMessages"), attribute.String("connect.wire_format", wireFormat))
	if h.controller == nil {
		span.SetStatus(codes.Error, "session controller unavailable")
		span.AddEvent("connect.error", trace.WithAttributes(attribute.String("error.code", "unavailable")))
		h.logConnect(req, "connect command", "SessionMessages", "", "", wireFormat, http.StatusServiceUnavailable, started, "unavailable", errors.New("session controller unavailable"))
		writeConnectError(w, http.StatusServiceUnavailable, "unavailable", "session controller unavailable")
		return
	}
	protoMode := wireFormat == "proto"
	span.SetAttributes(attribute.Bool("connect.proto", protoMode))
	body, err := decodeSessionMessagesRequest(req, protoMode)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "session_messages")))
		h.logConnect(req, "connect command", "SessionMessages", "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	sessionID, err := body.sessionID()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.AddEvent("connect.decode_error", trace.WithAttributes(attribute.String("error.type", "invalid_session_id")))
		h.logConnect(req, "connect command", "SessionMessages", "", "", wireFormat, http.StatusBadRequest, started, "invalid_argument", err)
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
		attribute.String("codex.reducer.source", "session_file"),
	)
	span.AddEvent("connect.request.decoded", trace.WithAttributes(attribute.String("codex.reducer.action", "session_messages_request")))
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
		h.logConnect(req, "connect command", "SessionMessages", sessionID.String(), "", wireFormat, statusForCommandError(err), started, codeForCommandError(err), err)
		writeConnectError(w, statusForCommandError(err), codeForCommandError(err), err.Error())
		return
	}
	span.SetAttributes(attribute.Int("messages.result_count", len(payload.Items)), attribute.Int64("connect.latency_ms", time.Since(started).Milliseconds()))
	span.AddEvent("connect.response.encoded", trace.WithAttributes(attribute.String("codex.reducer.action", "session_messages_response")))
	h.logConnect(req, "connect command", "SessionMessages", sessionID.String(), "", wireFormat, http.StatusOK, started, "", nil)
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
