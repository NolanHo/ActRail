package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
)

type errorEnvelope struct {
	OK      bool      `json:"ok"`
	TraceID string    `json:"trace_id,omitempty"`
	Error   errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(withTraceID(w, payload))
}

func withTraceID(w http.ResponseWriter, payload any) any {
	traceID := w.Header().Get("X-Trace-Id")
	if traceID == "" || payload == nil {
		return payload
	}
	v := reflect.ValueOf(payload)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return payload
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Struct {
		if f := v.FieldByName("TraceID"); f.IsValid() && f.Kind() == reflect.String {
			if f.String() != "" {
				return payload
			}
			cp := reflect.New(v.Type()).Elem()
			cp.Set(v)
			cp.FieldByName("TraceID").SetString(traceID)
			return cp.Interface()
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return payload
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return payload
	}
	if _, ok := obj["trace_id"]; !ok {
		encodedTraceID, err := json.Marshal(traceID)
		if err != nil {
			return payload
		}
		obj["trace_id"] = encodedTraceID
	}
	return obj
}

func writeError(w http.ResponseWriter, status int, code, message, field string) {
	writeJSON(w, status, errorEnvelope{
		OK:      false,
		TraceID: w.Header().Get("X-Trace-Id"),
		Error: errorBody{
			Code:    code,
			Message: message,
			Field:   field,
		},
	})
}
