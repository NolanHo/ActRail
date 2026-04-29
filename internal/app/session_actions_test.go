package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func newSessionActionFixture(t *testing.T) (*Stub, *process.FakeHandle, session.SessionID, session.SessionID) {
	t.Helper()
	svc, handles, sessionID, runtimeID := newSessionActionFixtureForBackend(t, "pi")
	return svc, (*handles)[0], sessionID, runtimeID
}

func newSessionActionFixtureForBackend(t *testing.T, backend string) (*Stub, *[]*process.FakeHandle, session.SessionID, session.SessionID) {
	t.Helper()
	handles := &[]*process.FakeHandle{}
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		handle := process.NewFakeHandle(spec)
		handle.SetPID(321 + len(*handles))
		*handles = append(*handles, handle)
		return handle
	}}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	cwd := t.TempDir()
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: backend, CWD: cwd})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(session) error = %v", err)
	}
	runtimeID, err := session.ParseSessionID(created.Session.RuntimeID)
	if err != nil {
		t.Fatalf("ParseSessionID(runtime) error = %v", err)
	}
	return svc, handles, sessionID, runtimeID
}

func TestStubSessionActionsMutateMetadataAndDelete(t *testing.T) {
	svc, handle, sessionID, _ := newSessionActionFixture(t)
	depCreated, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession(dependency) error = %v", err)
	}
	depID, err := session.ParseSessionID(depCreated.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(dependency) error = %v", err)
	}

	renamed, err := svc.RenameSession(context.Background(), RenameSessionRequest{SessionID: sessionID, Name: "Renamed task"})
	if err != nil {
		t.Fatalf("RenameSession() error = %v", err)
	}
	if !renamed.OK || renamed.Alias != "Renamed task" {
		t.Fatalf("RenameSession() = %+v", renamed)
	}

	focused, err := svc.FocusSession(context.Background(), FocusSessionRequest{SessionID: sessionID, Focused: true})
	if err != nil {
		t.Fatalf("FocusSession() error = %v", err)
	}
	if !focused.OK || !focused.Focused {
		t.Fatalf("FocusSession() = %+v", focused)
	}

	priority := 0.5
	snoozeUntil := int64(1760000100)
	dependency := depID.String()
	edited, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:           sessionID,
		PriorityOffset:      Float64Patch{Present: true, Value: &priority},
		SnoozeUntil:         Int64Patch{Present: true, Value: &snoozeUntil},
		DependencySessionID: StringPatch{Present: true, Value: &dependency},
	})
	if err != nil {
		t.Fatalf("EditSession() error = %v", err)
	}
	if !edited.OK || edited.PriorityOffset != priority || edited.SnoozeUntil == nil || *edited.SnoozeUntil != snoozeUntil {
		t.Fatalf("EditSession() = %+v", edited)
	}
	if edited.DependencySessionID == nil || *edited.DependencySessionID != depID.String() {
		t.Fatalf("EditSession().DependencySessionID = %+v, want %q", edited.DependencySessionID, depID)
	}

	model := "gpt-next"
	provider := "openrouter"
	switched, err := svc.SwitchSessionModel(context.Background(), SwitchSessionModelRequest{
		SessionID: sessionID,
		Model:     StringPatch{Present: true, Value: &model},
		Provider:  StringPatch{Present: true, Value: &provider},
	})
	if err != nil {
		t.Fatalf("SwitchSessionModel() error = %v", err)
	}
	if !switched.OK || switched.Model != model || switched.Provider != provider {
		t.Fatalf("SwitchSessionModel() = %+v", switched)
	}

	details, err := svc.SessionDetails(context.Background(), SessionDetailsRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionDetails() error = %v", err)
	}
	if details.Alias != "Renamed task" || !details.Focused || details.PriorityOffset != priority || details.Model != model || details.Provider != provider {
		t.Fatalf("SessionDetails() = %+v", details)
	}
	if details.DependencySessionID != depID.String() {
		t.Fatalf("SessionDetails().DependencySessionID = %q, want %q", details.DependencySessionID, depID)
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 2 {
		t.Fatalf("len(ListSessions().Items) = %d, want 2", len(listed.Items))
	}
	var listedRecord *SessionSummary
	for i := range listed.Items {
		if listed.Items[i].SessionID == sessionID.String() {
			listedRecord = &listed.Items[i]
			break
		}
	}
	if listedRecord == nil {
		t.Fatalf("ListSessions() missing session %q in %+v", sessionID, listed.Items)
	}
	if listedRecord.Alias != "Renamed task" || !listedRecord.Focused || listedRecord.Model != model {
		t.Fatalf("ListSessions() record = %+v", *listedRecord)
	}

	deleted, err := svc.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if !deleted.OK || !deleted.Removed || deleted.SessionID != sessionID.String() {
		t.Fatalf("DeleteSession() = %+v", deleted)
	}
	if handle.KillCalls() != 1 {
		t.Fatalf("handle.KillCalls() = %d, want 1", handle.KillCalls())
	}
	listed, err = svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() after delete error = %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].SessionID != depID.String() {
		t.Fatalf("ListSessions() after delete = %+v", listed.Items)
	}
}

func TestStubCreateSessionCanResumeListedPISessionCandidate(t *testing.T) {
	svc, handles, sessionID, _ := newSessionActionFixtureForBackend(t, "pi")
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	resumeID := sessionID.String()
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend:    "pi",
		CWD:             record.cwd,
		ResumeSessionID: &resumeID,
	})
	if err != nil {
		t.Fatalf("CreateSession(resume) error = %v", err)
	}
	if created.Session.SessionID == resumeID {
		t.Fatalf("CreateSession(resume).SessionID = %q, want new ActRail slot", created.Session.SessionID)
	}
	if len(*handles) != 2 {
		t.Fatalf("len(handles) = %d, want 2", len(*handles))
	}
	args := (*handles)[1].Spec().Command().Args()
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--session" && args[i+1] == record.importedSourcePath {
			found = true
		}
	}
	if !found {
		t.Fatalf("resume launch args = %#v, want --session %q", args, record.importedSourcePath)
	}
}

func TestStubSessionResumeCandidatesAndRuntimeRouteLookup(t *testing.T) {
	svc, _, sessionID, runtimeRoute := newSessionActionFixture(t)
	otherDir := t.TempDir()
	if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: otherDir}); err != nil {
		t.Fatalf("CreateSession(other) error = %v", err)
	}
	if _, ok, err := svc.registry.AppendMessage(sessionID, "user", "message", "Investigate backlog"); err != nil || !ok {
		t.Fatalf("AppendMessage() = (_, %v, %v), want ok=true err=nil", ok, err)
	}

	renamed, err := svc.RenameSession(context.Background(), RenameSessionRequest{SessionID: runtimeRoute, Name: "Runtime route rename"})
	if err != nil {
		t.Fatalf("RenameSession(runtime route) error = %v", err)
	}
	if renamed.Alias != "Runtime route rename" {
		t.Fatalf("RenameSession(runtime route) = %+v", renamed)
	}

	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	resume, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{
		CWD:          record.cwd,
		AgentBackend: "PI",
		Offset:       0,
		Limit:        1,
	})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	if !resume.OK || !resume.Exists || resume.Remaining != 0 || len(resume.Sessions) != 1 {
		t.Fatalf("SessionResumeCandidates() = %+v", resume)
	}
	candidate := resume.Sessions[0]
	if candidate.SessionID != sessionID.String() || candidate.Alias != "Runtime route rename" || candidate.FirstUserMessage != "Investigate backlog" {
		t.Fatalf("resume candidate = %+v", candidate)
	}
}

func TestPIResumeCandidateFromSourcePathPrefersSessionInfoName(t *testing.T) {
	cwd := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "pi-named.jsonl")
	body := `{"type":"session","version":3,"id":"pi-named","cwd":` + fmt.Sprintf("%q", cwd) + `}
{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"Investigate a long prompt that should not be the display label"}]}}
{"type":"session_info","name":"ActRail draft"}
{"type":"session_info","name":"ActRail"}
`
	if err := os.WriteFile(sourcePath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", sourcePath, err)
	}

	candidate, ok := piResumeCandidateFromSourcePath(cwd, sourcePath)
	if !ok {
		t.Fatal("piResumeCandidateFromSourcePath() ok = false, want true")
	}
	if candidate.DisplayName != "ActRail" || candidate.Title != "ActRail" || candidate.FirstUserMessage != "Investigate a long prompt that should not be the display label" {
		t.Fatalf("resume candidate = %+v, want session_info name before first user text", candidate)
	}
}

func TestStubSessionResumeCandidatesUseUpdatedTimeDescendingForDemotedSessions(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 0).UTC()
	svc := newStub(cfg, func() time.Time { return now })
	cwd := t.TempDir()
	for i := 0; i < 3; i++ {
		if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: cwd}); err != nil {
			t.Fatalf("CreateSession(%d) error = %v", i, err)
		}
	}
	blockedPriority := 1.0
	blockedDependency := "s_1"
	now = now.Add(time.Minute)
	if _, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:           mustSessionID(t, "s_2"),
		PriorityOffset:      Float64Patch{Present: true, Value: &blockedPriority},
		DependencySessionID: StringPatch{Present: true, Value: &blockedDependency},
	}); err != nil {
		t.Fatalf("EditSession(blocked) error = %v", err)
	}
	snoozedPriority := 1.0
	now = now.Add(time.Minute)
	snoozeUntil := now.Add(time.Hour).Unix()
	if _, err := svc.EditSession(context.Background(), EditSessionRequest{
		SessionID:      mustSessionID(t, "s_3"),
		PriorityOffset: Float64Patch{Present: true, Value: &snoozedPriority},
		SnoozeUntil:    Int64Patch{Present: true, Value: &snoozeUntil},
	}); err != nil {
		t.Fatalf("EditSession(snoozed) error = %v", err)
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	want := []string{"s_1", "s_3", "s_2"}
	listedOrder := make([]string, 0, len(want))
	for _, item := range listed.Items {
		if item.CWD == cwd && item.AgentBackend == "pi" {
			listedOrder = append(listedOrder, item.SessionID)
		}
	}
	assertSessionIDOrder(t, "ListSessions() filtered", listedOrder, want)

	resume, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{CWD: cwd, AgentBackend: "pi"})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	resumeOrder := make([]string, 0, len(resume.Sessions))
	for _, item := range resume.Sessions {
		resumeOrder = append(resumeOrder, item.SessionID)
	}
	assertSessionIDOrder(t, "SessionResumeCandidates()", resumeOrder, []string{"s_3", "s_2", "s_1"})
}

func TestStubSessionResumeCandidatesUseUpdatedTimeTieBreaker(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 0).UTC()
	svc := newStub(cfg, func() time.Time { return now })
	cwd := t.TempDir()
	for i := 0; i < 2; i++ {
		if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: cwd}); err != nil {
			t.Fatalf("CreateSession(%d) error = %v", i, err)
		}
	}

	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	want := []string{"s_1", "s_2"}
	listedOrder := make([]string, 0, len(want))
	for _, item := range listed.Items {
		if item.CWD == cwd && item.AgentBackend == "pi" {
			listedOrder = append(listedOrder, item.SessionID)
		}
	}
	assertSessionIDOrder(t, "ListSessions() filtered", listedOrder, want)

	resume, err := svc.SessionResumeCandidates(context.Background(), SessionResumeCandidatesRequest{CWD: cwd, AgentBackend: "pi"})
	if err != nil {
		t.Fatalf("SessionResumeCandidates() error = %v", err)
	}
	resumeOrder := make([]string, 0, len(resume.Sessions))
	for _, item := range resume.Sessions {
		resumeOrder = append(resumeOrder, item.SessionID)
	}
	assertSessionIDOrder(t, "SessionResumeCandidates()", resumeOrder, []string{"s_1", "s_2"})
}

func TestStubSessionActionsReturnNotFoundOrUnsupported(t *testing.T) {
	svc, _, sessionID, _ := newSessionActionFixture(t)
	unknown, err := session.ParseSessionID("s_404")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, err = svc.RenameSession(context.Background(), RenameSessionRequest{SessionID: unknown, Name: "Missing"})
	assertNotFound(t, err)

	_, err = svc.FocusSession(context.Background(), FocusSessionRequest{SessionID: unknown, Focused: true})
	assertNotFound(t, err)

	name := "Missing"
	_, err = svc.EditSession(context.Background(), EditSessionRequest{SessionID: unknown, Name: StringPatch{Present: true, Value: &name}})
	assertNotFound(t, err)

	model := "gpt-next"
	_, err = svc.SwitchSessionModel(context.Background(), SwitchSessionModelRequest{SessionID: unknown, Model: StringPatch{Present: true, Value: &model}})
	assertNotFound(t, err)

	_, err = svc.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: unknown})
	assertNotFound(t, err)

	_, err = svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: unknown})
	assertNotFound(t, err)

	_, err = svc.HandoffSession(context.Background(), HandoffSessionRequest{SessionID: unknown})
	assertNotFound(t, err)

	_, err = svc.HandoffSession(context.Background(), HandoffSessionRequest{SessionID: sessionID})
	assertUnsupported(t, err)
}

func TestStubRestartSessionReplacesRuntimeAndPreservesSessionState(t *testing.T) {
	svc, handles, sessionID, runtimeID := newSessionActionFixtureForBackend(t, "pi")
	if len(*handles) != 1 {
		t.Fatalf("len(handles) = %d, want 1", len(*handles))
	}
	if _, ok, err := svc.registry.SetBusy(sessionID, true); err != nil || !ok {
		t.Fatalf("registry.SetBusy() = (_, %v, %v), want ok=true err=nil", ok, err)
	}
	if _, err := svc.AppendSessionMessage(sessionID, "user", "message", "hello"); err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}
	if _, err := svc.AppendAssistantDelta(sessionID, "turn_restart", "partial reply"); err != nil {
		t.Fatalf("AppendAssistantDelta() error = %v", err)
	}
	if err := svc.SetSessionUIRequest(sessionID, SessionUIRequestSnapshot{RequestID: "ask_1", Kind: "ask_user", Prompt: "Choose one"}); err != nil {
		t.Fatalf("SetSessionUIRequest() error = %v", err)
	}
	if _, ok, err := svc.registry.SetTransport(sessionID, SessionTransportSnapshot{State: SessionTransportStateEnded, Reason: "helper_not_running"}); err != nil || !ok {
		t.Fatalf("registry.SetTransport() = (_, %v, %v), want ok=true err=nil", ok, err)
	}

	restarted, err := svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: runtimeID})
	if err != nil {
		t.Fatalf("RestartSession() error = %v", err)
	}
	if !restarted.OK || !restarted.Restarted {
		t.Fatalf("RestartSession() = %+v, want ok restarted response", restarted)
	}
	if restarted.SessionID != sessionID.String() {
		t.Fatalf("RestartSession().SessionID = %q, want %q", restarted.SessionID, sessionID)
	}
	if restarted.PreviousRuntimeID != runtimeID.String() {
		t.Fatalf("RestartSession().PreviousRuntimeID = %q, want %q", restarted.PreviousRuntimeID, runtimeID)
	}
	if restarted.RuntimeID == runtimeID.String() || restarted.RuntimeID == "" {
		t.Fatalf("RestartSession().RuntimeID = %q, want new runtime id", restarted.RuntimeID)
	}
	if restarted.Session == nil || restarted.Session.RuntimeID != restarted.RuntimeID {
		t.Fatalf("RestartSession().Session = %+v, want runtime %q", restarted.Session, restarted.RuntimeID)
	}
	if restarted.Session.TransportState != SessionTransportStateAttached.String() {
		t.Fatalf("RestartSession().Session.TransportState = %q, want %q", restarted.Session.TransportState, SessionTransportStateAttached)
	}
	if restarted.WSAttach == nil || restarted.WSAttach.SessionID != sessionID.String() {
		t.Fatalf("RestartSession().WSAttach = %+v, want session %q", restarted.WSAttach, sessionID)
	}
	if len(*handles) != 2 {
		t.Fatalf("len(handles) after restart = %d, want 2", len(*handles))
	}
	if (*handles)[0].KillCalls() != 1 {
		t.Fatalf("old handle KillCalls() = %d, want 1", (*handles)[0].KillCalls())
	}
	if (*handles)[1].KillCalls() != 0 {
		t.Fatalf("new handle KillCalls() = %d, want 0", (*handles)[1].KillCalls())
	}

	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() after restart error = %v", err)
	}
	updatedRuntimeID, ok := record.identity.RuntimeID()
	if !ok {
		t.Fatal("record.identity.RuntimeID() ok = false, want true")
	}
	if updatedRuntimeID.String() != restarted.RuntimeID {
		t.Fatalf("record runtime id = %q, want %q", updatedRuntimeID, restarted.RuntimeID)
	}
	if record.runtime.PID() != 322 {
		t.Fatalf("record.runtime.PID() = %d, want 322", record.runtime.PID())
	}
	if record.state.Busy() {
		t.Fatal("record.state.Busy() = true, want false after restart")
	}
	if record.uiRequest != nil {
		t.Fatalf("record.uiRequest = %+v, want nil after restart", record.uiRequest)
	}
	if record.transport != (SessionTransportSnapshot{}) {
		t.Fatalf("record.transport = %+v, want empty after restart", record.transport)
	}
	if record.resumeCursors != (SessionResumeCursors{}) {
		t.Fatalf("record.resumeCursors = %+v, want empty", record.resumeCursors)
	}
	if partial, ok := record.transcript.PartialAssistantTurn(); ok {
		t.Fatalf("record.transcript.PartialAssistantTurn() = %+v, want cleared partial", partial)
	}
	items := record.transcript.Items()
	if len(items) != 1 || items[0].Text() != "hello" {
		t.Fatalf("record transcript items = %+v, want preserved user message", items)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after restart error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy = true, want false after restart")
	}
	if state.UIRequest != nil {
		t.Fatalf("SessionState().UIRequest = %+v, want nil after restart", state.UIRequest)
	}
	if state.Transport.State != SessionTransportStateAttached {
		t.Fatalf("SessionState().Transport = %+v, want attached after restart", state.Transport)
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want nil after restart", state.PartialAssistantTurn)
	}
}

func TestStubRestartSessionSupportsCodexSessions(t *testing.T) {
	svc, _, sessionID, runtimeID := newSessionActionFixtureForBackend(t, "codex")

	restarted, err := svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("RestartSession(codex) error = %v", err)
	}
	if restarted.SessionID != sessionID.String() {
		t.Fatalf("RestartSession(codex).SessionID = %q, want %q", restarted.SessionID, sessionID)
	}
	if restarted.PreviousRuntimeID != runtimeID.String() {
		t.Fatalf("RestartSession(codex).PreviousRuntimeID = %q, want %q", restarted.PreviousRuntimeID, runtimeID)
	}
	if restarted.RuntimeID == runtimeID.String() || restarted.RuntimeID == "" {
		t.Fatalf("RestartSession(codex).RuntimeID = %q, want new runtime id", restarted.RuntimeID)
	}
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession(codex) error = %v", err)
	}
	if record.identity.Backend() != session.BackendCodex {
		t.Fatalf("record.identity.Backend() = %q, want %q", record.identity.Backend(), session.BackendCodex)
	}
}

func assertUnsupported(t *testing.T, err error) {
	t.Helper()
	appErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if appErr.Code != "unsupported" {
		t.Fatalf("error code = %q, want %q", appErr.Code, "unsupported")
	}
}

func assertSessionIDOrder(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s len = %d, want %d (%#v)", label, len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s order = %#v, want %#v", label, got, want)
		}
	}
}
