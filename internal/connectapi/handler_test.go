package connectapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"actrail/internal/app"
	"actrail/internal/domain/session"
	"actrail/internal/ws"
)

type controllerStub struct {
	sendReq app.SendRequest
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
	var body []byte
	body = appendProtoMessage(body, 1, encodeSessionIdentityProto(SessionIdentity{SessionID: "s_123"}))
	body = appendProtoString(body, 2, "hello")
	req := httptest.NewRequest(http.MethodPost, "/api/connect/actrail.v1.SessionCommandService/Send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/connect+proto")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if got := res.Header().Get("Content-Type"); got != "application/connect+proto" {
		t.Fatalf("Content-Type = %q, want application/connect+proto", got)
	}
	if controller.sendReq.SessionID != session.SessionID("s_123") || controller.sendReq.Text != "hello" {
		t.Fatalf("send req = %+v", controller.sendReq)
	}
	fields, err := readProtoFields(res.Body.Bytes())
	if err != nil {
		t.Fatalf("read response proto: %v", err)
	}
	if !strings.Contains(string(protoBytes(fields, 1)), `"busy":true`) {
		t.Fatalf("payload_json = %s", string(protoBytes(fields, 1)))
	}
}

func TestEventServiceSubscribeWritesProtoEnvelope(t *testing.T) {
	broker := NewBroker(10)
	broker.ObserveFrame(ws.Frame{Type: ws.FrameTypeSessionState, TS: float64(time.UnixMilli(1234).UnixMilli()) / 1000, Stream: "session:s_123", Payload: map[string]any{"session_id": "s_123", "busy": true}})
	h := NewHandler(&controllerStub{}, broker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/connect/actrail.v1.EventService/Subscribe", bytes.NewReader(appendProtoUint64(nil, 1, 0))).WithContext(ctx)
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
	length := binary.BigEndian.Uint32(data[1:5])
	if int(length) != len(data)-5 {
		t.Fatalf("frame length = %d body=%d", length, len(data)-5)
	}
	fields, err := readProtoFields(data[5:])
	if err != nil {
		t.Fatalf("read event proto: %v", err)
	}
	if protoUint64(fields, 1) != 1 || protoString(fields, 2) != string(ws.FrameTypeSessionState) || protoString(fields, 3) != "session:s_123" {
		t.Fatalf("event fields = %#v", fields)
	}
	if !strings.Contains(string(protoBytes(fields, 5)), `"busy":true`) {
		t.Fatalf("payload_json = %s", string(protoBytes(fields, 5)))
	}
}
