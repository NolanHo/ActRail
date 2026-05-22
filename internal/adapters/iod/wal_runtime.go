package iod

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"

	"actrail/internal/domain/session"
)

// ReplayResult is one durable WAL snapshot for replay after one cursor.
type ReplayResult struct {
	Records     []WALRecord
	LastOffset  WALOffset
	CorruptTail bool
}

type walTailSummary struct {
	LastOffset  WALOffset
	LastSeq     EventSeq
	CorruptTail bool
}

// WAL owns append ordering and checksum verification for one generation file.
type WAL struct {
	mu           sync.Mutex
	path         string
	sessionID    session.SessionID
	generationID GenerationID
	file         *os.File
	lastOffset   WALOffset
	lastSeq      EventSeq
}

func OpenWAL(path string, sessionID session.SessionID, generationID GenerationID) (*WAL, error) {
	trimmed := filepath.Clean(path)
	if trimmed == "" || trimmed == "." {
		return nil, fmt.Errorf("wal path is required")
	}
	if err := sessionID.Validate(); err != nil {
		return nil, err
	}
	if sessionID.IsHistorical() {
		return nil, fmt.Errorf("session id %q cannot use historical replay identity", sessionID)
	}
	if err := generationID.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(trimmed), 0o755); err != nil {
		return nil, fmt.Errorf("create wal dir: %w", err)
	}
	file, err := os.OpenFile(trimmed, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open wal: %w", err)
	}
	summary, err := SummarizeWAL(trimmed, sessionID, generationID)
	if err != nil {
		if os.IsNotExist(err) {
			summary = walTailSummary{}
		} else {
			_ = file.Close()
			return nil, err
		}
	}
	return &WAL{
		path:         trimmed,
		sessionID:    sessionID,
		generationID: generationID,
		file:         file,
		lastOffset:   summary.LastOffset,
		lastSeq:      summary.LastSeq,
	}, nil
}

func (w *WAL) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

func (w *WAL) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	return w.file.Close()
}

func (w *WAL) Append(class WALRecordClass, payload any) (WALRecord, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return WALRecord{}, err
	}
	return w.AppendRaw(class, raw)
}

func (w *WAL) AppendRaw(class WALRecordClass, payload json.RawMessage) (WALRecord, error) {
	if w == nil || w.file == nil {
		return WALRecord{}, fmt.Errorf("wal is not open")
	}
	if err := class.Validate(); err != nil {
		return WALRecord{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	offset := w.lastOffset + 1
	var seq *EventSeq
	if class.RequiresSeq() {
		next := EventSeq(w.lastSeq + 1)
		seq = &next
	}
	checksum, err := walChecksum(w.sessionID, w.generationID, offset, class, seq, payload)
	if err != nil {
		return WALRecord{}, err
	}
	header, err := NewWALRecordHeader(w.sessionID, w.generationID, offset, class, seq, checksum)
	if err != nil {
		return WALRecord{}, err
	}
	record, err := NewWALRecord(header, payload)
	if err != nil {
		return WALRecord{}, err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return WALRecord{}, fmt.Errorf("marshal wal record: %w", err)
	}
	if _, err := w.file.Write(append(encoded, '\n')); err != nil {
		return WALRecord{}, fmt.Errorf("write wal record: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return WALRecord{}, fmt.Errorf("sync wal record: %w", err)
	}
	w.lastOffset = offset
	if seq != nil {
		w.lastSeq = *seq
	}
	return record, nil
}

func (w *WAL) Replay(afterOffset WALOffset) (ReplayResult, error) {
	if w == nil {
		return ReplayResult{}, fmt.Errorf("wal is required")
	}
	w.mu.Lock()
	lastOffset := w.lastOffset
	w.mu.Unlock()
	if afterOffset > lastOffset {
		return ReplayResult{}, fmt.Errorf("replay cursor after offset %d exceeds wal last offset %d", afterOffset, lastOffset)
	}
	if afterOffset == lastOffset {
		return ReplayResult{LastOffset: lastOffset}, nil
	}
	return ReplayWAL(w.path, w.sessionID, w.generationID, afterOffset)
}

func ReplayWAL(path string, sessionID session.SessionID, generationID GenerationID, afterOffset WALOffset) (ReplayResult, error) {
	if err := sessionID.Validate(); err != nil {
		return ReplayResult{}, err
	}
	if sessionID.IsHistorical() {
		return ReplayResult{}, fmt.Errorf("session id %q cannot use historical replay identity", sessionID)
	}
	if err := generationID.Validate(); err != nil {
		return ReplayResult{}, err
	}
	if err := afterOffset.ValidateState(); err != nil {
		return ReplayResult{}, err
	}
	summary, err := SummarizeWAL(path, sessionID, generationID)
	if err != nil {
		if os.IsNotExist(err) {
			if afterOffset != 0 {
				return ReplayResult{}, fmt.Errorf("replay cursor after offset %d exceeds empty wal", afterOffset)
			}
			return ReplayResult{}, nil
		}
		return ReplayResult{}, err
	}
	if afterOffset > summary.LastOffset {
		return ReplayResult{}, fmt.Errorf("replay cursor after offset %d exceeds wal last offset %d", afterOffset, summary.LastOffset)
	}
	if afterOffset == summary.LastOffset {
		return ReplayResult{LastOffset: summary.LastOffset, CorruptTail: summary.CorruptTail}, nil
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			if afterOffset != 0 {
				return ReplayResult{}, fmt.Errorf("replay cursor after offset %d exceeds empty wal", afterOffset)
			}
			return ReplayResult{}, nil
		}
		return ReplayResult{}, fmt.Errorf("open wal for replay: %w", err)
	}
	defer file.Close()

	cursor, err := NewReplayCursor(sessionID, generationID, 0)
	if err != nil {
		return ReplayResult{}, err
	}
	lastSeq := EventSeq(0)
	result := ReplayResult{}
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return ReplayResult{}, fmt.Errorf("read wal: %w", readErr)
		}
		if len(line) == 0 {
			if readErr == io.EOF {
				break
			}
			continue
		}
		if readErr == io.EOF {
			result.CorruptTail = true
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var record WALRecord
		if err := json.Unmarshal(line, &record); err != nil {
			result.CorruptTail = true
			break
		}
		if err := cursor.Accepts(record.Header); err != nil {
			result.CorruptTail = true
			break
		}
		if ok, err := walRecordChecksumOK(record); err != nil {
			return ReplayResult{}, err
		} else if !ok {
			result.CorruptTail = true
			break
		}
		if record.Header.Seq != nil {
			if record.Header.Seq.Uint64() != lastSeq.Uint64()+1 {
				result.CorruptTail = true
				break
			}
			lastSeq = *record.Header.Seq
		}
		cursor, err = cursor.Advance(record.Header)
		if err != nil {
			result.CorruptTail = true
			break
		}
		result.LastOffset = record.Header.Offset
		if record.Header.Offset > afterOffset {
			result.Records = append(result.Records, record)
		}
	}
	if afterOffset > result.LastOffset {
		return ReplayResult{}, fmt.Errorf("replay cursor after offset %d exceeds wal last offset %d", afterOffset, result.LastOffset)
	}
	return result, nil
}

func SummarizeWAL(path string, sessionID session.SessionID, generationID GenerationID) (walTailSummary, error) {
	if err := sessionID.Validate(); err != nil {
		return walTailSummary{}, err
	}
	if sessionID.IsHistorical() {
		return walTailSummary{}, fmt.Errorf("session id %q cannot use historical replay identity", sessionID)
	}
	if err := generationID.Validate(); err != nil {
		return walTailSummary{}, err
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return walTailSummary{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return walTailSummary{}, fmt.Errorf("stat wal: %w", err)
	}
	size := info.Size()
	if size == 0 {
		return walTailSummary{}, nil
	}
	logicalEnd := size
	summary := walTailSummary{}
	var last [1]byte
	if _, err := file.ReadAt(last[:], size-1); err != nil {
		return walTailSummary{}, fmt.Errorf("read wal tail: %w", err)
	}
	if last[0] != '\n' {
		summary.CorruptTail = true
		newline, ok, err := findLastWALNewline(file, size-1)
		if err != nil {
			return walTailSummary{}, err
		}
		if !ok {
			return summary, nil
		}
		logicalEnd = newline + 1
	}

	const chunkSize int64 = 64 * 1024
	pos := logicalEnd
	var suffix []byte
	for pos > 0 && (summary.LastOffset == 0 || summary.LastSeq == 0) {
		n := chunkSize
		if pos < n {
			n = pos
		}
		pos -= n
		buf := make([]byte, n)
		if _, err := file.ReadAt(buf, pos); err != nil {
			return walTailSummary{}, fmt.Errorf("read wal summary chunk: %w", err)
		}
		data := append(buf, suffix...)
		lines := bytes.Split(data, []byte{'\n'})
		suffix = append(suffix[:0], lines[0]...)
		for i := len(lines) - 1; i >= 1; i-- {
			record, ok, err := parseWALSummaryLine(lines[i], sessionID, generationID)
			if err != nil {
				continue
			}
			if !ok {
				continue
			}
			if summary.LastOffset == 0 {
				summary.LastOffset = record.Header.Offset
			}
			if summary.LastSeq == 0 && record.Header.Seq != nil {
				summary.LastSeq = *record.Header.Seq
			}
			if summary.LastOffset != 0 && summary.LastSeq != 0 {
				return summary, nil
			}
		}
	}
	if len(suffix) != 0 && (summary.LastOffset == 0 || summary.LastSeq == 0) {
		record, ok, err := parseWALSummaryLine(suffix, sessionID, generationID)
		if err == nil && ok {
			if summary.LastOffset == 0 {
				summary.LastOffset = record.Header.Offset
			}
			if summary.LastSeq == 0 && record.Header.Seq != nil {
				summary.LastSeq = *record.Header.Seq
			}
		}
	}
	return summary, nil
}

func findLastWALNewline(file *os.File, endExclusive int64) (int64, bool, error) {
	const chunkSize int64 = 64 * 1024
	pos := endExclusive
	for pos > 0 {
		n := chunkSize
		if pos < n {
			n = pos
		}
		pos -= n
		buf := make([]byte, n)
		if _, err := file.ReadAt(buf, pos); err != nil {
			return 0, false, fmt.Errorf("read wal tail newline chunk: %w", err)
		}
		if idx := bytes.LastIndexByte(buf, '\n'); idx >= 0 {
			return pos + int64(idx), true, nil
		}
	}
	return 0, false, nil
}

func parseWALSummaryLine(line []byte, sessionID session.SessionID, generationID GenerationID) (WALRecord, bool, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return WALRecord{}, false, nil
	}
	var record WALRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return WALRecord{}, false, err
	}
	if record.Header.SessionID != sessionID || record.Header.GenerationID != generationID {
		return WALRecord{}, false, fmt.Errorf("wal summary identity mismatch")
	}
	if err := record.Validate(); err != nil {
		return WALRecord{}, false, err
	}
	if ok, err := walRecordChecksumOK(record); err != nil {
		return WALRecord{}, false, err
	} else if !ok {
		return WALRecord{}, false, fmt.Errorf("wal summary checksum mismatch")
	}
	return record, true, nil
}

func marshalPayload(payload any) (json.RawMessage, error) {
	switch v := payload.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		if len(v) == 0 {
			return nil, nil
		}
		copied := make(json.RawMessage, len(v))
		copy(copied, v)
		return copied, nil
	case []byte:
		if len(v) == 0 {
			return nil, nil
		}
		copied := make(json.RawMessage, len(v))
		copy(copied, v)
		return copied, nil
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal wal payload: %w", err)
		}
		return json.RawMessage(encoded), nil
	}
}

func walRecordChecksumOK(record WALRecord) (bool, error) {
	checksum, err := walChecksum(record.Header.SessionID, record.Header.GenerationID, record.Header.Offset, record.Header.Class, record.Header.Seq, record.Payload)
	if err != nil {
		return false, err
	}
	return checksum == record.Header.Checksum, nil
}

func walChecksum(sessionID session.SessionID, generationID GenerationID, offset WALOffset, class WALRecordClass, seq *EventSeq, payload json.RawMessage) (uint32, error) {
	material := struct {
		SessionID    session.SessionID `json:"session_id"`
		GenerationID GenerationID      `json:"generation_id"`
		Offset       WALOffset         `json:"offset"`
		Class        WALRecordClass    `json:"class"`
		Seq          *EventSeq         `json:"seq,omitempty"`
		Payload      json.RawMessage   `json:"payload,omitempty"`
	}{
		SessionID:    sessionID,
		GenerationID: generationID,
		Offset:       offset,
		Class:        class,
		Seq:          seq,
		Payload:      payload,
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return 0, fmt.Errorf("marshal wal checksum material: %w", err)
	}
	return crc32.ChecksumIEEE(encoded), nil
}
