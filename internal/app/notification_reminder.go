package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	if s.hermesFeishuReminderConfigured() {
		return true
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
	if s.hermesFeishuReminderConfigured() {
		if err := s.sendHermesFeishuUnreadReminder(ctx, sessionID, body); err == nil {
			return nil
		}
	}
	if s.larkCLIReminderConfigured() {
		if err := s.sendLarkCLIUnreadReminder(ctx, sessionID, seq, body); err == nil {
			return nil
		}
	}
	return s.sendFeishuUnreadReminder(ctx, sessionID, body)
}

func (s *Stub) hermesFeishuReminderConfigured() bool {
	if s == nil {
		return false
	}
	_, err := s.resolveHermesFeishuReminderConfig()
	return err == nil
}

func (s *Stub) larkCLIReminderConfigured() bool {
	return s != nil && strings.TrimSpace(s.cfg.Notifications.LarkCLIBin) != "" && (strings.TrimSpace(s.cfg.Notifications.LarkCLIChatID) != "" || strings.TrimSpace(s.cfg.Notifications.LarkCLIUserID) != "")
}

type hermesFeishuReminderConfig struct {
	AppID     string
	AppSecret string
	BaseURL   string
	ChatID    string
}

func (s *Stub) resolveHermesFeishuReminderConfig() (hermesFeishuReminderConfig, error) {
	var cfg hermesFeishuReminderConfig
	home := strings.TrimSpace(s.cfg.Notifications.HermesHome)
	if home == "" {
		return cfg, fmt.Errorf("hermes home is empty")
	}
	env, err := loadHermesEnv(filepath.Join(home, ".env"))
	if err != nil {
		return cfg, err
	}
	cfg.AppID = strings.TrimSpace(env["FEISHU_APP_ID"])
	cfg.AppSecret = strings.TrimSpace(env["FEISHU_APP_SECRET"])
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(s.cfg.Notifications.HermesFeishuURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.feishu.cn"
	}
	cfg.ChatID = strings.TrimSpace(s.cfg.Notifications.HermesFeishuChat)
	if cfg.ChatID == "" {
		cfg.ChatID = firstHermesFeishuChannel(filepath.Join(home, "channel_directory.json"))
	}
	if cfg.ChatID == "" {
		cfg.ChatID = strings.TrimSpace(env["FEISHU_HOME_CHANNEL"])
	}
	if cfg.AppID == "" || cfg.AppSecret == "" || cfg.ChatID == "" {
		return cfg, fmt.Errorf("hermes feishu reminder is incomplete")
	}
	return cfg, nil
}

func loadHermesEnv(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key != "" {
			values[key] = value
		}
	}
	return values, nil
}

func firstHermesFeishuChannel(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var directory struct {
		Platforms map[string][]struct {
			ID string `json:"id"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(raw, &directory); err != nil {
		return ""
	}
	for _, channel := range directory.Platforms["feishu"] {
		if id := strings.TrimSpace(channel.ID); id != "" {
			return id
		}
	}
	return ""
}

func (s *Stub) sendHermesFeishuUnreadReminder(ctx context.Context, sessionID session.SessionID, body string) error {
	cfg, err := s.resolveHermesFeishuReminderConfig()
	if err != nil {
		return err
	}
	token, err := requestHermesFeishuTenantToken(ctx, cfg)
	if err != nil {
		return err
	}
	return sendHermesFeishuText(ctx, cfg, token, unreadReminderText(s, sessionID, body))
}

func requestHermesFeishuTenantToken(ctx context.Context, cfg hermesFeishuReminderConfig) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"app_id":     cfg.AppID,
		"app_secret": cfg.AppSecret,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("hermes feishu token returned status %d", resp.StatusCode)
	}
	var decoded struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	if decoded.Code != 0 || strings.TrimSpace(decoded.TenantAccessToken) == "" {
		return "", fmt.Errorf("hermes feishu token failed: code=%d msg=%s", decoded.Code, decoded.Msg)
	}
	return decoded.TenantAccessToken, nil
}

func sendHermesFeishuText(ctx context.Context, cfg hermesFeishuReminderConfig, token string, text string) error {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{
		"receive_id": cfg.ChatID,
		"msg_type":   "text",
		"content":    string(content),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/open-apis/im/v1/messages?receive_id_type=chat_id", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("hermes feishu send returned status %d", resp.StatusCode)
	}
	var decoded struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	if decoded.Code != 0 {
		return fmt.Errorf("hermes feishu send failed: code=%d msg=%s", decoded.Code, decoded.Msg)
	}
	return nil
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
