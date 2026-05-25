package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"actrail/internal/domain/session"
)

const maxFeishuReminderBodyRunes = 600

var runLarkCLICommand = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	return cmd.CombinedOutput()
}

func (s *Stub) scheduleUnreadAssistantReminder(sessionID session.SessionID, msg SessionMessage) {
	if s == nil || msg.Seq == 0 || !s.unreadReminderConfigured() {
		return
	}
	delay := s.cfg.Notifications.ReminderDelay
	if delay <= 0 {
		delay = 4 * time.Second
	}
	go func(seq uint64, body string) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		ctx, cancel := context.WithTimeout(context.Background(), s.unreadReminderTimeout())
		defer cancel()
		if !s.assistantMessageStillUnread(ctx, sessionID, seq) {
			return
		}
		_ = s.sendUnreadReminder(ctx, sessionID, seq, body)
	}(msg.Seq, msg.Text)
}

func (s *Stub) unreadReminderConfigured() bool {
	if s == nil {
		return false
	}
	if strings.TrimSpace(s.cfg.Notifications.LarkCLIBin) != "" && (strings.TrimSpace(s.cfg.Notifications.LarkCLIChatID) != "" || strings.TrimSpace(s.cfg.Notifications.LarkCLIUserID) != "") {
		return true
	}
	return strings.TrimSpace(s.cfg.Notifications.FeishuWebhookURL) != ""
}

func (s *Stub) unreadReminderTimeout() time.Duration {
	if s == nil || s.cfg.Notifications.ReminderTimeout <= 0 {
		return 5 * time.Second
	}
	return s.cfg.Notifications.ReminderTimeout
}

func (s *Stub) assistantMessageStillUnread(ctx context.Context, sessionID session.SessionID, seq uint64) bool {
	if s == nil || seq == 0 {
		return false
	}
	record, ok := s.registry.Lookup(sessionID)
	if !ok || lastAssistantMessageSeq(record) < seq {
		return false
	}
	readSeqBySessionID, err := s.sessionReadSeqBySessionID(ctx)
	if err != nil {
		return false
	}
	return readSeqBySessionID[sessionID] < seq
}

func (s *Stub) sendUnreadReminder(ctx context.Context, sessionID session.SessionID, seq uint64, body string) error {
	if s.larkCLIReminderConfigured() {
		return s.sendLarkCLIUnreadReminder(ctx, sessionID, seq, body)
	}
	return s.sendFeishuUnreadReminder(ctx, sessionID, body)
}

func (s *Stub) larkCLIReminderConfigured() bool {
	return s != nil && strings.TrimSpace(s.cfg.Notifications.LarkCLIBin) != "" && (strings.TrimSpace(s.cfg.Notifications.LarkCLIChatID) != "" || strings.TrimSpace(s.cfg.Notifications.LarkCLIUserID) != "")
}

func (s *Stub) sendLarkCLIUnreadReminder(ctx context.Context, sessionID session.SessionID, seq uint64, body string) error {
	bin := strings.TrimSpace(s.cfg.Notifications.LarkCLIBin)
	if bin == "" {
		return nil
	}
	text := unreadReminderText(s, sessionID, body)
	identity := strings.TrimSpace(s.cfg.Notifications.LarkCLIAs)
	if identity == "" {
		identity = "bot"
	}
	args := []string{"im", "+messages-send", "--as", identity, "--text", text, "--idempotency-key", fmt.Sprintf("actrail-unread-%s-%d", sessionID, seq)}
	if chatID := strings.TrimSpace(s.cfg.Notifications.LarkCLIChatID); chatID != "" {
		args = append(args, "--chat-id", chatID)
	} else if userID := strings.TrimSpace(s.cfg.Notifications.LarkCLIUserID); userID != "" {
		args = append(args, "--user-id", userID)
	} else {
		return nil
	}
	output, err := runLarkCLICommand(ctx, bin, args...)
	if err != nil {
		return fmt.Errorf("lark-cli unread reminder failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *Stub) sendFeishuUnreadReminder(ctx context.Context, sessionID session.SessionID, body string) error {
	webhookURL := strings.TrimSpace(s.cfg.Notifications.FeishuWebhookURL)
	if webhookURL == "" {
		return nil
	}
	text := unreadReminderText(s, sessionID, body)
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu webhook returned status %d", resp.StatusCode)
	}
	return nil
}

func unreadReminderText(s *Stub, sessionID session.SessionID, body string) string {
	title := sessionID.String()
	if s != nil {
		if record, ok := s.registry.Lookup(sessionID); ok {
			title = firstNonEmptyString(record.alias, record.title, record.cwd, sessionID.String())
		}
	}
	return fmt.Sprintf("ActRail unread assistant reply\nSession: %s\nID: %s\n\n%s", title, sessionID, truncateFeishuReminderBody(body))
}

func truncateFeishuReminderBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "(empty message)"
	}
	runes := []rune(body)
	if len(runes) <= maxFeishuReminderBodyRunes {
		return body
	}
	return string(runes[:maxFeishuReminderBodyRunes]) + "..."
}
