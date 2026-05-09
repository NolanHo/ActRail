package authn

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"actrail/internal/config"
)

const sessionTokenNamespace = "actrail-auth-session"

func Configured(cfg config.Auth) bool {
	return cfg.Mode() == config.AuthModePassword
}

func CredentialsMatch(cfg config.Auth, username, password string) bool {
	expectedUsername := strings.TrimSpace(cfg.Username)
	expectedPassword := cfg.Password
	if expectedUsername == "" || expectedPassword == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(username)), []byte(expectedUsername)) != 1 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(expectedPassword)) == 1
}

func Authenticated(req *http.Request, cfg config.Auth) bool {
	if !Configured(cfg) {
		return true
	}
	if LocalhostRequest(req) {
		return true
	}
	token, err := SessionToken(cfg)
	if err != nil {
		return false
	}
	cookie, err := req.Cookie(cfg.CookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) == 1
}

func LocalhostRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	if !hostIsLoopback(req.Host) {
		return false
	}
	if !remoteAddrIsLoopback(req.RemoteAddr) {
		return false
	}
	return forwardedAddrsAreLoopback(req.Header.Values("X-Forwarded-For")) &&
		forwardedAddrsAreLoopback(req.Header.Values("X-Real-IP"))
}

func SessionCookie(cfg config.Auth) (*http.Cookie, error) {
	token, err := SessionToken(cfg)
	if err != nil {
		return nil, err
	}
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
	}, nil
}

func SessionToken(cfg config.Auth) (string, error) {
	if !Configured(cfg) {
		return "", fmt.Errorf("password auth is not configured")
	}
	sum := sha256.Sum256([]byte(sessionTokenNamespace + ":" + strings.TrimSpace(cfg.Username) + ":" + cfg.Password))
	return hex.EncodeToString(sum[:]), nil
}

func hostIsLoopback(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	name, _, err := net.SplitHostPort(host)
	if err == nil {
		host = name
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

func remoteAddrIsLoopback(remoteAddr string) bool {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	host = strings.Trim(host, "[]")
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

func forwardedAddrsAreLoopback(values []string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			addr, err := netip.ParseAddr(strings.Trim(part, "[]"))
			if err != nil || !addr.IsLoopback() {
				return false
			}
		}
	}
	return true
}
