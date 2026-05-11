package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/process"
	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/config"
	"actrail/internal/domain/session"
	piagentv1 "actrail/proto/pi/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func persistentTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Load()
	cfg.Storage.DataDir = t.TempDir()
	return cfg
}

func fakeRuntimeConfig() RuntimeConfig {
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPID(321)
	return RuntimeConfig{Runner: &process.FakeRunner{NextHandle: handle}}
}

func TestLookupSessionSeedsCodexRuntimeFromPersistedBackendSessionID(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()
	if err := catalog.ReplaceImportBundle(context.Background(), sqlitestore.ImportBundle{
		Sessions: []sqlitestore.SessionSnapshotRow{{
			Session: sqlitestore.SessionRow{
				SessionID:  "codex-resume-seed",
				Backend:    "codex",
				CWD:        "/workspace/codex-resume-seed",
				Title:      "Codex Resume Seed",
				CreatedAt:  now,
				UpdatedAt:  now,
				ActivityAt: now,
			},
		}},
		SessionSourceRefs: []sqlitestore.SessionSourceRefRow{{
			SessionID:        "codex-resume-seed",
			Backend:          "codex",
			BackendSessionID: "thread-codex-resume-seed",
		}},
		AppState: sqlitestore.AppStateRow{RecentCwds: []string{}, CwdGroups: []sqlitestore.CwdGroupRow{}},
		Provenance: sqlitestore.ImportProvenanceRow{
			Source:     "fixture",
			SnapshotAt: now,
		},
	}); err != nil {
		t.Fatalf("ReplaceImportBundle() error = %v", err)
	}

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	sessionID := mustSessionID(t, "codex-resume-seed")
	generationID := mustHelperGenerationID(t, "g_codex_resume_seed")
	svc.helpers.replaceAll(map[session.SessionID]attachedHelper{
		sessionID: {
			Binding: helperGenerationBinding{SessionID: sessionID, GenerationID: generationID},
			Manifest: iod.GenerationManifest{
				SessionID:    sessionID,
				GenerationID: generationID,
				HelloProof:   iod.HelloProof{ControlSocketPath: "/tmp/codex-resume-seed.sock"},
			},
			Hello: iod.HelloPacket{HelloProof: iod.HelloProof{ControlSocketPath: "/tmp/codex-resume-seed.sock"}},
		},
	}, nil)
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	if record.runtime.codex == nil {
		t.Fatal("lookupSession().runtime.codex = nil, want seeded codex runtime")
	}
	if got := record.runtime.PendingCodexResumeThreadID(); got != "thread-codex-resume-seed" {
		t.Fatalf("PendingCodexResumeThreadID() = %q, want persisted backend session id", got)
	}
	if _, threadID, _ := record.runtime.codex.snapshot(); threadID != "" {
		t.Fatalf("codex thread id = %q, want empty until Codex confirms resume", threadID)
	}
}

func TestLookupSessionWithoutHelperKeepsCodexResumePending(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()
	if err := catalog.ReplaceImportBundle(context.Background(), sqlitestore.ImportBundle{
		Sessions: []sqlitestore.SessionSnapshotRow{{
			Session: sqlitestore.SessionRow{
				SessionID:  "codex-resume-pending",
				Backend:    "codex",
				CWD:        "/workspace/codex-resume-pending",
				Title:      "Codex Resume Pending",
				CreatedAt:  now,
				UpdatedAt:  now,
				ActivityAt: now,
			},
			Live: sqlitestore.LiveStateRow{
				TransportGenerationID: "g_missing_codex",
				TransportState:        string(SessionTransportStateAttached),
			},
		}},
		SessionSourceRefs: []sqlitestore.SessionSourceRefRow{{
			SessionID:        "codex-resume-pending",
			Backend:          "codex",
			BackendSessionID: "thread-codex-resume-pending",
		}},
		AppState: sqlitestore.AppStateRow{RecentCwds: []string{}, CwdGroups: []sqlitestore.CwdGroupRow{}},
		Provenance: sqlitestore.ImportProvenanceRow{
			Source:     "fixture",
			SnapshotAt: now,
		},
	}); err != nil {
		t.Fatalf("ReplaceImportBundle() error = %v", err)
	}

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	record, err := svc.lookupSession(mustSessionID(t, "codex-resume-pending"))
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	if got := record.runtime.PendingCodexResumeThreadID(); got != "thread-codex-resume-pending" {
		t.Fatalf("PendingCodexResumeThreadID() = %q, want persisted backend session id", got)
	}
	if _, threadID, _ := record.runtime.codex.snapshot(); threadID != "" {
		t.Fatalf("codex thread id = %q, want empty until attached or resumed", threadID)
	}
	if transport := sessionTransportSnapshot(record); transport.State != SessionTransportStateEnded || transport.Reason == "" {
		t.Fatalf("sessionTransportSnapshot() = %+v, want ended with reason", transport)
	}
}

type importedPIDetachedFixture struct {
	SessionID               string
	CWD                     string
	Title                   string
	Alias                   string
	Focused                 bool
	UpdatedAt               time.Time
	ActivityAt              time.Time
	SourcePath              string
	FirstUserMessage        string
	HasLegacySessionUIState bool
	PriorityOffset          float64
	SnoozeUntil             *time.Time
	DependencySessionID     *string
}

func seedImportedPIDetachedSessions(t *testing.T, cfg config.Config, now time.Time, fixtures ...importedPIDetachedFixture) {
	t.Helper()
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	snapshots := make([]sqlitestore.SessionSnapshotRow, 0, len(fixtures))
	refs := make([]sqlitestore.SessionSourceRefRow, 0, len(fixtures))
	createdAt := now.Add(-48 * time.Hour)
	for _, fixture := range fixtures {
		snapshots = append(snapshots, sqlitestore.SessionSnapshotRow{
			Session: sqlitestore.SessionRow{
				SessionID:           fixture.SessionID,
				Backend:             "pi",
				CWD:                 fixture.CWD,
				Title:               fixture.Title,
				Alias:               fixture.Alias,
				CreatedAt:           createdAt,
				UpdatedAt:           fixture.UpdatedAt,
				ActivityAt:          fixture.ActivityAt,
				Focused:             fixture.Focused,
				PriorityOffset:      fixture.PriorityOffset,
				SnoozeUntil:         fixture.SnoozeUntil,
				DependencySessionID: fixture.DependencySessionID,
			},
			Queue: []sqlitestore.QueueItemRow{},
			Workspace: sqlitestore.WorkspaceStateRow{
				OpenPaths:    []string{},
				HistoryItems: []sqlitestore.WorkspaceHistoryItemRow{},
			},
		})
		refs = append(refs, sqlitestore.SessionSourceRefRow{
			SessionID:               fixture.SessionID,
			Backend:                 "pi",
			SourcePath:              fixture.SourcePath,
			FirstUserMessage:        fixture.FirstUserMessage,
			HasLegacySessionUIState: fixture.HasLegacySessionUIState,
		})
	}
	if err := catalog.ReplaceImportBundle(context.Background(), sqlitestore.ImportBundle{
		Sessions:          snapshots,
		SessionSourceRefs: refs,
		AppState:          sqlitestore.AppStateRow{RecentCwds: []string{}, CwdGroups: []sqlitestore.CwdGroupRow{}},
		HiddenSessionKeys: []sqlitestore.HiddenSessionKeyRow{},
		AppKV:             []sqlitestore.AppKVRow{},
		Warnings:          []sqlitestore.MigrationWarningRow{},
		Provenance: sqlitestore.ImportProvenanceRow{
			Source:      "fixture",
			SnapshotAt:  now,
			DetailsJSON: `{"fixture":"imported-pi-detached"}`,
		},
	}); err != nil {
		t.Fatalf("ReplaceImportBundle() error = %v", err)
	}
}

func writeImportedPISourceFile(t *testing.T, dir, name string, activity time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	if err := os.Chtimes(path, activity, activity); err != nil {
		t.Fatalf("Chtimes(%q) error = %v", path, err)
	}
	return path
}

func findSessionSummaryByID(t *testing.T, items []SessionSummary, sessionID string) SessionSummary {
	t.Helper()
	for _, item := range items {
		if item.SessionID == sessionID {
			return item
		}
	}
	t.Fatalf("session summary %q not found in %+v", sessionID, items)
	return SessionSummary{}
}

func findResumeCandidateByID(t *testing.T, items []SessionResumeCandidate, sessionID string) SessionResumeCandidate {
	t.Helper()
	for _, item := range items {
		if item.SessionID == sessionID {
			return item
		}
	}
	t.Fatalf("resume candidate %q not found in %+v", sessionID, items)
	return SessionResumeCandidate{}
}

func TestPersistentStubColdStartRehydratesSessionCatalog(t *testing.T) {
	cfg := persistentTestConfig(t)
	cwd := filepath.Join(t.TempDir(), "project")
	now := time.Unix(1760000000, 0).UTC()
	provider := "openrouter"
	model := "gpt-test"
	reasoning := "high"

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend:    "pi",
		PIAgentGRPC:     boolPtr(false),
		CWD:             cwd,
		Provider:        &provider,
		Model:           &model,
		ReasoningEffort: &reasoning,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if created.Session == nil {
		t.Fatal("CreateSession().Session = nil")
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	item := listed.Items[0]
	if item.SessionID != created.Session.SessionID {
		t.Fatalf("SessionID = %q, want %q", item.SessionID, created.Session.SessionID)
	}
	if item.RuntimeID != "" {
		t.Fatalf("RuntimeID = %q, want empty after cold start", item.RuntimeID)
	}
	if item.DisplayName != "project" {
		t.Fatalf("DisplayName = %q, want %q", item.DisplayName, "project")
	}
	if item.Title != "project" || item.Alias != "project" {
		t.Fatalf("cold-start title/alias = (%q, %q), want (%q, %q)", item.Title, item.Alias, "project", "project")
	}
	if item.ProviderChoice != provider || item.Model != model || item.ReasoningEffort != reasoning {
		t.Fatalf("persisted launch metadata = %+v", item)
	}

	sessionID := mustSessionID(t, created.Session.SessionID)
	details, err := rehydrated.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	if details.SessionID != created.Session.SessionID {
		t.Fatalf("details.SessionID = %q, want %q", details.SessionID, created.Session.SessionID)
	}
	if details.DisplayName != "project" || details.Provider != provider || details.Model != model {
		t.Fatalf("details = %+v", details)
	}
	if details.RuntimeID != "" {
		t.Fatalf("details.RuntimeID = %q, want empty after cold start", details.RuntimeID)
	}
}

func TestPersistentStubColdStartRehydratesImportedSessions(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760300000, 0).UTC()
	seedImportedPIDetachedSessions(t, cfg, now,
		importedPIDetachedFixture{
			SessionID:               "imported-pi-1",
			CWD:                     "/workspace/imported-a",
			Title:                   "",
			FirstUserMessage:        "hello importer",
			HasLegacySessionUIState: true,
			UpdatedAt:               now.Add(-time.Hour),
			ActivityAt:              now.Add(-time.Hour),
		},
		importedPIDetachedFixture{
			SessionID:               "imported-pi-2",
			CWD:                     "/workspace/imported-b",
			Title:                   "Imported B",
			HasLegacySessionUIState: false,
			UpdatedAt:               now.Add(-30 * time.Minute),
			ActivityAt:              now.Add(-30 * time.Minute),
		},
	)

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("len(ListSessions().Items) = %d, want 2", len(listed.Items))
	}
	if listed.Items[0].SessionID != "imported-pi-2" || listed.Items[1].SessionID != "imported-pi-1" {
		t.Fatalf("ListSessions() order = [%q %q], want [imported-pi-2 imported-pi-1]", listed.Items[0].SessionID, listed.Items[1].SessionID)
	}
	if listed.Items[1].DisplayName != "/workspace/imported-a" {
		t.Fatalf("ListSessions().Items[1].DisplayName = %q, want /workspace/imported-a", listed.Items[1].DisplayName)
	}
	if listed.Items[1].FirstUserMessage != "hello importer" {
		t.Fatalf("ListSessions().Items[1].FirstUserMessage = %q, want hello importer", listed.Items[1].FirstUserMessage)
	}
}

func TestPersistentStubColdStartRehydratesLiveSessionState(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/live-state"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	if _, err := svc.AppendAssistantDelta(sessionID, "turn-live", "partial text"); err != nil {
		t.Fatalf("AppendAssistantDelta() error = %v", err)
	}
	if _, ok, err := svc.registry.SetTransport(sessionID, SessionTransportSnapshot{GenerationID: "g_live", State: SessionTransportStateAttached, Reason: "attached"}); err != nil || !ok {
		t.Fatalf("SetTransport() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.SetSessionResumeCursor(sessionID, session.StreamKindMain, 42); err != nil {
		t.Fatalf("SetSessionResumeCursor(main) error = %v", err)
	}
	if err := svc.SetSessionResumeCursor(sessionID, session.StreamKindTransport, 84); err != nil {
		t.Fatalf("SetSessionResumeCursor(transport) error = %v", err)
	}
	if err := svc.SetSessionUIRequest(sessionID, SessionUIRequestSnapshot{RequestID: "ask-live", Kind: "ask_user", Prompt: "Choose"}); err != nil {
		t.Fatalf("SetSessionUIRequest() error = %v", err)
	}
	used := 123
	lastEvent := 456.5
	if _, ok, err := svc.registry.UpdateRuntimeMetadata(sessionID, "gpt-live", "openai", &SessionContextUsageSnapshot{UsedTokens: &used}, &SessionTurnTimingSnapshot{LastEventTS: &lastEvent}); err != nil || !ok {
		t.Fatalf("UpdateRuntimeMetadata() = (_, %v, %v), want ok", ok, err)
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog(reload) error = %v", err)
	}
	defer func() { _ = catalog.Close() }()
	records, err := loadPersistedSessions(catalog)
	if err != nil {
		t.Fatalf("loadPersistedSessions() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	record := records[0]
	if !record.state.Busy() {
		t.Fatal("persisted Busy = false, want true")
	}
	partial, ok := record.transcript.PartialAssistantTurn()
	if !ok || partial.TurnID().String() != "turn-live" || partial.Text() != "partial text" {
		t.Fatalf("persisted partial = %+v ok=%v", partial, ok)
	}
	if record.transport.GenerationID != "g_live" || record.transport.State != SessionTransportStateAttached || record.transport.Reason != "attached" {
		t.Fatalf("persisted transport = %+v", record.transport)
	}
	if record.resumeCursors.Session != "42" || record.resumeCursors.Transport != "84" {
		t.Fatalf("persisted resume cursors = %+v", record.resumeCursors)
	}
	if record.uiRequest == nil || record.uiRequest.RequestID != "ask-live" {
		t.Fatalf("persisted UI request = %+v", record.uiRequest)
	}
	if record.contextUsage == nil || record.contextUsage.UsedTokens == nil || *record.contextUsage.UsedTokens != 123 {
		t.Fatalf("persisted context usage = %+v", record.contextUsage)
	}
	if record.turnTiming == nil || record.turnTiming.LastEventTS == nil || *record.turnTiming.LastEventTS != lastEvent {
		t.Fatalf("persisted turn timing = %+v", record.turnTiming)
	}
	if !record.runtimeAgentRunning {
		t.Fatal("persisted runtimeAgentRunning = false, want true")
	}
}

func TestSessionStateDoesNotWaitForDeferredRuntimeRestore(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/tmp/deferred-state"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	rehydrated, err := newPersistentStubWithRuntimeOptions(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{}, persistentStubOptions{DeferRuntimeRestore: true})
	if err != nil {
		t.Fatalf("newPersistentStubWithRuntimeOptions(rehydrate) error = %v", err)
	}
	rehydrated.markRuntimeRestorePending()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := rehydrated.SessionState(ctx, SessionStateRequest{SessionID: sessionID}); err != nil {
		t.Fatalf("SessionState() while runtime restore pending error = %v", err)
	}
}

func TestPersistentStubMarksUnavailablePIAgentGRPCEndedOnRehydrate(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	server := &fakePiAgentServer{}
	grpcServer := grpc.NewServer()
	piagentv1.RegisterPiAgentServer(grpcServer, server)
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	go func() { _ = grpcServer.Serve(listener) }()

	runner := &process.FakeRunner{}
	createRuntimeCfg := RuntimeConfig{
		Runner:            runner,
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}), grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
		ResolveBinPath: func(session.Backend) (string, error) { return "/tmp/custom-pi", nil },
	}
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, createRuntimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	useGRPC := true
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir(), PIAgentGRPC: &useGRPC})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, false); err != nil || !ok {
		t.Fatalf("SetBusy(false) = (_, %v, %v), want ok=true err=nil", ok, err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	rehydrateRuntimeCfg := RuntimeConfig{
		Runner:            runner,
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return nil, errors.New("grpc unavailable")
		},
		ResolveBinPath: func(session.Backend) (string, error) { return "/tmp/custom-pi", nil },
	}
	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, rehydrateRuntimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(rehydrate) error = %v", err)
	}
	record, err := rehydrated.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	if record.runtime.piAgentGRPC != nil {
		t.Fatal("rehydrated runtime.piAgentGRPC != nil, want unavailable runtime")
	}
	if record.runtimeAgentRunning {
		t.Fatal("rehydrated runtimeAgentRunning = true, want false")
	}
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	if listed.Items[0].TransportState != SessionTransportStateEnded.String() || listed.Items[0].TransportReason != "pi_agent_grpc_unavailable" {
		t.Fatalf("ListSessions().Items[0] transport = (%q, %q), want ended pi_agent_grpc_unavailable", listed.Items[0].TransportState, listed.Items[0].TransportReason)
	}
	if listed.Items[0].Probing {
		t.Fatal("ListSessions().Items[0].Probing = true, want false for ended runtime")
	}
}

func TestPersistentStubReattachesRunningPIAgentGRPCWithoutStartingNewProcess(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	server := &fakePiAgentServer{}
	grpcServer := grpc.NewServer()
	piagentv1.RegisterPiAgentServer(grpcServer, server)
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	go func() { _ = grpcServer.Serve(listener) }()

	runner := &process.FakeRunner{}
	runtimeCfg := RuntimeConfig{
		Runner:            runner,
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}), grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
		ResolveBinPath: func(session.Backend) (string, error) { return "/tmp/custom-pi", nil },
	}
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, runtimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	useGRPC := true
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir(), PIAgentGRPC: &useGRPC})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	if len(runner.Starts) != 1 {
		t.Fatalf("len(runner.Starts) = %d, want initial start only", len(runner.Starts))
	}
	if err := svc.setRuntimeAgentRunning(sessionID, true); err != nil {
		t.Fatalf("setRuntimeAgentRunning() error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, false); err != nil || !ok {
		t.Fatalf("SetBusy(false) = (_, %v, %v), want ok=true err=nil", ok, err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, runtimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(rehydrate) error = %v", err)
	}
	if len(runner.Starts) != 1 {
		t.Fatalf("len(runner.Starts) = %d, want no restart on server rehydrate", len(runner.Starts))
	}
	record, err := rehydrated.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	if record.runtime.piAgentGRPC == nil {
		t.Fatal("rehydrated runtime.piAgentGRPC = nil, want reattached client")
	}
	if record.runtime.handle != nil {
		t.Fatalf("rehydrated runtime.handle = %+v, want nil attach-only runtime", record.runtime.handle)
	}
	if !record.runtimeAgentRunning {
		t.Fatal("rehydrated runtimeAgentRunning = false, want persisted running state")
	}
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	if listed.Items[0].Busy {
		t.Fatalf("ListSessions().Items[0].Busy = true, want idle runtime")
	}
	if listed.Items[0].TransportState != SessionTransportStateAttached.String() || listed.Items[0].ResetRequired {
		t.Fatalf("ListSessions().Items[0] transport = (%q, reset=%v), want attached without reset", listed.Items[0].TransportState, listed.Items[0].ResetRequired)
	}
	if listed.Items[0].Probing {
		t.Fatal("ListSessions().Items[0].Probing = true, want false for attached grpc runtime")
	}
}

func TestPersistentStubColdStartRehydratesManualInboxPromptExactlyOnce(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/queue-project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil {
		t.Fatalf("registry.SetBusy() error = %v", err)
	} else if !ok {
		t.Fatal("registry.SetBusy() ok = false")
	}
	queued, err := svc.Enqueue(context.Background(), EnqueueRequest{SessionID: sessionID, Text: "recover me"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if len(queued.Queue.Items) != 0 {
		t.Fatalf("len(Enqueue().Queue.Items) = %d, want 0", len(queued.Queue.Items))
	}
	inbox, err := svc.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox(create) error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Message != "recover me" || inbox.Items[0].State != "pending" {
		t.Fatalf("SessionInbox(create) = %+v, want pending manual item", inbox.Items)
	}
	firstID := inbox.Items[0].ItemID

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart1) error = %v", err)
	}
	state, err := rehydrated.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState(restart1) error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState(restart1).Busy = true, want false")
	}
	if len(state.Queue.Items) != 0 {
		t.Fatalf("len(SessionState(restart1).Queue.Items) = %d, want 0", len(state.Queue.Items))
	}
	inbox, err = rehydrated.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox(restart1) error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].ItemID != firstID || inbox.Items[0].Message != "recover me" {
		t.Fatalf("SessionInbox(restart1) = %+v", inbox.Items)
	}

	rehydratedAgain, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(2 * time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart2) error = %v", err)
	}
	state, err = rehydratedAgain.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState(restart2) error = %v", err)
	}
	if len(state.Queue.Items) != 0 {
		t.Fatalf("len(SessionState(restart2).Queue.Items) = %d, want 0", len(state.Queue.Items))
	}
	inbox, err = rehydratedAgain.SessionInbox(context.Background(), SessionInboxRequest{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SessionInbox(restart2) error = %v", err)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].ItemID != firstID {
		t.Fatalf("SessionInbox(restart2) = %+v, want item %q", inbox.Items, firstID)
	}
}

func TestPersistentStubColdStartRehydratesWorkspaceBrowserState(t *testing.T) {
	cfg := persistentTestConfig(t)
	rootDir := t.TempDir()
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: rootDir})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	updated, err := svc.UpdateSessionWorkspace(context.Background(), UpdateSessionWorkspaceRequest{
		SessionID:    sessionID,
		SelectedPath: "nested/file.txt",
		OpenPaths:    []string{"nested", "nested/file.txt"},
		HistoryItems: []WorkspaceHistoryItem{{Path: "README.md", Label: "Readme"}},
	})
	if err != nil {
		t.Fatalf("UpdateSessionWorkspace() error = %v", err)
	}
	if updated.SelectedPath != "nested/file.txt" {
		t.Fatalf("UpdateSessionWorkspace().SelectedPath = %q, want %q", updated.SelectedPath, "nested/file.txt")
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	workspaceState, err := rehydrated.SessionWorkspace(context.Background(), SessionWorkspaceRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionWorkspace() error = %v", err)
	}
	if workspaceState.RootPath != rootDir {
		t.Fatalf("SessionWorkspace().RootPath = %q, want %q", workspaceState.RootPath, rootDir)
	}
	if workspaceState.SelectedPath != "nested/file.txt" {
		t.Fatalf("SessionWorkspace().SelectedPath = %q, want %q", workspaceState.SelectedPath, "nested/file.txt")
	}
	if !reflect.DeepEqual(workspaceState.OpenPaths, []string{"nested/file.txt", "nested"}) {
		t.Fatalf("SessionWorkspace().OpenPaths = %#v, want selected path front and nested dir", workspaceState.OpenPaths)
	}
	if !reflect.DeepEqual(workspaceState.HistoryItems, []WorkspaceHistoryItem{{Path: "nested/file.txt", Label: "file.txt"}, {Path: "README.md", Label: "Readme"}}) {
		t.Fatalf("SessionWorkspace().HistoryItems = %#v", workspaceState.HistoryItems)
	}
}

func TestPersistentStubColdStartResumeCandidatesUseImportedPISourceActivity(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourceDir := t.TempDir()
	olderActivity := now.Add(-2 * time.Hour)
	newerActivity := now.Add(-30 * time.Minute)
	olderSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-1.jsonl", olderActivity)
	newerSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-2.jsonl", newerActivity)
	cwd := "/workspace/shared"
	seedImportedPIDetachedSessions(t, cfg, now,
		importedPIDetachedFixture{
			SessionID:  "imported-pi-1",
			CWD:        cwd,
			Title:      "Imported Pi 1",
			Alias:      "Imported sidebar 1",
			UpdatedAt:  now.Add(-10 * time.Minute),
			ActivityAt: now.Add(-10 * time.Minute),
			SourcePath: olderSourcePath,
		},
		importedPIDetachedFixture{
			SessionID:  "imported-pi-2",
			CWD:        cwd,
			Title:      "Imported Pi 2",
			Alias:      "Imported sidebar 2",
			UpdatedAt:  now.Add(-3 * time.Hour),
			ActivityAt: now.Add(-3 * time.Hour),
			SourcePath: newerSourcePath,
		},
	)

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	resume, err := rehydrated.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          cwd,
		AgentBackend: "pi",
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	if len(resume.Sessions) != 2 {
		t.Fatalf("len(SessionResumeCandidates().Sessions) = %d, want 2", len(resume.Sessions))
	}
	if resume.Sessions[0].SessionID != "imported-pi-2" || resume.Sessions[1].SessionID != "imported-pi-1" {
		t.Fatalf("SessionResumeCandidates() order = [%q %q], want [imported-pi-2 imported-pi-1]", resume.Sessions[0].SessionID, resume.Sessions[1].SessionID)
	}
	if resume.Sessions[0].UpdatedTS != timestampSeconds(newerActivity) {
		t.Fatalf("SessionResumeCandidates().Sessions[0].UpdatedTS = %v, want %v", resume.Sessions[0].UpdatedTS, timestampSeconds(newerActivity))
	}
	if resume.Sessions[1].UpdatedTS != timestampSeconds(olderActivity) {
		t.Fatalf("SessionResumeCandidates().Sessions[1].UpdatedTS = %v, want %v", resume.Sessions[1].UpdatedTS, timestampSeconds(olderActivity))
	}
}

func TestPersistentStubColdStartResumeCandidatesUseModifiedTimeDescending(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourceDir := t.TempDir()
	activeActivity := now.Add(-2 * time.Hour)
	blockedActivity := now.Add(-5 * time.Minute)
	snoozedActivity := now.Add(-2 * time.Minute)
	activeSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-1.jsonl", activeActivity)
	blockedSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-2.jsonl", blockedActivity)
	snoozedSourcePath := writeImportedPISourceFile(t, sourceDir, "imported-pi-3.jsonl", snoozedActivity)
	cwd := "/workspace/shared"
	priority := 1.0
	blockedDependency := "imported-pi-1"
	snoozeUntil := now.Add(2 * time.Hour)
	seedImportedPIDetachedSessions(t, cfg, now,
		importedPIDetachedFixture{
			SessionID:               "imported-pi-1",
			CWD:                     cwd,
			Title:                   "Active",
			UpdatedAt:               now.Add(-90 * time.Minute),
			ActivityAt:              now.Add(-90 * time.Minute),
			SourcePath:              activeSourcePath,
			HasLegacySessionUIState: true,
			PriorityOffset:          0,
		},
		importedPIDetachedFixture{
			SessionID:               "imported-pi-2",
			CWD:                     cwd,
			Title:                   "Blocked",
			UpdatedAt:               now.Add(-30 * time.Second),
			ActivityAt:              now.Add(-30 * time.Second),
			SourcePath:              blockedSourcePath,
			HasLegacySessionUIState: true,
			PriorityOffset:          priority,
			DependencySessionID:     &blockedDependency,
		},
		importedPIDetachedFixture{
			SessionID:               "imported-pi-3",
			CWD:                     cwd,
			Title:                   "Snoozed",
			UpdatedAt:               now.Add(-15 * time.Second),
			ActivityAt:              now.Add(-15 * time.Second),
			SourcePath:              snoozedSourcePath,
			HasLegacySessionUIState: true,
			PriorityOffset:          priority,
			SnoozeUntil:             &snoozeUntil,
		},
	)

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	want := []string{"imported-pi-1", "imported-pi-3", "imported-pi-2"}
	listedOrder := make([]string, 0, len(want))
	for _, item := range listed.Items {
		if item.CWD == cwd && item.AgentBackend == "pi" {
			listedOrder = append(listedOrder, item.SessionID)
		}
	}
	assertSessionIDOrder(t, "ListSessions() filtered", listedOrder, want)

	resume, err := rehydrated.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{CWD: cwd, AgentBackend: "pi"})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	resumeOrder := make([]string, 0, len(resume.Sessions))
	for _, item := range resume.Sessions {
		resumeOrder = append(resumeOrder, item.SessionID)
	}
	assertSessionIDOrder(t, "SessionResumeCandidates()", resumeOrder, []string{"imported-pi-3", "imported-pi-2", "imported-pi-1"})
}

func TestPersistentStubColdStartSessionDetailsUsesImportedPIDisplayTimestamp(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourcePath := writeImportedPISourceFile(t, t.TempDir(), "imported-pi.jsonl", now.Add(-45*time.Minute))
	recordedUpdatedAt := now.Add(-5 * time.Minute)
	recordedActivityAt := now.Add(-2 * time.Hour)
	seedImportedPIDetachedSessions(t, cfg, now, importedPIDetachedFixture{
		SessionID:  "imported-pi-1",
		CWD:        "/workspace/details",
		Title:      "Imported Pi",
		Alias:      "Imported sidebar",
		UpdatedAt:  recordedUpdatedAt,
		ActivityAt: recordedActivityAt,
		SourcePath: sourcePath,
	})

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	sessionID := mustSessionID(t, "imported-pi-1")
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	details, err := rehydrated.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	wantDisplayUpdated := timestampSeconds(now.Add(-45 * time.Minute))
	if details.LastUpdatedTS != wantDisplayUpdated {
		t.Fatalf("SessionDetails().LastUpdatedTS = %v, want %v", details.LastUpdatedTS, wantDisplayUpdated)
	}
	if details.LastUpdatedTS != listed.Items[0].LastUpdatedTS || details.LastUpdatedTS != listed.Items[0].UpdatedTS {
		t.Fatalf("SessionDetails/ListSessions timestamps = (%v, %v, %v), want identical display timestamp", details.LastUpdatedTS, listed.Items[0].LastUpdatedTS, listed.Items[0].UpdatedTS)
	}
	if details.LastActivityTS != timestampSeconds(recordedActivityAt) {
		t.Fatalf("SessionDetails().LastActivityTS = %v, want %v", details.LastActivityTS, timestampSeconds(recordedActivityAt))
	}
}

func TestPersistentStubColdStartImportedPIWithSidebarMetadataUsesSourceActivityAcrossViews(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourceActivity := now.Add(-20 * time.Minute)
	recordedUpdatedAt := now.Add(-3 * time.Hour)
	cwd := "/workspace/provenance-true"
	sourcePath := writeImportedPISourceFile(t, t.TempDir(), "imported-pi.jsonl", sourceActivity)
	seedImportedPIDetachedSessions(t, cfg, now, importedPIDetachedFixture{
		SessionID:  "imported-pi-1",
		CWD:        cwd,
		Title:      "Imported Pi",
		Alias:      "Imported sidebar",
		UpdatedAt:  recordedUpdatedAt,
		ActivityAt: recordedUpdatedAt,
		SourcePath: sourcePath,
	})

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	listedItem := findSessionSummaryByID(t, listed.Items, "imported-pi-1")
	resume, err := rehydrated.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{CWD: cwd, AgentBackend: "pi"})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	resumeItem := findResumeCandidateByID(t, resume.Sessions, "imported-pi-1")
	details, err := rehydrated.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: mustSessionID(t, "imported-pi-1")})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	want := timestampSeconds(sourceActivity)
	if listedItem.LastUpdatedTS != want || listedItem.UpdatedTS != want {
		t.Fatalf("ListSessions() timestamps = (%v, %v), want %v", listedItem.LastUpdatedTS, listedItem.UpdatedTS, want)
	}
	if resumeItem.UpdatedTS != want {
		t.Fatalf("SessionResumeCandidates().UpdatedTS = %v, want %v", resumeItem.UpdatedTS, want)
	}
	if details.LastUpdatedTS != want {
		t.Fatalf("SessionDetails().LastUpdatedTS = %v, want %v", details.LastUpdatedTS, want)
	}
}

func TestPersistentStubColdStartImportedPIWithoutLegacyUIStateUsesPersistedUpdatedAtAcrossViews(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourceActivity := now.Add(-10 * time.Minute)
	recordedUpdatedAt := now.Add(-2 * time.Hour)
	cwd := "/workspace/provenance-false"
	sourcePath := writeImportedPISourceFile(t, t.TempDir(), "imported-pi.jsonl", sourceActivity)
	seedImportedPIDetachedSessions(t, cfg, now, importedPIDetachedFixture{
		SessionID:               "imported-pi-1",
		CWD:                     cwd,
		Title:                   "Imported Pi",
		UpdatedAt:               recordedUpdatedAt,
		ActivityAt:              recordedUpdatedAt,
		SourcePath:              sourcePath,
		HasLegacySessionUIState: false,
	})

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	listedItem := findSessionSummaryByID(t, listed.Items, "imported-pi-1")
	resume, err := rehydrated.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{CWD: cwd, AgentBackend: "pi"})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	resumeItem := findResumeCandidateByID(t, resume.Sessions, "imported-pi-1")
	details, err := rehydrated.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: mustSessionID(t, "imported-pi-1")})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	want := timestampSeconds(recordedUpdatedAt)
	if listedItem.LastUpdatedTS != want || listedItem.UpdatedTS != want {
		t.Fatalf("ListSessions() timestamps = (%v, %v), want %v", listedItem.LastUpdatedTS, listedItem.UpdatedTS, want)
	}
	if resumeItem.UpdatedTS != want {
		t.Fatalf("SessionResumeCandidates().UpdatedTS = %v, want %v", resumeItem.UpdatedTS, want)
	}
	if details.LastUpdatedTS != want {
		t.Fatalf("SessionDetails().LastUpdatedTS = %v, want %v", details.LastUpdatedTS, want)
	}
}

func TestPersistentStubColdStartImportedPIMetadataEditsChangeDisplayTimestamp(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	sourceActivity := now.Add(2 * time.Hour)
	cwd := "/workspace/edited-imported"
	sourcePath := writeImportedPISourceFile(t, t.TempDir(), "imported-pi.jsonl", sourceActivity)
	seedImportedPIDetachedSessions(t, cfg, now, importedPIDetachedFixture{
		SessionID:               "imported-pi-1",
		CWD:                     cwd,
		Title:                   "Imported Pi",
		UpdatedAt:               now.Add(-6 * time.Hour),
		ActivityAt:              now.Add(-6 * time.Hour),
		SourcePath:              sourcePath,
		HasLegacySessionUIState: false,
	})

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	dependency, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/workspace/dependency"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	importedID := mustSessionID(t, "imported-pi-1")
	if _, err := svc.RenameSession(context.Background(), RenameSessionRequest{SessionID: importedID, Name: "Renamed Imported"}); err != nil {
		t.Fatalf("RenameSession() error = %v", err)
	}
	if _, err := svc.FocusSession(context.Background(), FocusSessionRequest{SessionID: importedID, Focused: true}); err != nil {
		t.Fatalf("FocusSession() error = %v", err)
	}
	priority := 1.5
	snoozeUntil := now.Add(4 * time.Hour).Unix()
	dependencyID := dependency.Session.SessionID
	if _, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:           importedID,
		PriorityOffset:      Float64Patch{Present: true, Value: &priority},
		SnoozeUntil:         Int64Patch{Present: true, Value: &snoozeUntil},
		DependencySessionID: StringPatch{Present: true, Value: &dependencyID},
	}); err != nil {
		t.Fatalf("EditSession() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	listed, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	listedItem := findSessionSummaryByID(t, listed.Items, "imported-pi-1")
	resume, err := rehydrated.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{CWD: cwd, AgentBackend: "pi"})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	resumeItem := findResumeCandidateByID(t, resume.Sessions, "imported-pi-1")
	details, err := rehydrated.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: importedID})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	want := timestampSeconds(sourceActivity)
	if listedItem.LastUpdatedTS != want || listedItem.UpdatedTS != want {
		t.Fatalf("ListSessions() timestamps after restart = (%v, %v), want %v", listedItem.LastUpdatedTS, listedItem.UpdatedTS, want)
	}
	if resumeItem.UpdatedTS != want {
		t.Fatalf("SessionResumeCandidates().UpdatedTS after restart = %v, want %v", resumeItem.UpdatedTS, want)
	}
	if details.LastUpdatedTS != want {
		t.Fatalf("SessionDetails().LastUpdatedTS after restart = %v, want %v", details.LastUpdatedTS, want)
	}
}

func TestPersistentStubColdStartRehydratesRecentCwdsAndCwdGroups(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/project-a"}); err != nil {
		t.Fatalf("CreateSession(project-a) error = %v", err)
	}
	if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/project-b"}); err != nil {
		t.Fatalf("CreateSession(project-b) error = %v", err)
	}
	label := "Project A"
	collapsed := true
	if _, err := svc.EditCwdGroup(context.Background(), EditCwdGroupRequest{CWD: "/tmp/project-a", Label: &label, Collapsed: &collapsed}); err != nil {
		t.Fatalf("EditCwdGroup() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	bootstrap := rehydrated.Bootstrap(context.Background(), BootstrapRequest{})
	if !reflect.DeepEqual(bootstrap.RecentCwds, []string{"/tmp/project-b", "/tmp/project-a"}) {
		t.Fatalf("Bootstrap().RecentCwds = %#v", bootstrap.RecentCwds)
	}
	meta, ok := bootstrap.CwdGroups["/tmp/project-a"]
	if !ok {
		t.Fatal("Bootstrap().CwdGroups missing /tmp/project-a")
	}
	if meta.Label != label || !meta.Collapsed {
		t.Fatalf("Bootstrap().CwdGroups[/tmp/project-a] = %+v", meta)
	}
}

func TestPersistentStubDeleteArchivesSessionRow(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/tmp/archive-me"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	deleted, err := svc.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if !deleted.OK || !deleted.Removed {
		t.Fatalf("DeleteSession() = %+v", deleted)
	}
	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 0 {
		t.Fatalf("len(ListSessions().Items) = %d, want 0 after archive", len(listed.Items))
	}

	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()
	active, err := catalog.ListSessions(context.Background(), false)
	if err != nil {
		t.Fatalf("ListSessions(false) error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("len(ListSessions(false)) = %d, want 0", len(active))
	}
	all, err := catalog.ListSessions(context.Background(), true)
	if err != nil {
		t.Fatalf("ListSessions(true) error = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(ListSessions(true)) = %d, want 1", len(all))
	}
	if all[0].SessionID != created.Session.SessionID {
		t.Fatalf("archived row session_id = %q, want %q", all[0].SessionID, created.Session.SessionID)
	}
	if all[0].ArchivedAt == nil {
		t.Fatal("archived row ArchivedAt = nil")
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, fakeRuntimeConfig())
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(rehydrate) error = %v", err)
	}
	rehydratedList, err := rehydrated.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("rehydrated.ListSessions() error = %v", err)
	}
	if len(rehydratedList.Items) != 0 {
		t.Fatalf("len(rehydrated.ListSessions().Items) = %d, want 0 archived rows skipped", len(rehydratedList.Items))
	}
}

func TestPersistentStubSessionMessagesInvalidatesPISourceCacheWhenFileGrows(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	sourcePath := filepath.Join(t.TempDir(), "pi-session.jsonl")
	initial := `{"type":"message","id":"u1","timestamp":"2026-04-29T09:00:00Z","message":{"role":"user","content":[{"type":"text","text":"first"}]}}
{"type":"message","id":"a1","timestamp":"2026-04-29T09:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"answer"}],"stopReason":"stop"}}
`
	if err := os.WriteFile(sourcePath, []byte(initial), 0o644); err != nil {
		t.Fatalf("WriteFile(initial source) error = %v", err)
	}
	if err := catalog.ReplaceImportBundle(context.Background(), sqlitestore.ImportBundle{
		Sessions: []sqlitestore.SessionSnapshotRow{{
			Session: sqlitestore.SessionRow{
				SessionID:  "imported-pi-cache",
				Backend:    "pi",
				CWD:        "/workspace/codoxear",
				Title:      "Imported Pi Cache",
				CreatedAt:  now,
				UpdatedAt:  now,
				ActivityAt: now,
			},
		}},
		SessionSourceRefs: []sqlitestore.SessionSourceRefRow{{
			SessionID:        "imported-pi-cache",
			Backend:          "pi",
			SourcePath:       sourcePath,
			FirstUserMessage: "first",
		}},
		AppState: sqlitestore.AppStateRow{RecentCwds: []string{}, CwdGroups: []sqlitestore.CwdGroupRow{}},
		Provenance: sqlitestore.ImportProvenanceRow{
			Source:     "fixture",
			SnapshotAt: now,
		},
	}); err != nil {
		t.Fatalf("ReplaceImportBundle() error = %v", err)
	}

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	sessionID := mustSessionID(t, "imported-pi-cache")
	first, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages(first) error = %v", err)
	}
	if len(first.Items) != 2 || first.TailSeq != 2 {
		t.Fatalf("SessionMessages(first) = %+v, want 2 items", first)
	}

	appended := `{"type":"message","id":"u2","timestamp":"2026-04-29T09:00:02Z","message":{"role":"user","content":[{"type":"text","text":"second"}]}}
`
	file, err := os.OpenFile(sourcePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(source append) error = %v", err)
	}
	if _, err := file.WriteString(appended); err != nil {
		_ = file.Close()
		t.Fatalf("WriteString(source append) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(source append) error = %v", err)
	}
	if err := os.Chtimes(sourcePath, now.Add(time.Second), now.Add(time.Second)); err != nil {
		t.Fatalf("Chtimes(source) error = %v", err)
	}

	second, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages(second) error = %v", err)
	}
	if len(second.Items) != 3 || second.TailSeq != 3 || second.Items[2].Text != "second" {
		t.Fatalf("SessionMessages(second) = %+v, want appended source row", second)
	}
}

func TestPersistentStubSessionMessagesLoadsImportedPIHistoryFromSourcePath(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	defer func() { _ = catalog.Close() }()

	sourcePath := filepath.Join("..", "domain", "pi", "testdata", "runtime_session.jsonl")
	if err := catalog.ReplaceImportBundle(context.Background(), sqlitestore.ImportBundle{
		Sessions: []sqlitestore.SessionSnapshotRow{{
			Session: sqlitestore.SessionRow{
				SessionID:   "imported-pi-1",
				Backend:     "pi",
				CWD:         "/workspace/codoxear",
				Title:       "Imported Pi",
				CreatedAt:   now,
				UpdatedAt:   now,
				ActivityAt:  now,
				Focused:     false,
				Hidden:      false,
				ArchivedAt:  nil,
				SnoozeUntil: nil,
			},
			Queue: []sqlitestore.QueueItemRow{},
			Workspace: sqlitestore.WorkspaceStateRow{
				OpenPaths:    []string{},
				HistoryItems: []sqlitestore.WorkspaceHistoryItemRow{},
			},
		}},
		SessionSourceRefs: []sqlitestore.SessionSourceRefRow{{
			SessionID:        "imported-pi-1",
			Backend:          "pi",
			SourcePath:       sourcePath,
			FirstUserMessage: "Summarize the current repository state.",
		}},
		AppState:          sqlitestore.AppStateRow{RecentCwds: []string{}, CwdGroups: []sqlitestore.CwdGroupRow{}},
		HiddenSessionKeys: []sqlitestore.HiddenSessionKeyRow{},
		AppKV:             []sqlitestore.AppKVRow{},
		Warnings:          []sqlitestore.MigrationWarningRow{},
		Provenance: sqlitestore.ImportProvenanceRow{
			Source:      "fixture",
			SnapshotAt:  now,
			DetailsJSON: `{"fixture":"runtime_session.jsonl"}`,
		},
	}); err != nil {
		t.Fatalf("ReplaceImportBundle() error = %v", err)
	}

	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	sessionID := mustSessionID(t, "imported-pi-1")

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	if listed.Items[0].FirstUserMessage != "Summarize the current repository state." {
		t.Fatalf("ListSessions().Items[0].FirstUserMessage = %q", listed.Items[0].FirstUserMessage)
	}

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 2 {
		t.Fatalf("len(SessionMessages().Items) = %d, want 2", len(messages.Items))
	}
	if messages.Items[0].Seq != 1 || messages.Items[0].Role != "user" || messages.Items[0].Text != "Summarize the current repository state." {
		t.Fatalf("SessionMessages().Items[0] = %+v", messages.Items[0])
	}
	if messages.Items[1].Seq != 2 || messages.Items[1].Role != "assistant" || messages.Items[1].Text == "" {
		t.Fatalf("SessionMessages().Items[1] = %+v", messages.Items[1])
	}
	if messages.TailSeq != 2 || messages.HasMore {
		t.Fatalf("SessionMessages() = %+v", messages)
	}

	latest, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 1})
	if err != nil {
		t.Fatalf("SessionMessages(limit=1) error = %v", err)
	}
	if len(latest.Items) != 1 || latest.Items[0].Seq != 2 || latest.Items[0].Role != "assistant" {
		t.Fatalf("SessionMessages(limit=1) = %+v", latest)
	}
	if !latest.HasMore || latest.NextBeforeSeq == nil || *latest.NextBeforeSeq != 2 {
		t.Fatalf("SessionMessages(limit=1) paging = %+v", latest)
	}
}

func mustSessionID(t *testing.T, raw string) session.SessionID {
	t.Helper()
	sessionID, err := session.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	return sessionID
}
