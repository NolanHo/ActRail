package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"actrail/internal/domain/pi"
)

const (
	sourceHistorySeqFactor    = 1000
	sourceHistoryInitialChunk = 1 << 20
	sourceHistoryMaxChunk     = 64 << 20
)

type sourceHistoryLine struct {
	Offset int64
	Text   []byte
}

type sourceHistoryPage struct {
	Items         []SessionMessage
	TailSeq       uint64
	HasMore       bool
	NextBeforeSeq *uint64
	Signature     string
}

func loadSourceHistoryPage(path string, req SessionMessagesRequest) (sourceHistoryPage, bool, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || req.AfterSeq != nil || req.Limit <= 0 {
		return sourceHistoryPage{}, false, nil
	}
	info, err := os.Stat(trimmed)
	if err != nil || info.IsDir() {
		return sourceHistoryPage{}, false, err
	}
	beforeOffset := info.Size()
	if req.BeforeSeq != nil {
		beforeOffset = sourceHistoryOffsetFromSeq(*req.BeforeSeq)
		if beforeOffset <= 0 {
			return sourceHistoryPage{TailSeq: sourceHistorySeqForOffset(info.Size()), Signature: sourceHistorySignature(trimmed, info)}, true, nil
		}
		if beforeOffset > info.Size() {
			beforeOffset = info.Size()
		}
	}
	lines, hasMore, err := readSourceHistoryLinesBefore(trimmed, beforeOffset, req.Limit)
	if err != nil {
		return sourceHistoryPage{}, true, err
	}
	items, err := sessionMessagesFromPILines(trimmed, lines)
	if err != nil {
		return sourceHistoryPage{}, true, err
	}
	if len(items) > req.Limit {
		items = append([]SessionMessage(nil), items[len(items)-req.Limit:]...)
		hasMore = true
	}
	response := sourceHistoryPage{
		Items:     items,
		TailSeq:   sourceHistorySeqForOffset(info.Size()),
		HasMore:   hasMore,
		Signature: sourceHistorySignature(trimmed, info),
	}
	if hasMore && len(items) > 0 {
		next := items[0].Seq
		response.NextBeforeSeq = &next
	}
	return response, true, nil
}

func sourceHistorySessionMessagesResponse(page sourceHistoryPage, req SessionMessagesRequest) SessionMessagesResponse {
	items := append([]SessionMessage(nil), page.Items...)
	if req.Deferred {
		activeTurnStartSeq := req.ActiveTurnStartSeq
		if activeTurnStartSeq == 0 {
			activeTurnStartSeq = activeTurnStartSeqForMessages(items)
		}
		for i := range items {
			items[i] = deferSessionMessageForRequest(items[i], req, page.TailSeq, activeTurnStartSeq)
		}
	}
	return SessionMessagesResponse{
		Items:         items,
		NextBeforeSeq: page.NextBeforeSeq,
		HasMore:       page.HasMore,
		TailSeq:       page.TailSeq,
	}
}

func readSourceHistoryLinesBefore(path string, beforeOffset int64, minItems int) ([]sourceHistoryLine, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("open pi session source %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("stat pi session source %q: %w", path, err)
	}
	upper := beforeOffset
	if upper > info.Size() {
		upper = info.Size()
	}
	if upper <= 0 {
		return nil, false, nil
	}
	chunk := int64(sourceHistoryInitialChunk)
	var lines []sourceHistoryLine
	for upper > 0 {
		lower := upper - chunk
		if lower < 0 {
			lower = 0
		}
		buf := make([]byte, upper-lower)
		if _, err := file.ReadAt(buf, lower); err != nil && err != io.EOF {
			return nil, false, fmt.Errorf("read pi session source %q: %w", path, err)
		}
		windowLines := sourceHistoryLinesFromWindow(buf, lower, lower == 0, upper == info.Size())
		lines = append(windowLines, lines...)
		if len(lines) > 0 && countSourceHistoryMessages(path, lines) >= minItems {
			break
		}
		if lower == 0 {
			break
		}
		upper = lower
		if chunk < sourceHistoryMaxChunk {
			chunk *= 2
			if chunk > sourceHistoryMaxChunk {
				chunk = sourceHistoryMaxChunk
			}
		}
	}
	if len(lines) == 0 {
		return nil, false, nil
	}
	return lines, lines[0].Offset > 0, nil
}

func sourceHistoryLinesFromWindow(buf []byte, base int64, includePrefix bool, includeSuffix bool) []sourceHistoryLine {
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
	out := make([]sourceHistoryLine, 0)
	pos := start
	for pos < end {
		next := bytes.IndexByte(buf[pos:end], '\n')
		lineEnd := end
		if next >= 0 {
			lineEnd = pos + next
		}
		line := bytes.TrimSpace(buf[pos:lineEnd])
		if len(line) > 0 {
			out = append(out, sourceHistoryLine{Offset: base + int64(pos), Text: append([]byte(nil), line...)})
		}
		if next < 0 {
			break
		}
		pos = lineEnd + 1
	}
	return out
}

func countSourceHistoryMessages(sourcePath string, lines []sourceHistoryLine) int {
	items, err := sessionMessagesFromPILines(sourcePath, lines)
	if err != nil {
		return 0
	}
	return len(items)
}

func sessionMessagesFromPILines(sourcePath string, lines []sourceHistoryLine) ([]SessionMessage, error) {
	items := make([]SessionMessage, 0, len(lines))
	for _, line := range lines {
		material, err := pi.ParseObjectJSON(line.Text)
		if err != nil {
			return nil, fmt.Errorf("parse imported pi session source %q at byte %d: %w", sourcePath, line.Offset, err)
		}
		for eventIndex, event := range material.Events {
			msg, ok := sessionMessageFromPIEvent(event)
			if !ok {
				continue
			}
			if msg.SourceOrder == "" {
				msg.SourceOrder = fmt.Sprintf("pi:%012d:%03d", line.Offset, eventIndex)
			}
			msg.Seq = sourceHistorySeqForOffset(line.Offset) + uint64(eventIndex)
			items = append(items, msg)
		}
	}
	return items, nil
}

func sourceHistorySeqForOffset(offset int64) uint64 {
	if offset <= 0 {
		return 1
	}
	return uint64(offset)*sourceHistorySeqFactor + 1
}

func sourceHistoryOffsetFromSeq(seq uint64) int64 {
	if seq == 0 {
		return 0
	}
	return int64((seq - 1) / sourceHistorySeqFactor)
}

func sourceHistorySignature(path string, info os.FileInfo) string {
	mod := time.Time{}
	if info != nil {
		mod = info.ModTime()
	}
	if info == nil {
		return "source-page:" + strings.TrimSpace(path)
	}
	return fmt.Sprintf("source-page:%s:%d:%d", strings.TrimSpace(path), info.Size(), mod.UnixNano())
}
