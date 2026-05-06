package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"actrail/internal/domain/pi"
	"actrail/internal/domain/session"
)

const (
	handoffSidecarFormatVersion = 1
	handoffRecentUserTurns      = 2
	handoffMaskedToolText       = "[masked: old tool result omitted. Search the source session JSONL if this output is needed.]"
)

type sessionHandoffSidecar struct {
	Version            int                   `json:"version"`
	Kind               string                `json:"kind"`
	SourceSessionID    string                `json:"source_session_id"`
	SourcePath         string                `json:"source_path,omitempty"`
	GeneratedAt        string                `json:"generated_at"`
	StartsAfterCompact bool                  `json:"starts_after_compact"`
	FirstSourceLine    int                   `json:"first_source_line"`
	RecentUserTurns    int                   `json:"recent_user_turns"`
	MaskedToolResults  int                   `json:"masked_tool_results"`
	Entries            []sessionHandoffEntry `json:"entries"`
}

type sessionHandoffEntry struct {
	Line         int            `json:"line,omitempty"`
	SourceID     string         `json:"source_id,omitempty"`
	ParentID     string         `json:"parent_id,omitempty"`
	Role         string         `json:"role,omitempty"`
	Kind         string         `json:"kind"`
	ToolCallID   string         `json:"tool_call_id,omitempty"`
	ToolName     string         `json:"tool_name,omitempty"`
	IsError      bool           `json:"is_error,omitempty"`
	Text         string         `json:"text,omitempty"`
	Arguments    map[string]any `json:"arguments,omitempty"`
	Masked       bool           `json:"masked,omitempty"`
	MaskReason   string         `json:"mask_reason,omitempty"`
	OriginalSize int            `json:"original_size,omitempty"`
}

func handoffPrompt(sidecarPath string) string {
	path := strings.TrimSpace(sidecarPath)
	return strings.Join([]string{
		"Continue the previous session from this ActRail handoff sidecar:",
		path,
		"Read the sidecar first. It contains the effective recent context: user text, assistant text, tool calls, and selected tool results.",
		"Do not read the source session JSONL unless the sidecar marks a tool result as masked and that exact output is needed.",
		"If source inspection is needed, search the source file for the sidecar line number, source_id, tool_call_id, or nearby text instead of reading the whole file.",
		"Continue from the last user instruction in the sidecar.",
	}, "\n")
}

func (s *Stub) writeSessionHandoffSidecar(record sessionRecord) (string, error) {
	sourcePath := strings.TrimSpace(record.importedSourcePath)
	if sourcePath == "" {
		return "", fmt.Errorf("session %q has no source path for handoff", record.identity.SessionID())
	}
	sidecar, err := buildSessionHandoffSidecar(record.identity.SessionID(), sourcePath, s.registry.now())
	if err != nil {
		return "", err
	}
	path, err := newSessionHandoffSidecarPath(record.cwd, record.identity.SessionID(), s.registry.now())
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(sidecar, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal handoff sidecar: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", fmt.Errorf("write handoff sidecar %q: %w", path, err)
	}
	return path, nil
}

func newSessionHandoffSidecarPath(cwd string, sessionID session.SessionID, now time.Time) (string, error) {
	if err := sessionID.Validate(); err != nil {
		return "", err
	}
	root := piHistoryBaseRoot()
	dir := filepath.Join(root, piSessionDirName(cwd), "actrail-handoffs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir handoff sidecar dir %q: %w", dir, err)
	}
	stamp := now.UTC().Format("2006-01-02T15-04-05-000Z")
	return filepath.Join(dir, fmt.Sprintf("%s_handoff_%s.json", stamp, sessionID.String())), nil
}

func buildSessionHandoffSidecar(sessionID session.SessionID, sourcePath string, now time.Time) (sessionHandoffSidecar, error) {
	rows, err := handoffSourceRows(sourcePath)
	if err != nil {
		return sessionHandoffSidecar{}, err
	}
	start := lastCompactionEndRow(rows)
	selected := rows[start:]
	entries := make([]sessionHandoffEntry, 0, len(selected))
	for _, row := range selected {
		entries = append(entries, handoffEntriesFromRow(row)...)
	}
	masked := maskOldHandoffToolResults(entries)
	return sessionHandoffSidecar{
		Version:            handoffSidecarFormatVersion,
		Kind:               "actrail_session_handoff",
		SourceSessionID:    sessionID.String(),
		SourcePath:         sourcePath,
		GeneratedAt:        now.UTC().Format(time.RFC3339Nano),
		StartsAfterCompact: start > 0,
		FirstSourceLine:    firstSelectedSourceLine(selected),
		RecentUserTurns:    handoffRecentUserTurns,
		MaskedToolResults:  masked,
		Entries:            entries,
	}, nil
}

type handoffSourceRow struct {
	line     int
	raw      map[string]any
	material pi.Material
}

func handoffSourceRows(sourcePath string) ([]handoffSourceRow, error) {
	file, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open handoff source %q: %w", sourcePath, err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	rows := make([]handoffSourceRow, 0)
	lineNo := 0
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && len(line) == 0 {
			if !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("read handoff source %q line %d: %w", sourcePath, lineNo+1, err)
			}
			break
		}
		lineNo++
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			var raw map[string]any
			if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
				return nil, fmt.Errorf("parse handoff source %q line %d: %w", sourcePath, lineNo, err)
			}
			material := pi.ParseRawObject(raw)
			rows = append(rows, handoffSourceRow{line: lineNo, raw: raw, material: material})
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("read handoff source %q line %d: %w", sourcePath, lineNo, err)
			}
			break
		}
	}
	return rows, nil
}

func lastCompactionEndRow(rows []handoffSourceRow) int {
	start := 0
	for i, row := range rows {
		for _, event := range row.material.Events {
			if event.Compaction != nil && event.Compaction.Phase == "end" && !event.Compaction.Aborted {
				start = i + 1
			}
		}
	}
	if start >= len(rows) {
		return 0
	}
	return start
}

func firstSelectedSourceLine(rows []handoffSourceRow) int {
	if len(rows) == 0 {
		return 0
	}
	return rows[0].line
}

func handoffEntriesFromRow(row handoffSourceRow) []sessionHandoffEntry {
	out := make([]sessionHandoffEntry, 0, len(row.material.Events))
	for _, event := range row.material.Events {
		sourceID := strings.TrimSpace(event.RawID)
		parentID := strings.TrimSpace(event.ParentID)
		switch event.Kind {
		case pi.EventKindMessage:
			if event.Message == nil {
				continue
			}
			text := strings.TrimSpace(event.Message.Text)
			if text == "" || strings.TrimSpace(event.Message.StopReason) == "status" {
				continue
			}
			if event.Message.Role != pi.MessageRoleUser && event.Message.Role != pi.MessageRoleAssistant {
				continue
			}
			if event.Message.Role == pi.MessageRoleAssistant && !event.Message.CommitLike {
				continue
			}
			out = append(out, sessionHandoffEntry{Line: row.line, SourceID: sourceID, ParentID: parentID, Role: string(event.Message.Role), Kind: "message", Text: text})
		case pi.EventKindTool:
			if event.Tool == nil {
				continue
			}
			kind := "tool_call"
			if event.Tool.Result {
				kind = "tool_result"
			}
			out = append(out, sessionHandoffEntry{
				Line:       row.line,
				SourceID:   sourceID,
				ParentID:   parentID,
				Kind:       kind,
				ToolCallID: strings.TrimSpace(event.Tool.CallID),
				ToolName:   strings.TrimSpace(event.Tool.Name),
				IsError:    event.Tool.IsError,
				Text:       strings.TrimSpace(event.Tool.Text),
				Arguments:  event.Tool.Arguments,
			})
		case pi.EventKindError:
			if event.Error == nil || strings.TrimSpace(event.Error.Message) == "" {
				continue
			}
			out = append(out, sessionHandoffEntry{Line: row.line, SourceID: sourceID, ParentID: parentID, Kind: "error", IsError: true, Text: strings.TrimSpace(event.Error.Message)})
		}
	}
	return out
}

func maskOldHandoffToolResults(entries []sessionHandoffEntry) int {
	remainingUserTurns := handoffRecentUserTurns
	masked := 0
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == "message" && entries[i].Role == string(pi.MessageRoleUser) {
			if remainingUserTurns > 0 {
				remainingUserTurns--
				continue
			}
		}
		if remainingUserTurns == 0 && entries[i].Kind == "tool_result" && strings.TrimSpace(entries[i].Text) != "" {
			entries[i].OriginalSize = len(entries[i].Text)
			entries[i].Text = handoffMaskedToolText
			entries[i].Masked = true
			entries[i].MaskReason = "older_than_recent_user_turn_window"
			masked++
		}
	}
	return masked
}
