package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"actrail/internal/app"
	"actrail/internal/config"
	"actrail/internal/domain/session"
	"actrail/internal/httpapi/authn"
)

type Router struct {
	cfg config.Config
	app app.Service
	ws  http.Handler
}

type authStatus struct {
	OK bool `json:"ok"`
}

type loginRequest struct {
	Password string `json:"password"`
}

func New(cfg config.Config, svc app.Service, wsHandler http.Handler) http.Handler {
	r := Router{cfg: cfg, app: svc, ws: wsHandler}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", r.healthz)
	mux.HandleFunc("GET /api/me", r.me)
	mux.HandleFunc("POST /api/login", r.login)
	mux.HandleFunc("POST /api/logout", r.logout)
	mux.HandleFunc("GET /api/bootstrap", r.bootstrap)
	mux.HandleFunc("GET /api/sessions", r.listSessions)
	mux.HandleFunc("POST /api/sessions", r.createSession)
	mux.HandleFunc("GET /api/session_resume_candidates", r.notImplemented("session resume candidates not implemented"))
	mux.HandleFunc("GET /api/sessions/{session_id}/details", r.sessionDetails)
	mux.HandleFunc("GET /api/sessions/{session_id}/messages", r.sessionMessages)
	mux.HandleFunc("GET /api/sessions/{session_id}/state", r.sessionState)
	mux.HandleFunc("GET /api/sessions/{session_id}/workspace", r.sessionWorkspace)
	mux.HandleFunc("GET /api/sessions/{session_id}/file/list", r.workspaceFileList)
	mux.HandleFunc("GET /api/sessions/{session_id}/file/read", r.workspaceFileRead)
	mux.HandleFunc("GET /api/sessions/{session_id}/git/file_versions", r.gitFileVersions)
	mux.HandleFunc("POST /api/sessions/{session_id}/rename", r.notImplemented("session rename not implemented"))
	mux.HandleFunc("POST /api/sessions/{session_id}/focus", r.notImplemented("session focus not implemented"))
	mux.HandleFunc("POST /api/sessions/{session_id}/edit", r.notImplemented("session edit not implemented"))
	mux.HandleFunc("POST /api/sessions/{session_id}/model", r.notImplemented("session model switch not implemented"))
	mux.HandleFunc("POST /api/sessions/{session_id}/delete", r.notImplemented("session delete not implemented"))
	mux.HandleFunc("POST /api/sessions/{session_id}/restart", r.notImplemented("session restart not implemented"))
	mux.HandleFunc("POST /api/sessions/{session_id}/handoff", r.notImplemented("session handoff not implemented"))
	mux.Handle("GET /api/ws", r.ws)

	return mux
}

func (r Router) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (r Router) me(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, authStatus{OK: authn.Authenticated(req, r.cfg.Auth)})
}

func (r Router) login(w http.ResponseWriter, req *http.Request) {
	var body loginRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	if strings.TrimSpace(body.Password) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "password required", "password")
		return
	}
	if !authn.Configured(r.cfg.Auth) {
		writeError(w, http.StatusNotImplemented, "unsupported", "password auth not configured", "")
		return
	}
	if !authn.PasswordMatches(r.cfg.Auth, body.Password) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid password", "")
		return
	}
	cookie, err := authn.SessionCookie(r.cfg.Auth)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
		return
	}
	http.SetCookie(w, cookie)
	writeJSON(w, http.StatusOK, authStatus{OK: true})
}

func (r Router) logout(w http.ResponseWriter, _ *http.Request) {
	cookie := &http.Cookie{
		Name:     r.cfg.Auth.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	}
	http.SetCookie(w, cookie)
	writeJSON(w, http.StatusOK, authStatus{OK: true})
}

func (r Router) bootstrap(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, r.app.Bootstrap(req.Context()))
}

func (r Router) listSessions(w http.ResponseWriter, req *http.Request) {
	offset, err := queryInt(req, "offset")
	if err != nil {
		writeAppError(w, err)
		return
	}
	limit, err := queryInt(req, "limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	groupOffset, err := queryInt(req, "group_offset")
	if err != nil {
		writeAppError(w, err)
		return
	}
	groupLimit, err := queryInt(req, "group_limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.ListSessions(req.Context(), app.ListSessionsRequest{
		GroupKey:    strings.TrimSpace(req.URL.Query().Get("group_key")),
		Offset:      offset,
		Limit:       limit,
		GroupOffset: groupOffset,
		GroupLimit:  groupLimit,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) createSession(w http.ResponseWriter, req *http.Request) {
	var body app.CreateSessionRequest
	if err := decodeJSONBody(req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	payload, err := r.app.CreateSession(req.Context(), body)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionDetails(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.SessionDetails(req.Context(), app.SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionMessages(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	beforeSeq, err := queryUint64(req, "before_seq")
	if err != nil {
		writeAppError(w, err)
		return
	}
	limit, err := queryInt(req, "limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	init, err := queryBool(req, "init")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.SessionMessages(req.Context(), app.SessionMessagesRequest{
		SessionID: sessionID,
		BeforeSeq: beforeSeq,
		Limit:     limit,
		Init:      init,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionState(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.SessionState(req.Context(), app.SessionStateRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) sessionWorkspace(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	payload, err := r.app.SessionWorkspace(req.Context(), app.SessionWorkspaceRequest{SessionID: sessionID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) workspaceFileList(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	pathValue, err := queryRelativePath(req, "path", false)
	if err != nil {
		writeAppError(w, err)
		return
	}
	limit, err := queryInt(req, "limit")
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.WorkspaceFileList(req.Context(), app.WorkspaceFileListRequest{
		SessionID: sessionID,
		Path:      pathValue,
		Search:    strings.TrimSpace(req.URL.Query().Get("search")),
		Limit:     limit,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) workspaceFileRead(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	pathValue, err := queryRelativePath(req, "path", true)
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.WorkspaceFileRead(req.Context(), app.WorkspaceFileReadRequest{
		SessionID: sessionID,
		Path:      pathValue,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) gitFileVersions(w http.ResponseWriter, req *http.Request) {
	sessionID, ok := routeSessionID(w, req)
	if !ok {
		return
	}
	pathValue, err := queryRelativePath(req, "path", true)
	if err != nil {
		writeAppError(w, err)
		return
	}
	payload, err := r.app.GitFileVersions(req.Context(), app.GitFileVersionsRequest{
		SessionID: sessionID,
		Path:      pathValue,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) notImplemented(message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "unsupported", message, "")
	}
}

func writeAppError(w http.ResponseWriter, err error) {
	var appErr *app.Error
	if errors.As(err, &appErr) {
		status := http.StatusInternalServerError
		switch appErr.Code {
		case "invalid_request":
			status = http.StatusBadRequest
		case "unauthorized":
			status = http.StatusUnauthorized
		case "forbidden":
			status = http.StatusForbidden
		case "not_found":
			status = http.StatusNotFound
		case "conflict", "transport_reset_required":
			status = http.StatusConflict
		case "unsupported":
			status = http.StatusNotImplemented
		}
		writeError(w, status, appErr.Code, appErr.Message, appErr.Field)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
}

func decodeJSONBody(req *http.Request, dst any) error {
	dec := json.NewDecoder(req.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func routeSessionID(w http.ResponseWriter, req *http.Request) (session.SessionID, bool) {
	value := strings.TrimSpace(req.PathValue("session_id"))
	sessionID, err := session.ParseSessionID(value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), "session_id")
		return "", false
	}
	return sessionID, true
}

func queryInt(req *http.Request, key string) (int, error) {
	value := strings.TrimSpace(req.URL.Query().Get(key))
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, app.Invalid(key, key+" must be an integer")
	}
	if n < 0 {
		return 0, app.Invalid(key, key+" must be non-negative")
	}
	return n, nil
}

func queryUint64(req *http.Request, key string) (*uint64, error) {
	value := strings.TrimSpace(req.URL.Query().Get(key))
	if value == "" {
		return nil, nil
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil, app.Invalid(key, key+" must be an unsigned integer")
	}
	return &n, nil
}

func queryBool(req *http.Request, key string) (bool, error) {
	value := strings.TrimSpace(req.URL.Query().Get(key))
	if value == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(value)
	if err != nil {
		return false, app.Invalid(key, key+" must be a boolean")
	}
	return v, nil
}

func queryRelativePath(req *http.Request, key string, required bool) (string, error) {
	value := strings.TrimSpace(req.URL.Query().Get(key))
	if value == "" {
		if required {
			return "", app.Invalid(key, key+" required")
		}
		return "", nil
	}
	if strings.HasPrefix(value, "/") {
		return "", app.Invalid(key, key+" must be relative")
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		if required {
			return "", app.Invalid(key, key+" must identify a file")
		}
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", app.Invalid(key, key+" escapes workspace root")
	}
	return strings.TrimPrefix(cleaned, "./"), nil
}
