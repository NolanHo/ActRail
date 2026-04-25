package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"actrail/internal/app"
	"actrail/internal/config"
	"actrail/internal/domain/session"
	"actrail/internal/httpapi/authn"
	"actrail/internal/ws"
)

type serviceStub struct {
	base              *app.Stub
	listSessionsFunc  func(context.Context, app.ListSessionsRequest) (app.ListSessionsResponse, error)
	createSessionFunc func(context.Context, app.CreateSessionRequest) (app.CreateSessionResponse, error)
	detailsFunc       func(context.Context, app.SessionDetailsRequest) (app.SessionDetailsResponse, error)
	messagesFunc      func(context.Context, app.SessionMessagesRequest) (app.SessionMessagesResponse, error)
	stateFunc         func(context.Context, app.SessionStateRequest) (app.SessionStateResponse, error)
	workspaceFunc     func(context.Context, app.SessionWorkspaceRequest) (app.SessionWorkspaceResponse, error)
	fileListFunc      func(context.Context, app.WorkspaceFileListRequest) (app.WorkspaceFileListResponse, error)
	fileReadFunc      func(context.Context, app.WorkspaceFileReadRequest) (app.WorkspaceFileReadResponse, error)
	gitFunc           func(context.Context, app.GitFileVersionsRequest) (app.GitFileVersionsResponse, error)
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

func (s serviceStub) SessionDetails(ctx context.Context, req app.SessionDetailsRequest) (app.SessionDetailsResponse, error) {
	if s.detailsFunc != nil {
		return s.detailsFunc(ctx, req)
	}
	return s.base.SessionDetails(ctx, req)
}

func (s serviceStub) SessionMessages(ctx context.Context, req app.SessionMessagesRequest) (app.SessionMessagesResponse, error) {
	if s.messagesFunc != nil {
		return s.messagesFunc(ctx, req)
	}
	return s.base.SessionMessages(ctx, req)
}

func (s serviceStub) SessionState(ctx context.Context, req app.SessionStateRequest) (app.SessionStateResponse, error) {
	if s.stateFunc != nil {
		return s.stateFunc(ctx, req)
	}
	return s.base.SessionState(ctx, req)
}

func (s serviceStub) SessionWorkspace(ctx context.Context, req app.SessionWorkspaceRequest) (app.SessionWorkspaceResponse, error) {
	if s.workspaceFunc != nil {
		return s.workspaceFunc(ctx, req)
	}
	return s.base.SessionWorkspace(ctx, req)
}

func (s serviceStub) WorkspaceFileList(ctx context.Context, req app.WorkspaceFileListRequest) (app.WorkspaceFileListResponse, error) {
	if s.fileListFunc != nil {
		return s.fileListFunc(ctx, req)
	}
	return s.base.WorkspaceFileList(ctx, req)
}

func (s serviceStub) WorkspaceFileRead(ctx context.Context, req app.WorkspaceFileReadRequest) (app.WorkspaceFileReadResponse, error) {
	if s.fileReadFunc != nil {
		return s.fileReadFunc(ctx, req)
	}
	return s.base.WorkspaceFileRead(ctx, req)
}

func (s serviceStub) GitFileVersions(ctx context.Context, req app.GitFileVersionsRequest) (app.GitFileVersionsResponse, error) {
	if s.gitFunc != nil {
		return s.gitFunc(ctx, req)
	}
	return s.base.GitFileVersions(ctx, req)
}

type fixtureService struct {
	listReq      app.ListSessionsRequest
	createReq    app.CreateSessionRequest
	detailsReq   app.SessionDetailsRequest
	messagesReq  app.SessionMessagesRequest
	stateReq     app.SessionStateRequest
	workspaceReq app.SessionWorkspaceRequest
	fileListReq  app.WorkspaceFileListRequest
	fileReadReq  app.WorkspaceFileReadRequest
	gitReq       app.GitFileVersionsRequest
}

func (s *fixtureService) Bootstrap(_ context.Context) app.BootstrapSnapshot {
	return app.BootstrapSnapshot{
		ProtocolVersion: 1,
		Capabilities: app.Capabilities{
			WSRealtime:     true,
			Voice:          false,
			Harness:        false,
			Notifications:  false,
			PIUI:           true,
			WorkspaceRead:  true,
			WorkspaceWrite: false,
		},
		WS: app.WSConfig{
			URL:                 "/api/ws",
			HeartbeatIntervalMS: 15000,
			ResumeBufferEvents:  500,
		},
		LaunchDefaults: app.LaunchConfig{
			DefaultBackend:    "pi",
			AvailableBackends: []string{"pi", "codex"},
			Providers:         []string{},
			Models:            []string{},
		},
		UI: app.UIConfig{DeferredFeatures: []string{"voice", "harness", "notifications"}},
	}
}

func (s *fixtureService) ListSessions(_ context.Context, req app.ListSessionsRequest) (app.ListSessionsResponse, error) {
	s.listReq = req
	return app.ListSessionsResponse{
		Items: []app.SessionSummary{{
			SessionID:     "s_123",
			RuntimeID:     "r_123",
			ThreadID:      "t_123",
			AgentBackend:  "pi",
			Title:         "Current task",
			CWD:           "/root/code/ActRail",
			Busy:          true,
			LastUpdatedTS: 1760000000,
			Historical:    false,
		}},
		RemainingCount: 0,
		GroupKey:       nil,
	}, nil
}

func (s *fixtureService) CreateSession(_ context.Context, req app.CreateSessionRequest) (app.CreateSessionResponse, error) {
	s.createReq = req
	return app.CreateSessionResponse{
		OK: true,
		Session: &app.CreatedSession{
			SessionID:    "s_123",
			RuntimeID:    "r_123",
			ThreadID:     "t_123",
			AgentBackend: req.AgentBackend,
			CWD:          req.CWD,
			Busy:         false,
		},
		WSAttach: &app.SessionAttachRequest{
			SessionID:            "s_123",
			SuggestSubscriptions: []string{"session:s_123"},
		},
	}, nil
}

func (s *fixtureService) SessionDetails(_ context.Context, req app.SessionDetailsRequest) (app.SessionDetailsResponse, error) {
	s.detailsReq = req
	return app.SessionDetailsResponse{
		SessionID:      req.SessionID.String(),
		RuntimeID:      "r_123",
		ThreadID:       "t_123",
		Title:          "Current task",
		CWD:            "/root/code/ActRail",
		AgentBackend:   "pi",
		Provider:       "openrouter",
		Model:          "gpt-test",
		Busy:           true,
		QueueLength:    1,
		LastUpdatedTS:  1760000001,
		LastActivityTS: 1760000002,
		Historical:     false,
		Capabilities: app.SessionCapabilitySnapshot{
			WSRealtime:     true,
			Voice:          false,
			Harness:        false,
			Notifications:  false,
			PIUI:           true,
			WorkspaceRead:  true,
			WorkspaceWrite: false,
		},
	}, nil
}

func (s *fixtureService) SessionMessages(_ context.Context, req app.SessionMessagesRequest) (app.SessionMessagesResponse, error) {
	s.messagesReq = req
	next := uint64(100)
	return app.SessionMessagesResponse{
		Items: []app.SessionMessage{{
			Seq:  101,
			Role: "assistant",
			Kind: "message",
			Text: "...",
			TS:   1760000000,
		}},
		NextBeforeSeq: &next,
		HasMore:       true,
		TailSeq:       180,
	}, nil
}

func (s *fixtureService) SessionState(_ context.Context, req app.SessionStateRequest) (app.SessionStateResponse, error) {
	s.stateReq = req
	return app.SessionStateResponse{
		Busy: true,
		Queue: app.SessionQueueSnapshot{Items: []app.QueuedPromptSnapshot{{
			ID:    "q_1",
			Text:  "queued prompt",
			State: "queued",
		}}},
		UIRequest: &app.SessionUIRequestSnapshot{
			RequestID: "ui_123",
			Kind:      "prompt",
			Prompt:    "Need input",
		},
		PartialAssistantTurn: &app.PartialAssistantTurnSnapshot{
			TurnID: "turn_123",
			Text:   "partial",
		},
		TailSeq: 180,
		ResumeCursors: app.SessionResumeCursors{
			Session:   "cur_session",
			UI:        "cur_ui",
			Transport: "cur_transport",
		},
	}, nil
}

func (s *fixtureService) SessionWorkspace(_ context.Context, req app.SessionWorkspaceRequest) (app.SessionWorkspaceResponse, error) {
	s.workspaceReq = req
	return app.SessionWorkspaceResponse{
		RootPath:     "/root/code/ActRail",
		SelectedPath: "README.md",
		OpenPaths:    []string{"README.md", "go.mod"},
		HistoryItems: []app.WorkspaceHistoryItem{{Path: "README.md", Label: "Current"}},
	}, nil
}

func (s *fixtureService) WorkspaceFileList(_ context.Context, req app.WorkspaceFileListRequest) (app.WorkspaceFileListResponse, error) {
	s.fileListReq = req
	return app.WorkspaceFileListResponse{
		RootPath: "/root/code/ActRail",
		Path:     req.Path,
		Items: []app.WorkspaceFileEntry{{
			Path:      "internal/httpapi/router.go",
			Name:      "router.go",
			Kind:      "file",
			SizeBytes: 1024,
		}},
		Truncated: false,
	}, nil
}

func (s *fixtureService) WorkspaceFileRead(_ context.Context, req app.WorkspaceFileReadRequest) (app.WorkspaceFileReadResponse, error) {
	s.fileReadReq = req
	return app.WorkspaceFileReadResponse{
		Path:     req.Path,
		Kind:     "text",
		MIMEType: "text/plain",
		Encoding: "utf-8",
		Text:     "package main",
	}, nil
}

func (s *fixtureService) GitFileVersions(_ context.Context, req app.GitFileVersionsRequest) (app.GitFileVersionsResponse, error) {
	s.gitReq = req
	return app.GitFileVersionsResponse{
		Path: req.Path,
		Items: []app.GitFileVersion{{
			VersionID:  "head",
			Label:      "HEAD",
			CommitHash: "abc1234",
			Author:     "ActRail",
			CommitTS:   1760000000,
			Message:    "Current version",
			Current:    true,
		}},
	}, nil
}

func newTestRouter(cfg config.Config, svc app.Service) http.Handler {
	return New(cfg, svc, ws.NewHandler(cfg))
}

func TestBootstrapRoute(t *testing.T) {
	h := newTestRouter(config.Load(), &fixtureService{})
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
	decodeJSON(t, res, &body)
	if body.ProtocolVersion != 1 {
		t.Fatalf("expected protocol version 1, got %d", body.ProtocolVersion)
	}
	if body.WS.URL != "/api/ws" {
		t.Fatalf("expected websocket path /api/ws, got %q", body.WS.URL)
	}
}

func TestSnapshotRoutesReturnContractShapes(t *testing.T) {
	svc := &fixtureService{}
	h := newTestRouter(config.Load(), svc)

	t.Run("list sessions", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions?offset=2&limit=10&group_offset=1&group_limit=5", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}

		var body app.ListSessionsResponse
		decodeJSON(t, res, &body)
		if len(body.Items) != 1 || body.Items[0].SessionID != "s_123" {
			t.Fatalf("unexpected session payload: %+v", body.Items)
		}
		if svc.listReq.Offset != 2 || svc.listReq.Limit != 10 || svc.listReq.GroupOffset != 1 || svc.listReq.GroupLimit != 5 {
			t.Fatalf("unexpected list request: %+v", svc.listReq)
		}
	})

	t.Run("create session", func(t *testing.T) {
		body := bytes.NewBufferString(`{"agent_backend":"pi","cwd":"/root/code/ActRail"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		if res.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
		}

		var payload app.CreateSessionResponse
		decodeJSON(t, res, &payload)
		if !payload.OK || payload.Session == nil || payload.WSAttach == nil {
			t.Fatalf("unexpected create payload: %+v", payload)
		}
		if svc.createReq.AgentBackend != "pi" || svc.createReq.CWD != "/root/code/ActRail" {
			t.Fatalf("unexpected create request: %+v", svc.createReq)
		}
	})

	t.Run("session details", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/details", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.SessionDetailsResponse
		decodeJSON(t, res, &payload)
		if payload.SessionID != "s_123" || payload.AgentBackend != "pi" {
			t.Fatalf("unexpected details payload: %+v", payload)
		}
	})

	t.Run("session messages", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/messages?before_seq=120&limit=20&init=true", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.SessionMessagesResponse
		decodeJSON(t, res, &payload)
		if len(payload.Items) != 1 || payload.Items[0].Seq != 101 || payload.TailSeq != 180 {
			t.Fatalf("unexpected messages payload: %+v", payload)
		}
		if svc.messagesReq.BeforeSeq == nil || *svc.messagesReq.BeforeSeq != 120 || svc.messagesReq.Limit != 20 || !svc.messagesReq.Init {
			t.Fatalf("unexpected messages request: %+v", svc.messagesReq)
		}
	})

	t.Run("session state", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/state", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.SessionStateResponse
		decodeJSON(t, res, &payload)
		if !payload.Busy || len(payload.Queue.Items) != 1 || payload.ResumeCursors.Transport != "cur_transport" {
			t.Fatalf("unexpected state payload: %+v", payload)
		}
	})

	t.Run("session workspace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/workspace", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.SessionWorkspaceResponse
		decodeJSON(t, res, &payload)
		if payload.RootPath != "/root/code/ActRail" || len(payload.OpenPaths) != 2 {
			t.Fatalf("unexpected workspace payload: %+v", payload)
		}
	})

	t.Run("workspace file list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/file/list?path=internal/httpapi&search=router&limit=25", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.WorkspaceFileListResponse
		decodeJSON(t, res, &payload)
		if payload.Path != "internal/httpapi" || len(payload.Items) != 1 {
			t.Fatalf("unexpected file list payload: %+v", payload)
		}
		if svc.fileListReq.Path != "internal/httpapi" || svc.fileListReq.Search != "router" || svc.fileListReq.Limit != 25 {
			t.Fatalf("unexpected file list request: %+v", svc.fileListReq)
		}
	})

	t.Run("workspace file read", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/file/read?path=./internal/httpapi/../httpapi/router.go", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.WorkspaceFileReadResponse
		decodeJSON(t, res, &payload)
		if payload.Path != "internal/httpapi/router.go" || payload.Kind != "text" {
			t.Fatalf("unexpected file read payload: %+v", payload)
		}
		if svc.fileReadReq.Path != "internal/httpapi/router.go" {
			t.Fatalf("expected normalized path, got %+v", svc.fileReadReq)
		}
	})

	t.Run("git file versions", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s_123/git/file_versions?path=go.mod", nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)

		var payload app.GitFileVersionsResponse
		decodeJSON(t, res, &payload)
		if payload.Path != "go.mod" || len(payload.Items) != 1 || payload.Items[0].Label != "HEAD" {
			t.Fatalf("unexpected git versions payload: %+v", payload)
		}
		if svc.gitReq.Path != "go.mod" {
			t.Fatalf("unexpected git request: %+v", svc.gitReq)
		}
	})
}

func TestRouteValidationReturnsErrorEnvelope(t *testing.T) {
	h := newTestRouter(config.Load(), app.NewStub(config.Load()))

	cases := []struct {
		name   string
		method string
		target string
		body   string
		status int
		code   string
		field  string
	}{
		{name: "list sessions offset not integer", method: http.MethodGet, target: "/api/sessions?offset=nope", status: http.StatusBadRequest, code: "invalid_request", field: "offset"},
		{name: "list sessions limit negative", method: http.MethodGet, target: "/api/sessions?limit=-1", status: http.StatusBadRequest, code: "invalid_request", field: "limit"},
		{name: "list sessions group offset not integer", method: http.MethodGet, target: "/api/sessions?group_offset=x", status: http.StatusBadRequest, code: "invalid_request", field: "group_offset"},
		{name: "list sessions group limit negative", method: http.MethodGet, target: "/api/sessions?group_limit=-2", status: http.StatusBadRequest, code: "invalid_request", field: "group_limit"},
		{name: "create session missing cwd", method: http.MethodPost, target: "/api/sessions", body: `{"agent_backend":"pi"}`, status: http.StatusBadRequest, code: "invalid_request", field: "cwd"},
		{name: "details invalid session id", method: http.MethodGet, target: "/api/sessions/%20/details", status: http.StatusBadRequest, code: "invalid_request", field: "session_id"},
		{name: "messages invalid before seq", method: http.MethodGet, target: "/api/sessions/s_123/messages?before_seq=nope", status: http.StatusBadRequest, code: "invalid_request", field: "before_seq"},
		{name: "messages invalid init", method: http.MethodGet, target: "/api/sessions/s_123/messages?init=nope", status: http.StatusBadRequest, code: "invalid_request", field: "init"},
		{name: "file read missing path", method: http.MethodGet, target: "/api/sessions/s_123/file/read", status: http.StatusBadRequest, code: "invalid_request", field: "path"},
		{name: "file read absolute path", method: http.MethodGet, target: "/api/sessions/s_123/file/read?path=/etc/passwd", status: http.StatusBadRequest, code: "invalid_request", field: "path"},
		{name: "git file versions path escape", method: http.MethodGet, target: "/api/sessions/s_123/git/file_versions?path=../go.mod", status: http.StatusBadRequest, code: "invalid_request", field: "path"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = bytes.NewBuffer(nil)
			}
			req := httptest.NewRequest(tc.method, tc.target, body)
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			assertErrorEnvelope(t, res, tc.status, tc.code, tc.field)
		})
	}
}

func TestRouteHelpersAcceptHistoricalSessionIDs(t *testing.T) {
	historical, err := session.NewHistoricalIdentity("s_123", "pi")
	if err != nil {
		t.Fatalf("create historical identity: %v", err)
	}
	svc := &fixtureService{}
	h := newTestRouter(config.Load(), svc)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+historical.HTTPRouteKey()+"/details", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}
	if svc.detailsReq.SessionID.String() != historical.HTTPRouteKey() {
		t.Fatalf("expected historical session id %q, got %q", historical.HTTPRouteKey(), svc.detailsReq.SessionID)
	}
}

func TestMeRequiresValidCookieValue(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: cfg.Auth.CookieName, Value: "wrong"})
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}
	var body authStatus
	decodeJSON(t, res, &body)
	if body.OK {
		t.Fatal("GET /api/me returned ok=true for invalid cookie value")
	}
}

func TestMeReportsTrueForConfiguredValidCookie(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	cookie, err := authn.SessionCookie(cfg.Auth)
	if err != nil {
		t.Fatalf("SessionCookie() error = %v", err)
	}
	req.AddCookie(cookie)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	var body authStatus
	decodeJSON(t, res, &body)
	if !body.OK {
		t.Fatal("GET /api/me returned ok=false for valid cookie")
	}
}

func TestLoginRejectsInvalidPasswordWithSharedErrorEnvelope(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"wrong"}`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, res.Code)
	}
	var body errorEnvelope
	decodeJSON(t, res, &body)
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
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password"`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, res.Code)
	}
	var body errorEnvelope
	decodeJSON(t, res, &body)
	if body.Error.Code != "invalid_request" {
		t.Fatalf("error code = %q, want %q", body.Error.Code, "invalid_request")
	}
}

func TestLoginSetsAuthCookieOnSuccess(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := newTestRouter(cfg, newServiceStub(cfg))
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"secret"}`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d, want 1", len(cookies))
	}
	token, err := authn.SessionToken(cfg.Auth)
	if err != nil {
		t.Fatalf("SessionToken() error = %v", err)
	}
	if cookies[0].Value != token {
		t.Fatalf("cookie value = %q, want issued auth token", cookies[0].Value)
	}
}

func TestWriteAppErrorMapsTransportResetRequiredToConflict(t *testing.T) {
	cfg := config.Load()
	svc := newServiceStub(cfg)
	svc.listSessionsFunc = func(context.Context, app.ListSessionsRequest) (app.ListSessionsResponse, error) {
		return app.ListSessionsResponse{}, &app.Error{Code: "transport_reset_required", Message: "resume cursor expired", Field: "resume_from"}
	}
	h := newTestRouter(cfg, svc)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, res.Code)
	}
	var body errorEnvelope
	decodeJSON(t, res, &body)
	if body.Error.Code != "transport_reset_required" {
		t.Fatalf("error code = %q, want %q", body.Error.Code, "transport_reset_required")
	}
}

func TestUnsupportedMetadataRoutesUseErrorEnvelope(t *testing.T) {
	h := newTestRouter(config.Load(), app.NewStub(config.Load()))
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s_123/rename", bytes.NewBufferString(`{"name":"New title"}`))
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	assertErrorEnvelope(t, res, http.StatusNotImplemented, "unsupported", "")
}

func decodeJSON(t *testing.T, res *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.NewDecoder(res.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func assertErrorEnvelope(t *testing.T, res *httptest.ResponseRecorder, status int, code, field string) {
	t.Helper()
	if res.Code != status {
		t.Fatalf("expected status %d, got %d", status, res.Code)
	}
	var body struct {
		OK    bool `json:"ok"`
		Error struct {
			Code  string `json:"code"`
			Field string `json:"field"`
		} `json:"error"`
	}
	decodeJSON(t, res, &body)
	if body.OK {
		t.Fatalf("expected ok=false, got true")
	}
	if body.Error.Code != code {
		t.Fatalf("expected error code %q, got %q", code, body.Error.Code)
	}
	if body.Error.Field != field {
		t.Fatalf("expected error field %q, got %q", field, body.Error.Field)
	}
}
