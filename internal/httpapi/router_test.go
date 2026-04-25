package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"actrail/internal/app"
	"actrail/internal/config"
	"actrail/internal/ws"
)

func TestBootstrapRoute(t *testing.T) {
	cfg := config.Load()
	h := New(cfg, app.NewStub(cfg), ws.NewHandler(cfg))
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}

	var body struct {
		ProtocolVersion int `json:"protocol_version"`
		WS              struct {
			URL string `json:"url"`
		} `json:"ws"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ProtocolVersion != 1 {
		t.Fatalf("expected protocol version 1, got %d", body.ProtocolVersion)
	}
	if body.WS.URL != "/api/ws" {
		t.Fatalf("expected websocket path /api/ws, got %q", body.WS.URL)
	}
}

func TestSessionsRouteReturnsEmptyCatalog(t *testing.T) {
	cfg := config.Load()
	h := New(cfg, app.NewStub(cfg), ws.NewHandler(cfg))
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}

	var body struct {
		Items []any `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 0 {
		t.Fatalf("expected no sessions, got %d", len(body.Items))
	}
}

func TestRouteSkeletonPreservesUnsupportedEndpoints(t *testing.T) {
	cfg := config.Load()
	h := New(cfg, app.NewStub(cfg), ws.NewHandler(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/rename", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusNotImplemented {
		t.Fatalf("expected status %d, got %d", http.StatusNotImplemented, res.Code)
	}
}
