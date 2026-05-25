package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"actrail/internal/domain/session"
)

const maxFeishuReminderBodyRunes = 600

func (s *Stub) scheduleUnreadAssistantReminder(sessionID session.SessionID, msg SessionMessage) {
	if s == nil || msg.Seq == 0 || strings.TrimSpace(s.cfg.Notifications.FeishuWebhookURL) == "" {
		return
	}
	delay := s.cfg.Notifications.FeishuDelay
	if delay <= 0 {
		delay = 4 * time.Second
	}
	go func(seq uint64, body string) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		ctx, cancel := context.WithTimeout(context.Background(), s.feishuReminderTimeout())
		defer cancel()
		if !s.assistantMessageStillUnread(ctx, sessionID, seq) {
			return
		}
		_ = s.sendFeishuUnreadReminder(ctx, sessionID, body)
	}(msg.Seq, msg.Text)
}

func (s *Stub) feishuReminderTimeout() time.Duration {
	if s == nil || s.cfg.Notifications.FeishuTimeout <= 0 {
		return 5 * time.Second
	}
	return s.cfg.Notifications.FeishuTimeout
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

func (s *Stub) sendFeishuUnreadReminder(ctx context.Context, sessionID session.SessionID, body string) error {
	webhookURL := strings.TrimSpace(s.cfg.Notifications.FeishuWebhookURL)
	if webhookURL == "" {
		return nil
	}
	title := sessionID.String()
	if record, ok := s.registry.Lookup(sessionID); ok {
		title = firstNonEmptyString(record.alias, record.title, record.cwd, sessionID.String())
	}
	text := fmt.Sprintf("ActRail unread assistant reply\nSession: %s\nID: %s\n\n%s", title, sessionID, truncateFeishuReminderBody(body))
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
