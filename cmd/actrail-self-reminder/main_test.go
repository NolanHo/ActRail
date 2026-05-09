package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseDuration(t *testing.T) {
	cases := map[string]int{
		"30":  30,
		"30s": 30,
		"10m": 600,
		"2h":  7200,
		"1d":  86400,
	}
	for input, want := range cases {
		got, err := parseDuration(input)
		if err != nil {
			t.Fatalf("parseDuration(%q) error: %v", input, err)
		}
		if got != want {
			t.Fatalf("parseDuration(%q)=%d want %d", input, got, want)
		}
	}
	if _, err := parseDuration("soon"); err == nil {
		t.Fatal("parseDuration(\"soon\") expected error")
	}
}

func TestCreateUsesSessionEnvAndPasswordLogin(t *testing.T) {
	t.Setenv("ACTRAIL_SESSION_ID", "sess-1")
	t.Setenv("ACTRAIL_AUTH_USERNAME", "nolan")
	t.Setenv("ACTRAIL_AUTH_PASSWORD", "secret")

	var sawLogin bool
	var createPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			sawLogin = true
			if r.Method != http.MethodPost {
				t.Fatalf("login method=%s", r.Method)
			}
			var loginBody map[string]any
			if err := json.NewDecoder(r.Body).Decode(&loginBody); err != nil {
				t.Fatalf("decode login body: %v", err)
			}
			if loginBody["username"] != "nolan" || loginBody["password"] != "secret" {
				t.Fatalf("login body=%#v", loginBody)
			}
			http.SetCookie(w, &http.Cookie{Name: "actrail_auth", Value: "token-1"})
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/api/scheduler/self-reminders":
			if got := r.Header.Get("Cookie"); got != "actrail_auth=token-1" {
				t.Fatalf("Cookie=%q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				t.Fatalf("decode create payload: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"self_reminder": map[string]any{
					"item_id":    "self_reminder_1",
					"session_id": createPayload["session_id"],
					"kind":       "self_reminder",
					"state":      "scheduled",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout strings.Builder
	code := run([]string{"--base-url", server.URL, "create", "--after", "10m", "--message", "check build"}, &stdout, &strings.Builder{})
	if code != 0 {
		t.Fatalf("run code=%d", code)
	}
	if !sawLogin {
		t.Fatal("expected login request")
	}
	if createPayload["session_id"] != "sess-1" {
		t.Fatalf("session_id=%v", createPayload["session_id"])
	}
	if createPayload["duration_seconds"] != float64(600) {
		t.Fatalf("duration_seconds=%v", createPayload["duration_seconds"])
	}
	if !strings.Contains(stdout.String(), `"item_id": "self_reminder_1"`) {
		t.Fatalf("stdout missing self reminder: %s", stdout.String())
	}
}

func TestListFiltersSelfReminders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/scheduler" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "50" {
			t.Fatalf("limit=%q", r.URL.Query().Get("limit"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"items": []map[string]any{
				{"item_id": "self_reminder_1", "session_id": "sess-1", "kind": "self_reminder", "state": "scheduled"},
				{"item_id": "self_reminder_2", "session_id": "sess-2", "kind": "self_reminder", "state": "scheduled"},
				{"item_id": "supervisor_1", "session_id": "sess-1", "kind": "supervisor", "state": "scheduled"},
			},
		})
	}))
	defer server.Close()

	var stdout strings.Builder
	code := run([]string{"--base-url", server.URL, "list", "--limit", "50", "--session-id", "sess-1", "--state", "scheduled"}, &stdout, &strings.Builder{})
	if code != 0 {
		t.Fatalf("run code=%d", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "self_reminder_1") {
		t.Fatalf("stdout missing expected item: %s", output)
	}
	if strings.Contains(output, "self_reminder_2") || strings.Contains(output, "supervisor_1") {
		t.Fatalf("stdout contains unfiltered items: %s", output)
	}
}
