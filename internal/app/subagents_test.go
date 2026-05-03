package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func TestListSubagentsEmptyUntilRuntimeBackendCreatesActors(t *testing.T) {
	s := NewStubForTest(config.Load(), time.Now, RuntimeConfig{})
	res, err := s.ListSubagents(context.Background(), ListSubagentsRequest{})
	if err != nil {
		t.Fatalf("ListSubagents() error = %v", err)
	}
	if !res.OK || len(res.Roots) != 0 || res.TotalCount != 0 || res.NonLeafCount != 0 {
		t.Fatalf("ListSubagents() = %#v, want empty ok snapshot", res)
	}
}

func TestPersistentStubListSubagentsEmpty(t *testing.T) {
	s, err := NewPersistentStubForTest(persistentTestConfig(t), time.Now, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	res, err := s.ListSubagents(context.Background(), ListSubagentsRequest{})
	if err != nil {
		t.Fatalf("ListSubagents() error = %v", err)
	}
	if !res.OK || res.TotalCount != 0 {
		t.Fatalf("ListSubagents() = %#v, want empty ok snapshot", res)
	}
}

func TestPersistentStubResumeAskParentAfterRestart(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000100, 0).UTC()
	created, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	cwd := t.TempDir()
	parent, err := created.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: cwd})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	spawned, err := created.SpawnSubagent(context.Background(), SpawnSubagentRequest{ParentSessionID: parent.Session.SessionID, Name: "reviewer", AgentBackend: "pi", CWD: cwd})
	if err != nil {
		t.Fatalf("SpawnSubagent() error = %v", err)
	}
	_, err = created.subagents.askParent(spawned.ActorID, "turn_1", "Continue?", "ctx")
	if err != nil {
		t.Fatalf("askParent() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(rehydrate) error = %v", err)
	}
	listed, err := rehydrated.ListSubagents(context.Background(), ListSubagentsRequest{})
	if err != nil {
		t.Fatalf("ListSubagents() error = %v", err)
	}
	if listed.TotalCount != 1 || listed.Roots[0].Question == nil || listed.Roots[0].Status != SubagentStatusWaitingForParent {
		t.Fatalf("rehydrated pending question = %+v", listed)
	}
	answerCh := make(chan appAskParentResult, 1)
	go func() {
		answer, err := rehydrated.ResumeAskParent(context.Background(), AskParentRequest{ActorID: spawned.ActorID, QuestionID: listed.Roots[0].Question.QuestionID})
		answerCh <- appAskParentResult{answer: answer, err: err}
	}()
	if _, err := rehydrated.AnswerSubagent(context.Background(), AnswerSubagentRequest{ActorID: spawned.ActorID, QuestionID: listed.Roots[0].Question.QuestionID, Answer: "Continue"}); err != nil {
		t.Fatalf("AnswerSubagent() error = %v", err)
	}
	select {
	case got := <-answerCh:
		if got.err != nil || got.answer.Answer != "Continue" {
			t.Fatalf("ResumeAskParent() = %+v, %v", got.answer, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("ResumeAskParent did not return")
	}

	restartedAgain, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(2 * time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart after answer) error = %v", err)
	}
	resumed, err := restartedAgain.ResumeAskParent(context.Background(), AskParentRequest{ActorID: spawned.ActorID, QuestionID: listed.Roots[0].Question.QuestionID})
	if err != nil {
		t.Fatalf("ResumeAskParent(after answer) error = %v", err)
	}
	if resumed.Answer != "Continue" {
		t.Fatalf("ResumeAskParent(after answer) = %+v, want persisted answer", resumed)
	}
}

type appAskParentResult struct {
	answer AskParentResponse
	err    error
}

func TestPersistentStubRehydratesSubagentActorsAndEvents(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000000, 0).UTC()
	created, err := NewPersistentStubForTest(cfg, func() time.Time { return now }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(create) error = %v", err)
	}
	cwd := t.TempDir()
	parent, err := created.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: cwd})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	spawned, err := created.SpawnSubagent(context.Background(), SpawnSubagentRequest{ParentSessionID: parent.Session.SessionID, Name: "reviewer", Role: "review", AgentBackend: "pi", CWD: cwd})
	if err != nil {
		t.Fatalf("SpawnSubagent() error = %v", err)
	}
	if _, err := created.PromptSubagent(context.Background(), PromptSubagentRequest{ActorID: spawned.ActorID, Prompt: "Inspect this"}); err != nil {
		t.Fatalf("PromptSubagent() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(rehydrate) error = %v", err)
	}
	listed, err := rehydrated.ListSubagents(context.Background(), ListSubagentsRequest{IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListSubagents() error = %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Roots) != 1 {
		t.Fatalf("ListSubagents() = %+v, want one actor", listed)
	}
	actor := listed.Roots[0]
	if actor.ActorID != spawned.ActorID || actor.ChildSessionID != spawned.ChildSessionID || actor.Status != SubagentStatusRunning || len(actor.Messages) != 1 {
		t.Fatalf("rehydrated actor = %+v, want persisted prompt actor", actor)
	}
	events, err := rehydrated.SubagentEvents(context.Background(), SubagentEventsRequest{ActorID: spawned.ActorID})
	if err != nil {
		t.Fatalf("SubagentEvents() error = %v", err)
	}
	if len(events.Events) != 3 || events.Events[0].Type != "subagent.started" || events.Events[2].Type != "subagent.prompt" {
		t.Fatalf("events = %+v, want started, turn_started, prompt", events.Events)
	}
	again, err := rehydrated.SpawnSubagent(context.Background(), SpawnSubagentRequest{ParentSessionID: parent.Session.SessionID, Name: "reviewer", AgentBackend: "pi", CWD: cwd})
	if err == nil || again.OK {
		t.Fatalf("SpawnSubagent duplicate = %+v, %v, want conflict", again, err)
	}
}

func TestSpawnSubagentCreatesChildSessionAndActor(t *testing.T) {
	s := newStub(config.Load(), time.Now)
	parent, err := s.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/repo"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	res, err := s.SpawnSubagent(context.Background(), SpawnSubagentRequest{ParentSessionID: parent.Session.SessionID, Name: "reviewer", Role: "review", AgentBackend: "pi", CWD: "/repo"})
	if err != nil {
		t.Fatalf("SpawnSubagent() error = %v", err)
	}
	if !res.OK || res.ActorID == "" || res.ChildSessionID == "" || res.Actor == nil {
		t.Fatalf("SpawnSubagent() = %+v, want actor and child session", res)
	}
	listed, err := s.ListSubagents(context.Background(), ListSubagentsRequest{})
	if err != nil {
		t.Fatalf("ListSubagents() error = %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Roots) != 1 || listed.Roots[0].ChildSessionID != res.ChildSessionID {
		t.Fatalf("ListSubagents() = %+v, want spawned actor", listed)
	}
}

func TestSpawnSubagentHidesChildSessionFromSessionList(t *testing.T) {
	s := newStub(config.Load(), time.Now)
	parent, err := s.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/repo"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	spawned, err := s.SpawnSubagent(context.Background(), SpawnSubagentRequest{ParentSessionID: parent.Session.SessionID, Name: "reviewer", AgentBackend: "pi", CWD: "/repo"})
	if err != nil {
		t.Fatalf("SpawnSubagent() error = %v", err)
	}
	sessions, err := s.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if sessions.TotalCount != 1 || len(sessions.Items) != 1 || sessions.Items[0].SessionID != parent.Session.SessionID {
		t.Fatalf("ListSessions() = %+v, want parent only", sessions)
	}
	if sessions.Items[0].SessionID == spawned.ChildSessionID {
		t.Fatalf("child session %q leaked into session list", spawned.ChildSessionID)
	}
	if _, ok := s.registry.Lookup(session.SessionID(spawned.ChildSessionID)); !ok {
		t.Fatalf("hidden child session %q not readable by registry", spawned.ChildSessionID)
	}
}

func TestCloseSubagentKillsChildRuntimeWithoutDeletingHistory(t *testing.T) {
	var handles []*process.FakeHandle
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		h := process.NewFakeHandle(spec)
		handles = append(handles, h)
		return h
	}}
	s := NewStubForTest(config.Load(), time.Now, RuntimeConfig{Runner: runner})
	parent, err := s.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/repo"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	spawned, err := s.SpawnSubagent(context.Background(), SpawnSubagentRequest{ParentSessionID: parent.Session.SessionID, Name: "reviewer", AgentBackend: "pi", CWD: "/repo"})
	if err != nil {
		t.Fatalf("SpawnSubagent() error = %v", err)
	}
	if len(handles) != 2 {
		t.Fatalf("runtime starts = %d, want parent and child", len(handles))
	}
	if _, err := s.CloseSubagent(context.Background(), CloseSubagentRequest{ActorID: spawned.ActorID}); err != nil {
		t.Fatalf("CloseSubagent() error = %v", err)
	}
	if handles[0].KillCalls() != 0 || handles[1].KillCalls() != 1 {
		t.Fatalf("kill calls = parent %d child %d, want 0 and 1", handles[0].KillCalls(), handles[1].KillCalls())
	}
	listed, err := s.ListSubagents(context.Background(), ListSubagentsRequest{IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListSubagents() error = %v", err)
	}
	if listed.TotalCount != 1 || listed.Roots[0].Status != SubagentStatusClosed {
		t.Fatalf("ListSubagents(include closed) = %+v, want closed actor history", listed)
	}
}

func TestSpawnSubagentRejectsMissingParentSession(t *testing.T) {
	s := newStub(config.Load(), time.Now)
	_, err := s.SpawnSubagent(context.Background(), SpawnSubagentRequest{ParentSessionID: "missing", Name: "reviewer", AgentBackend: "pi", CWD: "/repo"})
	var appErr *Error
	if !errors.As(err, &appErr) || appErr.Code != "not_found" {
		t.Fatalf("SpawnSubagent() error = %v, want not_found", err)
	}
}

func TestSubagentRegistryActiveNameConflictAllowsClosedRecreate(t *testing.T) {
	r := newSubagentRegistry(time.Now)
	first, err := r.spawn("parent", "", "child_1", "reviewer", "review", "pi", "", "/repo")
	if err != nil {
		t.Fatalf("spawn first error = %v", err)
	}
	_, err = r.spawn("parent", "", "child_2", "reviewer", "review", "pi", "", "/repo")
	var appErr *Error
	if !errors.As(err, &appErr) || appErr.Code != "conflict" {
		t.Fatalf("spawn duplicate error = %v, want conflict", err)
	}
	if _, err := r.close(first.ActorID); err != nil {
		t.Fatalf("close first error = %v", err)
	}
	if _, err := r.spawn("parent", "", "child_3", "reviewer", "review", "pi", "", "/repo"); err != nil {
		t.Fatalf("spawn after close error = %v", err)
	}
}

func TestSubagentStatusJSONSerialization(t *testing.T) {
	statuses := []SubagentStatus{SubagentStatusRunning, SubagentStatusIdle, SubagentStatusWaitingForParent, SubagentStatusCompleted, SubagentStatusFailed, SubagentStatusClosed, SubagentStatusUnknown}
	for _, status := range statuses {
		encoded, err := json.Marshal(SubagentNode{ActorID: "actor", Status: status})
		if err != nil {
			t.Fatalf("Marshal(%q) error = %v", status, err)
		}
		if !strings.Contains(string(encoded), `"status":"`+string(status)+`"`) {
			t.Fatalf("Marshal(%q) = %s", status, encoded)
		}
	}
}

func TestSubagentAskParentAbortReturnsTerminalResult(t *testing.T) {
	r := newSubagentRegistry(time.Now)
	actor, err := r.spawn("parent", "", "child", "reviewer", "review", "pi", "", "/repo")
	if err != nil {
		t.Fatalf("spawn error = %v", err)
	}
	qid, err := r.askParent(actor.ActorID, "turn_1", "Continue?", "")
	if err != nil {
		t.Fatalf("askParent error = %v", err)
	}
	answerCh := make(chan subagentParentAnswer, 1)
	errCh := make(chan error, 1)
	go func() {
		answer, err := r.waitForAnswer(context.Background(), actor.ActorID, qid)
		if err != nil {
			errCh <- err
			return
		}
		answerCh <- answer
	}()
	if _, err := r.abort(actor.ActorID, "turn_1"); err != nil {
		t.Fatalf("abort error = %v", err)
	}
	select {
	case got := <-answerCh:
		if got.terminal != "aborted" || got.answer != "" {
			t.Fatalf("answer = %+v, want aborted terminal", got)
		}
	case err := <-errCh:
		t.Fatalf("waitForAnswer error = %v", err)
	case <-time.After(time.Second):
		t.Fatal("waitForAnswer did not return")
	}
}

func TestSubagentRegistryTreeAndClosedFilter(t *testing.T) {
	r := newSubagentRegistry(time.Now)
	lead, err := r.spawn("parent", "", "child_lead", "lead", "main", "pi", "", "/repo")
	if err != nil {
		t.Fatalf("spawn lead error = %v", err)
	}
	if _, err := r.spawn("child_lead", lead.ActorID, "child_leaf", "leaf", "worker", "pi", "", "/repo"); err != nil {
		t.Fatalf("spawn leaf error = %v", err)
	}
	closed, err := r.spawn("child_lead", lead.ActorID, "child_closed", "closed", "worker", "pi", "", "/repo")
	if err != nil {
		t.Fatalf("spawn closed error = %v", err)
	}
	if _, err := r.close(closed.ActorID); err != nil {
		t.Fatalf("close error = %v", err)
	}
	visible := r.snapshot(false)
	if got := countSubagentNodes(visible); got != 2 {
		t.Fatalf("countSubagentNodes(visible) = %d, want 2", got)
	}
	if got := countNonLeafSubagentNodes(visible); got != 1 {
		t.Fatalf("countNonLeafSubagentNodes(visible) = %d, want 1", got)
	}
	withClosed := r.snapshot(true)
	if got := countSubagentNodes(withClosed); got != 3 {
		t.Fatalf("countSubagentNodes(withClosed) = %d, want 3", got)
	}
}

func TestSubagentAskParentAnswerAndReplay(t *testing.T) {
	r := newSubagentRegistry(time.Now)
	actor, err := r.spawn("parent", "", "child", "reviewer", "review", "pi", "", "/repo")
	if err != nil {
		t.Fatalf("spawn error = %v", err)
	}
	qid, err := r.askParent(actor.ActorID, "turn_1", "Use A or B?", "context")
	if err != nil {
		t.Fatalf("askParent error = %v", err)
	}
	answerCh := make(chan subagentParentAnswer, 1)
	errCh := make(chan error, 1)
	go func() {
		answer, err := r.waitForAnswer(context.Background(), actor.ActorID, qid)
		if err != nil {
			errCh <- err
			return
		}
		answerCh <- answer
	}()
	if _, err := r.answer(actor.ActorID, qid, "Use B"); err != nil {
		t.Fatalf("answer error = %v", err)
	}
	select {
	case got := <-answerCh:
		if got.answer != "Use B" || got.terminal != "" {
			t.Fatalf("answer = %+v, want Use B", got)
		}
	case err := <-errCh:
		t.Fatalf("waitForAnswer error = %v", err)
	case <-time.After(time.Second):
		t.Fatal("waitForAnswer did not return")
	}
	events, err := r.eventsAfter(actor.ActorID, "")
	if err != nil {
		t.Fatalf("eventsAfter error = %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("len(events) = %d, want at least 3", len(events))
	}
	replayed, err := r.eventsAfter(actor.ActorID, events[0].EventID)
	if err != nil {
		t.Fatalf("eventsAfter cursor error = %v", err)
	}
	if len(replayed) != len(events)-1 {
		t.Fatalf("len(replayed) = %d, want %d", len(replayed), len(events)-1)
	}
}
