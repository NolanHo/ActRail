package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type codexSessionLine struct {
	Timestamp string         `json:"timestamp"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
}

const codexSessionFileMaxLineBytes = 64 << 20

func (s *Stub) codexSessionFileForRecord(record sessionRecord) (string, string, error) {
	sourcePath := strings.TrimSpace(record.importedSourcePath)
	if sourcePath != "" {
		threadID, ok, err := codexSessionIDFromFile(context.Background(), sourcePath)
		if err != nil {
			return "", "", err
		}
		if ok {
			if want := strings.TrimSpace(record.importedBackendSessionID); want == "" || want == threadID {
				return filepath.Clean(sourcePath), threadID, nil
			}
		}
	}
	if threadID := strings.TrimSpace(record.importedBackendSessionID); threadID != "" {
		if path, ok := discoverCodexSessionFileByID(context.Background(), threadID); ok {
			return path, threadID, nil
		}
	}
	if record.runtime.codex != nil {
		_, threadID, _ := record.runtime.codex.snapshot()
		threadID = strings.TrimSpace(threadID)
		if threadID != "" {
			if path, ok := discoverCodexSessionFileByID(context.Background(), threadID); ok {
				return path, threadID, nil
			}
		}
	}
	return "", "", nil
}

func codexSessionFileSignature(path string) (string, bool) {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return "", false
	}
	info, err := os.Stat(cleaned)
	if err != nil || info.IsDir() {
		return "", false
	}
	return fmt.Sprintf("codex-session-file:%s:%d:%d", filepath.Clean(cleaned), info.Size(), info.ModTime().UnixNano()), true
}

func codexSourcePathMatchesSessionID(path, sessionID string) bool {
	got, ok, err := codexSessionIDFromFile(context.Background(), path)
	return err == nil && ok && got == strings.TrimSpace(sessionID)
}

func codexSessionIDFromFile(ctx context.Context, path string) (string, bool, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), codexSessionFileMaxLineBytes)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry codexSessionLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return "", false, err
		}
		if strings.TrimSpace(entry.Type) != "session_meta" {
			continue
		}
		id := strings.TrimSpace(stringValue(entry.Payload["id"]))
		return id, id != "", nil
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	return "", false, nil
}

func discoverCodexSessionFileByID(ctx context.Context, sessionID string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", false
	}
	root := codexSessionRoot()
	matches, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*"+sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return "", false
	}
	best := ""
	var bestMod time.Time
	for _, match := range matches {
		if err := ctx.Err(); err != nil {
			return "", false
		}
		id, ok, err := codexSessionIDFromFile(ctx, match)
		if err != nil || !ok || id != sessionID {
			continue
		}
		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			best = filepath.Clean(match)
			bestMod = info.ModTime()
		}
	}
	return best, best != ""
}

func codexSessionRoot() string {
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".codex")
		}
	}
	if home == "" {
		home = ".codex"
	}
	return filepath.Join(home, "sessions")
}

func codexSessionMessagesFromFile(ctx context.Context, path string) ([]SessionMessage, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("open codex session file %q: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), codexSessionFileMaxLineBytes)
	items := make([]SessionMessage, 0)
	lineNo := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry codexSessionLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("parse codex session file %q line %d: %w", path, lineNo, err)
		}
		if msg, ok := sessionMessageFromCodexSessionEntry(entry, lineNo); ok && !duplicateCodexSessionMessage(items, msg) {
			items = append(items, msg)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan codex session file %q: %w", path, err)
	}
	return items, nil
}

func codexSessionMessagesFromJSONLLines(ctx context.Context, sourcePath string, lines []string) ([]SessionMessage, error) {
	items := make([]SessionMessage, 0)
	for idx, raw := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lineNo := idx + 1
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var entry codexSessionLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("parse codex session history %q line %d: %w", sourcePath, lineNo, err)
		}
		if msg, ok := sessionMessageFromCodexSessionEntry(entry, lineNo); ok && !duplicateCodexSessionMessage(items, msg) {
			items = append(items, msg)
		}
	}
	return items, nil
}

func sessionMessageFromCodexSessionEntry(entry codexSessionLine, lineNo int) (SessionMessage, bool) {
	payload := entry.Payload
	if payload == nil {
		return SessionMessage{}, false
	}
	ts := codexSessionTimestamp(entry.Timestamp)
	switch strings.TrimSpace(entry.Type) {
	case "event_msg":
		switch strings.TrimSpace(stringValue(payload["type"])) {
		case "user_message":
			text := strings.TrimSpace(firstStringValue(payload["message"], payload["text"]))
			if text == "" {
				return SessionMessage{}, false
			}
			return SessionMessage{
				Seq:         uint64(lineNo),
				Role:        "user",
				Kind:        "message",
				Text:        text,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:event:user:%06d", lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
			}, true
		case "agent_message":
			text := strings.TrimSpace(firstStringValue(payload["message"], payload["text"]))
			if text == "" {
				return SessionMessage{}, false
			}
			msg := SessionMessage{
				Seq:         uint64(lineNo),
				Role:        "assistant",
				Kind:        "message",
				Text:        text,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:event:assistant:%06d", lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
			}
			if phase := strings.TrimSpace(stringValue(payload["phase"])); phase != "" {
				msg.Details = map[string]any{"phase": phase}
			}
			return msg, true
		}
	case "response_item":
		switch strings.TrimSpace(stringValue(payload["type"])) {
		case "message":
			role := strings.TrimSpace(stringValue(payload["role"]))
			if role != "assistant" && role != "user" && role != "system" && role != "developer" {
				return SessionMessage{}, false
			}
			text := strings.TrimSpace(codexContentText(payload["content"]))
			if text == "" {
				text = strings.TrimSpace(stringValue(payload["text"]))
			}
			if text == "" {
				return SessionMessage{}, false
			}
			kind := "message"
			if codexSessionResponseItemIsInjectedPrompt(role, text) {
				role = "system"
				kind = "system_prompt"
			}
			msg := SessionMessage{
				Seq:         uint64(lineNo),
				Role:        role,
				Kind:        kind,
				Text:        text,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:item:%s:%06d", role, lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
			}
			if phase := strings.TrimSpace(stringValue(payload["phase"])); phase != "" {
				msg.Details = map[string]any{"phase": phase}
			}
			return msg, true
		case "function_call":
			name := strings.TrimSpace(stringValue(payload["name"]))
			callID := strings.TrimSpace(stringValue(payload["call_id"]))
			arguments := strings.TrimSpace(stringValue(payload["arguments"]))
			if name == "" && callID == "" {
				return SessionMessage{}, false
			}
			msg := SessionMessage{
				Seq:         uint64(lineNo),
				Kind:        "tool",
				Type:        "tool",
				Text:        arguments,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:item:tool:%06d", lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
				Name:        name,
				Summary:     name,
				ToolCallID:  callID,
				Details: map[string]any{
					"name":      name,
					"arguments": arguments,
				},
			}
			return msg, true
		case "function_call_output":
			callID := strings.TrimSpace(stringValue(payload["call_id"]))
			output := strings.TrimSpace(stringValue(payload["output"]))
			if callID == "" && output == "" {
				return SessionMessage{}, false
			}
			msg := SessionMessage{
				Seq:         uint64(lineNo),
				Kind:        "tool_result",
				Type:        "tool_result",
				Text:        output,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:item:tool-result:%06d", lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
				ToolCallID:  callID,
				IsError:     boolValue(payload["is_error"]) || codexFunctionOutputIsError(output),
				Details: map[string]any{
					"output": output,
				},
			}
			return msg, true
		case "reasoning":
			summary := strings.TrimSpace(codexReasoningSummary(payload["summary"]))
			if summary == "" {
				return SessionMessage{}, false
			}
			return SessionMessage{
				Seq:         uint64(lineNo),
				Kind:        "reasoning",
				Type:        "reasoning",
				Text:        summary,
				Summary:     summary,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:item:reasoning:%06d", lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
			}, true
		}
	}
	return SessionMessage{}, false
}

func codexSessionResponseItemIsInjectedPrompt(role, text string) bool {
	normalizedRole := strings.TrimSpace(role)
	if normalizedRole == "system" || normalizedRole == "developer" {
		return true
	}
	if normalizedRole != "user" {
		return false
	}
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "# AGENTS.md instructions for ") ||
		strings.HasPrefix(trimmed, "# Instructions from AGENTS.md") ||
		strings.Contains(trimmed, "<environment_context>") ||
		strings.Contains(trimmed, "<INSTRUCTIONS>")
}

func codexSessionTimestamp(raw string) float64 {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0
	}
	return timestampSeconds(parsed)
}

func codexContentText(value any) string {
	content, ok := value.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch strings.TrimSpace(stringValue(obj["type"])) {
		case "text", "input_text", "output_text":
			if text := stringValue(obj["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "")
}

func firstStringValue(values ...any) string {
	for _, value := range values {
		if text := stringValue(value); text != "" {
			return text
		}
	}
	return ""
}

func codexFunctionOutputIsError(output string) bool {
	text := strings.TrimSpace(output)
	if text == "" {
		return false
	}
	marker := "Process exited with code "
	index := strings.LastIndex(text, marker)
	if index < 0 {
		return false
	}
	code := strings.TrimSpace(text[index+len(marker):])
	return code != "" && !strings.HasPrefix(code, "0")
}

func codexReasoningSummary(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(stringValue(item)); text != "" {
			parts = append(parts, text)
			continue
		}
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text := strings.TrimSpace(firstStringValue(obj["text"], obj["summary"])); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func duplicateCodexSessionMessage(items []SessionMessage, candidate SessionMessage) bool {
	text := strings.TrimSpace(candidate.Text)
	if text == "" || candidate.Kind != "message" {
		return false
	}
	for i := len(items) - 1; i >= 0 && i >= len(items)-4; i-- {
		item := items[i]
		if item.Kind != candidate.Kind || item.Role != candidate.Role || strings.TrimSpace(item.Text) != text {
			continue
		}
		if item.TS == 0 || candidate.TS == 0 || closeTimestamp(item.TS, candidate.TS) {
			return true
		}
	}
	return false
}
