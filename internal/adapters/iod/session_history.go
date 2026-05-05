package iod

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultSessionHistoryWarmLines = 3000

type SessionHistorySnapshot struct {
	SourcePath string
	Lines      []string
	Warmed     bool
	Complete   bool
}

type sessionHistoryCache struct {
	mu         sync.RWMutex
	path       string
	lines      []string
	warmed     bool
	complete   bool
	lastSize   int64
	lastMod    time.Time
	loadCancel context.CancelFunc
}

func newSessionHistoryCache(path string) *sessionHistoryCache {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil
	}
	return &sessionHistoryCache{path: filepath.Clean(trimmed)}
}

func (c *sessionHistoryCache) Start(ctx context.Context) {
	if c == nil {
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
	defer c.mu.RUnlock()
	return SessionHistorySnapshot{
		SourcePath: c.path,
		Lines:      append([]string(nil), c.lines...),
		Warmed:     c.warmed,
		Complete:   c.complete,
	}, nil
}

func (c *sessionHistoryCache) warmTail(limit int) error {
	if c == nil {
		return nil
	}
	lines, size, mod, err := tailLines(c.path, limit)
	if err != nil {
		if os.IsNotExist(err) {
			c.mu.Lock()
			c.lines = nil
			c.warmed = true
			c.complete = false
			c.lastSize = 0
			c.lastMod = time.Time{}
			c.mu.Unlock()
			return nil
		}
		return err
	}
	c.mu.Lock()
	c.lines = lines
	c.warmed = true
	c.complete = false
	c.lastSize = size
	c.lastMod = mod
	c.mu.Unlock()
	return nil
}

func (c *sessionHistoryCache) loadFull(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lines, size, mod, err := readAllLines(c.path)
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
	c.lines = lines
	c.warmed = true
	c.complete = true
	c.lastSize = size
	c.lastMod = mod
	c.mu.Unlock()
	return nil
}

func (c *sessionHistoryCache) refreshIfChanged(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat session history %q: %w", c.path, err)
	}
	c.mu.RLock()
	unchanged := c.warmed && info.Size() == c.lastSize && info.ModTime().Equal(c.lastMod)
	c.mu.RUnlock()
	if unchanged {
		return nil
	}
	return c.warmTail(defaultSessionHistoryWarmLines)
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
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, time.Time{}, fmt.Errorf("scan session history %q: %w", path, err)
	}
	return lines, info.Size(), info.ModTime(), nil
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
