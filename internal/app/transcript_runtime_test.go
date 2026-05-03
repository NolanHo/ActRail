package app

import (
	"context"
	"testing"
	"time"

	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestSessionRegistryTranscriptMutationsSyncState(t *testing.T) {
	now := time.Unix(1760000000, 0).UTC()
	registry := newSessionRegistry(func() time.Time { return now })
	record, err := registry.Create(sessionCreateSpec{Backend: session.BackendPI, CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	sessionID := record.identity.SessionID()

	item, ok, err := registry.AppendMessage(sessionID, "user", "message", "prompt")
	if err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	if !ok || item.Seq().Uint64() != 1 {
		t.Fatalf("AppendMessage() = (%d, %v), want (1, true)", item.Seq(), ok)
	}
	stored, ok := registry.Lookup(sessionID)
	if !ok {
		t.Fatal("Lookup() after AppendMessage ok = false, want true")
	}
	if stored.state.Busy() {
		t.Fatal("state.Busy() after AppendMessage = true, want false")
	}
	if stored.state.Tail().Seq().Uint64() != 1 {
		t.Fatalf("state.Tail().Seq() = %d, want 1", stored.state.Tail().Seq())
	}

	partial, ok, err := registry.AppendAssistantDelta(sessionID, "turn_1", "hel")
	if err != nil {
		t.Fatalf("AppendAssistantDelta(first) error = %v", err)
	}
	if !ok || partial.Text() != "hel" {
		t.Fatalf("AppendAssistantDelta(first) = (%q, %v), want (%q, true)", partial.Text(), ok, "hel")
	}
	partial, ok, err = registry.AppendAssistantDelta(sessionID, "turn_1", "lo")
	if err != nil {
		t.Fatalf("AppendAssistantDelta(second) error = %v", err)
	}
	if !ok || partial.Text() != "hello" {
		t.Fatalf("AppendAssistantDelta(second) = (%q, %v), want (%q, true)", partial.Text(), ok, "hello")
	}
	stored, ok = registry.Lookup(sessionID)
	if !ok {
		t.Fatal("Lookup() after AppendAssistantDelta ok = false, want true")
	}
	if !stored.state.Busy() {
		t.Fatal("state.Busy() after AppendAssistantDelta = false, want true")
	}
	if !stored.state.Tail().Live() || stored.state.Tail().Seq().Uint64() != 1 {
		t.Fatalf("state.Tail() after AppendAssistantDelta = %+v, want live seq 1", stored.state.Tail())
	}
	storedPartial, ok := stored.transcript.PartialAssistantTurn()
	if !ok || storedPartial.Text() != "hello" {
		t.Fatalf("stored.transcript.PartialAssistantTurn() = (%q, %v), want (%q, true)", storedPartial.Text(), ok, "hello")
	}

	committed, ok, err := registry.CommitAssistantTurn(sessionID, "turn_1", "")
	if err != nil {
		t.Fatalf("CommitAssistantTurn() error = %v", err)
	}
	if !ok || committed.Seq().Uint64() != 2 || committed.Text() != "hello" {
		t.Fatalf("CommitAssistantTurn() = (%d, %q, %v), want (2, %q, true)", committed.Seq(), committed.Text(), ok, "hello")
	}
	stored, ok = registry.Lookup(sessionID)
	if !ok {
		t.Fatal("Lookup() after CommitAssistantTurn ok = false, want true")
	}
	if stored.state.Busy() {
		t.Fatal("state.Busy() after CommitAssistantTurn = true, want false")
	}
	if stored.state.Tail().Live() || stored.state.Tail().Seq().Uint64() != 2 {
		t.Fatalf("state.Tail() after CommitAssistantTurn = %+v, want committed seq 2", stored.state.Tail())
	}
	if _, ok := stored.transcript.PartialAssistantTurn(); ok {
		t.Fatal("stored.transcript.PartialAssistantTurn() ok = true after commit, want false")
	}
}

func TestStubSessionMessagesReturnsEmptyHistoryForKnownSession(t *testing.T) {
	cfg := config.Load()
	svc := newStub(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() })
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() error = %v", err)
	}
	if len(messages.Items) != 0 {
		t.Fatalf("len(SessionMessages().Items) = %d, want 0", len(messages.Items))
	}
	if messages.HasMore {
		t.Fatal("SessionMessages().HasMore = true, want false")
	}
	if messages.NextBeforeSeq != nil {
		t.Fatalf("SessionMessages().NextBeforeSeq = %v, want nil", *messages.NextBeforeSeq)
	}
	if messages.TailSeq != 0 {
		t.Fatalf("SessionMessages().TailSeq = %d, want 0", messages.TailSeq)
	}
}

func TestStubTranscriptWriterFeedsSessionStateAndHistory(t *testing.T) {
	cfg := config.Load()
	now := time.Unix(1760000000, 0).UTC()
	svc := newStub(cfg, func() time.Time { return now })
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/root/code/ActRail"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	first, err := svc.AppendSessionMessage(sessionID, "user", "message", "prompt")
	if err != nil {
		t.Fatalf("AppendSessionMessage() error = %v", err)
	}
	if first.Seq != 1 || first.Role != "user" || first.Text != "prompt" {
		t.Fatalf("AppendSessionMessage() = %+v, want seq 1 user prompt", first)
	}
	partial, err := svc.AppendAssistantDelta(sessionID, "turn_1", "hel")
	if err != nil {
		t.Fatalf("AppendAssistantDelta(first) error = %v", err)
	}
	if partial == nil || partial.Text != "hel" {
		t.Fatalf("AppendAssistantDelta(first) = %+v, want text hel", partial)
	}
	partial, err = svc.AppendAssistantDelta(sessionID, "turn_1", "lo")
	if err != nil {
		t.Fatalf("AppendAssistantDelta(second) error = %v", err)
	}
	if partial == nil || partial.Text != "hello" {
		t.Fatalf("AppendAssistantDelta(second) = %+v, want text hello", partial)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() during partial turn error = %v", err)
	}
	if !state.Busy {
		t.Fatal("SessionState().Busy during partial turn = false, want true")
	}
	if state.TailSeq != 1 {
		t.Fatalf("SessionState().TailSeq during partial turn = %d, want 1", state.TailSeq)
	}
	if state.PartialAssistantTurn == nil || state.PartialAssistantTurn.TurnID != "turn_1" || state.PartialAssistantTurn.Text != "hello" {
		t.Fatalf("SessionState().PartialAssistantTurn = %+v, want turn_1 hello", state.PartialAssistantTurn)
	}

	messages, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionMessages() during partial turn error = %v", err)
	}
	if len(messages.Items) != 1 || messages.Items[0].Seq != 1 || messages.Items[0].Text != "prompt" {
		t.Fatalf("SessionMessages() during partial turn = %+v, want only seq 1 prompt", messages)
	}
	if messages.TailSeq != 1 {
		t.Fatalf("SessionMessages().TailSeq during partial turn = %d, want 1", messages.TailSeq)
	}

	second, err := svc.CommitAssistantTurn(sessionID, "turn_1", "")
	if err != nil {
		t.Fatalf("CommitAssistantTurn() error = %v", err)
	}
	if second.Seq != 2 || second.Role != "assistant" || second.Text != "hello" {
		t.Fatalf("CommitAssistantTurn() = %+v, want seq 2 assistant hello", second)
	}

	state, err = svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() after commit error = %v", err)
	}
	if state.Busy {
		t.Fatal("SessionState().Busy after commit = true, want false")
	}
	if state.PartialAssistantTurn != nil {
		t.Fatalf("SessionState().PartialAssistantTurn after commit = %+v, want nil", state.PartialAssistantTurn)
	}
	if state.TailSeq != 2 {
		t.Fatalf("SessionState().TailSeq after commit = %d, want 2", state.TailSeq)
	}

	latest, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 1})
	if err != nil {
		t.Fatalf("SessionMessages(limit=1) error = %v", err)
	}
	if len(latest.Items) != 1 || latest.Items[0].Seq != 2 || latest.Items[0].Text != "hello" {
		t.Fatalf("SessionMessages(limit=1) = %+v, want only seq 2 hello", latest)
	}
	if !latest.HasMore {
		t.Fatal("SessionMessages(limit=1).HasMore = false, want true")
	}
	if latest.NextBeforeSeq == nil || *latest.NextBeforeSeq != 2 {
		t.Fatalf("SessionMessages(limit=1).NextBeforeSeq = %v, want 2", latest.NextBeforeSeq)
	}

	before := uint64(2)
	older, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, BeforeSeq: &before, Limit: 10})
	if err != nil {
		t.Fatalf("SessionMessages(before=2) error = %v", err)
	}
	if len(older.Items) != 1 || older.Items[0].Seq != 1 || older.Items[0].Text != "prompt" {
		t.Fatalf("SessionMessages(before=2) = %+v, want only seq 1 prompt", older)
	}
	if older.HasMore {
		t.Fatal("SessionMessages(before=2).HasMore = true, want false")
	}
}

func TestStubSessionMessagesReturnsNotFoundForUnknownSession(t *testing.T) {
	cfg := config.Load()
	svc := newStub(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() })
	unknown, err := session.ParseSessionID("s_999")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}

	_, err = svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: unknown})
	assertNotFound(t, err)
}
