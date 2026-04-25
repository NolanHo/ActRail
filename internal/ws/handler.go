package ws

import (
	"net/http"
	"strconv"

	"actrail/internal/config"
)

type Handler struct {
	cfg config.Config
}

func NewHandler(cfg config.Config) http.Handler {
	return Handler{cfg: cfg}
}

func (h Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if version := req.URL.Query().Get("protocol_version"); version != "" {
		want := strconv.Itoa(h.cfg.Protocol.Version)
		if version != want {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"invalid_request","message":"unknown protocol version","field":"protocol_version"}}`))
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"unsupported","message":"websocket transport not implemented"}}`))
}
