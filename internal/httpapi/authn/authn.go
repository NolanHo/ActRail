package authn

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"

	"actrail/internal/config"
)

const sessionTokenNamespace = "actrail-auth-session"

func Configured(cfg config.Auth) bool {
	return cfg.Mode() == config.AuthModePassword
}

func PasswordMatches(cfg config.Auth, password string) bool {
	expected := cfg.Password
	if expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(expected)) == 1
}

func Authenticated(req *http.Request, cfg config.Auth) bool {
	if !Configured(cfg) {
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
	sum := sha256.Sum256([]byte(sessionTokenNamespace + ":" + cfg.Password))
	return hex.EncodeToString(sum[:]), nil
}
