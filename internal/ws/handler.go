package ws

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"actrail/internal/config"
	"actrail/internal/httpapi/authn"
)

type Handler struct {
	cfg config.Config
}

type errorEnvelope struct {
	OK    bool      `json:"ok"`
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func NewHandler(cfg config.Config) http.Handler {
	return Handler{cfg: cfg}
}

func (h Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !authn.Authenticated(req, h.cfg.Auth) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid auth cookie required", "")
		return
	}
	if version := strings.TrimSpace(req.URL.Query().Get("protocol_version")); version != "" {
		want := strconv.Itoa(h.cfg.Protocol.Version)
		if version != want {
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown protocol version", "protocol_version")
			return
		}
	}
	writeError(w, http.StatusNotImplemented, "unsupported", "websocket transport not implemented", "")
}

func writeError(w http.ResponseWriter, status int, code, message, field string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		OK: false,
		Error: errorBody{
			Code:    code,
			Message: message,
			Field:   field,
		},
	})
}
