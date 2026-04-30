package connectapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"actrail/internal/app"
	"actrail/internal/domain/session"
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
