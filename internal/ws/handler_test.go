package ws

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"actrail/internal/config"
)

func TestHandlerRejectsUnknownProtocolVersion(t *testing.T) {
	h := NewHandler(config.Load())
	req := httptest.NewRequest(http.MethodGet, "/api/ws?protocol_version=999", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, res.Code)
	}
}

func TestHandlerReportsNotImplemented(t *testing.T) {
	h := NewHandler(config.Load())
	req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusNotImplemented {
		t.Fatalf("expected status %d, got %d", http.StatusNotImplemented, res.Code)
	}
}
