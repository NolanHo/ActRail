package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"actrail/internal/config"
)

func TestUnreadAssistantReminderSendsFeishuWebhook(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("Decode(feishu payload) error = %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config.Load()
	cfg.Notifications.FeishuWebhookURL = server.URL
	cfg.Notifications.HermesHome = filepath.Join(t.TempDir(), "missing-hermes")
	cfg.Notifications.LarkCLIChatID = ""
	cfg.Notifications.LarkCLIUserID = ""
	cfg.Notifications.ReminderDelay = time.Millisecond
	cfg.Notifications.ReminderTimeout = time.Second
	svc := newStubWithRuntime(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	title := "Unread reminder"
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir(), Title: &title})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	msg, err := svc.AppendSessionMessage(sessionID, "assistant", "message", "final answer ready")
	if err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}

	svc.scheduleUnreadAssistantReminder(sessionID, msg)

	select {
	case payload := <-requests:
		if payload["msg_type"] != "text" {
			t.Fatalf("payload[msg_type] = %v, want text", payload["msg_type"])
		}
		content, ok := payload["content"].(map[string]any)
		if !ok {
			t.Fatalf("payload[content] = %#v, want object", payload["content"])
		}
		text, _ := content["text"].(string)
		if !strings.Contains(text, "Unread reminder") || !strings.Contains(text, "final answer ready") {
			t.Fatalf("feishu text = %q, want session title and body", text)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for feishu webhook request")
	}
}

func TestUnreadAssistantReminderSkipsReadMessage(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config.Load()
	cfg.Notifications.FeishuWebhookURL = server.URL
	cfg.Notifications.HermesHome = filepath.Join(t.TempDir(), "missing-hermes")
	cfg.Notifications.LarkCLIChatID = ""
	cfg.Notifications.LarkCLIUserID = ""
	cfg.Notifications.ReminderDelay = 10 * time.Millisecond
	cfg.Notifications.ReminderTimeout = time.Second
	svc := newStubWithRuntime(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	msg, err := svc.AppendSessionMessage(sessionID, "assistant", "message", "already read")
	if err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}
	if _, err := svc.MarkSessionRead(context.Background(), MarkSessionReadRequest{SessionID: sessionID.String(), ReadSeq: msg.Seq}); err != nil {
		t.Fatalf("MarkSessionRead() error = %v", err)
	}

	svc.scheduleUnreadAssistantReminder(sessionID, msg)

	select {
	case <-requests:
		t.Fatal("unexpected feishu webhook for read message")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestUnreadAssistantReminderPrefersLarkCLI(t *testing.T) {
	oldRun := runLarkCLICommand
	defer func() { runLarkCLICommand = oldRun }()
	calls := make(chan []string, 1)
	runLarkCLICommand = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		calls <- append([]string{bin}, args...)
		return []byte(`{"ok":true}`), nil
	}

	cfg := config.Load()
	cfg.Notifications.FeishuWebhookURL = "http://127.0.0.1:1/unused"
	cfg.Notifications.HermesHome = filepath.Join(t.TempDir(), "missing-hermes")
	cfg.Notifications.LarkCLIBin = "/tmp/lark-cli"
	cfg.Notifications.LarkCLIChatID = "oc_test"
	cfg.Notifications.LarkCLIUserID = ""
	cfg.Notifications.LarkCLIAs = "bot"
	cfg.Notifications.ReminderDelay = time.Millisecond
	cfg.Notifications.ReminderTimeout = time.Second
	svc := newStubWithRuntime(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	title := "Lark reminder"
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir(), Title: &title})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	msg, err := svc.AppendSessionMessage(sessionID, "assistant", "message", "sent by lark-cli")
	if err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}

	svc.scheduleUnreadAssistantReminder(sessionID, msg)

	select {
	case args := <-calls:
		joined := strings.Join(args, "\x00")
		for _, want := range []string{"/tmp/lark-cli", "im", "+messages-send", "--as", "bot", "--text", "Lark reminder", "--chat-id", "oc_test"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("lark-cli args = %#v, missing %q", args, want)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lark-cli call")
	}
}

func TestUnreadAssistantReminderPrefersHermesFeishu(t *testing.T) {
	tokenRequests := make(chan map[string]string, 1)
	messageRequests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("Decode(token payload) error = %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			tokenRequests <- payload
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":                0,
				"tenant_access_token": "tenant-token",
			})
		case "/open-apis/im/v1/messages":
			if got := r.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Errorf("Authorization = %q, want bearer token", got)
			}
			if got := r.URL.Query().Get("receive_id_type"); got != "chat_id" {
				t.Errorf("receive_id_type = %q, want chat_id", got)
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("Decode(message payload) error = %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			messageRequests <- payload
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("FEISHU_APP_ID=cli_test\nFEISHU_APP_SECRET=secret_test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	directory := `{"platforms":{"feishu":[{"id":"oc_hermes","name":"home","type":"dm"}]}}`
	if err := os.WriteFile(filepath.Join(home, "channel_directory.json"), []byte(directory), 0o600); err != nil {
		t.Fatalf("WriteFile(channel_directory.json) error = %v", err)
	}

	oldRun := runLarkCLICommand
	defer func() { runLarkCLICommand = oldRun }()
	runLarkCLICommand = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		t.Fatalf("unexpected lark-cli call: %s %#v", bin, args)
		return nil, nil
	}

	cfg := config.Load()
	cfg.Notifications.HermesHome = home
	cfg.Notifications.HermesFeishuURL = server.URL
	cfg.Notifications.FeishuWebhookURL = "http://127.0.0.1:1/unused"
	cfg.Notifications.LarkCLIBin = "/tmp/lark-cli"
	cfg.Notifications.LarkCLIChatID = "oc_lark"
	cfg.Notifications.LarkCLIUserID = ""
	cfg.Notifications.ReminderDelay = time.Millisecond
	cfg.Notifications.ReminderTimeout = time.Second
	svc := newStubWithRuntime(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	title := "Hermes reminder"
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir(), Title: &title})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	msg, err := svc.AppendSessionMessage(sessionID, "assistant", "message", "sent by hermes")
	if err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}

	svc.scheduleUnreadAssistantReminder(sessionID, msg)

	select {
	case payload := <-tokenRequests:
		if payload["app_id"] != "cli_test" || payload["app_secret"] != "secret_test" {
			t.Fatalf("token payload = %#v, want hermes env credentials", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hermes feishu token request")
	}
	select {
	case payload := <-messageRequests:
		if payload["receive_id"] != "oc_hermes" || payload["msg_type"] != "text" {
			t.Fatalf("message payload = %#v, want hermes channel text send", payload)
		}
		content, _ := payload["content"].(string)
		if !strings.Contains(content, "Hermes reminder") || !strings.Contains(content, "sent by hermes") {
			t.Fatalf("message content = %q, want session title and body", content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hermes feishu message request")
	}
}

func TestUnreadAssistantReminderFallsBackWhenHermesFeishuFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 999, "msg": "temporary failure"})
	}))
	defer server.Close()

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("FEISHU_APP_ID=cli_test\nFEISHU_APP_SECRET=secret_test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	directory := `{"platforms":{"feishu":[{"id":"oc_hermes","name":"home","type":"dm"}]}}`
	if err := os.WriteFile(filepath.Join(home, "channel_directory.json"), []byte(directory), 0o600); err != nil {
		t.Fatalf("WriteFile(channel_directory.json) error = %v", err)
	}

	oldRun := runLarkCLICommand
	defer func() { runLarkCLICommand = oldRun }()
	calls := make(chan []string, 1)
	runLarkCLICommand = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		calls <- append([]string{bin}, args...)
		return []byte(`{"ok":true}`), nil
	}

	cfg := config.Load()
	cfg.Notifications.HermesHome = home
	cfg.Notifications.HermesFeishuURL = server.URL
	cfg.Notifications.LarkCLIBin = "/tmp/lark-cli"
	cfg.Notifications.LarkCLIChatID = "oc_lark"
	cfg.Notifications.LarkCLIUserID = ""
	cfg.Notifications.ReminderDelay = time.Millisecond
	cfg.Notifications.ReminderTimeout = time.Second
	svc := newStubWithRuntime(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	msg, err := svc.AppendSessionMessage(sessionID, "assistant", "message", "fallback body")
	if err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}

	svc.scheduleUnreadAssistantReminder(sessionID, msg)

	select {
	case args := <-calls:
		if joined := strings.Join(args, "\x00"); !strings.Contains(joined, "--chat-id\x00oc_lark") {
			t.Fatalf("lark-cli args = %#v, want fallback chat id", args)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lark-cli fallback")
	}
}
