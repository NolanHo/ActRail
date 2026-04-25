package app

import (
	"context"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func newSessionActionFixture(t *testing.T) (*Stub, *process.FakeHandle, session.SessionID, session.SessionID) {
	t.Helper()
	var handle *process.FakeHandle
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		handle = process.NewFakeHandle(spec)
		handle.SetPID(321)
		return handle
	}}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	cwd := t.TempDir()
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: cwd})
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
	return svc, handle, sessionID, runtimeID
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
	if listed.Items[0].Alias != "Renamed task" || !listed.Items[0].Focused || listed.Items[0].Model != model {
		t.Fatalf("ListSessions().Items[0] = %+v", listed.Items[0])
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

	_, err = svc.RestartSession(context.Background(), RestartSessionRequest{SessionID: sessionID})
	assertUnsupported(t, err)

	_, err = svc.HandoffSession(context.Background(), HandoffSessionRequest{SessionID: sessionID})
	assertUnsupported(t, err)
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
