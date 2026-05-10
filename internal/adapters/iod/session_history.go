package iod

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultSessionHistoryWarmLines = 3000
const sessionHistoryMaxLineBytes = 64 << 20

type SessionHistorySnapshot struct {
	SourcePath   string
	Lines        []string
	Messages     []SessionHistoryMessage
	IndexedCount int
	TaskComplete bool
	Warmed       bool
	Complete     bool
}

type sessionHistoryCache struct {
	mu         sync.RWMutex
	path       string
	codex      bool
	lines      []string
	messages   []SessionHistoryMessage
	indexed    []sessionHistoryIndexEntry
	taskDone   bool
	lastKind   string
	warmed     bool
	complete   bool
	lastSize   int64
	lastMod    time.Time
	lineCount  int
	loadCancel context.CancelFunc
}

type sessionHistoryIndexEntry struct {
	LineNo   uint64
	Offset   int64
	Length   int
	Role     string
	Kind     string
	TS       float64
	TextHash string
}

func newSessionHistoryCache(path string, codex bool) *sessionHistoryCache {
	trimmed := strings.TrimSpace(path)
	cache := &sessionHistoryCache{codex: codex}
	if trimmed != "" {
		cache.path = filepath.Clean(trimmed)
	}
	return cache
}

func (c *sessionHistoryCache) Start(ctx context.Context) {
	if c == nil {
		return
	}
	if strings.TrimSpace(c.currentPath()) == "" {
		return
	}
	_ = c.warmTail(defaultSessionHistoryWarmLines)
	loadCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.loadCancel = cancel
	c.mu.Unlock()
	go func() {
		_ = c.loadFull(loadCtx)
	}()
}

func (c *sessionHistoryCache) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	cancel := c.loadCancel
	c.loadCancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *sessionHistoryCache) Snapshot(ctx context.Context) (SessionHistorySnapshot, error) {
	if c == nil {
		return SessionHistorySnapshot{}, nil
	}
	if err := c.refreshIfChanged(ctx); err != nil {
		return SessionHistorySnapshot{}, err
	}
	c.mu.RLock()
	path := c.path
	lines := append([]string(nil), c.lines...)
	messages := append([]SessionHistoryMessage(nil), c.messages...)
	indexed := append([]sessionHistoryIndexEntry(nil), c.indexed...)
	taskDone := c.taskDone
	warmed := c.warmed
	complete := c.complete
	codex := c.codex
	c.mu.RUnlock()
	if codex && len(indexed) > 0 {
		var err error
		messages, err = readCodexHistoryMessagesAt(ctx, path, indexed)
		if err != nil {
			return SessionHistorySnapshot{}, err
		}
	}
	return SessionHistorySnapshot{
		SourcePath:   path,
		Lines:        lines,
		Messages:     messages,
		IndexedCount: len(indexed),
		TaskComplete: taskDone,
		Warmed:       warmed,
		Complete:     complete,
	}, nil
}

func (c *sessionHistoryCache) SetPath(ctx context.Context, path string) {
	if c == nil {
		return
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return
	}
	cleaned := filepath.Clean(trimmed)
	c.mu.Lock()
	if c.path == cleaned {
		c.mu.Unlock()
		return
	}
	if c.loadCancel != nil {
		c.loadCancel()
	}
	loadCtx, cancel := context.WithCancel(ctx)
	c.path = cleaned
	c.lines = nil
	c.messages = nil
	c.indexed = nil
	c.taskDone = false
	c.lastKind = ""
	c.warmed = false
	c.complete = false
	c.lastSize = 0
	c.lastMod = time.Time{}
	c.lineCount = 0
	c.loadCancel = cancel
	c.mu.Unlock()

	_ = c.warmTail(defaultSessionHistoryWarmLines)
	go func() {
		_ = c.loadFull(loadCtx)
	}()
}

func (c *sessionHistoryCache) currentPath() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.path
}

func (c *sessionHistoryCache) warmTail(limit int) error {
	if c == nil {
		return nil
	}
	path := c.currentPath()
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if c.codex {
		return c.loadFull(context.Background())
	}
	lines, size, mod, err := tailLines(path, limit)
	if err != nil {
		if os.IsNotExist(err) {
			c.mu.Lock()
			if c.path != path {
				c.mu.Unlock()
				return nil
			}
			c.lines = nil
			c.messages = nil
			c.indexed = nil
			c.warmed = true
			c.complete = false
			c.lastSize = 0
			c.lastMod = time.Time{}
			c.lineCount = 0
			c.lastKind = ""
			c.mu.Unlock()
			return nil
		}
		return err
	}
	c.mu.Lock()
	if c.path != path {
		c.mu.Unlock()
		return nil
	}
	c.lines = lines
	c.messages = nil
	c.indexed = nil
	c.warmed = true
	c.complete = false
	c.lastSize = size
	c.lastMod = mod
	c.lineCount = len(lines)
	c.lastKind = ""
	c.mu.Unlock()
	return nil
}

func (c *sessionHistoryCache) loadFull(ctx context.Context) error {
	if c == nil {
		return nil
	}
	path := c.currentPath()
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lines, messages, indexed, taskDone, lastKind, lineCount, size, mod, err := c.readHistoryFile(ctx, path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.path != path {
		c.mu.Unlock()
		return nil
	}
	c.lines = lines
	c.messages = messages
	c.indexed = indexed
	c.taskDone = taskDone
	c.lastKind = lastKind
	c.warmed = true
	c.complete = true
	c.lastSize = size
	c.lastMod = mod
	c.lineCount = lineCount
	c.mu.Unlock()
	return nil
}

func (c *sessionHistoryCache) refreshIfChanged(ctx context.Context) error {
	if c == nil {
		return nil
	}
	path := c.currentPath()
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat session history %q: %w", path, err)
	}
	c.mu.RLock()
	unchanged := c.warmed && info.Size() == c.lastSize && info.ModTime().Equal(c.lastMod)
	warmed := c.warmed
	codex := c.codex
	lastSize := c.lastSize
	lastLineCount := c.lineCount
	lastIndexed := append([]sessionHistoryIndexEntry(nil), c.indexed...)
	lastKind := c.lastKind
	c.mu.RUnlock()
	if unchanged {
		return nil
	}
	if codex {
		if !warmed || info.Size() < lastSize || info.Size() == lastSize {
			return c.loadFull(ctx)
		}
		lines, indexed, nextKind, lineCount, size, mod, err := readCodexHistoryRange(ctx, path, lastSize, lastLineCount, defaultSessionHistoryWarmLines, lastIndexed)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		c.mu.Lock()
		if c.path != path {
			c.mu.Unlock()
			return nil
		}
		c.lines = mergeTailLines(c.lines, lines, defaultSessionHistoryWarmLines)
		c.messages = nil
		c.indexed = indexed
		if nextKind != "" {
			lastKind = nextKind
		}
		c.lastKind = lastKind
		c.taskDone = lastKind == "task_complete"
		c.warmed = true
		c.complete = true
		c.lastSize = size
		c.lastMod = mod
		c.lineCount = lineCount
		c.mu.Unlock()
		return nil
	}
	return c.warmTail(defaultSessionHistoryWarmLines)
}

func (c *sessionHistoryCache) readHistoryFile(ctx context.Context, path string) ([]string, []SessionHistoryMessage, []sessionHistoryIndexEntry, bool, string, int, int64, time.Time, error) {
	if !c.codex {
		lines, size, mod, err := readAllLines(path)
		return lines, nil, nil, false, "", len(lines), size, mod, err
	}
	return readCodexHistoryFile(ctx, path, defaultSessionHistoryWarmLines)
}

func readAllLines(path string) ([]string, int64, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	lines := make([]string, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), sessionHistoryMaxLineBytes)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, time.Time{}, fmt.Errorf("scan session history %q: %w", path, err)
	}
	return lines, info.Size(), info.ModTime(), nil
}

type codexHistoryLine struct {
	Timestamp string         `json:"timestamp"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
}

func readCodexHistoryFile(ctx context.Context, path string, tailLimit int) ([]string, []SessionHistoryMessage, []sessionHistoryIndexEntry, bool, string, int, int64, time.Time, error) {
	lines, indexed, lastRelevant, lineCount, size, mod, err := readCodexHistoryRange(ctx, path, 0, 0, tailLimit, nil)
	return lines, nil, indexed, lastRelevant == "task_complete", lastRelevant, lineCount, size, mod, err
}

func readCodexHistoryRange(ctx context.Context, path string, startOffset int64, startLineNo int, tailLimit int, existing []sessionHistoryIndexEntry) ([]string, []sessionHistoryIndexEntry, string, int, int64, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, "", startLineNo, 0, time.Time{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, "", startLineNo, 0, time.Time{}, err
	}
	if startOffset > 0 {
		if _, err := file.Seek(startOffset, io.SeekStart); err != nil {
			return nil, nil, "", startLineNo, 0, time.Time{}, fmt.Errorf("seek codex session history %q: %w", path, err)
		}
	}
	tail := make([]string, 0, tailLimit)
	indexed := append([]sessionHistoryIndexEntry(nil), existing...)
	lastRelevant := ""
	lineNo := startLineNo
	offset := startOffset
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		raw, readErr := reader.ReadString('\n')
		if raw == "" && readErr == io.EOF {
			break
		}
		if raw == "" && readErr != nil {
			return nil, nil, "", startLineNo, 0, time.Time{}, fmt.Errorf("scan codex session history %q: %w", path, readErr)
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, "", startLineNo, 0, time.Time{}, err
		}
		lineNo++
		line := strings.TrimRight(raw, "\r\n")
		if tailLimit > 0 {
			if len(tail) == tailLimit {
				copy(tail, tail[1:])
				tail[tailLimit-1] = line
			} else {
				tail = append(tail, line)
			}
		}
		message, ok, err := codexHistoryMessageFromLine(line, lineNo)
		if err != nil {
			return nil, nil, "", startLineNo, 0, time.Time{}, err
		}
		if relevant := codexHistoryRelevantKind(line); relevant != "" {
			lastRelevant = relevant
		}
		if ok {
			index := sessionHistoryIndexEntry{
				LineNo:   uint64(lineNo),
				Offset:   offset,
				Length:   len(raw),
				Role:     message.Role,
				Kind:     message.Kind,
				TS:       message.TS,
				TextHash: codexHistoryMessageTextHash(message),
			}
			if !duplicateCodexHistoryIndex(indexed, index) {
				indexed = append(indexed, index)
			}
		}
		offset += int64(len(raw))
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, nil, "", startLineNo, 0, time.Time{}, fmt.Errorf("scan codex session history %q: %w", path, readErr)
		}
	}
	return tail, indexed, lastRelevant, lineNo, info.Size(), info.ModTime(), nil
}

func codexHistoryRelevantKind(raw string) string {
	line := strings.TrimSpace(raw)
	if line == "" {
		return ""
	}
	var entry codexHistoryLine
	if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Payload == nil {
		return ""
	}
	switch strings.TrimSpace(entry.Type) {
	case "event_msg":
		switch strings.TrimSpace(historyString(entry.Payload["type"])) {
		case "user_message", "agent_message", "task_complete":
			return strings.TrimSpace(historyString(entry.Payload["type"]))
		}
	case "response_item":
		if strings.TrimSpace(historyString(entry.Payload["type"])) != "message" {
			return ""
		}
		switch strings.TrimSpace(historyString(entry.Payload["role"])) {
		case "user", "assistant":
			return "response_message"
		}
	}
	return ""
}

func readCodexHistoryMessagesAt(ctx context.Context, path string, indexed []sessionHistoryIndexEntry) ([]SessionHistoryMessage, error) {
	if strings.TrimSpace(path) == "" || len(indexed) == 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	messages := make([]SessionHistoryMessage, 0, len(indexed))
	for _, entry := range indexed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Length <= 0 || entry.Offset < 0 {
			continue
		}
		buf := make([]byte, entry.Length)
		n, err := file.ReadAt(buf, entry.Offset)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("read codex session history %q line %d: %w", path, entry.LineNo, err)
		}
		msg, ok, err := codexHistoryMessageFromLine(string(buf[:n]), int(entry.LineNo))
		if err != nil {
			return nil, err
		}
		if ok && !duplicateCodexHistoryMessage(messages, msg) {
			messages = append(messages, msg)
		}
	}
	return messages, nil
}

func codexHistoryMessageFromLine(raw string, lineNo int) (SessionHistoryMessage, bool, error) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return SessionHistoryMessage{}, false, nil
	}
	var entry codexHistoryLine
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return SessionHistoryMessage{}, false, fmt.Errorf("parse codex session line %d: %w", lineNo, err)
	}
	payload := entry.Payload
	if payload == nil {
		return SessionHistoryMessage{}, false, nil
	}
	ts := codexHistoryTimestamp(entry.Timestamp)
	switch strings.TrimSpace(entry.Type) {
	case "event_msg":
		switch strings.TrimSpace(historyString(payload["type"])) {
		case "user_message":
			text := strings.TrimSpace(firstHistoryString(payload["message"], payload["text"]))
			if text == "" {
				return SessionHistoryMessage{}, false, nil
			}
			return SessionHistoryMessage{
				Seq:         uint64(lineNo),
				Role:        "user",
				Kind:        "message",
				Text:        text,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:event:user:%06d", lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
			}, true, nil
		case "agent_message":
			text := strings.TrimSpace(firstHistoryString(payload["message"], payload["text"]))
			if text == "" {
				return SessionHistoryMessage{}, false, nil
			}
			msg := SessionHistoryMessage{
				Seq:         uint64(lineNo),
				Role:        "assistant",
				Kind:        "message",
				Text:        text,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:event:assistant:%06d", lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
			}
			if phase := strings.TrimSpace(historyString(payload["phase"])); phase != "" {
				msg.Details = map[string]any{"phase": phase}
			}
			return msg, true, nil
		}
	case "response_item":
		switch strings.TrimSpace(historyString(payload["type"])) {
		case "message":
			role := strings.TrimSpace(historyString(payload["role"]))
			if role != "assistant" && role != "user" && role != "system" && role != "developer" {
				return SessionHistoryMessage{}, false, nil
			}
			text := strings.TrimSpace(codexHistoryContentText(payload["content"]))
			if text == "" {
				text = strings.TrimSpace(historyString(payload["text"]))
			}
			if text == "" {
				return SessionHistoryMessage{}, false, nil
			}
			kind := "message"
			if codexHistoryResponseItemIsInjectedPrompt(role, text) {
				role = "system"
				kind = "system_prompt"
			}
			msg := SessionHistoryMessage{
				Seq:         uint64(lineNo),
				Role:        role,
				Kind:        kind,
				Text:        text,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:item:%s:%06d", role, lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
			}
			if phase := strings.TrimSpace(historyString(payload["phase"])); phase != "" {
				msg.Details = map[string]any{"phase": phase}
			}
			return msg, true, nil
		case "function_call":
			name := strings.TrimSpace(historyString(payload["name"]))
			callID := strings.TrimSpace(historyString(payload["call_id"]))
			arguments := strings.TrimSpace(historyString(payload["arguments"]))
			if name == "" && callID == "" {
				return SessionHistoryMessage{}, false, nil
			}
			return SessionHistoryMessage{
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
			}, true, nil
		case "function_call_output":
			callID := strings.TrimSpace(historyString(payload["call_id"]))
			output := strings.TrimSpace(historyString(payload["output"]))
			if callID == "" && output == "" {
				return SessionHistoryMessage{}, false, nil
			}
			return SessionHistoryMessage{
				Seq:         uint64(lineNo),
				Kind:        "tool_result",
				Type:        "tool_result",
				Text:        output,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:item:tool-result:%06d", lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
				ToolCallID:  callID,
				IsError:     historyBool(payload["is_error"]) || codexHistoryFunctionOutputIsError(output),
				Details: map[string]any{
					"output": output,
				},
			}, true, nil
		case "reasoning":
			summary := strings.TrimSpace(codexHistoryReasoningSummary(payload["summary"]))
			if summary == "" {
				return SessionHistoryMessage{}, false, nil
			}
			return SessionHistoryMessage{
				Seq:         uint64(lineNo),
				Kind:        "reasoning",
				Type:        "reasoning",
				Text:        summary,
				Summary:     summary,
				TS:          ts,
				EventID:     fmt.Sprintf("codex:item:reasoning:%06d", lineNo),
				SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
			}, true, nil
		}
	}
	return SessionHistoryMessage{}, false, nil
}

func codexHistoryTimestamp(raw string) float64 {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0
	}
	return float64(parsed.UnixNano()) / float64(time.Second)
}

func codexHistoryContentText(value any) string {
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
		switch strings.TrimSpace(historyString(obj["type"])) {
		case "text", "input_text", "output_text":
			if text := historyString(obj["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "")
}

func codexHistoryResponseItemIsInjectedPrompt(role, text string) bool {
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

func codexHistoryFunctionOutputIsError(output string) bool {
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

func codexHistoryReasoningSummary(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text := strings.TrimSpace(historyString(item)); text != "" {
			parts = append(parts, text)
			continue
		}
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text := strings.TrimSpace(firstHistoryString(obj["text"], obj["summary"])); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func codexHistoryMessageTextHash(message SessionHistoryMessage) string {
	if message.Kind != "message" {
		return ""
	}
	text := strings.TrimSpace(message.Text)
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", sum[:8])
}

func duplicateCodexHistoryIndex(items []sessionHistoryIndexEntry, candidate sessionHistoryIndexEntry) bool {
	if candidate.TextHash == "" || candidate.Kind != "message" {
		return false
	}
	for i := len(items) - 1; i >= 0 && i >= len(items)-4; i-- {
		item := items[i]
		if item.Kind != candidate.Kind || item.Role != candidate.Role || item.TextHash != candidate.TextHash {
			continue
		}
		if item.TS == 0 || candidate.TS == 0 || closeHistoryTimestamp(item.TS, candidate.TS) {
			return true
		}
	}
	return false
}

func duplicateCodexHistoryMessage(items []SessionHistoryMessage, candidate SessionHistoryMessage) bool {
	text := strings.TrimSpace(candidate.Text)
	if text == "" || candidate.Kind != "message" {
		return false
	}
	for i := len(items) - 1; i >= 0 && i >= len(items)-4; i-- {
		item := items[i]
		if item.Kind != candidate.Kind || item.Role != candidate.Role || strings.TrimSpace(item.Text) != text {
			continue
		}
		if item.TS == 0 || candidate.TS == 0 || closeHistoryTimestamp(item.TS, candidate.TS) {
			return true
		}
	}
	return false
}

func closeHistoryTimestamp(a, b float64) bool {
	if a > b {
		return a-b < 0.001
	}
	return b-a < 0.001
}

func mergeTailLines(existing, incoming []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	if len(incoming) == 0 {
		if len(existing) > limit {
			return append([]string(nil), existing[len(existing)-limit:]...)
		}
		return append([]string(nil), existing...)
	}
	merged := make([]string, 0, len(existing)+len(incoming))
	merged = append(merged, existing...)
	merged = append(merged, incoming...)
	if len(merged) > limit {
		merged = merged[len(merged)-limit:]
	}
	return append([]string(nil), merged...)
}

func firstHistoryString(values ...any) string {
	for _, value := range values {
		if text := historyString(value); text != "" {
			return text
		}
	}
	return ""
}

func historyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func historyBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	default:
		return false
	}
}

func tailLines(path string, limit int) ([]string, int64, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	if limit <= 0 || info.Size() == 0 {
		return readAllLines(path)
	}
	const initialChunk = int64(1 << 20)
	const maxChunk = int64(64 << 20)
	upper := info.Size()
	chunk := initialChunk
	var lines []string
	for upper > 0 && len(lines) <= limit {
		lower := upper - chunk
		if lower < 0 {
			lower = 0
		}
		buf := make([]byte, upper-lower)
		if _, err := file.ReadAt(buf, lower); err != nil && err != io.EOF {
			return nil, 0, time.Time{}, fmt.Errorf("read session history %q: %w", path, err)
		}
		windowLines := tailWindowLines(buf, lower == 0, upper == info.Size())
		lines = append(windowLines, lines...)
		if lower == 0 {
			break
		}
		upper = lower
		if chunk < maxChunk {
			chunk *= 2
			if chunk > maxChunk {
				chunk = maxChunk
			}
		}
	}
	if len(lines) > limit {
		lines = append([]string(nil), lines[len(lines)-limit:]...)
	}
	return lines, info.Size(), info.ModTime(), nil
}

func tailWindowLines(buf []byte, includePrefix bool, includeSuffix bool) []string {
	if len(buf) == 0 {
		return nil
	}
	start := 0
	if !includePrefix {
		idx := bytes.IndexByte(buf, '\n')
		if idx < 0 {
			return nil
		}
		start = idx + 1
	}
	end := len(buf)
	if !includeSuffix && end > start && buf[end-1] != '\n' {
		idx := bytes.LastIndexByte(buf[start:end], '\n')
		if idx < 0 {
			return nil
		}
		end = start + idx + 1
	}
	out := make([]string, 0)
	pos := start
	for pos < end {
		next := bytes.IndexByte(buf[pos:end], '\n')
		lineEnd := end
		if next >= 0 {
			lineEnd = pos + next
		}
		line := strings.TrimSpace(string(buf[pos:lineEnd]))
		if line != "" {
			out = append(out, line)
		}
		if next < 0 {
			break
		}
		pos = lineEnd + 1
	}
	return out
}
