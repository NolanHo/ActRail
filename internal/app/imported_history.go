package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/pi"
)

func applyImportedSourceRefs(records []sessionRecord, refs []sqlitestore.SessionSourceRefRow) []sessionRecord {
	if len(records) == 0 || len(refs) == 0 {
		return records
	}
	bySessionID := make(map[string]sqlitestore.SessionSourceRefRow, len(refs))
	for _, row := range refs {
		bySessionID[strings.TrimSpace(row.SessionID)] = row
	}
	for i := range records {
		row, ok := bySessionID[records[i].identity.SessionID().String()]
		if !ok {
			continue
		}
		records[i].importedSourcePath = strings.TrimSpace(row.SourcePath)
		records[i].importedFirstUserMessage = strings.TrimSpace(row.FirstUserMessage)
		records[i].importedHasLegacySessionUIState = row.HasLegacySessionUIState
	}
	return records
}

func firstUserMessageForRecord(record sessionRecord) string {
	if text := firstUserMessage(record.transcript); text != "" {
		return text
	}
	return strings.TrimSpace(record.importedFirstUserMessage)
}

func loadDetachedImportedPIHistory(_ context.Context, record sessionRecord, req SessionMessagesRequest) (SessionMessagesResponse, bool, error) {
	if record.transcript.Len() > 0 {
		return SessionMessagesResponse{}, false, nil
	}
	if _, ok := record.transcript.PartialAssistantTurn(); ok {
		return SessionMessagesResponse{}, false, nil
	}
	sourcePath := strings.TrimSpace(record.importedSourcePath)
	if sourcePath == "" || record.identity.Backend().String() != "pi" {
		return SessionMessagesResponse{}, false, nil
	}
	items, err := importedSessionMessagesFromSourcePath(sourcePath)
	if err != nil {
		return SessionMessagesResponse{}, true, err
	}
	return paginateSessionMessages(items, req.BeforeSeq, req.Limit), true, nil
}

func importedSessionMessagesFromSourcePath(sourcePath string) ([]SessionMessage, error) {
	body, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("read imported pi session source %q: %w", sourcePath, err)
	}
	material, err := pi.ParseJSONLBytes(body)
	if err != nil {
		return nil, fmt.Errorf("parse imported pi session source %q: %w", sourcePath, err)
	}
	items := make([]SessionMessage, 0, len(material.Events))
	var seq uint64
	for _, event := range material.Events {
		if event.Kind != pi.EventKindMessage || event.Message == nil {
			continue
		}
		if strings.TrimSpace(event.Message.Text) == "" {
			continue
		}
		if event.Message.Role != pi.MessageRoleUser && event.Message.Role != pi.MessageRoleAssistant {
			continue
		}
		if event.Message.Role == pi.MessageRoleAssistant && !event.Message.CommitLike {
			continue
		}
		seq++
		items = append(items, SessionMessage{
			Seq:  seq,
			Role: string(event.Message.Role),
			Kind: "message",
			Text: event.Message.Text,
			TS:   event.Timestamp,
		})
	}
	return items, nil
}

func paginateSessionMessages(items []SessionMessage, before *uint64, limit int) SessionMessagesResponse {
	upper := len(items)
	if before != nil {
		upper = 0
		for idx, item := range items {
			if item.Seq >= *before {
				upper = idx
				break
			}
			upper = idx + 1
		}
	}
	start := 0
	if limit > 0 && upper > limit {
		start = upper - limit
	}
	page := append([]SessionMessage(nil), items[start:upper]...)
	response := SessionMessagesResponse{
		Items:   page,
		HasMore: start > 0,
	}
	if len(items) > 0 {
		response.TailSeq = items[len(items)-1].Seq
	}
	if response.HasMore && len(page) > 0 {
		next := page[0].Seq
		response.NextBeforeSeq = &next
	}
	return response
}
