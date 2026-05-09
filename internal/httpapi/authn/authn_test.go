package authn

import (
	"net/http/httptest"
	"testing"

	"actrail/internal/config"
)

func TestAuthenticatedBypassesPasswordAuthForLocalhostRequest(t *testing.T) {
	cfg := passwordAuthConfig()
	req := httptest.NewRequest("GET", "http://127.0.0.1:8743/api/bootstrap", nil)
	req.RemoteAddr = "127.0.0.1:43210"

	if !Authenticated(req, cfg) {
		t.Fatal("Authenticated() returned false for direct localhost request")
	}
}

func TestAuthenticatedBypassesPasswordAuthForLocalhostForwardedRequest(t *testing.T) {
	cfg := passwordAuthConfig()
	req := httptest.NewRequest("GET", "http://localhost:18743/api/bootstrap", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "::1")

	if !Authenticated(req, cfg) {
		t.Fatal("Authenticated() returned false for localhost request forwarded by local proxy")
	}
}

func TestAuthenticatedDoesNotBypassForPublicHost(t *testing.T) {
	cfg := passwordAuthConfig()
	req := httptest.NewRequest("GET", "http://115.191.57.106:18743/api/bootstrap", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")

	if Authenticated(req, cfg) {
		t.Fatal("Authenticated() returned true for public host without auth cookie")
	}
}

func TestAuthenticatedDoesNotBypassForExternalForwardedClient(t *testing.T) {
	cfg := passwordAuthConfig()
	req := httptest.NewRequest("GET", "http://localhost:18743/api/bootstrap", nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")

	if Authenticated(req, cfg) {
		t.Fatal("Authenticated() returned true for external forwarded client without auth cookie")
	}
}

func TestAuthenticatedDoesNotBypassForExternalRemoteAddr(t *testing.T) {
	cfg := passwordAuthConfig()
	req := httptest.NewRequest("GET", "http://localhost:18743/api/bootstrap", nil)
	req.RemoteAddr = "203.0.113.10:43210"

	if Authenticated(req, cfg) {
		t.Fatal("Authenticated() returned true for external remote addr without auth cookie")
	}
}

func passwordAuthConfig() config.Auth {
	return config.Auth{
		CookieName: "actrail_auth",
		Username:   "nolan",
		Password:   "secret",
	}
}
