package app

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"actrail/internal/domain/session"
)

const (
	codexSessionFileScopeAll = "all"
	codexSessionFileScopeCWD = "cwd"
)

type CodexSessionFilesRequest struct {
	Scope  string
	CWD    string
	Offset int
	Limit  int
	Query  string
}

type CodexSessionFileSummary struct {
	ThreadID         string  `json:"thread_id"`
	SessionID        string  `json:"session_id,omitempty"`
	Title            string  `json:"title,omitempty"`
	DisplayName      string  `json:"display_name,omitempty"`
	CWD              string  `json:"cwd,omitempty"`
	Path             string  `json:"path,omitempty"`
	FirstUserMessage string  `json:"first_user_message,omitempty"`
	UpdatedTS        float64 `json:"updated_ts,omitempty"`
	Archived         bool    `json:"archived,omitempty"`
	Source           string  `json:"source,omitempty"`
}

type CodexSessionFilesResponse struct {
	OK        bool                      `json:"ok"`
	Scope     string                    `json:"scope"`
	CWD       string                    `json:"cwd,omitempty"`
	Offset    int                       `json:"offset"`
	Limit     int                       `json:"limit"`
	Remaining int                       `json:"remaining"`
	Items     []CodexSessionFileSummary `json:"items"`
}

type CodexSessionFileRequest struct {
	ThreadID string
	Path     string
	Limit    int
}

type CodexSessionFileTurn struct {
	Index     int              `json:"index"`
	User      *SessionMessage  `json:"user,omitempty"`
	Assistant *SessionMessage  `json:"assistant,omitempty"`
	Messages  []SessionMessage `json:"messages,omitempty"`
}

type CodexSessionFileResponse struct {
	OK        bool                    `json:"ok"`
	Summary   CodexSessionFileSummary `json:"summary"`
	Items     []SessionMessage        `json:"items"`
	Turns     []CodexSessionFileTurn  `json:"turns"`
	TailSeq   uint64                  `json:"tail_seq,omitempty"`
	HasMore   bool                    `json:"has_more,omitempty"`
	Truncated bool                    `json:"truncated,omitempty"`
}

type RenameCodexSessionFileRequest struct {
	ThreadID string
	Name     string
}

type RenameCodexSessionFileResponse struct {
	OK      bool                    `json:"ok"`
	Summary CodexSessionFileSummary `json:"summary"`
}

type codexSessionFileIndex struct {
	itemsByThread map[string]CodexSessionFileSummary
}

func (s *Stub) CodexSessionFiles(ctx context.Context, req CodexSessionFilesRequest) (CodexSessionFilesResponse, error) {
	scope := normalizeCodexSessionFileScope(req.Scope)
	cwd := normalizeSessionCWD(req.CWD)
	if scope == codexSessionFileScopeCWD && cwd == "" {
		return CodexSessionFilesResponse{}, Invalid("cwd", "cwd required for cwd scope")
	}
	offset, limit := normalizeOffsetLimit(req.Offset, req.Limit, 50)
	index, err := s.codexSessionFileIndex(ctx, scope, cwd)
	if err != nil {
		return CodexSessionFilesResponse{}, err
	}
	items := make([]CodexSessionFileSummary, 0, len(index.itemsByThread))
	query := strings.ToLower(strings.TrimSpace(req.Query))
	for _, item := range index.itemsByThread {
		if query != "" && !codexSessionFileSummaryMatchesQuery(item, query) {
			continue
		}
		items = append(items, item)
	}
	sortCodexSessionFileSummaries(items)
	start, end := paginate(len(items), offset, limit)
	return CodexSessionFilesResponse{
		OK:        true,
		Scope:     scope,
		CWD:       cwd,
		Offset:    offset,
		Limit:     limit,
		Remaining: len(items) - end,
		Items:     append([]CodexSessionFileSummary(nil), items[start:end]...),
	}, nil
}

func (s *Stub) CodexSessionFile(ctx context.Context, req CodexSessionFileRequest) (CodexSessionFileResponse, error) {
	summary, err := s.codexSessionFileSummaryForRequest(ctx, req.ThreadID, req.Path)
	if err != nil {
		return CodexSessionFileResponse{}, err
	}
	if strings.TrimSpace(summary.Path) == "" {
		return CodexSessionFileResponse{}, NotFound("codex session file not found")
	}
	items, err := codexSessionMessagesFromFile(ctx, summary.Path)
	if err != nil {
		return CodexSessionFileResponse{}, err
	}
	tailSeq := uint64(0)
	if len(items) > 0 {
		tailSeq = items[len(items)-1].Seq
	}
	limit := req.Limit
	truncated := false
	if limit > 0 && len(items) > limit {
		items = append([]SessionMessage(nil), items[len(items)-limit:]...)
		truncated = true
	} else {
		items = append([]SessionMessage(nil), items...)
	}
	return CodexSessionFileResponse{
		OK:        true,
		Summary:   summary,
		Items:     items,
		Turns:     codexSessionFileTurns(items),
		TailSeq:   tailSeq,
		HasMore:   truncated,
		Truncated: truncated,
	}, nil
}

func (s *Stub) RenameCodexSessionFile(ctx context.Context, req RenameCodexSessionFileRequest) (RenameCodexSessionFileResponse, error) {
	threadID := normalizeCodexThreadID(req.ThreadID)
	if threadID == "" {
		return RenameCodexSessionFileResponse{}, Invalid("thread_id", "thread_id required")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return RenameCodexSessionFileResponse{}, Invalid("name", "name required")
	}
	summary, err := s.codexSessionFileSummaryForRequest(ctx, threadID, "")
	if err != nil {
		return RenameCodexSessionFileResponse{}, err
	}
	if strings.TrimSpace(summary.ThreadID) == "" {
		return RenameCodexSessionFileResponse{}, NotFound("codex session file not found")
	}
	if err := updateCodexThreadTitle(threadID, name); err != nil {
		return RenameCodexSessionFileResponse{}, err
	}
	if err := appendCodexThreadName(threadID, name, time.Now().UTC()); err != nil {
		return RenameCodexSessionFileResponse{}, err
	}
	s.renameMatchingActRailCodexSessions(threadID, summary.Path, name)
	summary.Title = name
	summary.DisplayName = name
	return RenameCodexSessionFileResponse{OK: true, Summary: summary}, nil
}

func (s *Stub) codexSessionFileIndex(ctx context.Context, scope, cwd string) (codexSessionFileIndex, error) {
	items := map[string]CodexSessionFileSummary{}
	if err := addCodexStateDBSessionFiles(ctx, items, scope, cwd); err != nil {
		return codexSessionFileIndex{}, err
	}
	s.decorateCodexSessionFileSummaries(items)
	return codexSessionFileIndex{itemsByThread: items}, nil
}

func addCodexStateDBSessionFiles(ctx context.Context, items map[string]CodexSessionFileSummary, scope, cwd string) error {
	dbPath, ok := latestCodexStateDBPath()
	if !ok {
		return nil
	}
	db, err := sql.Open("sqlite", codexStateDBDSN(dbPath))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	query := `SELECT id, rollout_path, cwd, title, first_user_message, archived,
			COALESCE(updated_at_ms, updated_at * 1000, created_at_ms, created_at * 1000, 0) AS updated_ms
		FROM threads`
	args := []any{}
	if scope == codexSessionFileScopeCWD {
		query += ` WHERE cwd = ?`
		args = append(args, cwd)
	}
	query += ` ORDER BY updated_ms DESC, id DESC`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, rowCWD string
		var rolloutPath, title, firstUser sql.NullString
		var archived int
		var updatedMS int64
		if err := rows.Scan(&id, &rolloutPath, &rowCWD, &title, &firstUser, &archived, &updatedMS); err != nil {
			return err
		}
		threadID := strings.TrimSpace(id)
		if threadID == "" {
			continue
		}
		path := cleanOptionalPath(rolloutPath.String)
		firstUserText := strings.TrimSpace(firstUser.String)
		if isSyntheticResumePreviewUserText(firstUserText) {
			firstUserText = ""
		}
		name := strings.TrimSpace(title.String)
		if name == "" {
			name = truncateResumeTitle(firstUserText)
		}
		if name == "" {
			name = threadID
		}
		items[threadID] = CodexSessionFileSummary{
			ThreadID:         threadID,
			SessionID:        codexHistoricalSessionID(threadID),
			Title:            name,
			DisplayName:      name,
			CWD:              normalizeSessionCWD(rowCWD),
			Path:             path,
			FirstUserMessage: firstUserText,
			UpdatedTS:        timestampSeconds(time.UnixMilli(updatedMS).UTC()),
			Archived:         archived != 0,
			Source:           "state_db",
		}
	}
	return rows.Err()
}

func (s *Stub) codexSessionFileSummaryForRequest(ctx context.Context, threadID, path string) (CodexSessionFileSummary, error) {
	threadID = normalizeCodexThreadID(threadID)
	path = cleanOptionalPath(path)
	if threadID == "" && path == "" {
		return CodexSessionFileSummary{}, Invalid("thread_id", "thread_id or path required")
	}
	index, err := s.codexSessionFileIndex(ctx, codexSessionFileScopeAll, "")
	if err != nil {
		return CodexSessionFileSummary{}, err
	}
	if threadID != "" {
		if item, ok := index.itemsByThread[threadID]; ok {
			return item, nil
		}
		return CodexSessionFileSummary{}, NotFound("codex session file not found")
	}
	if !pathWithinCodexSessionRoot(path) {
		return CodexSessionFileSummary{}, Forbidden("path is outside codex sessions")
	}
	return s.codexSessionFileSummaryFromPath(ctx, path)
}

func (s *Stub) codexSessionFileSummaryFromPath(ctx context.Context, path string) (CodexSessionFileSummary, error) {
	if !pathWithinCodexSessionRoot(path) {
		return CodexSessionFileSummary{}, Forbidden("path is outside codex sessions")
	}
	threadID, cwd, firstUser, ok := codexResumeCandidateMetaFromSourcePath(path)
	if !ok {
		return CodexSessionFileSummary{}, NotFound("codex session file not found")
	}
	index, err := s.codexSessionFileIndex(ctx, codexSessionFileScopeAll, "")
	if err == nil {
		if item, ok := index.itemsByThread[threadID]; ok {
			item.Path = firstNonEmptyString(item.Path, cleanOptionalPath(path))
			return item, nil
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return CodexSessionFileSummary{}, NotFound("codex session file not found")
	}
	name := codexThreadNameFromSourcePath(path)
	if name == "" {
		name = truncateResumeTitle(firstUser)
	}
	if name == "" {
		name = threadID
	}
	return s.decorateCodexSessionFileSummary(CodexSessionFileSummary{
		ThreadID:         threadID,
		SessionID:        codexHistoricalSessionID(threadID),
		Title:            name,
		DisplayName:      name,
		CWD:              normalizeSessionCWD(cwd),
		Path:             cleanOptionalPath(path),
		FirstUserMessage: firstUser,
		UpdatedTS:        timestampSeconds(info.ModTime()),
		Source:           "rollout",
	}), nil
}

func (s *Stub) decorateCodexSessionFileSummary(item CodexSessionFileSummary) CodexSessionFileSummary {
	return s.decorateCodexSessionFileSummaryWithNames(item, codexThreadNamesByIDs([]string{item.ThreadID}))
}

func (s *Stub) decorateCodexSessionFileSummaries(items map[string]CodexSessionFileSummary) {
	if len(items) == 0 {
		return
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ThreadID); id != "" {
			ids = append(ids, id)
		}
	}
	names := codexThreadNamesByIDs(ids)
	for key, item := range items {
		items[key] = s.decorateCodexSessionFileSummaryWithNames(item, names)
	}
}

func (s *Stub) decorateCodexSessionFileSummaryWithNames(item CodexSessionFileSummary, names map[string]string) CodexSessionFileSummary {
	item.ThreadID = strings.TrimSpace(item.ThreadID)
	if item.ThreadID != "" {
		item.SessionID = codexHistoricalSessionID(item.ThreadID)
	}
	if strings.TrimSpace(names[item.ThreadID]) != "" {
		name := strings.TrimSpace(names[item.ThreadID])
		item.Title = name
		item.DisplayName = name
	}
	if s != nil && s.registry != nil {
		if owner, ok := s.registry.FindCodexRuntimeOwner(item.ThreadID, item.Path); ok {
			if name := strings.TrimSpace(sessionDisplayName(owner)); name != "" {
				item.Title = name
				item.DisplayName = name
			}
			item.CWD = firstNonEmptyString(item.CWD, owner.cwd)
			item.Path = firstNonEmptyString(item.Path, owner.importedSourcePath)
			item.FirstUserMessage = firstNonEmptyString(item.FirstUserMessage, firstUserMessageForRecord(owner))
		}
	}
	if item.DisplayName == "" {
		item.DisplayName = item.Title
	}
	if item.Title == "" {
		item.Title = firstNonEmptyString(truncateResumeTitle(item.FirstUserMessage), item.ThreadID)
		item.DisplayName = item.Title
	}
	return item
}

func updateCodexThreadTitle(threadID, name string) error {
	dbPath, ok := latestCodexStateDBPath()
	if !ok {
		return NotFound("codex state database not found")
	}
	db, err := sql.Open("sqlite", codexStateDBWriteDSN(dbPath))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	result, err := db.Exec(`UPDATE threads SET title = ? WHERE id = ?`, strings.TrimSpace(name), strings.TrimSpace(threadID))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return NotFound("codex thread metadata not found")
	}
	return nil
}

func appendCodexThreadName(threadID, name string, now time.Time) error {
	threadID = strings.TrimSpace(threadID)
	name = strings.TrimSpace(name)
	if threadID == "" || name == "" {
		return Invalid("name", "thread_id and name required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	path := codexSessionIndexPath()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	entry := codexSessionIndexEntry{
		ID:         threadID,
		ThreadName: name,
		UpdatedAt:  now.UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Stub) renameMatchingActRailCodexSessions(threadID, sourcePath, name string) {
	if s == nil || s.registry == nil {
		return
	}
	sourcePath = cleanOptionalPath(sourcePath)
	for _, record := range s.registry.ListAll() {
		if record.identity.Backend() != session.BackendCodex {
			continue
		}
		if strings.TrimSpace(record.importedBackendSessionID) != threadID && (sourcePath == "" || cleanOptionalPath(record.importedSourcePath) != sourcePath) {
			continue
		}
		_, _, _ = s.registry.Update(record.identity.SessionID(), false, func(record *sessionRecord) error {
			record.title = name
			record.alias = name
			return nil
		})
	}
}

func codexSessionFileTurns(items []SessionMessage) []CodexSessionFileTurn {
	turns := make([]CodexSessionFileTurn, 0)
	var current *CodexSessionFileTurn
	for _, item := range items {
		role := strings.TrimSpace(item.Role)
		if role == "user" {
			turn := CodexSessionFileTurn{Index: len(turns) + 1}
			msg := item
			turn.User = &msg
			turn.Messages = append(turn.Messages, item)
			turns = append(turns, turn)
			current = &turns[len(turns)-1]
			continue
		}
		if role == "assistant" {
			if current == nil || current.Assistant != nil {
				turn := CodexSessionFileTurn{Index: len(turns) + 1}
				turns = append(turns, turn)
				current = &turns[len(turns)-1]
			}
			msg := item
			current.Assistant = &msg
			current.Messages = append(current.Messages, item)
			continue
		}
		if current != nil {
			current.Messages = append(current.Messages, item)
		}
	}
	return turns
}

func sortCodexSessionFileSummaries(items []CodexSessionFileSummary) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].UpdatedTS != items[j].UpdatedTS {
			return items[i].UpdatedTS > items[j].UpdatedTS
		}
		return items[i].ThreadID < items[j].ThreadID
	})
}

func normalizeCodexSessionFileScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", codexSessionFileScopeCWD:
		return codexSessionFileScopeCWD
	case codexSessionFileScopeAll:
		return codexSessionFileScopeAll
	default:
		return codexSessionFileScopeCWD
	}
}

func normalizeOffsetLimit(offset, limit, defaultLimit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	if limit == 0 {
		limit = defaultLimit
	}
	return offset, limit
}

func codexSessionFileSummaryMatchesQuery(item CodexSessionFileSummary, query string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		item.ThreadID,
		item.Title,
		item.DisplayName,
		item.CWD,
		item.Path,
		item.FirstUserMessage,
	}, "\n"))
	return strings.Contains(haystack, query)
}

func codexHistoricalSessionID(threadID string) string {
	durableID, err := session.NewDurableID(strings.TrimSpace(threadID))
	if err != nil {
		return ""
	}
	sessionID, err := session.NewHistoricalSessionID(session.BackendCodex, durableID)
	if err != nil {
		return ""
	}
	return sessionID.String()
}

func normalizeCodexThreadID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if ref, err := session.ParseHistoricalSessionID(value); err == nil && ref.Backend == session.BackendCodex {
		return strings.TrimSpace(ref.Durable.String())
	}
	return value
}

func pathWithinCodexSessionRoot(path string) bool {
	path = cleanOptionalPath(path)
	if path == "" {
		return false
	}
	root, err := filepath.Abs(codexSessionRoot())
	if err != nil {
		root = codexSessionRoot()
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..")
}

func codexThreadNameFromIndex(threadID string) string {
	file, err := os.Open(codexSessionIndexPath())
	if err != nil {
		return ""
	}
	defer file.Close()
	name := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), codexSessionFileMaxLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry codexSessionIndexEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if strings.TrimSpace(entry.ID) == strings.TrimSpace(threadID) && strings.TrimSpace(entry.ThreadName) != "" {
			name = strings.TrimSpace(entry.ThreadName)
		}
	}
	return name
}
