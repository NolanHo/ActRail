package connectapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"actrail/internal/app"
	"actrail/internal/domain/session"
	"actrail/internal/ws"
	actrailv1 "actrail/proto/actrail/v1"
	"google.golang.org/protobuf/proto"
)

type controllerStub struct {
	sendReq     app.SendRequest
	stateReq    app.SessionStateRequest
	messagesReq app.SessionMessagesRequest
}

func (s *controllerStub) Send(_ context.Context, req app.SendRequest) (app.SendResponse, error) {
	s.sendReq = req
	return app.SendResponse{Busy: true}, nil
}
func (s *controllerStub) Enqueue(context.Context, app.EnqueueRequest) (app.EnqueueResponse, error) {
	return app.EnqueueResponse{}, nil
}
func (s *controllerStub) CancelQueue(context.Context, app.CancelQueueRequest) (app.CancelQueueResponse, error) {
	return app.CancelQueueResponse{}, nil
}
func (s *controllerStub) Interrupt(context.Context, app.InterruptRequest) (app.InterruptResponse, error) {
	return app.InterruptResponse{}, nil
}
func (s *controllerStub) RespondUI(context.Context, app.UIResponseRequest) (app.UIResponseResponse, error) {
	return app.UIResponseResponse{}, nil
}
func (s *controllerStub) SessionState(_ context.Context, req app.SessionStateRequest) (app.SessionStateResponse, error) {
	s.stateReq = req
	return app.SessionStateResponse{Busy: true, TailSeq: 3}, nil
}
func (s *controllerStub) SessionMessages(_ context.Context, req app.SessionMessagesRequest) (app.SessionMessagesResponse, error) {
	s.messagesReq = req
	return app.SessionMessagesResponse{Items: []app.SessionMessage{{Seq: 1, Role: "user", Kind: "message", Text: "hello"}}, TailSeq: 1}, nil
}

func TestSessionCommandServiceSendMapsToController(t *testing.T) {
	controller := &controllerStub{}
	h := NewHandler(controller, NewBroker(10))
	req := httptest.NewRequest(http.MethodPost, "/api/connect/actrail.v1.SessionCommandService/Send", strings.NewReader(`{"session":{"sessionId":"s_123"},"text":"hello"}`))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if controller.sendReq.SessionID != session.SessionID("s_123") || controller.sendReq.Text != "hello" {
		t.Fatalf("send req = %+v", controller.sendReq)
	}
	var body commandResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(body.PayloadJSON)
	if err != nil {
		t.Fatalf("decode payloadJson: %v", err)
	}
	if !strings.Contains(string(decoded), `"busy":true`) {
		t.Fatalf("payloadJson decoded = %s", string(decoded))
	}
}

func TestSessionCommandServiceSendAcceptsProto(t *testing.T) {
	controller := &controllerStub{}
	h := NewHandler(controller, NewBroker(10))
	body, err := proto.Marshal(&actrailv1.SendRequest{Session: &actrailv1.SessionIdentity{SessionId: "s_123"}, Text: "hello"})
	if err != nil {
		t.Fatalf("marshal request proto: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/connect/actrail.v1.SessionCommandService/Send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/connect+proto")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if got := res.Header().Get("Content-Type"); got != "application/proto" {
		t.Fatalf("Content-Type = %q, want application/proto", got)
	}
	if controller.sendReq.SessionID != session.SessionID("s_123") || controller.sendReq.Text != "hello" {
		t.Fatalf("send req = %+v", controller.sendReq)
	}
	var response actrailv1.CommandResponse
	if err := proto.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("read response proto: %v", err)
	}
	if !strings.Contains(string(response.GetPayloadJson()), `"busy":true`) {
		t.Fatalf("payload_json = %s", string(response.GetPayloadJson()))
	}
}

func TestEventServiceSubscribeWritesProtoEnvelope(t *testing.T) {
	broker := NewBroker(10)
	broker.ObserveFrame(ws.Frame{Type: ws.FrameTypeSessionState, TS: float64(time.UnixMilli(1234).UnixMilli()) / 1000, Stream: "session:s_123", Payload: map[string]any{"session_id": "s_123", "busy": true}})
	h := NewHandler(&controllerStub{}, broker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body, err := proto.Marshal(&actrailv1.SubscribeRequest{})
	if err != nil {
		t.Fatalf("marshal subscribe proto: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/connect/actrail.v1.EventService/Subscribe", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/connect+proto")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	data := res.Body.Bytes()
	if len(data) < 5 {
		t.Fatalf("response body too short: %d", len(data))
	}
	var event actrailv1.EventEnvelope
	if err := proto.Unmarshal(data[5:], &event); err != nil {
		t.Fatalf("read event proto: %v", err)
	}
	if event.GetId() != 1 || event.GetType() != string(ws.FrameTypeSessionState) || event.GetStream() != "session:s_123" {
		t.Fatalf("event = %#v", &event)
	}
	if !strings.Contains(string(event.GetPayloadJson()), `"busy":true`) {
		t.Fatalf("payload_json = %s", string(event.GetPayloadJson()))
	}
}

func TestSessionStateProto(t *testing.T) {
	stub := &controllerStub{}
	h := NewHandler(stub, NewBroker(100), nil)
	body, err := proto.Marshal(&actrailv1.SessionStateRequest{Session: &actrailv1.SessionIdentity{SessionId: "sess-1"}})
	if err != nil {
		t.Fatalf("marshal session state proto: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, connectBasePath+sessionCommandService+"/SessionState", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/connect+proto")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if stub.stateReq.SessionID.String() != "sess-1" {
		t.Fatalf("state req = %+v", stub.stateReq)
	}
	var response actrailv1.CommandResponse
	if err := proto.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !strings.Contains(string(response.GetPayloadJson()), `"tail_seq":3`) {
		t.Fatalf("payload_json = %s", string(response.GetPayloadJson()))
	}
}

func TestSessionMessagesProto(t *testing.T) {
	stub := &controllerStub{}
	h := NewHandler(stub, NewBroker(100), nil)
	after := uint64(4)
	body, err := proto.Marshal(&actrailv1.SessionMessagesRequest{SessionId: "sess-1", AfterSeq: &after, Limit: 20, Init: true, Deferred: true, ActiveTurnStartSeq: 10})
	if err != nil {
		t.Fatalf("marshal session messages proto: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, connectBasePath+sessionCommandService+"/SessionMessages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/connect+proto")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if stub.messagesReq.SessionID.String() != "sess-1" || stub.messagesReq.AfterSeq == nil || *stub.messagesReq.AfterSeq != 4 || !stub.messagesReq.Deferred || stub.messagesReq.ActiveTurnStartSeq != 10 {
		t.Fatalf("messages req = %+v", stub.messagesReq)
	}
	if got := res.Header().Get("Content-Type"); !strings.Contains(got, "application/proto") {
		t.Fatalf("Content-Type = %q", got)
	}
	var response actrailv1.SessionMessagesResponse
	if err := proto.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.TailSeq != 1 || len(response.EventsJson) != 1 {
		t.Fatalf("response = %+v", response)
	}
}
