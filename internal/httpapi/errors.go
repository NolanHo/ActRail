package httpapi

import (
	"encoding/json"
	"net/http"
)

type errorEnvelope struct {
	OK    bool      `json:"ok"`
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message, field string) {
	writeJSON(w, status, errorEnvelope{
		OK: false,
		Error: errorBody{
			Code:    code,
			Message: message,
			Field:   field,
		},
	})
}
