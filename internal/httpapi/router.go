package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"actrail/internal/app"
	"actrail/internal/config"
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
	mux.HandleFunc("GET /api/sessions/{session_id}/details", r.notImplemented("session details not implemented"))
	mux.HandleFunc("GET /api/sessions/{session_id}/messages", r.notImplemented("session message snapshot not implemented"))
	mux.HandleFunc("GET /api/sessions/{session_id}/state", r.notImplemented("session state snapshot not implemented"))
	mux.HandleFunc("GET /api/sessions/{session_id}/workspace", r.notImplemented("session workspace snapshot not implemented"))
	mux.HandleFunc("GET /api/sessions/{session_id}/file/list", r.notImplemented("workspace file listing not implemented"))
	mux.HandleFunc("GET /api/sessions/{session_id}/file/read", r.notImplemented("workspace file read not implemented"))
	mux.HandleFunc("GET /api/sessions/{session_id}/git/file_versions", r.notImplemented("git file versions not implemented"))
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
	offset, err := queryNonNegativeInt(req, "offset")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), "offset")
		return
	}
	limit, err := queryNonNegativeInt(req, "limit")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), "limit")
		return
	}
	groupOffset, err := queryNonNegativeInt(req, "group_offset")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), "group_offset")
		return
	}
	groupLimit, err := queryNonNegativeInt(req, "group_limit")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), "group_limit")
		return
	}
	payload, err := r.app.ListSessions(req.Context(), app.ListSessionsRequest{
		GroupKey:    req.URL.Query().Get("group_key"),
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
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
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
		case "conflict":
			status = http.StatusConflict
		case "unsupported":
			status = http.StatusNotImplemented
		case "transport_reset_required":
			status = http.StatusConflict
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

func queryNonNegativeInt(req *http.Request, key string) (int, error) {
	value := strings.TrimSpace(req.URL.Query().Get(key))
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("query parameter %q must be an integer", key)
	}
	if n < 0 {
		return 0, fmt.Errorf("query parameter %q must be a non-negative integer", key)
	}
	return n, nil
}
