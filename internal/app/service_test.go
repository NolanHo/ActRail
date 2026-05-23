package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestStubCreateListDetailsAndStateUseRegistry(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 500000000).UTC()
	svc := newStub(cfg, func() time.Time { return now })

	title := "Current task"
	provider := "openrouter"
	model := "gpt-test"
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend: "PI",
		PIAgentGRPC:  boolPtr(false),
		CWD:          "/root/code/ActRail",
		Provider:     &provider,
		Model:        &model,
		Title:        &title,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if !created.OK {
		t.Fatal("CreateSession().OK = false, want true")
	}
	if created.Session == nil {
		t.Fatal("CreateSession().Session = nil, want value")
	}
	if created.Session.SessionID != "s_1" {
		t.Fatalf("CreateSession().SessionID = %q, want %q", created.Session.SessionID, "s_1")
	}
	if created.Session.RuntimeID != "r_1" {
		t.Fatalf("CreateSession().RuntimeID = %q, want %q", created.Session.RuntimeID, "r_1")
	}
	if created.Session.ThreadID != "t_1" {
		t.Fatalf("CreateSession().ThreadID = %q, want %q", created.Session.ThreadID, "t_1")
	}
	if !created.Session.Focused {
		t.Fatal("CreateSession().Focused = false, want true")
	}
	if created.WSAttach == nil || len(created.WSAttach.SuggestSubscriptions) != 1 || created.WSAttach.SuggestSubscriptions[0] != "session:s_1" {
		t.Fatalf("CreateSession().WSAttach = %+v, want session:s_1", created.WSAttach)
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	if listed.Items[0].SessionID != created.Session.SessionID {
		t.Fatalf("ListSessions().Items[0].SessionID = %q, want %q", listed.Items[0].SessionID, created.Session.SessionID)
	}
	if listed.Items[0].AgentBackend != "pi" {
		t.Fatalf("ListSessions().Items[0].AgentBackend = %q, want %q", listed.Items[0].AgentBackend, "pi")
	}
	if listed.Items[0].Title != title {
		t.Fatalf("ListSessions().Items[0].Title = %q, want %q", listed.Items[0].Title, title)
	}
	if listed.Items[0].CWD != "/root/code/ActRail" {
		t.Fatalf("ListSessions().Items[0].CWD = %q, want %q", listed.Items[0].CWD, "/root/code/ActRail")
	}
	if listed.Items[0].Busy {
		t.Fatal("ListSessions().Items[0].Busy = true, want false")
	}
	if !listed.Items[0].Focused {
		t.Fatal("ListSessions().Items[0].Focused = false, want true")
	}
	if listed.Items[0].Historical {
		t.Fatal("ListSessions().Items[0].Historical = true, want false")
	}
	if listed.Items[0].TransportState != SessionTransportStateAttached.String() {
		t.Fatalf("ListSessions().Items[0].TransportState = %q, want %q", listed.Items[0].TransportState, SessionTransportStateAttached)
	}
	if listed.Items[0].GenerationID != "" {
		t.Fatalf("ListSessions().Items[0].GenerationID = %q, want empty without iod helper", listed.Items[0].GenerationID)
	}
	if listed.Items[0].LastUpdatedTS != timestampSeconds(now) {
		t.Fatalf("ListSessions().Items[0].LastUpdatedTS = %v, want %v", listed.Items[0].LastUpdatedTS, timestampSeconds(now))
	}

	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	details, err := svc.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	if details.Provider != provider {
		t.Fatalf("SessionDetails().Provider = %q, want %q", details.Provider, provider)
	}
	if details.Model != model {
		t.Fatalf("SessionDetails().Model = %q, want %q", details.Model, model)
	}
	if details.QueueLength != 0 {
		t.Fatalf("SessionDetails().QueueLength = %d, want 0", details.QueueLength)
	}
	if details.LastUpdatedTS != timestampSeconds(now) || details.LastActivityTS != timestampSeconds(now) {
		t.Fatalf("SessionDetails() timestamps = (%v, %v), want (%v, %v)", details.LastUpdatedTS, details.LastActivityTS, timestampSeconds(now), timestampSeconds(now))
	}
	if details.Capabilities.WSRealtime || !details.Capabilities.PIUI || !details.Capabilities.WorkspaceRead {
		t.Fatalf("SessionDetails().Capabilities = %+v, want disabled ws and enabled pi_ui/workspace_read", details.Capabilities)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false")
	}
	if len(state.Queue.Items) != 0 {
		t.Fatalf("len(SessionState().Queue.Items) = %d, want 0", len(state.Queue.Items))
	}
	if state.TailSeq != 0 {
		t.Fatalf("SessionState().TailSeq = %d, want 0", state.TailSeq)
	}
	if state.Transport.State != SessionTransportStateAttached {
		t.Fatalf("SessionState().Transport = %+v, want attached direct-runtime transport", state.Transport)
	}
	if state.Transport.GenerationID != "" {
		t.Fatalf("SessionState().Transport.GenerationID = %q, want empty without iod helper", state.Transport.GenerationID)
	}
	if state.UIRequest != nil {
		t.Fatalf("SessionState().UIRequest = %+v, want nil", state.UIRequest)
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want nil", state.PartialAssistantTurn)
	}
	if state.ResumeCursors != (SessionResumeCursors{}) {
		t.Fatalf("SessionState().ResumeCursors = %+v, want empty", state.ResumeCursors)
	}
}

func TestStubSessionReadStateControlsUnreadSummary(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 0).UTC()
	svc := newStub(cfg, func() time.Time { return now })
	title := "Unread project"
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend: "pi",
		PIAgentGRPC:  boolPtr(false),
		CWD:          "/root/code/ActRail",
		Title:        &title,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	if _, err := svc.AppendSessionMessage(sessionID, "user", "message", "hello"); err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}
	if _, err := svc.AppendAssistantDelta(sessionID, "turn_1", "reply"); err != nil {
		t.Fatalf("AppendAssistantDelta() error = %v", err)
	}
	if _, err := svc.CommitAssistantTurn(sessionID, "turn_1", ""); err != nil {
		t.Fatalf("CommitAssistantTurn() error = %v", err)
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 || !listed.Items[0].HasUnreadAssistant {
		t.Fatalf("ListSessions().Items = %+v, want unread assistant", listed.Items)
	}

	read, err := svc.MarkSessionRead(context.Background(), MarkSessionReadRequest{SessionID: created.Session.SessionID})
	if err != nil {
		t.Fatalf("MarkSessionRead() error = %v", err)
	}
	if !read.OK || read.ReadSeq != 2 {
		t.Fatalf("MarkSessionRead() = %+v, want read seq 2", read)
	}
	listed, err = svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions(after read) error = %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].HasUnreadAssistant {
		t.Fatalf("ListSessions(after read).Items = %+v, want no unread assistant", listed.Items)
	}
}

func TestStubEmitAllSessionStatesPublishesCurrentSnapshots(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 0).UTC()
	svc := newStub(cfg, func() time.Time { return now })
	sink := &captureRuntimeSink{}
	svc.SetRuntimeEventSink(sink)

	first, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/repo/a"})
	if err != nil {
		t.Fatalf("CreateSession(first) error = %v", err)
	}
	second, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/repo/b"})
	if err != nil {
		t.Fatalf("CreateSession(second) error = %v", err)
	}
	firstID, err := session.ParseSessionID(first.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(first) error = %v", err)
	}
	secondID, err := session.ParseSessionID(second.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(second) error = %v", err)
	}
	if _, err := svc.AppendSessionMessage(firstID, "user", "message", "work on this"); err != nil {
		t.Fatalf("AppendSessionMessage(first) error = %v", err)
	}
	if _, ok, err := svc.registry.SetBusy(firstID, true); err != nil || !ok {
		t.Fatalf("SetBusy(first) = (_, %v, %v), want ok=true err=nil", ok, err)
	}

	before := sink.snapshot()
	emitted := svc.EmitAllSessionStates()
	if emitted != 2 {
		t.Fatalf("EmitAllSessionStates() = %d, want 2", emitted)
	}
	after := sink.snapshot()
	fresh := after.states[len(before.states):]
	if len(fresh) != 2 {
		t.Fatalf("fresh session state events = %d, want 2", len(fresh))
	}

	byID := map[session.SessionID]SessionStateEvent{}
	for _, event := range fresh {
		byID[event.SessionID] = event
	}
	firstEvent, ok := byID[firstID]
	if !ok {
		t.Fatalf("missing heartbeat state for %s", firstID)
	}
	if !firstEvent.Busy || firstEvent.BusyReason != "state_busy" || firstEvent.QueueLen != 0 || firstEvent.TailSeq != 1 {
		t.Fatalf("first heartbeat event = %+v, want busy state with tail 1", firstEvent)
	}
	secondEvent, ok := byID[secondID]
	if !ok {
		t.Fatalf("missing heartbeat state for %s", secondID)
	}
	if secondEvent.Busy || secondEvent.TailSeq != 0 {
		t.Fatalf("second heartbeat event = %+v, want idle state with tail 0", secondEvent)
	}
}

func TestStubListSessionsUsesAttachedHelperForTransportSnapshot(t *testing.T) {
	cfg := config.Load()
	svc := newStub(cfg, time.Now)
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/docs"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_list_attach")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{}
		record.transport = SessionTransportSnapshot{}
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok=true err=nil", ok, err)
	}
	svc.helpers.replaceAll(map[session.SessionID]attachedHelper{
		sessionID: {
			Binding:      helperGenerationBinding{SessionID: sessionID, GenerationID: generationID},
			ManifestPath: "/tmp/actrail-iod/g_list_attach/generation-manifest.json",
			Hello:        iod.HelloPacket{HelloProof: iod.HelloProof{HelperPID: 12345}},
		},
	}, nil)

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	if listed.Items[0].TransportState != SessionTransportStateAttached.String() || listed.Items[0].GenerationID != generationID.String() {
		t.Fatalf("ListSessions().Items[0] transport = (%q, %q), want attached %q", listed.Items[0].TransportState, listed.Items[0].GenerationID, generationID)
	}
	if listed.Items[0].IOD == nil || listed.Items[0].IOD.Mode != "std" {
		t.Fatalf("ListSessions().Items[0].IOD = %+v, want std mode", listed.Items[0].IOD)
	}
}

func TestCodexSessionStateAndListHideGenerationID(t *testing.T) {
	cfg := config.Load()
	svc := newStub(cfg, time.Now)
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: "/root/docs"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	generationID, err := iod.NewGenerationID("g_codex_ui_hidden")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.transport = SessionTransportSnapshot{GenerationID: generationID.String(), State: SessionTransportStateAttached}
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			codex:    newCodexRuntimeState(session.BackendCodex),
			helper: &runtimeIODHelper{
				sessionID:    sessionID,
				generationID: generationID,
			},
			helperBinding: &RuntimeHelperBinding{
				HelperSessionID: sessionID,
				GenerationID:    generationID,
			},
			currentHelperBinding: func(session.SessionID) (*RuntimeHelperBinding, error) {
				return &RuntimeHelperBinding{HelperSessionID: sessionID, GenerationID: generationID}, nil
			},
		}
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok=true err=nil", ok, err)
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if listed.Items[0].TransportState != SessionTransportStateAttached.String() || listed.Items[0].GenerationID != "" {
		t.Fatalf("ListSessions().Items[0] transport = (%q, %q), want attached without generation", listed.Items[0].TransportState, listed.Items[0].GenerationID)
	}
	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.GenerationID != "" {
		t.Fatalf("SessionState().Transport = %+v, want attached without generation", state.Transport)
	}
	record, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatalf("registry.Lookup(%q) = false", sessionID)
	}
	if internal := svc.sessionTransportSnapshot(record); internal.GenerationID != generationID.String() {
		t.Fatalf("internal transport generation = %q, want %q", internal.GenerationID, generationID)
	}
}

func TestStubListSessionsUsesStablePagination(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 0).UTC()
	svc := newStub(cfg, func() time.Time { return now })
	for _, cwd := range []string{"/tmp/one", "/tmp/two", "/tmp/three"} {
		if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: cwd}); err != nil {
			t.Fatalf("CreateSession(%q) error = %v", cwd, err)
		}
	}

	paged, err := svc.ListSessions(context.Background(), ListSessionsRequest{Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(paged.Items) != 1 {
		t.Fatalf("len(paged.Items) = %d, want 1", len(paged.Items))
	}
	if paged.Items[0].SessionID != "s_2" {
		t.Fatalf("paged.Items[0].SessionID = %q, want %q", paged.Items[0].SessionID, "s_2")
	}
	if paged.RemainingCount != 1 {
		t.Fatalf("paged.RemainingCount = %d, want 1", paged.RemainingCount)
	}
}

func TestStubDetailsAndStateReturnNotFoundForUnknownSession(t *testing.T) {
	cfg := config.Load()
	svc := newStub(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() })
	unknown, err := session.ParseSessionID("s_999")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, err = svc.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: unknown})
	assertNotFound(t, err)

	_, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: unknown})
	assertNotFound(t, err)
}

func TestStubCreateSessionReturnsNotFoundForUnknownResumeSession(t *testing.T) {
	cfg := config.Load()
	svc := newStub(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() })
	resume := "s_404"

	_, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend:    "pi",
		PIAgentGRPC:     boolPtr(false),
		CWD:             "/root/code/ActRail",
		ResumeSessionID: &resume,
	})
	assertNotFound(t, err)
}

func TestStubCreateSessionReturnsInvalidForMissingWorkingDirectory(t *testing.T) {
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Runner: process.NewExecRunner(),
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/bin/true", nil
		},
	})
	missing := filepath.Join(t.TempDir(), "missing")

	_, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: missing})
	assertInvalid(t, err, "cwd")
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("CreateSession() error = %v, want does not exist", err)
	}
}

func assertNotFound(t *testing.T, err error) {
	t.Helper()
	var appErr *Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if appErr.Code != "not_found" {
		t.Fatalf("error code = %q, want %q", appErr.Code, "not_found")
	}
}
