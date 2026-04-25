package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"actrail/internal/config"
	"actrail/internal/httpapi/authn"
)

func TestHandlerRejectsMissingAuthCookie(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := NewHandler(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, res.Code)
	}
	var body errorEnvelope
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "unauthorized" {
		t.Fatalf("error code = %q, want %q", body.Error.Code, "unauthorized")
	}
}

func TestHandlerRejectsUnknownProtocolVersion(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := NewHandler(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/ws?protocol_version=999", nil)
	cookie, err := authn.SessionCookie(cfg.Auth)
	if err != nil {
		t.Fatalf("SessionCookie() error = %v", err)
	}
	req.AddCookie(cookie)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, res.Code)
	}
	var body errorEnvelope
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Field != "protocol_version" {
		t.Fatalf("error field = %q, want %q", body.Error.Field, "protocol_version")
	}
}

func TestHandlerReportsNotImplementedForValidAuth(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := NewHandler(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	cookie, err := authn.SessionCookie(cfg.Auth)
	if err != nil {
		t.Fatalf("SessionCookie() error = %v", err)
	}
	req.AddCookie(cookie)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusNotImplemented {
		t.Fatalf("expected status %d, got %d", http.StatusNotImplemented, res.Code)
	}
}
