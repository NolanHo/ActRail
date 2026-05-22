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

	"github.com/fsnotify/fsnotify"
)

const defaultSessionHistoryWarmLines = 5000
const defaultCodexHistoryWarmMessages = 5000
const defaultCodexHistoryWarmBytes = 1 << 20
const defaultCodexHistoryTailLineBytes = 256 << 10
const defaultCodexHistoryMessageTextBytes = 64 << 10
const sessionHistoryMaxLineBytes = 64 << 20
const sessionHistoryPollFallbackInterval = 30 * time.Second
const sessionHistoryNotifyDebounce = 150 * time.Millisecond
const sessionHistoryCleanupInterval = time.Hour

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
	codexRoot  string
	threadID   string
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
	Source   string
}

func newSessionHistoryCache(path string, codex bool) *sessionHistoryCache {
	trimmed := strings.TrimSpace(path)
	cache := &sessionHistoryCache{codex: codex, codexRoot: codexSessionRoot()}
	if trimmed != "" {
		cache.path = filepath.Clean(trimmed)
	}
	return cache
}

func (c *sessionHistoryCache) Start(ctx context.Context) {
	if c == nil {
		return
	}
	if c.codex {
		_ = c.discoverCodexPath(ctx)
	}
	c.ensureWatcher(ctx)
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
	if err := ctx.Err(); err != nil {
		return SessionHistorySnapshot{}, err
	}
	if c.codex {
		_ = c.discoverCodexPath(ctx)
	}
	c.ensureWatcher(ctx)
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
	c.mu.RUnlock()
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
	var oldCancel context.CancelFunc
	c.mu.Lock()
	if c.path == cleaned {
		hasWatcher := c.loadCancel != nil
		c.mu.Unlock()
		if !hasWatcher {
			c.ensureWatcher(ctx)
		}
		return
	}
	if c.loadCancel != nil {
		oldCancel = c.loadCancel
	}
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
	c.loadCancel = nil
	c.mu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}
	c.ensureWatcher(ctx)
}

func (c *sessionHistoryCache) SetCodexThreadID(ctx context.Context, threadID string) {
	if c == nil || !c.codex {
		return
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}
	var oldCancel context.CancelFunc
	needsDiscover := true
	c.mu.Lock()
	if c.threadID == threadID {
		hasPath := strings.TrimSpace(c.path) != ""
		c.mu.Unlock()
		if !hasPath {
			_ = c.discoverCodexPath(ctx)
		}
		return
	}
	c.threadID = threadID
	if strings.TrimSpace(c.path) != "" {
		currentThreadID, ok, err := codexSessionIDFromHistoryFile(ctx, c.path)
		if err == nil && ok && currentThreadID == threadID {
			needsDiscover = false
		} else {
			if c.loadCancel != nil {
				oldCancel = c.loadCancel
			}
			c.path = ""
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
			c.loadCancel = nil
		}
	}
	c.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
	if needsDiscover {
		_ = c.discoverCodexPath(ctx)
	}
}

func (c *sessionHistoryCache) currentPath() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.path
}

func (c *sessionHistoryCache) currentCodexThreadID() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return strings.TrimSpace(c.threadID)
}

func (c *sessionHistoryCache) discoverCodexPath(ctx context.Context) error {
	if c == nil || !c.codex {
		return nil
	}
	c.mu.RLock()
	threadID := c.threadID
	currentPath := c.path
	root := c.codexRoot
	c.mu.RUnlock()
	if strings.TrimSpace(currentPath) != "" || strings.TrimSpace(threadID) == "" {
		return nil
	}
	path, ok := discoverCodexSessionHistoryPath(ctx, root, threadID)
	if !ok {
		return nil
	}
	c.SetPath(ctx, path)
	return nil
}

func (c *sessionHistoryCache) ensureWatcher(ctx context.Context) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if strings.TrimSpace(c.path) == "" || c.loadCancel != nil {
		c.mu.Unlock()
		return
	}
	loadCtx, cancel := context.WithCancel(ctx)
	c.loadCancel = cancel
	c.mu.Unlock()

	ready := make(chan struct{})
	go c.watch(loadCtx, ready)
	select {
	case <-ready:
	case <-loadCtx.Done():
		return
	}
	_ = c.refreshIfChanged(loadCtx)
}

func (c *sessionHistoryCache) watch(ctx context.Context, ready chan<- struct{}) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		close(ready)
		c.watchByPolling(ctx)
		return
	}
	defer watcher.Close()

	path := c.currentPath()
	c.ensureNotifyTargets(watcher, path)
	close(ready)
	fallback := time.NewTicker(sessionHistoryPollFallbackInterval)
	defer fallback.Stop()
	cleanup := time.NewTicker(sessionHistoryCleanupInterval)
	defer cleanup.Stop()
	var debounce <-chan time.Time
	var debounceTimer *time.Timer
	defer func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if c.historyEventMatches(event, path) {
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.NewTimer(sessionHistoryNotifyDebounce)
				debounce = debounceTimer.C
			}
		case <-watcher.Errors:
		case <-debounce:
			debounce = nil
			_ = c.refreshIfChanged(ctx)
			path = c.currentPath()
			c.ensureNotifyTargets(watcher, path)
		case <-fallback.C:
			_ = c.refreshIfChanged(ctx)
			path = c.currentPath()
			c.ensureNotifyTargets(watcher, path)
		case <-cleanup.C:
			c.compact()
		}
	}
}

func (c *sessionHistoryCache) watchByPolling(ctx context.Context) {
	ticker := time.NewTicker(sessionHistoryPollFallbackInterval)
	defer ticker.Stop()
	cleanup := time.NewTicker(sessionHistoryCleanupInterval)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.refreshIfChanged(ctx)
		case <-cleanup.C:
			c.compact()
		}
	}
}

func (c *sessionHistoryCache) ensureNotifyTargets(watcher *fsnotify.Watcher, path string) {
	if watcher == nil || strings.TrimSpace(path) == "" {
		return
	}
	cleaned := filepath.Clean(path)
	dir := filepath.Dir(cleaned)
	watched := make(map[string]bool, len(watcher.WatchList()))
	for _, target := range watcher.WatchList() {
		watched[filepath.Clean(target)] = true
	}
	if dir != "." && !watched[dir] {
		_ = watcher.Add(dir)
	}
	if _, err := os.Stat(cleaned); err == nil && !watched[cleaned] {
		_ = watcher.Add(cleaned)
	}
}

func (c *sessionHistoryCache) historyEventMatches(event fsnotify.Event, path string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(event.Name) == "" {
		return false
	}
	cleanedPath := filepath.Clean(path)
	cleanedEvent := filepath.Clean(event.Name)
	if cleanedEvent == cleanedPath {
		return event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Remove) || event.Has(fsnotify.Chmod)
	}
	return filepath.Dir(cleanedPath) == cleanedEvent
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
	lastMessages := append([]SessionHistoryMessage(nil), c.messages...)
	lastKind := c.lastKind
	c.mu.RUnlock()
	if unchanged {
		return nil
	}
	if codex {
		if !warmed || info.Size() < lastSize || info.Size() == lastSize {
			return c.loadFull(ctx)
		}
		lines, indexed, messages, nextKind, lineCount, size, mod, err := readCodexHistoryRange(ctx, path, lastSize, lastLineCount, defaultSessionHistoryWarmLines, defaultCodexHistoryWarmMessages, lastIndexed, lastMessages)
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
		c.messages = messages
		c.indexed = indexed
		if nextKind != "" {
			lastKind = nextKind
		}
		c.lastKind = lastKind
		c.taskDone = codexHistoryTerminalKind(lastKind)
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

func (c *sessionHistoryCache) compact() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.lines = trimStringTail(c.lines, defaultSessionHistoryWarmLines)
	c.messages = trimSessionHistoryMessages(c.messages, defaultCodexHistoryWarmMessages)
	c.mu.Unlock()
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
	lines, indexed, messages, lastRelevant, lineCount, size, mod, err := readCodexHistoryRange(ctx, path, 0, 0, tailLimit, defaultCodexHistoryWarmMessages, nil, nil)
	return lines, messages, indexed, codexHistoryTerminalKind(lastRelevant), lastRelevant, lineCount, size, mod, err
}

func discoverCodexSessionHistoryPath(ctx context.Context, root, threadID string) (string, bool) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return "", false
	}
	root = strings.TrimSpace(root)
	if root == "" {
		root = codexSessionRoot()
	}
	matches, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*"+threadID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return "", false
	}
	best := ""
	var bestMod time.Time
	for _, match := range matches {
		if err := ctx.Err(); err != nil {
			return "", false
		}
		id, ok, err := codexSessionIDFromHistoryFile(ctx, match)
		if err != nil || !ok || id != threadID {
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

func codexSessionIDFromHistoryFile(ctx context.Context, path string) (string, bool, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), sessionHistoryMaxLineBytes)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry codexHistoryLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return "", false, err
		}
		if strings.TrimSpace(entry.Type) != "session_meta" || entry.Payload == nil {
			continue
		}
		id := strings.TrimSpace(historyString(entry.Payload["id"]))
		return id, id != "", nil
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	return "", false, nil
}

func readCodexHistoryRange(ctx context.Context, path string, startOffset int64, startLineNo int, tailLineLimit int, messageLimit int, existing []sessionHistoryIndexEntry, existingMessages []SessionHistoryMessage) ([]string, []sessionHistoryIndexEntry, []SessionHistoryMessage, string, int, int64, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, "", startLineNo, 0, time.Time{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, nil, "", startLineNo, 0, time.Time{}, err
	}
	if startOffset > 0 {
		if _, err := file.Seek(startOffset, io.SeekStart); err != nil {
			return nil, nil, nil, "", startLineNo, 0, time.Time{}, fmt.Errorf("seek codex session history %q: %w", path, err)
		}
	}
	tail := newBoundedStringTail(tailLineLimit, defaultCodexHistoryWarmBytes)
	indexed := append([]sessionHistoryIndexEntry(nil), existing...)
	messages := append([]SessionHistoryMessage(nil), existingMessages...)
	lastRelevant := ""
	lineNo := startLineNo
	committedLineNo := startLineNo
	offset := startOffset
	committedOffset := startOffset
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		raw, readErr := reader.ReadString('\n')
		if raw == "" && readErr == io.EOF {
			break
		}
		if raw == "" && readErr != nil {
			return nil, nil, nil, "", startLineNo, 0, time.Time{}, fmt.Errorf("scan codex session history %q: %w", path, readErr)
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, "", startLineNo, 0, time.Time{}, err
		}
		lineNo++
		if readErr == io.EOF && !strings.HasSuffix(raw, "\n") {
			break
		}
		line := strings.TrimRight(raw, "\r\n")
		message, ok, err := codexHistoryMessageFromLine(line, lineNo)
		tail.append(line)
		if err != nil {
			return nil, nil, nil, "", startLineNo, 0, time.Time{}, err
		}
		if relevant := codexHistoryRelevantKind(line); relevant != "" {
			lastRelevant = relevant
		}
		if ok {
			message = trimSessionHistoryMessageBody(message, defaultCodexHistoryMessageTextBytes)
			index := sessionHistoryIndexEntry{
				LineNo:   uint64(lineNo),
				Offset:   offset,
				Length:   len(raw),
				Role:     message.Role,
				Kind:     message.Kind,
				TS:       message.TS,
				TextHash: codexHistoryMessageTextHash(message),
				Source:   codexHistorySource(line),
			}
			if !duplicateCodexHistoryIndex(indexed, index) {
				indexed = append(indexed, index)
				if !duplicateCodexHistoryMessage(messages, message) {
					messages = append(messages, message)
					messages = trimSessionHistoryMessages(messages, messageLimit)
				}
			}
		}
		offset += int64(len(raw))
		committedOffset = offset
		committedLineNo = lineNo
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, nil, nil, "", startLineNo, 0, time.Time{}, fmt.Errorf("scan codex session history %q: %w", path, readErr)
		}
	}
	return tail.lines(), indexed, messages, lastRelevant, committedLineNo, committedOffset, info.ModTime(), nil
}

type boundedStringTail struct {
	limit int
	bytes int
	items []string
	total int
}

func newBoundedStringTail(limit int, bytes int) *boundedStringTail {
	if limit < 0 {
		limit = 0
	}
	if bytes < 0 {
		bytes = 0
	}
	return &boundedStringTail{limit: limit, bytes: bytes}
}

func (t *boundedStringTail) append(line string) {
	if t == nil || t.limit == 0 {
		return
	}
	line = trimUTF8Bytes(line, defaultCodexHistoryTailLineBytes)
	t.items = append(t.items, line)
	t.total += len(line)
	for len(t.items) > t.limit || (t.bytes > 0 && t.total > t.bytes && len(t.items) > 1) {
		t.total -= len(t.items[0])
		t.items[0] = ""
		t.items = t.items[1:]
	}
}

func (t *boundedStringTail) lines() []string {
	if t == nil || len(t.items) == 0 {
		return nil
	}
	return append([]string(nil), t.items...)
}

func trimSessionHistoryMessages(messages []SessionHistoryMessage, limit int) []SessionHistoryMessage {
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	trimmed := make([]SessionHistoryMessage, limit)
	copy(trimmed, messages[len(messages)-limit:])
	return trimmed
}

func trimStringTail(lines []string, limit int) []string {
	if limit <= 0 || len(lines) <= limit {
		return lines
	}
	trimmed := make([]string, limit)
	copy(trimmed, lines[len(lines)-limit:])
	return trimmed
}

func trimSessionHistoryMessageBody(message SessionHistoryMessage, limit int) SessionHistoryMessage {
	if limit <= 0 {
		return message
	}
	message.Text = trimUTF8Bytes(message.Text, limit)
	message.Summary = trimUTF8Bytes(message.Summary, limit)
	if len(message.Details) > 0 {
		details := make(map[string]any, len(message.Details)+1)
		truncated := false
		for key, value := range message.Details {
			if text, ok := value.(string); ok {
				trimmed := trimUTF8Bytes(text, limit)
				if trimmed != text {
					truncated = true
				}
				details[key] = trimmed
				continue
			}
			details[key] = value
		}
		if truncated {
			details["truncated"] = true
		}
		message.Details = details
	}
	return message
}

func trimUTF8Bytes(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := 0
	for idx := range text {
		if idx > limit {
			break
		}
		cut = idx
	}
	if cut <= 0 {
		cut = limit
	}
	return text[:cut] + "\n[truncated]"
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
		case "user_message", "agent_message", "task_started", "task_complete", "turn_aborted":
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

func codexHistoryTerminalKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "task_complete", "turn_aborted":
		return true
	default:
		return false
	}
}

func codexHistorySource(raw string) string {
	line := strings.TrimSpace(raw)
	if line == "" {
		return ""
	}
	var entry codexHistoryLine
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return ""
	}
	return strings.TrimSpace(entry.Type)
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
			if msg, ok := codexSubagentNotificationHistoryMessage(text, lineNo, ts); ok {
				msg.EventID = fmt.Sprintf("codex:event:subagent-notification:%06d", lineNo)
				return msg, true, nil
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
			if msg, ok := codexSubagentNotificationHistoryMessage(text, lineNo, ts); ok {
				msg.EventID = fmt.Sprintf("codex:item:subagent-notification:%06d", lineNo)
				return msg, true, nil
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

func codexSubagentNotificationHistoryMessage(text string, lineNo int, ts float64) (SessionHistoryMessage, bool) {
	payload, ok := decodeCodexHistorySubagentNotification(text)
	if !ok {
		return SessionHistoryMessage{}, false
	}
	result := strings.TrimSpace(payload.Text)
	if result == "" {
		return SessionHistoryMessage{}, false
	}
	return SessionHistoryMessage{
		Seq:         uint64(lineNo),
		Kind:        "custom_message",
		Type:        "custom_message",
		Text:        result,
		TS:          ts,
		SourceOrder: fmt.Sprintf("codex:%06d", lineNo),
		Name:        "Codex Subagent",
		Summary:     strings.TrimSpace(payload.Role),
		Details: map[string]any{
			"custom_type": "codex-subagent-message",
			"role":        strings.TrimSpace(payload.Role),
			"text":        result,
			"thread_id":   strings.TrimSpace(payload.ThreadID),
		},
	}, true
}

type codexHistorySubagentNotification struct {
	AgentPath string                     `json:"agent_path"`
	Status    map[string]json.RawMessage `json:"status"`
}

type codexHistorySubagentMessage struct {
	Role     string
	Text     string
	ThreadID string
}

func decodeCodexHistorySubagentNotification(text string) (codexHistorySubagentMessage, bool) {
	const start = "<subagent_notification>"
	const end = "</subagent_notification>"
	raw := strings.TrimSpace(text)
	if !strings.HasPrefix(raw, start) || !strings.HasSuffix(raw, end) {
		return codexHistorySubagentMessage{}, false
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, start), end))
	if body == "" {
		return codexHistorySubagentMessage{}, false
	}
	var notification codexHistorySubagentNotification
	if err := json.Unmarshal([]byte(body), &notification); err != nil {
		return codexHistorySubagentMessage{}, false
	}
	result := codexHistorySubagentNotificationStatusText(notification.Status)
	if result == "" {
		return codexHistorySubagentMessage{}, false
	}
	return codexHistorySubagentMessage{
		Role:     "assistant",
		Text:     result,
		ThreadID: strings.TrimSpace(notification.AgentPath),
	}, true
}

func codexHistorySubagentNotificationStatusText(status map[string]json.RawMessage) string {
	if len(status) == 0 {
		return ""
	}
	for _, key := range []string{"completed", "failed", "cancelled", "error", "message"} {
		if raw, ok := status[key]; ok {
			if text := codexHistorySubagentNotificationJSONText(raw); text != "" {
				return text
			}
		}
	}
	for _, raw := range status {
		if text := codexHistorySubagentNotificationJSONText(raw); text != "" {
			return text
		}
	}
	return ""
}

func codexHistorySubagentNotificationJSONText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(encoded))
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
		if codexHistoryDuplicateSources(item.Source, candidate.Source) {
			return true
		}
	}
	return false
}

func codexHistoryDuplicateSources(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return (left == "event_msg" && right == "response_item") || (left == "response_item" && right == "event_msg")
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
		if codexHistoryDuplicateEventIDs(item.EventID, candidate.EventID) || (item.TS != 0 && candidate.TS != 0 && closeHistoryTimestamp(item.TS, candidate.TS)) {
			return true
		}
	}
	return false
}

func codexHistoryDuplicateEventIDs(left, right string) bool {
	leftEvent := strings.HasPrefix(strings.TrimSpace(left), "codex:event:")
	rightEvent := strings.HasPrefix(strings.TrimSpace(right), "codex:event:")
	leftItem := strings.HasPrefix(strings.TrimSpace(left), "codex:item:")
	rightItem := strings.HasPrefix(strings.TrimSpace(right), "codex:item:")
	return (leftEvent && rightItem) || (leftItem && rightEvent)
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
