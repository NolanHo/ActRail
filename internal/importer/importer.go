package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"

	_ "modernc.org/sqlite"
)

const sideJSONFreshnessWindow = 72 * time.Hour

type Options struct {
	SourceSQLitePath string
	TargetSQLitePath string
	SideDir          string
	SnapshotAt       time.Time
}

type SourceCounts struct {
	Sessions             int `json:"sessions"`
	SessionUIState       int `json:"session_ui_state"`
	HiddenSessionKeys    int `json:"hidden_session_keys"`
	SessionFiles         int `json:"session_files"`
	SessionQueueItems    int `json:"session_queue_items"`
	RecentCwds           int `json:"recent_cwds"`
	CwdGroups            int `json:"cwd_groups"`
	AppKV                int `json:"app_kv"`
	LegacyImportUnmapped int `json:"legacy_import_unmapped"`
	SessionKeyUnion      int `json:"session_key_union"`
}

type ImportedCounts struct {
	SessionCatalogRows       int `json:"session_catalog_rows"`
	SessionSourceRefRows     int `json:"session_source_ref_rows"`
	SessionUIStateMergedRows int `json:"session_ui_state_merged_rows"`
	HiddenSessionKeyRows     int `json:"hidden_session_key_rows"`
	SessionQueueItemRows     int `json:"session_queue_item_rows"`
	SessionFileMappedRows    int `json:"session_file_mapped_rows"`
	SessionFileSkippedRows   int `json:"session_file_skipped_rows"`
	RecentCwdRows            int `json:"recent_cwd_rows"`
	CwdGroupRows             int `json:"cwd_group_rows"`
	AppKVRows                int `json:"app_kv_rows"`
	MigrationWarningRows     int `json:"migration_warning_rows"`
	ImportProvenanceRows     int `json:"import_provenance_rows"`
}

type Validation struct {
	WarningCount            int        `json:"warning_count"`
	UnmappedCount           int        `json:"unmapped_count"`
	OrphanSessionUIState    int        `json:"orphan_session_ui_state"`
	OrphanSessionFiles      int        `json:"orphan_session_files"`
	OrphanSessionQueueItems int        `json:"orphan_session_queue_items"`
	Mismatches              []Mismatch `json:"mismatches,omitempty"`
}

type Mismatch struct {
	Name     string `json:"name"`
	Source   int    `json:"source"`
	Imported int    `json:"imported"`
	Details  string `json:"details,omitempty"`
}

type SideJSONAudit struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	ModifiedAt time.Time `json:"modified_at"`
	Fresh      bool      `json:"fresh"`
	Ignored    bool      `json:"ignored"`
	EntryCount int       `json:"entry_count"`
	ParseError string    `json:"parse_error,omitempty"`
}

type Report struct {
	SourceSQLitePath string          `json:"source_sqlite_path"`
	TargetSQLitePath string          `json:"target_sqlite_path"`
	SnapshotAt       time.Time       `json:"snapshot_at"`
	SourceCounts     SourceCounts    `json:"source_counts"`
	ImportedCounts   ImportedCounts  `json:"imported_counts"`
	Validation       Validation      `json:"validation"`
	SideJSON         []SideJSONAudit `json:"side_json,omitempty"`
}

type legacySessionRow struct {
	Backend          string
	SessionID        string
	CWD              string
	SourcePath       string
	Title            string
	FirstUserMessage string
	CreatedAt        *time.Time
	UpdatedAt        *time.Time
	PendingStartup   bool
}

type legacyUIStateRow struct {
	Backend             string
	SessionID           string
	Alias               string
	Focused             bool
	Hidden              bool
	PriorityOffset      float64
	SnoozeUntil         *time.Time
	DependencyBackend   string
	DependencySessionID string
}

type legacySessionFileRow struct {
	Backend    string
	SessionID  string
	Path       string
	Ordinal    int
	LastUsedAt *time.Time
}

type legacyQueueItemRow struct {
	Backend   string
	SessionID string
	Ordinal   int
	Text      string
}

type legacyRecentCwdRow struct {
	CWD        string
	LastUsedAt *time.Time
}

type legacyCwdGroupRow struct {
	CWD       string
	Label     string
	Collapsed bool
}

type legacyAppKVRow struct {
	Namespace string
	Key       string
	ValueJSON string
}

type legacyImportUnmappedRow struct {
	ID          int64
	SourceName  string
	LegacyKey   string
	PayloadJSON string
	ImportedAt  *time.Time
}

type legacySnapshot struct {
	Sessions             []legacySessionRow
	SessionUIState       []legacyUIStateRow
	HiddenSessionKeys    []string
	SessionFiles         []legacySessionFileRow
	SessionQueueItems    []legacyQueueItemRow
	RecentCwds           []legacyRecentCwdRow
	CwdGroups            []legacyCwdGroupRow
	AppKV                []legacyAppKVRow
	LegacyImportUnmapped []legacyImportUnmappedRow
}

type importBuildState struct {
	OrphanSessionUIState    int
	OrphanSessionFiles      int
	OrphanSessionQueueItems int
	SessionFileSkippedRows  int
	UnmappedCount           int
}

type sessionKey struct {
	Backend   string
	SessionID string
}

func (k sessionKey) String() string {
	return k.Backend + ":" + k.SessionID
}

func Run(ctx context.Context, opts Options) (Report, error) {
	opts, err := normalizeOptions(opts)
	if err != nil {
		return Report{}, err
	}
	sourceDB, err := openLegacySource(opts.SourceSQLitePath)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = sourceDB.Close() }()

	source, err := readLegacySnapshot(ctx, sourceDB)
	if err != nil {
		return Report{}, err
	}
	sideAudit, err := auditSideJSON(opts.SideDir, opts.SnapshotAt)
	if err != nil {
		return Report{}, err
	}
	bundle, sourceCounts, importedCounts, validation, err := buildImportBundle(source, opts, sideAudit)
	if err != nil {
		return Report{}, err
	}
	if err := os.MkdirAll(filepath.Dir(opts.TargetSQLitePath), 0o755); err != nil {
		return Report{}, fmt.Errorf("ensure target sqlite dir: %w", err)
	}
	catalog, err := sqlitestore.OpenSessionCatalog(opts.TargetSQLitePath)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = catalog.Close() }()
	if err := catalog.ReplaceImportBundle(ctx, bundle); err != nil {
		return Report{}, err
	}
	actual, err := collectTargetCounts(ctx, catalog)
	if err != nil {
		return Report{}, err
	}
	mergeTargetCounts(&importedCounts, actual)
	validation.Mismatches = append(validation.Mismatches,
		mismatch("session_catalog_rows", sourceCounts.Sessions, importedCounts.SessionCatalogRows, "only sessions rows become visible/importable sessions; orphan auxiliary rows remain in migration warnings"),
		mismatch("session_source_ref_rows", sourceCounts.Sessions, importedCounts.SessionSourceRefRows, "source refs preserve source_path and first_user_message for sessions rows"),
		mismatch("hidden_session_key_rows", sourceCounts.HiddenSessionKeys, importedCounts.HiddenSessionKeyRows, "hidden session compatibility keys"),
		mismatch("session_queue_item_rows", sourceCounts.SessionQueueItems, importedCounts.SessionQueueItemRows, "queue items imported with generated durable queue ids"),
		mismatch("session_file_mapped_rows", sourceCounts.SessionFiles, importedCounts.SessionFileMappedRows+importedCounts.SessionFileSkippedRows, "session_files mapped into workspace history or explicit warnings"),
		mismatch("recent_cwd_rows", sourceCounts.RecentCwds, importedCounts.RecentCwdRows, "recent cwd list ordered by source last_used_ts desc"),
		mismatch("cwd_group_rows", sourceCounts.CwdGroups, importedCounts.CwdGroupRows, "cwd group metadata"),
		mismatch("app_kv_rows", sourceCounts.AppKV, importedCounts.AppKVRows, "compat app_kv import"),
		mismatch("migration_warning_rows", validation.WarningCount, importedCounts.MigrationWarningRows, "legacy unmapped rows and import-time warnings"),
	)
	validation.Mismatches = compactMismatches(validation.Mismatches)
	return Report{
		SourceSQLitePath: opts.SourceSQLitePath,
		TargetSQLitePath: opts.TargetSQLitePath,
		SnapshotAt:       opts.SnapshotAt,
		SourceCounts:     sourceCounts,
		ImportedCounts:   importedCounts,
		Validation:       validation,
		SideJSON:         sideAudit,
	}, nil
}

func normalizeOptions(opts Options) (Options, error) {
	opts.SourceSQLitePath = strings.TrimSpace(opts.SourceSQLitePath)
	opts.TargetSQLitePath = strings.TrimSpace(opts.TargetSQLitePath)
	opts.SideDir = strings.TrimSpace(opts.SideDir)
	if opts.SourceSQLitePath == "" {
		return Options{}, fmt.Errorf("source sqlite path is required")
	}
	if opts.TargetSQLitePath == "" {
		return Options{}, fmt.Errorf("target sqlite path is required")
	}
	if opts.SnapshotAt.IsZero() {
		opts.SnapshotAt = time.Now().UTC()
	} else {
		opts.SnapshotAt = opts.SnapshotAt.UTC()
	}
	return opts, nil
}

func openLegacySource(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open legacy sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA query_only=ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set legacy sqlite query_only: %w", err)
	}
	return db, nil
}

func readLegacySnapshot(ctx context.Context, db *sql.DB) (legacySnapshot, error) {
	sessions, err := readLegacySessions(ctx, db)
	if err != nil {
		return legacySnapshot{}, err
	}
	uiState, err := readLegacyUIState(ctx, db)
	if err != nil {
		return legacySnapshot{}, err
	}
	hiddenKeys, err := readLegacyHiddenSessionKeys(ctx, db)
	if err != nil {
		return legacySnapshot{}, err
	}
	files, err := readLegacySessionFiles(ctx, db)
	if err != nil {
		return legacySnapshot{}, err
	}
	queue, err := readLegacySessionQueueItems(ctx, db)
	if err != nil {
		return legacySnapshot{}, err
	}
	recent, err := readLegacyRecentCwds(ctx, db)
	if err != nil {
		return legacySnapshot{}, err
	}
	groups, err := readLegacyCwdGroups(ctx, db)
	if err != nil {
		return legacySnapshot{}, err
	}
	appKV, err := readLegacyAppKV(ctx, db)
	if err != nil {
		return legacySnapshot{}, err
	}
	unmapped, err := readLegacyImportUnmapped(ctx, db)
	if err != nil {
		return legacySnapshot{}, err
	}
	return legacySnapshot{
		Sessions:             sessions,
		SessionUIState:       uiState,
		HiddenSessionKeys:    hiddenKeys,
		SessionFiles:         files,
		SessionQueueItems:    queue,
		RecentCwds:           recent,
		CwdGroups:            groups,
		AppKV:                appKV,
		LegacyImportUnmapped: unmapped,
	}, nil
}

func buildImportBundle(source legacySnapshot, opts Options, sideAudit []SideJSONAudit) (sqlitestore.ImportBundle, SourceCounts, ImportedCounts, Validation, error) {
	sourceCounts := countSource(source)
	bundle := sqlitestore.ImportBundle{
		Sessions:          []sqlitestore.SessionSnapshotRow{},
		SessionSourceRefs: []sqlitestore.SessionSourceRefRow{},
		AppState:          sqlitestore.AppStateRow{RecentCwds: []string{}, CwdGroups: []sqlitestore.CwdGroupRow{}},
		HiddenSessionKeys: []sqlitestore.HiddenSessionKeyRow{},
		AppKV:             []sqlitestore.AppKVRow{},
		Warnings:          []sqlitestore.MigrationWarningRow{},
	}
	state := importBuildState{}
	sessionByKey := make(map[sessionKey]legacySessionRow, len(source.Sessions))
	uiByKey := make(map[sessionKey]legacyUIStateRow, len(source.SessionUIState))
	filesByKey := make(map[sessionKey][]legacySessionFileRow)
	queueByKey := make(map[sessionKey][]legacyQueueItemRow)
	for _, row := range source.Sessions {
		normalized := normalizeLegacySessionRow(row)
		key := sessionKey{Backend: normalized.Backend, SessionID: normalized.SessionID}
		sessionByKey[key] = normalized
	}
	for _, row := range source.SessionUIState {
		normalized := normalizeLegacyUIStateRow(row)
		key := sessionKey{Backend: normalized.Backend, SessionID: normalized.SessionID}
		uiByKey[key] = normalized
	}
	for _, row := range source.SessionFiles {
		normalized := normalizeLegacySessionFileRow(row)
		key := sessionKey{Backend: normalized.Backend, SessionID: normalized.SessionID}
		filesByKey[key] = append(filesByKey[key], normalized)
	}
	for _, row := range source.SessionQueueItems {
		normalized := normalizeLegacyQueueItemRow(row)
		key := sessionKey{Backend: normalized.Backend, SessionID: normalized.SessionID}
		queueByKey[key] = append(queueByKey[key], normalized)
	}
	hiddenKeySet := hiddenSessionKeySet(source.HiddenSessionKeys)
	for _, key := range sortedSessionKeys(sessionByKey) {
		sessionRow := sessionByKey[key]
		uiRow, hasUI := uiByKey[key]
		if sessionRow.PendingStartup {
			appendWarning(&bundle.Warnings, &state, "sessions", key.String(), "ignored_pending_startup", "pending_startup is runtime-only and not imported", sessionRow)
		}
		if hasUI && uiRow.DependencyBackend != "" && uiRow.DependencyBackend != key.Backend {
			appendWarning(&bundle.Warnings, &state, "session_ui_state", key.String(), "dependency_backend_mismatch", "dependency_backend does not match row backend; dependency_session_id preserved without backend remap", uiRow)
		}
		snapshot := sqlitestore.SessionSnapshotRow{
			Session:   buildSessionCatalogRow(key, sessionRow, true, uiRow, hasUI, sessionHiddenByKeys(key, hiddenKeySet), opts.SnapshotAt),
			Queue:     buildQueueRows(key, queueByKey[key]),
			Workspace: buildWorkspaceRows(key, sessionRow, true, filesByKey[key], &bundle.Warnings, &state),
		}
		bundle.Sessions = append(bundle.Sessions, snapshot)
		bundle.SessionSourceRefs = append(bundle.SessionSourceRefs, sqlitestore.SessionSourceRefRow{
			SessionID:        key.SessionID,
			Backend:          key.Backend,
			SourcePath:       strings.TrimSpace(sessionRow.SourcePath),
			FirstUserMessage: strings.TrimSpace(sessionRow.FirstUserMessage),
		})
	}
	for _, key := range sortedOrphanKeys(uiByKey, sessionByKey) {
		state.OrphanSessionUIState++
		appendWarning(&bundle.Warnings, &state, "session_ui_state", key.String(), "orphan_session_ui_state", "session_ui_state row has no sessions row; preserved in migration warnings", uiByKey[key])
	}
	for _, key := range sortedOrphanKeys(filesByKey, sessionByKey) {
		state.OrphanSessionFiles++
		appendWarning(&bundle.Warnings, &state, "session_files", key.String(), "orphan_session_files", "session_files rows have no sessions row; preserved in migration warnings", filesByKey[key])
	}
	for _, key := range sortedOrphanKeys(queueByKey, sessionByKey) {
		state.OrphanSessionQueueItems++
		appendWarning(&bundle.Warnings, &state, "session_queue_items", key.String(), "orphan_session_queue_items", "session_queue_items rows have no sessions row; preserved in migration warnings", queueByKey[key])
	}
	bundle.AppState = buildAppState(source)
	for _, key := range source.HiddenSessionKeys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		bundle.HiddenSessionKeys = append(bundle.HiddenSessionKeys, sqlitestore.HiddenSessionKeyRow{Key: trimmed})
	}
	for _, row := range source.AppKV {
		bundle.AppKV = append(bundle.AppKV, sqlitestore.AppKVRow{
			Namespace: strings.TrimSpace(row.Namespace),
			Key:       strings.TrimSpace(row.Key),
			ValueJSON: strings.TrimSpace(row.ValueJSON),
		})
	}
	for _, row := range source.LegacyImportUnmapped {
		payload := map[string]any{
			"id":           row.ID,
			"source_name":  row.SourceName,
			"legacy_key":   row.LegacyKey,
			"payload_json": row.PayloadJSON,
		}
		if row.ImportedAt != nil {
			payload["imported_at"] = row.ImportedAt.UTC().Format(time.RFC3339Nano)
		}
		appendWarning(&bundle.Warnings, &state, row.SourceName, strings.TrimSpace(row.LegacyKey), "legacy_import_unmapped", "legacy_import_unmapped row preserved in migration warnings", payload)
	}
	preReport := map[string]any{
		"source_counts": sourceCounts,
		"side_json":     sideAudit,
		"warning_count": len(bundle.Warnings),
		"snapshot_at":   opts.SnapshotAt.UTC().Format(time.RFC3339Nano),
		"target_sqlite": opts.TargetSQLitePath,
		"source_sqlite": opts.SourceSQLitePath,
	}
	detailsJSON, err := json.Marshal(preReport)
	if err != nil {
		return sqlitestore.ImportBundle{}, SourceCounts{}, ImportedCounts{}, Validation{}, fmt.Errorf("marshal import provenance details: %w", err)
	}
	bundle.Provenance = sqlitestore.ImportProvenanceRow{
		Source:      opts.SourceSQLitePath,
		SnapshotAt:  opts.SnapshotAt,
		DetailsJSON: string(detailsJSON),
	}
	importedCounts := ImportedCounts{
		SessionCatalogRows:       len(bundle.Sessions),
		SessionSourceRefRows:     len(bundle.SessionSourceRefs),
		SessionUIStateMergedRows: len(source.SessionUIState),
		HiddenSessionKeyRows:     len(bundle.HiddenSessionKeys),
		SessionQueueItemRows:     countQueueRows(bundle.Sessions),
		SessionFileMappedRows:    countWorkspaceHistoryRows(bundle.Sessions),
		SessionFileSkippedRows:   state.SessionFileSkippedRows,
		RecentCwdRows:            len(bundle.AppState.RecentCwds),
		CwdGroupRows:             len(bundle.AppState.CwdGroups),
		AppKVRows:                len(bundle.AppKV),
		MigrationWarningRows:     len(bundle.Warnings),
		ImportProvenanceRows:     1,
	}
	validation := Validation{
		WarningCount:            len(bundle.Warnings),
		UnmappedCount:           state.UnmappedCount,
		OrphanSessionUIState:    state.OrphanSessionUIState,
		OrphanSessionFiles:      state.OrphanSessionFiles,
		OrphanSessionQueueItems: state.OrphanSessionQueueItems,
	}
	return bundle, sourceCounts, importedCounts, validation, nil
}

func normalizeLegacySessionRow(row legacySessionRow) legacySessionRow {
	row.Backend = strings.TrimSpace(row.Backend)
	row.SessionID = strings.TrimSpace(row.SessionID)
	row.CWD = cleanAbsPath(row.CWD)
	row.SourcePath = strings.TrimSpace(row.SourcePath)
	row.Title = strings.TrimSpace(row.Title)
	row.FirstUserMessage = strings.TrimSpace(row.FirstUserMessage)
	return row
}

func normalizeLegacyUIStateRow(row legacyUIStateRow) legacyUIStateRow {
	row.Backend = strings.TrimSpace(row.Backend)
	row.SessionID = strings.TrimSpace(row.SessionID)
	row.Alias = strings.TrimSpace(row.Alias)
	row.DependencyBackend = strings.TrimSpace(row.DependencyBackend)
	row.DependencySessionID = strings.TrimSpace(row.DependencySessionID)
	return row
}

func normalizeLegacySessionFileRow(row legacySessionFileRow) legacySessionFileRow {
	row.Backend = strings.TrimSpace(row.Backend)
	row.SessionID = strings.TrimSpace(row.SessionID)
	row.Path = strings.TrimSpace(row.Path)
	return row
}

func normalizeLegacyQueueItemRow(row legacyQueueItemRow) legacyQueueItemRow {
	row.Backend = strings.TrimSpace(row.Backend)
	row.SessionID = strings.TrimSpace(row.SessionID)
	row.Text = strings.TrimSpace(row.Text)
	return row
}

func sortedSessionKeys(items map[sessionKey]legacySessionRow) []sessionKey {
	keys := make([]sessionKey, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Backend != keys[j].Backend {
			return keys[i].Backend < keys[j].Backend
		}
		return keys[i].SessionID < keys[j].SessionID
	})
	return keys
}

func sortedOrphanKeys[T any](items map[sessionKey]T, sessions map[sessionKey]legacySessionRow) []sessionKey {
	keys := make([]sessionKey, 0, len(items))
	for key := range items {
		if _, ok := sessions[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Backend != keys[j].Backend {
			return keys[i].Backend < keys[j].Backend
		}
		return keys[i].SessionID < keys[j].SessionID
	})
	return keys
}

func hiddenSessionKeySet(keys []string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		set[trimmed] = struct{}{}
	}
	return set
}

func sessionHiddenByKeys(key sessionKey, hiddenKeys map[string]struct{}) bool {
	for _, candidate := range hiddenSessionKeyCandidates(key) {
		if _, ok := hiddenKeys[candidate]; ok {
			return true
		}
	}
	return false
}

func hiddenSessionKeyCandidates(key sessionKey) []string {
	return []string{
		key.SessionID,
		fmt.Sprintf("session:%s", key.SessionID),
		fmt.Sprintf("history:%s:%s", key.Backend, key.SessionID),
		fmt.Sprintf("thread:%s:%s", key.Backend, key.SessionID),
		fmt.Sprintf("resume:%s:%s", key.Backend, key.SessionID),
	}
}

func buildSessionCatalogRow(key sessionKey, sessionRow legacySessionRow, hasSession bool, uiRow legacyUIStateRow, hasUI bool, hidden bool, snapshotAt time.Time) sqlitestore.SessionRow {
	createdAt, updatedAt, activityAt := resolveSessionTimes(sessionRow, hasSession, snapshotAt)
	row := sqlitestore.SessionRow{
		SessionID:      key.SessionID,
		Backend:        key.Backend,
		CWD:            sessionRow.CWD,
		Title:          sessionRow.Title,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		ActivityAt:     activityAt,
		Focused:        false,
		Hidden:         hidden,
		PriorityOffset: 0,
	}
	if hasUI {
		row.Alias = uiRow.Alias
		row.Focused = uiRow.Focused
		row.PriorityOffset = uiRow.PriorityOffset
		row.SnoozeUntil = cloneTimePtr(uiRow.SnoozeUntil)
		if uiRow.DependencySessionID != "" {
			dep := uiRow.DependencySessionID
			row.DependencySessionID = &dep
		}
		return row
	}
	row.Alias = ""
	return row
}

func resolveSessionTimes(row legacySessionRow, hasSession bool, snapshotAt time.Time) (time.Time, time.Time, time.Time) {
	if !hasSession {
		return snapshotAt, snapshotAt, snapshotAt
	}
	created := chooseTime(row.CreatedAt, row.UpdatedAt, snapshotAt)
	updated := chooseTime(row.UpdatedAt, row.CreatedAt, snapshotAt)
	activity := chooseTime(row.UpdatedAt, row.CreatedAt, snapshotAt)
	return created, updated, activity
}

func chooseTime(primary, secondary *time.Time, fallback time.Time) time.Time {
	if primary != nil && !primary.IsZero() {
		return primary.UTC()
	}
	if secondary != nil && !secondary.IsZero() {
		return secondary.UTC()
	}
	return fallback.UTC()
}

func buildQueueRows(key sessionKey, rows []legacyQueueItemRow) []sqlitestore.QueueItemRow {
	if len(rows) == 0 {
		return []sqlitestore.QueueItemRow{}
	}
	sorted := append([]legacyQueueItemRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Ordinal < sorted[j].Ordinal })
	items := make([]sqlitestore.QueueItemRow, 0, len(sorted))
	for _, row := range sorted {
		items = append(items, sqlitestore.QueueItemRow{
			Ordinal: row.Ordinal,
			ItemID:  fmt.Sprintf("legacy:%s:%d", key.SessionID, row.Ordinal),
			Text:    row.Text,
			State:   "queued",
		})
	}
	return items
}

func buildWorkspaceRows(key sessionKey, sessionRow legacySessionRow, hasSession bool, rows []legacySessionFileRow, warnings *[]sqlitestore.MigrationWarningRow, state *importBuildState) sqlitestore.WorkspaceStateRow {
	workspace := sqlitestore.WorkspaceStateRow{OpenPaths: []string{}, HistoryItems: []sqlitestore.WorkspaceHistoryItemRow{}}
	if len(rows) == 0 {
		return workspace
	}
	sorted := append([]legacySessionFileRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Ordinal < sorted[j].Ordinal })
	for _, row := range sorted {
		rel, ok := normalizeLegacyWorkspacePath(sessionRow.CWD, row.Path)
		if !ok {
			state.SessionFileSkippedRows++
			appendWarning(warnings, state, "session_files", key.String(), "unmapped_session_file_path", "session_files path is outside cwd or cannot be represented as a workspace-relative path", row)
			continue
		}
		workspace.HistoryItems = append(workspace.HistoryItems, sqlitestore.WorkspaceHistoryItemRow{
			Ordinal: row.Ordinal,
			Path:    rel,
			Label:   workspaceLabel(rel),
		})
		if workspace.SelectedPath == "" && hasSession {
			workspace.SelectedPath = rel
		}
	}
	return workspace
}

func buildAppState(source legacySnapshot) sqlitestore.AppStateRow {
	recent := append([]legacyRecentCwdRow(nil), source.RecentCwds...)
	sort.SliceStable(recent, func(i, j int) bool {
		left := recent[i].LastUsedAt
		right := recent[j].LastUsedAt
		switch {
		case left == nil && right == nil:
			return recent[i].CWD < recent[j].CWD
		case left == nil:
			return false
		case right == nil:
			return true
		case !left.Equal(*right):
			return left.After(*right)
		default:
			return recent[i].CWD < recent[j].CWD
		}
	})
	state := sqlitestore.AppStateRow{RecentCwds: []string{}, CwdGroups: []sqlitestore.CwdGroupRow{}}
	seen := make(map[string]struct{}, len(recent))
	for _, row := range recent {
		cwd := cleanAbsPath(row.CWD)
		if cwd == "" {
			continue
		}
		if _, ok := seen[cwd]; ok {
			continue
		}
		seen[cwd] = struct{}{}
		state.RecentCwds = append(state.RecentCwds, cwd)
	}
	groups := append([]legacyCwdGroupRow(nil), source.CwdGroups...)
	sort.Slice(groups, func(i, j int) bool { return cleanAbsPath(groups[i].CWD) < cleanAbsPath(groups[j].CWD) })
	for _, row := range groups {
		cwd := cleanAbsPath(row.CWD)
		if cwd == "" {
			continue
		}
		state.CwdGroups = append(state.CwdGroups, sqlitestore.CwdGroupRow{
			CWD:       cwd,
			Label:     strings.TrimSpace(row.Label),
			Collapsed: row.Collapsed,
		})
	}
	return state
}

func appendWarning(dst *[]sqlitestore.MigrationWarningRow, state *importBuildState, sourceTable, legacyKey, code, message string, payload any) {
	body := map[string]any{"payload": payload}
	payloadJSON := "{}"
	if encoded, err := json.Marshal(body); err == nil {
		payloadJSON = string(encoded)
	}
	if strings.HasPrefix(code, "unmapped_") || code == "legacy_import_unmapped" {
		state.UnmappedCount++
	}
	*dst = append(*dst, sqlitestore.MigrationWarningRow{
		SourceTable: sourceTable,
		LegacyKey:   legacyKey,
		WarningCode: code,
		Message:     message,
		PayloadJSON: payloadJSON,
	})
}

func countSource(source legacySnapshot) SourceCounts {
	keySet := make(map[sessionKey]struct{})
	for _, row := range source.Sessions {
		keySet[sessionKey{Backend: strings.TrimSpace(row.Backend), SessionID: strings.TrimSpace(row.SessionID)}] = struct{}{}
	}
	for _, row := range source.SessionUIState {
		keySet[sessionKey{Backend: strings.TrimSpace(row.Backend), SessionID: strings.TrimSpace(row.SessionID)}] = struct{}{}
	}
	for _, row := range source.SessionFiles {
		keySet[sessionKey{Backend: strings.TrimSpace(row.Backend), SessionID: strings.TrimSpace(row.SessionID)}] = struct{}{}
	}
	for _, row := range source.SessionQueueItems {
		keySet[sessionKey{Backend: strings.TrimSpace(row.Backend), SessionID: strings.TrimSpace(row.SessionID)}] = struct{}{}
	}
	return SourceCounts{
		Sessions:             len(source.Sessions),
		SessionUIState:       len(source.SessionUIState),
		HiddenSessionKeys:    len(source.HiddenSessionKeys),
		SessionFiles:         len(source.SessionFiles),
		SessionQueueItems:    len(source.SessionQueueItems),
		RecentCwds:           len(source.RecentCwds),
		CwdGroups:            len(source.CwdGroups),
		AppKV:                len(source.AppKV),
		LegacyImportUnmapped: len(source.LegacyImportUnmapped),
		SessionKeyUnion:      len(keySet),
	}
}

func countQueueRows(rows []sqlitestore.SessionSnapshotRow) int {
	total := 0
	for _, row := range rows {
		total += len(row.Queue)
	}
	return total
}

func countWorkspaceHistoryRows(rows []sqlitestore.SessionSnapshotRow) int {
	total := 0
	for _, row := range rows {
		total += len(row.Workspace.HistoryItems)
	}
	return total
}

type targetCounts struct {
	SessionCatalog   int
	SessionSourceRef int
	HiddenKeys       int
	AppKV            int
	Warnings         int
	Provenance       int
	QueueItems       int
	HistoryItems     int
	RecentCwds       int
	CwdGroups        int
}

func collectTargetCounts(ctx context.Context, catalog *sqlitestore.SessionCatalog) (targetCounts, error) {
	snapshots, err := catalog.ListSessionSnapshots(ctx, true)
	if err != nil {
		return targetCounts{}, err
	}
	refs, err := catalog.ListSessionSourceRefs(ctx)
	if err != nil {
		return targetCounts{}, err
	}
	hidden, err := catalog.ListHiddenSessionKeys(ctx)
	if err != nil {
		return targetCounts{}, err
	}
	appKV, err := catalog.ListAppKV(ctx)
	if err != nil {
		return targetCounts{}, err
	}
	warnings, err := catalog.ListMigrationWarnings(ctx)
	if err != nil {
		return targetCounts{}, err
	}
	provenance, err := catalog.ListImportProvenance(ctx)
	if err != nil {
		return targetCounts{}, err
	}
	appState, err := catalog.LoadAppState(ctx)
	if err != nil {
		return targetCounts{}, err
	}
	counts := targetCounts{
		SessionCatalog:   len(snapshots),
		SessionSourceRef: len(refs),
		HiddenKeys:       len(hidden),
		AppKV:            len(appKV),
		Warnings:         len(warnings),
		Provenance:       len(provenance),
		RecentCwds:       len(appState.RecentCwds),
		CwdGroups:        len(appState.CwdGroups),
	}
	for _, snapshot := range snapshots {
		counts.QueueItems += len(snapshot.Queue)
		counts.HistoryItems += len(snapshot.Workspace.HistoryItems)
	}
	return counts, nil
}

func mergeTargetCounts(dst *ImportedCounts, actual targetCounts) {
	dst.SessionCatalogRows = actual.SessionCatalog
	dst.SessionSourceRefRows = actual.SessionSourceRef
	dst.HiddenSessionKeyRows = actual.HiddenKeys
	dst.AppKVRows = actual.AppKV
	dst.MigrationWarningRows = actual.Warnings
	dst.ImportProvenanceRows = actual.Provenance
	dst.SessionQueueItemRows = actual.QueueItems
	dst.SessionFileMappedRows = actual.HistoryItems
	dst.RecentCwdRows = actual.RecentCwds
	dst.CwdGroupRows = actual.CwdGroups
}

func mismatch(name string, source, imported int, details string) Mismatch {
	return Mismatch{Name: name, Source: source, Imported: imported, Details: details}
}

func compactMismatches(items []Mismatch) []Mismatch {
	out := make([]Mismatch, 0, len(items))
	for _, item := range items {
		if item.Source == item.Imported {
			continue
		}
		out = append(out, item)
	}
	return out
}

func cloneTimePtr(ts *time.Time) *time.Time {
	if ts == nil {
		return nil
	}
	copied := ts.UTC()
	return &copied
}

func cleanAbsPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
}

func normalizeLegacyWorkspacePath(cwd, raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	if filepath.IsAbs(trimmed) {
		if strings.TrimSpace(cwd) == "" {
			return "", false
		}
		rel, err := filepath.Rel(cwd, trimmed)
		if err != nil {
			return "", false
		}
		rel = filepath.Clean(rel)
		if rel == "." || rel == "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		return filepath.ToSlash(rel), true
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(cleaned), true
}

func workspaceLabel(rel string) string {
	base := filepath.Base(rel)
	if base == "." || base == string(filepath.Separator) {
		return rel
	}
	return base
}

func readLegacySessions(ctx context.Context, db *sql.DB) ([]legacySessionRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT backend, session_id, cwd, source_path, title, first_user_message, created_at, updated_at, pending_startup FROM sessions ORDER BY backend ASC, session_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query legacy sessions: %w", err)
	}
	defer rows.Close()
	items := make([]legacySessionRow, 0)
	for rows.Next() {
		var (
			row              legacySessionRow
			cwd              sql.NullString
			sourcePath       sql.NullString
			title            sql.NullString
			firstUserMessage sql.NullString
			createdAt        sql.NullFloat64
			updatedAt        sql.NullFloat64
			pendingStartup   int
		)
		if err := rows.Scan(&row.Backend, &row.SessionID, &cwd, &sourcePath, &title, &firstUserMessage, &createdAt, &updatedAt, &pendingStartup); err != nil {
			return nil, fmt.Errorf("scan legacy sessions row: %w", err)
		}
		row.CWD = nullStringValue(cwd)
		row.SourcePath = nullStringValue(sourcePath)
		row.Title = nullStringValue(title)
		row.FirstUserMessage = nullStringValue(firstUserMessage)
		row.CreatedAt = unixSecondsPtr(createdAt)
		row.UpdatedAt = unixSecondsPtr(updatedAt)
		row.PendingStartup = pendingStartup != 0
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy sessions rows: %w", err)
	}
	return items, nil
}

func readLegacyUIState(ctx context.Context, db *sql.DB) ([]legacyUIStateRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT backend, session_id, alias, focused, hidden, priority_offset, snooze_until, dependency_backend, dependency_session_id FROM session_ui_state ORDER BY backend ASC, session_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query legacy session_ui_state: %w", err)
	}
	defer rows.Close()
	items := make([]legacyUIStateRow, 0)
	for rows.Next() {
		var (
			row                 legacyUIStateRow
			alias               sql.NullString
			focused             int
			hidden              int
			snoozeUntil         sql.NullFloat64
			dependencyBackend   sql.NullString
			dependencySessionID sql.NullString
		)
		if err := rows.Scan(&row.Backend, &row.SessionID, &alias, &focused, &hidden, &row.PriorityOffset, &snoozeUntil, &dependencyBackend, &dependencySessionID); err != nil {
			return nil, fmt.Errorf("scan legacy session_ui_state row: %w", err)
		}
		row.Alias = nullStringValue(alias)
		row.Focused = focused != 0
		row.Hidden = hidden != 0
		row.SnoozeUntil = unixSecondsPtr(snoozeUntil)
		row.DependencyBackend = nullStringValue(dependencyBackend)
		row.DependencySessionID = nullStringValue(dependencySessionID)
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy session_ui_state rows: %w", err)
	}
	return items, nil
}

func readLegacyHiddenSessionKeys(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT key FROM hidden_session_keys ORDER BY key ASC`)
	if err != nil {
		return nil, fmt.Errorf("query legacy hidden_session_keys: %w", err)
	}
	defer rows.Close()
	items := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan legacy hidden_session_keys row: %w", err)
		}
		items = append(items, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy hidden_session_keys rows: %w", err)
	}
	return items, nil
}

func readLegacySessionFiles(ctx context.Context, db *sql.DB) ([]legacySessionFileRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT backend, session_id, path, ordinal, last_used_ts FROM session_files ORDER BY backend ASC, session_id ASC, ordinal ASC`)
	if err != nil {
		return nil, fmt.Errorf("query legacy session_files: %w", err)
	}
	defer rows.Close()
	items := make([]legacySessionFileRow, 0)
	for rows.Next() {
		var (
			row        legacySessionFileRow
			lastUsedAt sql.NullFloat64
		)
		if err := rows.Scan(&row.Backend, &row.SessionID, &row.Path, &row.Ordinal, &lastUsedAt); err != nil {
			return nil, fmt.Errorf("scan legacy session_files row: %w", err)
		}
		row.LastUsedAt = unixSecondsPtr(lastUsedAt)
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy session_files rows: %w", err)
	}
	return items, nil
}

func readLegacySessionQueueItems(ctx context.Context, db *sql.DB) ([]legacyQueueItemRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT backend, session_id, ordinal, text FROM session_queue_items ORDER BY backend ASC, session_id ASC, ordinal ASC`)
	if err != nil {
		return nil, fmt.Errorf("query legacy session_queue_items: %w", err)
	}
	defer rows.Close()
	items := make([]legacyQueueItemRow, 0)
	for rows.Next() {
		var row legacyQueueItemRow
		if err := rows.Scan(&row.Backend, &row.SessionID, &row.Ordinal, &row.Text); err != nil {
			return nil, fmt.Errorf("scan legacy session_queue_items row: %w", err)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy session_queue_items rows: %w", err)
	}
	return items, nil
}

func readLegacyRecentCwds(ctx context.Context, db *sql.DB) ([]legacyRecentCwdRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT cwd, last_used_ts FROM recent_cwds ORDER BY last_used_ts DESC, cwd ASC`)
	if err != nil {
		return nil, fmt.Errorf("query legacy recent_cwds: %w", err)
	}
	defer rows.Close()
	items := make([]legacyRecentCwdRow, 0)
	for rows.Next() {
		var (
			row        legacyRecentCwdRow
			lastUsedAt sql.NullFloat64
		)
		if err := rows.Scan(&row.CWD, &lastUsedAt); err != nil {
			return nil, fmt.Errorf("scan legacy recent_cwds row: %w", err)
		}
		row.LastUsedAt = unixSecondsPtr(lastUsedAt)
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy recent_cwds rows: %w", err)
	}
	return items, nil
}

func readLegacyCwdGroups(ctx context.Context, db *sql.DB) ([]legacyCwdGroupRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT cwd, label, collapsed FROM cwd_groups ORDER BY cwd ASC`)
	if err != nil {
		return nil, fmt.Errorf("query legacy cwd_groups: %w", err)
	}
	defer rows.Close()
	items := make([]legacyCwdGroupRow, 0)
	for rows.Next() {
		var (
			row       legacyCwdGroupRow
			label     sql.NullString
			collapsed int
		)
		if err := rows.Scan(&row.CWD, &label, &collapsed); err != nil {
			return nil, fmt.Errorf("scan legacy cwd_groups row: %w", err)
		}
		row.Label = nullStringValue(label)
		row.Collapsed = collapsed != 0
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy cwd_groups rows: %w", err)
	}
	return items, nil
}

func readLegacyAppKV(ctx context.Context, db *sql.DB) ([]legacyAppKVRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT namespace, key, value_json FROM app_kv ORDER BY namespace ASC, key ASC`)
	if err != nil {
		return nil, fmt.Errorf("query legacy app_kv: %w", err)
	}
	defer rows.Close()
	items := make([]legacyAppKVRow, 0)
	for rows.Next() {
		var row legacyAppKVRow
		if err := rows.Scan(&row.Namespace, &row.Key, &row.ValueJSON); err != nil {
			return nil, fmt.Errorf("scan legacy app_kv row: %w", err)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy app_kv rows: %w", err)
	}
	return items, nil
}

func readLegacyImportUnmapped(ctx context.Context, db *sql.DB) ([]legacyImportUnmappedRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, source_name, legacy_key, payload_json, imported_at FROM legacy_import_unmapped ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query legacy_import_unmapped: %w", err)
	}
	defer rows.Close()
	items := make([]legacyImportUnmappedRow, 0)
	for rows.Next() {
		var (
			row        legacyImportUnmappedRow
			importedAt sql.NullFloat64
		)
		if err := rows.Scan(&row.ID, &row.SourceName, &row.LegacyKey, &row.PayloadJSON, &importedAt); err != nil {
			return nil, fmt.Errorf("scan legacy_import_unmapped row: %w", err)
		}
		row.ImportedAt = unixSecondsPtr(importedAt)
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy_import_unmapped rows: %w", err)
	}
	return items, nil
}

func nullStringValue(raw sql.NullString) string {
	if !raw.Valid {
		return ""
	}
	return raw.String
}

func unixSecondsPtr(raw sql.NullFloat64) *time.Time {
	if !raw.Valid {
		return nil
	}
	sec := int64(raw.Float64)
	nsec := int64((raw.Float64 - float64(sec)) * float64(time.Second))
	ts := time.Unix(sec, nsec).UTC()
	return &ts
}

func auditSideJSON(dir string, snapshotAt time.Time) ([]SideJSONAudit, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	names := []string{
		"session_aliases.json",
		"session_sidebar.json",
		"hidden_sessions.json",
		"session_files.json",
		"session_queues.json",
		"recent_cwds.json",
		"cwd_groups.json",
		"voice_settings.json",
	}
	items := make([]SideJSONAudit, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat side json %q: %w", path, err)
		}
		entry := SideJSONAudit{Name: name, Path: path, ModifiedAt: info.ModTime().UTC()}
		entry.Fresh = !entry.ModifiedAt.Before(snapshotAt.Add(-sideJSONFreshnessWindow))
		entry.Ignored = !entry.Fresh
		data, err := os.ReadFile(path)
		if err != nil {
			entry.ParseError = err.Error()
			items = append(items, entry)
			continue
		}
		var decoded any
		if err := json.Unmarshal(data, &decoded); err != nil {
			entry.ParseError = err.Error()
			items = append(items, entry)
			continue
		}
		entry.EntryCount = countJSONEntries(decoded)
		items = append(items, entry)
	}
	return items, nil
}

func countJSONEntries(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case map[string]any:
		return len(typed)
	case nil:
		return 0
	default:
		return 1
	}
}
