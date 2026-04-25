package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"actrail/internal/app"
	"actrail/internal/config"
	"actrail/internal/httpapi/authn"
	"actrail/internal/ws"
)

type serviceStub struct {
	base              *app.Stub
	listSessionsFunc  func(context.Context, app.ListSessionsRequest) (app.ListSessionsResponse, error)
	createSessionFunc func(context.Context, app.CreateSessionRequest) (app.CreateSessionResponse, error)
}

func newServiceStub(cfg config.Config) serviceStub {
	return serviceStub{base: app.NewStub(cfg)}
}

func (s serviceStub) Bootstrap(ctx context.Context) app.BootstrapSnapshot {
	return s.base.Bootstrap(ctx)
}

func (s serviceStub) ListSessions(ctx context.Context, req app.ListSessionsRequest) (app.ListSessionsResponse, error) {
	if s.listSessionsFunc != nil {
		return s.listSessionsFunc(ctx, req)
	}
	return s.base.ListSessions(ctx, req)
}

func (s serviceStub) CreateSession(ctx context.Context, req app.CreateSessionRequest) (app.CreateSessionResponse, error) {
	if s.createSessionFunc != nil {
		return s.createSessionFunc(ctx, req)
	}
	return s.base.CreateSession(ctx, req)
}

func TestBootstrapRoute(t *testing.T) {
	cfg := config.Load()
	h := New(cfg, newServiceStub(cfg), ws.NewHandler(cfg))
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
	h := New(cfg, newServiceStub(cfg), ws.NewHandler(cfg))
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

func TestMeRequiresValidCookieValue(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := New(cfg, newServiceStub(cfg), ws.NewHandler(cfg))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: cfg.Auth.CookieName, Value: "wrong"})
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}
	var body authStatus
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.OK {
		t.Fatal("GET /api/me returned ok=true for invalid cookie value")
	}
}

func TestMeReportsTrueForConfiguredValidCookie(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := New(cfg, newServiceStub(cfg), ws.NewHandler(cfg))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	cookie, err := authn.SessionCookie(cfg.Auth)
	if err != nil {
		t.Fatalf("SessionCookie() error = %v", err)
	}
	req.AddCookie(cookie)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	var body authStatus
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.OK {
		t.Fatal("GET /api/me returned ok=false for valid cookie")
	}
}

func TestLoginRejectsInvalidPasswordWithSharedErrorEnvelope(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := New(cfg, newServiceStub(cfg), ws.NewHandler(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"wrong"}`))
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
	if body.Error.Message != "invalid password" {
		t.Fatalf("error message = %q, want %q", body.Error.Message, "invalid password")
	}
}

func TestLoginRejectsInvalidJSONWithSharedErrorEnvelope(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := New(cfg, newServiceStub(cfg), ws.NewHandler(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password"`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, res.Code)
	}
	var body errorEnvelope
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "invalid_request" {
		t.Fatalf("error code = %q, want %q", body.Error.Code, "invalid_request")
	}
}

func TestLoginSetsAuthCookieOnSuccess(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := New(cfg, newServiceStub(cfg), ws.NewHandler(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"secret"}`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}
	cookie := res.Result().Cookies()
	if len(cookie) != 1 {
		t.Fatalf("cookie count = %d, want 1", len(cookie))
	}
	token, err := authn.SessionToken(cfg.Auth)
	if err != nil {
		t.Fatalf("SessionToken() error = %v", err)
	}
	if cookie[0].Value != token {
		t.Fatalf("cookie value = %q, want issued auth token", cookie[0].Value)
	}
}

func TestSessionsRouteRejectsInvalidIntegerQueryParams(t *testing.T) {
	cfg := config.Load()
	h := New(cfg, newServiceStub(cfg), ws.NewHandler(cfg))
	for _, tc := range []struct {
		name       string
		query      string
		field      string
		wantStatus int
	}{
		{name: "offset not integer", query: "/api/sessions?offset=nope", field: "offset", wantStatus: http.StatusBadRequest},
		{name: "limit negative", query: "/api/sessions?limit=-1", field: "limit", wantStatus: http.StatusBadRequest},
		{name: "group offset not integer", query: "/api/sessions?group_offset=x", field: "group_offset", wantStatus: http.StatusBadRequest},
		{name: "group limit negative", query: "/api/sessions?group_limit=-2", field: "group_limit", wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.query, nil)
			res := httptest.NewRecorder()

			h.ServeHTTP(res, req)

			if res.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d", tc.wantStatus, res.Code)
			}
			var body errorEnvelope
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != "invalid_request" {
				t.Fatalf("error code = %q, want %q", body.Error.Code, "invalid_request")
			}
			if body.Error.Field != tc.field {
				t.Fatalf("error field = %q, want %q", body.Error.Field, tc.field)
			}
		})
	}
}

func TestWriteAppErrorMapsTransportResetRequiredToConflict(t *testing.T) {
	cfg := config.Load()
	svc := newServiceStub(cfg)
	svc.listSessionsFunc = func(context.Context, app.ListSessionsRequest) (app.ListSessionsResponse, error) {
		return app.ListSessionsResponse{}, &app.Error{Code: "transport_reset_required", Message: "resume cursor expired", Field: "resume_from"}
	}
	h := New(cfg, svc, ws.NewHandler(cfg))
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, res.Code)
	}
	var body errorEnvelope
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "transport_reset_required" {
		t.Fatalf("error code = %q, want %q", body.Error.Code, "transport_reset_required")
	}
}

func TestRouteSkeletonPreservesUnsupportedEndpoints(t *testing.T) {
	cfg := config.Load()
	h := New(cfg, newServiceStub(cfg), ws.NewHandler(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/rename", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusNotImplemented {
		t.Fatalf("expected status %d, got %d", http.StatusNotImplemented, res.Code)
	}
}
