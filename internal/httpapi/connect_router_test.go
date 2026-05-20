package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"actrail/internal/config"
	"actrail/internal/connectapi"
)

func TestConnectRoutesRequireAuth(t *testing.T) {
	cfg := config.Load()
	cfg.Auth.Password = "secret"
	h := New(cfg, newServiceStub(cfg), connectapi.NewHandler(nil, connectapi.NewBroker(10)))
	req := httptest.NewRequest(http.MethodPost, "/api/connect/actrail.v1.SessionCommandService/Send", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	assertErrorEnvelope(t, res, http.StatusUnauthorized, "unauthorized", "")
}
