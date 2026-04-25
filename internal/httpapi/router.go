package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"actrail/internal/app"
	"actrail/internal/config"
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

type loginResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
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
	_, err := req.Cookie(r.cfg.Auth.CookieName)
	writeJSON(w, http.StatusOK, authStatus{OK: err == nil})
}

func (r Router) login(w http.ResponseWriter, req *http.Request) {
	var body loginRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil && !errors.Is(err, http.ErrBodyNotAllowed) {
		writeJSON(w, http.StatusBadRequest, loginResponse{OK: false, Error: "invalid json"})
		return
	}
	if body.Password == "" {
		writeJSON(w, http.StatusBadRequest, loginResponse{OK: false, Error: "password required"})
		return
	}
	writeJSON(w, http.StatusNotImplemented, loginResponse{OK: false, Error: "login not implemented"})
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
	payload, err := r.app.ListSessions(req.Context(), app.ListSessionsRequest{
		GroupKey:    req.URL.Query().Get("group_key"),
		Offset:      queryInt(req, "offset"),
		Limit:       queryInt(req, "limit"),
		GroupOffset: queryInt(req, "group_offset"),
		GroupLimit:  queryInt(req, "group_limit"),
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
		}
		writeError(w, status, appErr.Code, appErr.Message, appErr.Field)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
}

func queryInt(req *http.Request, key string) int {
	v := req.URL.Query().Get(key)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}
