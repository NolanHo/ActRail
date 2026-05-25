package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	cfg.Notifications.FeishuDelay = time.Millisecond
	cfg.Notifications.FeishuTimeout = time.Second
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
	cfg.Notifications.FeishuDelay = 10 * time.Millisecond
	cfg.Notifications.FeishuTimeout = time.Second
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
