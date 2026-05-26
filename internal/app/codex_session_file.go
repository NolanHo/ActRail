package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"actrail/internal/domain/session"
)

type codexSessionLine struct {
	Timestamp string         `json:"timestamp"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
}

const codexSessionFileMaxLineBytes = 64 << 20
const codexSessionFilePageInitialChunk = 1 << 20
const codexSessionFilePageMaxChunk = 64 << 20
const codexSessionFilePageMinSize = codexSessionFilePageInitialChunk
const codexSessionFileCompletionInitialChunk = 64 << 10
const codexSessionFileCompletionMaxChunk = 1 << 20

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

func (s *Stub) materializeCodexForkSessionFile(record sessionRecord, threadID string) (string, bool, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || record.identity.Backend() != session.BackendCodex {
		return "", false, nil
	}
	if path, ok := discoverCodexSessionFileByID(context.Background(), threadID); ok {
		return path, true, nil
	}
	parentPath, parentThreadID := s.codexForkParentSource(record)
	if strings.TrimSpace(parentPath) == "" {
		return "", false, nil
	}
	if parentThreadID != "" && !codexSourcePathMatchesSessionID(parentPath, parentThreadID) {
		return "", false, nil
	}
	now := time.Now().UTC()
	if s != nil && s.registry != nil && s.registry.now != nil {
		now = s.registry.now().UTC()
	}
	dir := filepath.Join(codexSessionRoot(), now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("create codex fork session dir %q: %w", dir, err)
	}
	path := filepath.Join(dir, fmt.Sprintf("rollout-%s-%s.jsonl", now.Format("2006-01-02T15-04-05"), threadID))
	if err := copyCodexForkSessionFile(parentPath, path, threadID, record.cwd); err != nil {
		if os.IsExist(err) {
			if discovered, ok := discoverCodexSessionFileByID(context.Background(), threadID); ok {
				return discovered, true, nil
			}
		}
		return "", false, err
	}
	return filepath.Clean(path), true, nil
}

func copyCodexForkSessionFile(parentPath, childPath, childThreadID, cwd string) error {
	in, err := os.Open(strings.TrimSpace(parentPath))
	if err != nil {
		return fmt.Errorf("open codex fork parent session file %q: %w", parentPath, err)
	}
	defer in.Close()
	out, err := os.OpenFile(strings.TrimSpace(childPath), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create codex fork child session file %q: %w", childPath, err)
	}
	committed := false
	defer func() {
		_ = out.Close()
		if !committed {
			_ = os.Remove(childPath)
		}
	}()
	writer := bufio.NewWriter(out)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), codexSessionFileMaxLineBytes)
	sawSessionMeta := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		nextLine, rewrote, err := codexForkSessionLine(line, childThreadID, cwd)
		if err != nil {
			return err
		}
		if rewrote {
			sawSessionMeta = true
		}
		if _, err := writer.Write(nextLine); err != nil {
			return fmt.Errorf("write codex fork child session file %q: %w", childPath, err)
		}
		if err := writer.WriteByte('\n'); err != nil {
			return fmt.Errorf("write codex fork child session file %q: %w", childPath, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan codex fork parent session file %q: %w", parentPath, err)
	}
	if !sawSessionMeta {
		return fmt.Errorf("codex fork parent session file %q has no session_meta", parentPath)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush codex fork child session file %q: %w", childPath, err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync codex fork child session file %q: %w", childPath, err)
	}
	committed = true
	return nil
}

func codexForkSessionLine(line []byte, childThreadID, cwd string) ([]byte, bool, error) {
	var entry map[string]any
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil, false, fmt.Errorf("parse codex fork parent session line: %w", err)
	}
	if strings.TrimSpace(stringValue(entry["type"])) != "session_meta" {
		return append([]byte(nil), line...), false, nil
	}
	payload, _ := entry["payload"].(map[string]any)
	if payload == nil {
		payload = map[string]any{}
		entry["payload"] = payload
	}
	payload["id"] = strings.TrimSpace(childThreadID)
	if strings.TrimSpace(cwd) != "" {
		payload["cwd"] = strings.TrimSpace(cwd)
	}
	body, err := json.Marshal(entry)
	if err != nil {
		return nil, false, fmt.Errorf("encode codex fork child session line: %w", err)
	}
	return body, true, nil
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

func codexSessionMessagesPageFromFile(ctx context.Context, path string, req SessionMessagesRequest) (SessionMessagesResponse, bool, bool, error) {
	if req.Limit <= 0 || req.IncludeToolEvents || req.IncludeToolDetails || strings.TrimSpace(req.EventID) != "" || strings.TrimSpace(req.ToolCallID) != "" {
		return SessionMessagesResponse{}, false, false, nil
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return SessionMessagesResponse{}, false, false, nil
	}
	info, err := os.Stat(trimmed)
	if err != nil || info.IsDir() {
		return SessionMessagesResponse{}, false, false, err
	}
	if info.Size() < codexSessionFilePageMinSize {
		return SessionMessagesResponse{}, false, false, nil
	}
	if req.AfterSeq != nil && *req.AfterSeq > 0 {
		return codexSessionMessagesAfterPageFromFile(ctx, trimmed, info, req)
	}
	beforeOffset := info.Size()
	if req.BeforeSeq != nil {
		beforeOffset = codexSessionFileOffsetFromSeq(*req.BeforeSeq)
		if beforeOffset <= 0 {
			return SessionMessagesResponse{TailSeq: codexSessionFileSeqForOffset(info.Size())}, false, true, nil
		}
		if beforeOffset > info.Size() {
			beforeOffset = info.Size()
		}
	}
	lines, hasMore, err := readCodexSessionFileLinesBefore(ctx, trimmed, beforeOffset, req.Limit)
	if err != nil {
		return SessionMessagesResponse{}, false, true, err
	}
	items, err := codexSessionMessagesFromLocatedLines(ctx, trimmed, lines)
	if err != nil {
		return SessionMessagesResponse{}, false, true, err
	}
	filterReq := req
	filterReq.Limit = 0
	visibleItems := filterSessionMessagesForRequest(items, filterReq)
	if len(visibleItems) == 0 && hasMore {
		return SessionMessagesResponse{}, false, false, nil
	}
	response := paginateSessionMessagesForRequest(visibleItems, req)
	response.TailSeq = codexSessionFileSeqForOffset(info.Size())
	if hasMore && len(response.Items) > 0 {
		response.HasMore = true
		next := response.Items[0].Seq
		response.NextBeforeSeq = &next
	}
	complete := codexSessionMessagesHaveAuthoritativeCompletion(items) || codexSessionLinesHaveTaskComplete(ctx, locatedCodexSessionFileLineTexts(lines))
	return response, complete, true, nil
}

func codexSessionMessagesAfterPageFromFile(ctx context.Context, path string, info os.FileInfo, req SessionMessagesRequest) (SessionMessagesResponse, bool, bool, error) {
	afterOffset := codexSessionFileOffsetFromSeq(*req.AfterSeq)
	if afterOffset < 0 {
		afterOffset = 0
	}
	if afterOffset > info.Size() {
		afterOffset = info.Size()
	}
	lines, reachedEOF, err := readCodexSessionFileLinesAfter(ctx, path, afterOffset, req.Limit)
	if err != nil {
		return SessionMessagesResponse{}, false, true, err
	}
	if len(lines) == 0 {
		return SessionMessagesResponse{TailSeq: codexSessionFileSeqForOffset(info.Size())}, false, true, nil
	}
	items, err := codexSessionMessagesFromLocatedLines(ctx, path, lines)
	if err != nil {
		return SessionMessagesResponse{}, false, true, err
	}
	pageReq := req
	pageReq.AfterSeq = nil
	pageReq.BeforeSeq = nil
	response := paginateSessionMessagesForRequest(items, pageReq)
	if reachedEOF {
		response.TailSeq = codexSessionFileSeqForOffset(info.Size())
	} else if len(response.Items) > 0 {
		response.TailSeq = response.Items[len(response.Items)-1].Seq
		response.HasMore = true
	}
	complete := reachedEOF && (codexSessionMessagesHaveAuthoritativeCompletion(items) || codexSessionLinesHaveTaskComplete(ctx, locatedCodexSessionFileLineTexts(lines)))
	return response, complete, true, nil
}

func codexSessionFileTailHasAuthoritativeCompletion(ctx context.Context, path string) (bool, bool) {
	return codexSessionFileTailCheck(ctx, path, codexSessionLineAuthoritativeCompletion)
}

func codexSessionFileTailHasTerminalLifecycle(ctx context.Context, path string) (bool, bool) {
	return codexSessionFileTailCheck(ctx, path, codexSessionLineTerminalLifecycle)
}

func codexSessionFileTailCheck(ctx context.Context, path string, check func(string) (bool, bool)) (bool, bool) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false, false
	}
	file, err := os.Open(trimmed)
	if err != nil {
		return false, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return false, false
	}
	upper := info.Size()
	chunk := int64(codexSessionFileCompletionInitialChunk)
	for upper > 0 {
		if err := ctx.Err(); err != nil {
			return false, false
		}
		lower := upper - chunk
		if lower < 0 {
			lower = 0
		}
		buf := make([]byte, upper-lower)
		if _, err := file.ReadAt(buf, lower); err != nil && err != io.EOF {
			return false, false
		}
		lines := codexSessionFileLinesFromWindow(buf, lower, lower == 0, upper == info.Size())
		for i := len(lines) - 1; i >= 0; i-- {
			complete, relevant := check(string(lines[i].Text))
			if relevant {
				return complete, true
			}
		}
		if lower == 0 {
			break
		}
		upper = lower
		if chunk < codexSessionFileCompletionMaxChunk {
			chunk *= 2
			if chunk > codexSessionFileCompletionMaxChunk {
				chunk = codexSessionFileCompletionMaxChunk
			}
		}
	}
	return false, true
}

type locatedCodexSessionFileLine struct {
	Offset int64
	Text   []byte
}

func readCodexSessionFileLinesBefore(ctx context.Context, path string, beforeOffset int64, limit int) ([]locatedCodexSessionFileLine, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("open codex session file %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("stat codex session file %q: %w", path, err)
	}
	upper := beforeOffset
	if upper > info.Size() {
		upper = info.Size()
	}
	if upper <= 0 {
		return nil, false, nil
	}
	chunk := int64(codexSessionFilePageInitialChunk)
	var lines []locatedCodexSessionFileLine
	for upper > 0 {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		lower := upper - chunk
		if lower < 0 {
			lower = 0
		}
		buf := make([]byte, upper-lower)
		if _, err := file.ReadAt(buf, lower); err != nil && err != io.EOF {
			return nil, false, fmt.Errorf("read codex session file %q: %w", path, err)
		}
		windowLines := codexSessionFileLinesFromWindow(buf, lower, lower == 0, upper == info.Size())
		lines = append(windowLines, lines...)
		if len(lines) > 0 && codexSessionLinesCoverTurnBoundaryPage(ctx, path, lines, limit) {
			break
		}
		if lower == 0 {
			break
		}
		upper = lower
		if chunk < codexSessionFilePageMaxChunk {
			chunk *= 2
			if chunk > codexSessionFilePageMaxChunk {
				chunk = codexSessionFilePageMaxChunk
			}
		}
	}
	return lines, len(lines) > 0 && lines[0].Offset > 0, nil
}

func codexSessionLinesCoverTurnBoundaryPage(ctx context.Context, sourcePath string, lines []locatedCodexSessionFileLine, limit int) bool {
	conversationLimit := effectiveSessionMessagesConversationLimit(limit)
	if conversationLimit <= 0 {
		return true
	}
	items, err := codexSessionMessagesFromLocatedLines(ctx, sourcePath, lines)
	if err != nil {
		return false
	}
	req := SessionMessagesRequest{}
	visibleItems := filterSessionMessagesForRequest(items, req)
	if len(visibleItems) == 0 {
		return false
	}
	page := paginateSessionMessages(visibleItems, nil, nil, limit)
	if len(page.Items) == 0 || page.Items[0].Role != "user" {
		return false
	}
	return countConversationSessionMessages(page.Items, 0, len(page.Items)) >= conversationLimit || page.HasMore
}

func readCodexSessionFileLinesAfter(ctx context.Context, path string, afterOffset int64, limit int) ([]locatedCodexSessionFileLine, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("open codex session file %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("stat codex session file %q: %w", path, err)
	}
	start := afterOffset
	if start < 0 {
		start = 0
	}
	if start > info.Size() {
		start = info.Size()
	}
	if start > 0 && start < info.Size() {
		previous := []byte{0}
		if _, err := file.ReadAt(previous, start-1); err != nil && err != io.EOF {
			return nil, false, fmt.Errorf("read codex session file %q: %w", path, err)
		}
		if previous[0] != '\n' {
			aligned, err := nextCodexSessionLineOffset(file, path, start, info.Size())
			if err != nil {
				return nil, false, err
			}
			start = aligned
		}
	}
	if start >= info.Size() {
		return nil, true, nil
	}
	chunk := int64(codexSessionFilePageInitialChunk)
	var lines []locatedCodexSessionFileLine
	upper := start
	for upper < info.Size() {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		next := upper + chunk
		if next > info.Size() {
			next = info.Size()
		}
		buf := make([]byte, next-upper)
		if _, err := file.ReadAt(buf, upper); err != nil && err != io.EOF {
			return nil, false, fmt.Errorf("read codex session file %q: %w", path, err)
		}
		windowLines := codexSessionFileLinesFromWindow(buf, upper, true, next == info.Size())
		lines = append(lines, windowLines...)
		if limit > 0 && len(lines) > 0 && countCodexSessionVisibleMessages(ctx, lines, limit) >= limit {
			return lines, false, nil
		}
		if next == info.Size() {
			break
		}
		upper = next
		if chunk < codexSessionFilePageMaxChunk {
			chunk *= 2
			if chunk > codexSessionFilePageMaxChunk {
				chunk = codexSessionFilePageMaxChunk
			}
		}
	}
	return lines, true, nil
}

func nextCodexSessionLineOffset(file *os.File, path string, start int64, size int64) (int64, error) {
	chunk := int64(64 << 10)
	cursor := start
	for cursor < size {
		end := cursor + chunk
		if end > size {
			end = size
		}
		buf := make([]byte, end-cursor)
		if _, err := file.ReadAt(buf, cursor); err != nil && err != io.EOF {
			return 0, fmt.Errorf("read codex session file %q: %w", path, err)
		}
		if idx := bytes.IndexByte(buf, '\n'); idx >= 0 {
			return cursor + int64(idx) + 1, nil
		}
		cursor = end
	}
	return size, nil
}

func codexSessionFileLinesFromWindow(buf []byte, base int64, includePrefix bool, includeSuffix bool) []locatedCodexSessionFileLine {
	if len(buf) == 0 {
		return nil
	}
	start := 0
	if !includePrefix {
		if idx := bytes.IndexByte(buf, '\n'); idx >= 0 {
			start = idx + 1
		} else {
			return nil
		}
	}
	end := len(buf)
	if !includeSuffix && end > start && buf[end-1] != '\n' {
		if idx := bytes.LastIndexByte(buf[start:end], '\n'); idx >= 0 {
			end = start + idx + 1
		} else {
			return nil
		}
	}
	out := make([]locatedCodexSessionFileLine, 0)
	pos := start
	for pos < end {
		next := bytes.IndexByte(buf[pos:end], '\n')
		lineEnd := end
		if next >= 0 {
			lineEnd = pos + next
		}
		line := bytes.TrimSpace(buf[pos:lineEnd])
		if len(line) > 0 {
			out = append(out, locatedCodexSessionFileLine{Offset: base + int64(pos), Text: append([]byte(nil), line...)})
		}
		if next < 0 {
			break
		}
		pos = lineEnd + 1
	}
	return out
}

func codexSessionMessagesFromLocatedLines(ctx context.Context, sourcePath string, lines []locatedCodexSessionFileLine) ([]SessionMessage, error) {
	items := make([]SessionMessage, 0)
	for _, raw := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := strings.TrimSpace(string(raw.Text))
		if line == "" {
			continue
		}
		var entry codexSessionLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("parse codex session history %q offset %d: %w", sourcePath, raw.Offset, err)
		}
		lineSeq := codexSessionFileSeqForOffset(raw.Offset)
		if msg, ok := sessionMessageFromCodexSessionEntry(entry, int(lineSeq)); ok && !duplicateCodexSessionMessage(items, msg) {
			msg.Seq = lineSeq
			items = append(items, msg)
		}
	}
	return items, nil
}

func countCodexSessionVisibleMessages(ctx context.Context, lines []locatedCodexSessionFileLine, limit int) int {
	count := 0
	seen := make([]SessionMessage, 0, limit)
	for i := len(lines) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return 0
		}
		raw := lines[i]
		line := strings.TrimSpace(string(raw.Text))
		if line == "" {
			continue
		}
		var entry codexSessionLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return 0
		}
		lineSeq := codexSessionFileSeqForOffset(raw.Offset)
		msg, ok := sessionMessageFromCodexSessionEntry(entry, int(lineSeq))
		if !ok {
			continue
		}
		msg.Seq = lineSeq
		if duplicateCodexSessionMessage(seen, msg) {
			continue
		}
		seen = append(seen, msg)
		if msg.Role == "user" || msg.Role == "assistant" {
			count++
			if limit > 0 && count >= limit {
				return count
			}
		}
	}
	return count
}

func locatedCodexSessionFileLineTexts(lines []locatedCodexSessionFileLine) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, string(line.Text))
	}
	return out
}

func codexSessionLineAuthoritativeCompletion(raw string) (bool, bool) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return false, false
	}
	var entry codexSessionLine
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return false, false
	}
	switch strings.TrimSpace(entry.Type) {
	case "event_msg":
		kind := strings.TrimSpace(stringValue(entry.Payload["type"]))
		switch kind {
		case "task_complete", "turn_aborted":
			return true, true
		case "task_started", "user_message":
			return false, true
		case "agent_message":
			return strings.TrimSpace(stringValue(entry.Payload["phase"])) == "final_answer", true
		}
	case "response_item":
		switch strings.TrimSpace(stringValue(entry.Payload["type"])) {
		case "message":
			if strings.TrimSpace(stringValue(entry.Payload["role"])) == "assistant" && strings.TrimSpace(stringValue(entry.Payload["phase"])) == "final_answer" {
				return true, true
			}
			return false, true
		case "function_call", "function_call_output", "reasoning", "custom_tool_call", "custom_tool_call_output":
			return false, true
		}
	}
	return false, false
}

func codexSessionLineTerminalLifecycle(raw string) (bool, bool) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return false, false
	}
	var entry codexSessionLine
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return false, false
	}
	switch strings.TrimSpace(entry.Type) {
	case "event_msg":
		switch strings.TrimSpace(stringValue(entry.Payload["type"])) {
		case "task_complete", "turn_aborted":
			return true, true
		case "task_started", "user_message", "agent_message":
			return false, true
		}
	case "response_item":
		switch strings.TrimSpace(stringValue(entry.Payload["type"])) {
		case "message", "function_call", "function_call_output", "reasoning", "custom_tool_call", "custom_tool_call_output":
			return false, true
		}
	}
	return false, false
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

func codexSessionFileSeqForOffset(offset int64) uint64 {
	if offset < 0 {
		offset = 0
	}
	return uint64(offset)*sourceHistorySeqFactor + 1
}

func codexSessionFileOffsetFromSeq(seq uint64) int64 {
	if seq == 0 {
		return 0
	}
	return int64((seq - 1) / sourceHistorySeqFactor)
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
			if msg, ok := codexSubagentNotificationSessionMessage(text, lineNo, ts); ok {
				msg.EventID = fmt.Sprintf("codex:event:subagent-notification:%06d", lineNo)
				return msg, true
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
			if msg, ok := codexSubagentNotificationSessionMessage(text, lineNo, ts); ok {
				msg.EventID = fmt.Sprintf("codex:item:subagent-notification:%06d", lineNo)
				return msg, true
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

func codexSubagentNotificationSessionMessage(text string, lineNo int, ts float64) (SessionMessage, bool) {
	payload, ok := decodeCodexSubagentNotification(text)
	if !ok {
		return SessionMessage{}, false
	}
	msg := SessionMessage{
		Seq:         uint64(lineNo),
		Kind:        "custom_message",
		TS:          ts,
		SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
	}
	return codexSubagentNotificationMessageFromPayload(msg, payload), true
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
