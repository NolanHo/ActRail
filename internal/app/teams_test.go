package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func testSpawnTeamRequest(parentSessionID, name, role, cwd string) SpawnTeamRequest {
	useGRPC := false
	return SpawnTeamRequest{ParentSessionID: parentSessionID, Name: name, Role: role, AgentBackend: "pi", CWD: cwd, PIAgentGRPC: &useGRPC}
}

func TestListTeamsEmptyUntilRuntimeBackendCreatesActors(t *testing.T) {
	s := NewStubForTest(config.Load(), time.Now, RuntimeConfig{})
	res, err := s.ListTeams(context.Background(), ListTeamsRequest{})
	if err != nil {
		t.Fatalf("ListTeams() error = %v", err)
	}
	if !res.OK || len(res.Roots) != 0 || res.TotalCount != 0 || res.NonLeafCount != 0 {
		t.Fatalf("ListTeams() = %#v, want empty ok snapshot", res)
	}
	if len(res.Members) != 1 || res.Members[0].Handle != DefaultHumanMemberHandle || res.Members[0].Kind != TeamMemberKindHuman {
		t.Fatalf("ListTeams() members = %#v, want default human teammate", res.Members)
	}
}

func TestPersistentStubListTeamsEmpty(t *testing.T) {
	s, err := NewPersistentStubForTest(persistentTestConfig(t), time.Now, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	res, err := s.ListTeams(context.Background(), ListTeamsRequest{})
	if err != nil {
		t.Fatalf("ListTeams() error = %v", err)
	}
	if !res.OK || res.TotalCount != 0 {
		t.Fatalf("ListTeams() = %#v, want empty ok snapshot", res)
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
	spawned, err := created.SpawnTeam(context.Background(), testSpawnTeamRequest(parent.Session.SessionID, "reviewer", "", cwd))
	if err != nil {
		t.Fatalf("SpawnTeam() error = %v", err)
	}
	_, err = created.teams.askParent(spawned.ActorID, "turn_1", "Continue?", "ctx")
	if err != nil {
		t.Fatalf("askParent() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(rehydrate) error = %v", err)
	}
	listed, err := rehydrated.ListTeams(context.Background(), ListTeamsRequest{})
	if err != nil {
		t.Fatalf("ListTeams() error = %v", err)
	}
	if listed.TotalCount != 1 || listed.Roots[0].Question == nil || listed.Roots[0].Status != TeamStatusWaitingForParent {
		t.Fatalf("rehydrated pending question = %+v", listed)
	}
	answerCh := make(chan appAskParentResult, 1)
	go func() {
		answer, err := rehydrated.ResumeAskParent(context.Background(), AskParentRequest{ActorID: spawned.ActorID, QuestionID: listed.Roots[0].Question.QuestionID})
		answerCh <- appAskParentResult{answer: answer, err: err}
	}()
	if _, err := rehydrated.AnswerTeam(context.Background(), AnswerTeamRequest{ActorID: spawned.ActorID, QuestionID: listed.Roots[0].Question.QuestionID, Answer: "Continue"}); err != nil {
		t.Fatalf("AnswerTeam() error = %v", err)
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

func TestAskTeamDefaultsToHumanTeammate(t *testing.T) {
	s := newStub(config.Load(), time.Now)
	actor, err := s.teams.spawn("parent", "", "child", "reviewer", "review", "pi", "", "/repo")
	if err != nil {
		t.Fatalf("spawn() error = %v", err)
	}

	answerCh := make(chan appAskParentResult, 1)
	go func() {
		answer, err := s.AskTeam(context.Background(), AskTeamRequest{ActorID: actor.ActorID, TurnID: "turn_1", Question: "Proceed?", Context: "Need approval"})
		answerCh <- appAskParentResult{answer: answer, err: err}
	}()

	var questionID string
	deadline := time.After(time.Second)
	for questionID == "" {
		select {
		case <-deadline:
			t.Fatal("AskTeam did not create a pending human question")
		default:
			node := s.teams.lookupNode(actor.ActorID)
			if node != nil && node.Status == TeamStatusWaitingForParent && node.Question != nil {
				questionID = node.Question.QuestionID
				if node.Question.Context != "Need approval" {
					t.Fatalf("question context = %q, want Need approval", node.Question.Context)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	if _, err := s.AnswerTeam(context.Background(), AnswerTeamRequest{ActorID: actor.ActorID, QuestionID: questionID, Answer: "Continue"}); err != nil {
		t.Fatalf("AnswerTeam() error = %v", err)
	}
	select {
	case got := <-answerCh:
		if got.err != nil {
			t.Fatalf("AskTeam() error = %v", got.err)
		}
		if got.answer.Answer != "Continue" || got.answer.TargetMember != DefaultHumanMemberHandle || got.answer.QuestionID != questionID {
			t.Fatalf("AskTeam() = %+v, want human answer", got.answer)
		}
	case <-time.After(time.Second):
		t.Fatal("AskTeam did not return after human answer")
	}
}

func TestAskTeamRejectsUnknownTarget(t *testing.T) {
	s := newStub(config.Load(), time.Now)
	_, err := s.AskTeam(context.Background(), AskTeamRequest{ActorID: "actor_1", To: "reviewer", Question: "Proceed?"})
	var appErr *Error
	if !errors.As(err, &appErr) || appErr.Code != "invalid_request" || appErr.Field != "to" {
		t.Fatalf("AskTeam() error = %v, want invalid to", err)
	}
}

func TestPersistentStubRehydratesTeamActorsAndEvents(t *testing.T) {
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
	spawned, err := created.SpawnTeam(context.Background(), testSpawnTeamRequest(parent.Session.SessionID, "reviewer", "review", cwd))
	if err != nil {
		t.Fatalf("SpawnTeam() error = %v", err)
	}
	if _, err := created.PromptTeam(context.Background(), PromptTeamRequest{ActorID: spawned.ActorID, Prompt: "Inspect this"}); err != nil {
		t.Fatalf("PromptTeam() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(rehydrate) error = %v", err)
	}
	listed, err := rehydrated.ListTeams(context.Background(), ListTeamsRequest{IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListTeams() error = %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Roots) != 1 {
		t.Fatalf("ListTeams() = %+v, want one actor", listed)
	}
	actor := listed.Roots[0]
	if actor.ActorID != spawned.ActorID || actor.ChildSessionID != spawned.ChildSessionID || actor.Status != TeamStatusRunning || len(actor.Messages) != 1 {
		t.Fatalf("rehydrated actor = %+v, want persisted prompt actor", actor)
	}
	events, err := rehydrated.TeamEvents(context.Background(), TeamEventsRequest{ActorID: spawned.ActorID})
	if err != nil {
		t.Fatalf("TeamEvents() error = %v", err)
	}
	if len(events.Events) != 3 || events.Events[0].Type != "team.started" || events.Events[2].Type != "team.prompt" {
		t.Fatalf("events = %+v, want started, turn_started, prompt", events.Events)
	}
	again, err := rehydrated.SpawnTeam(context.Background(), testSpawnTeamRequest(parent.Session.SessionID, "reviewer", "", cwd))
	if err == nil || again.OK {
		t.Fatalf("SpawnTeam duplicate = %+v, %v, want conflict", again, err)
	}
}

func TestPersistentStubReconcilesTerminalTeamActorsAfterRuntimeRestore(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000200, 0).UTC()
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	if err := catalog.UpsertSessionSnapshot(context.Background(), sqlitestore.SessionSnapshotRow{
		Session: sqlitestore.SessionRow{SessionID: "parent", Backend: "codex", CWD: "/repo", Title: "parent", CreatedAt: now, UpdatedAt: now, ActivityAt: now},
	}); err != nil {
		t.Fatalf("UpsertSessionSnapshot(parent) error = %v", err)
	}
	if err := catalog.UpsertSessionSnapshot(context.Background(), sqlitestore.SessionSnapshotRow{
		Session: sqlitestore.SessionRow{SessionID: "child", Backend: "codex", CWD: "/repo", Title: "reviewer", CreatedAt: now, UpdatedAt: now, ActivityAt: now, Hidden: true},
		Live: sqlitestore.LiveStateRow{
			Busy:                false,
			TransportState:      string(SessionTransportStateEnded),
			TransportReason:     "codex turn completed",
			RuntimeAgentRunning: false,
			UpdatedAt:           now,
		},
	}); err != nil {
		t.Fatalf("UpsertSessionSnapshot(child) error = %v", err)
	}
	if err := catalog.ReplaceTeamSnapshot(context.Background(), sqlitestore.TeamSnapshotRow{
		Actor: sqlitestore.TeamActorRow{ActorID: "actor_1", ChildSessionID: "child", ParentSessionID: "parent", Name: "reviewer", Status: string(TeamStatusRunning), TurnID: "turn_1", LastEventID: "event_2", LastEventAt: &now, CWD: "/repo", CreatedAt: now, UpdatedAt: now},
		Events: []sqlitestore.TeamEventRow{
			{ActorID: "actor_1", EventID: "event_1", Type: "team.started", ChildSessionID: "child", ParentSessionID: "parent", Status: string(TeamStatusIdle), TS: teamTimestamp(now)},
			{ActorID: "actor_1", EventID: "event_2", Type: "team.prompt", ChildSessionID: "child", ParentSessionID: "parent", TurnID: "turn_1", Message: "review", Status: string(TeamStatusRunning), TS: teamTimestamp(now)},
		},
	}); err != nil {
		t.Fatalf("ReplaceTeamSnapshot() error = %v", err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := rehydrated.ListTeams(context.Background(), ListTeamsRequest{IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListTeams() error = %v", err)
	}
	if listed.TotalCount != 1 || listed.Roots[0].Status != TeamStatusCompleted {
		t.Fatalf("ListTeams() = %+v, want reconciled completed actor", listed)
	}
	events, err := rehydrated.TeamEvents(context.Background(), TeamEventsRequest{ActorID: "actor_1"})
	if err != nil {
		t.Fatalf("TeamEvents() error = %v", err)
	}
	if len(events.Events) != 3 || events.Events[2].Type != "team.status" || events.Events[2].Status != TeamStatusCompleted {
		t.Fatalf("TeamEvents() = %+v, want completed status reconciliation event", events.Events)
	}
}

func TestPersistentStubReconcilesUnavailablePITeamActorsAfterRuntimeRestore(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000250, 0).UTC()
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	if err := catalog.UpsertSessionSnapshot(context.Background(), sqlitestore.SessionSnapshotRow{
		Session: sqlitestore.SessionRow{SessionID: "parent", Backend: "pi", CWD: "/repo", Title: "parent", CreatedAt: now, UpdatedAt: now, ActivityAt: now},
	}); err != nil {
		t.Fatalf("UpsertSessionSnapshot(parent) error = %v", err)
	}
	if err := catalog.UpsertSessionSnapshot(context.Background(), sqlitestore.SessionSnapshotRow{
		Session: sqlitestore.SessionRow{SessionID: "child", Backend: "pi", CWD: "/repo", Title: "reviewer", CreatedAt: now, UpdatedAt: now, ActivityAt: now, Hidden: true},
		Live: sqlitestore.LiveStateRow{
			Busy:                false,
			TransportState:      string(SessionTransportStateEnded),
			TransportReason:     "pi_agent_grpc_unavailable",
			RuntimeAgentRunning: false,
			UpdatedAt:           now,
		},
	}); err != nil {
		t.Fatalf("UpsertSessionSnapshot(child) error = %v", err)
	}
	if err := catalog.ReplaceTeamSnapshot(context.Background(), sqlitestore.TeamSnapshotRow{
		Actor: sqlitestore.TeamActorRow{ActorID: "actor_1", ChildSessionID: "child", ParentSessionID: "parent", Name: "reviewer", Status: string(TeamStatusRunning), TurnID: "turn_1", LastEventID: "event_2", LastEventAt: &now, CWD: "/repo", CreatedAt: now, UpdatedAt: now},
		Events: []sqlitestore.TeamEventRow{
			{ActorID: "actor_1", EventID: "event_1", Type: "team.started", ChildSessionID: "child", ParentSessionID: "parent", Status: string(TeamStatusIdle), TS: teamTimestamp(now)},
			{ActorID: "actor_1", EventID: "event_2", Type: "team.prompt", ChildSessionID: "child", ParentSessionID: "parent", TurnID: "turn_1", Message: "review", Status: string(TeamStatusRunning), TS: teamTimestamp(now)},
		},
	}); err != nil {
		t.Fatalf("ReplaceTeamSnapshot() error = %v", err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	listed, err := rehydrated.ListTeams(context.Background(), ListTeamsRequest{IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListTeams() error = %v", err)
	}
	if listed.TotalCount != 1 || listed.Roots[0].Status != TeamStatusFailed {
		t.Fatalf("ListTeams() = %+v, want failed unavailable PI actor", listed)
	}
}

func TestRuntimeRestoreReconcilesHiddenTerminalTeamChildSessions(t *testing.T) {
	cfg := persistentTestConfig(t)
	now := time.Unix(1760000300, 0).UTC()
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		t.Fatalf("OpenSessionCatalog() error = %v", err)
	}
	if err := catalog.UpsertSessionSnapshot(context.Background(), sqlitestore.SessionSnapshotRow{
		Session: sqlitestore.SessionRow{SessionID: "parent", Backend: "codex", CWD: "/repo", Title: "parent", CreatedAt: now, UpdatedAt: now, ActivityAt: now},
	}); err != nil {
		t.Fatalf("UpsertSessionSnapshot(parent) error = %v", err)
	}
	if err := catalog.UpsertSessionSnapshot(context.Background(), sqlitestore.SessionSnapshotRow{
		Session: sqlitestore.SessionRow{SessionID: "child", Backend: "codex", CWD: "/repo", Title: "reviewer", CreatedAt: now, UpdatedAt: now, ActivityAt: now, Hidden: true},
		Live: sqlitestore.LiveStateRow{
			Busy:                false,
			TransportState:      string(SessionTransportStateEnded),
			TransportReason:     "codex turn completed",
			RuntimeAgentRunning: true,
			UpdatedAt:           now,
		},
	}); err != nil {
		t.Fatalf("UpsertSessionSnapshot(child) error = %v", err)
	}
	if err := catalog.ReplaceTeamSnapshot(context.Background(), sqlitestore.TeamSnapshotRow{
		Actor: sqlitestore.TeamActorRow{ActorID: "actor_1", ChildSessionID: "child", ParentSessionID: "parent", Name: "reviewer", Status: string(TeamStatusRunning), TurnID: "turn_1", LastEventID: "event_2", LastEventAt: &now, CWD: "/repo", CreatedAt: now, UpdatedAt: now},
		Events: []sqlitestore.TeamEventRow{
			{ActorID: "actor_1", EventID: "event_1", Type: "team.started", ChildSessionID: "child", ParentSessionID: "parent", Status: string(TeamStatusIdle), TS: teamTimestamp(now)},
			{ActorID: "actor_1", EventID: "event_2", Type: "team.prompt", ChildSessionID: "child", ParentSessionID: "parent", TurnID: "turn_1", Message: "review", Status: string(TeamStatusRunning), TS: teamTimestamp(now)},
		},
	}); err != nil {
		t.Fatalf("ReplaceTeamSnapshot() error = %v", err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return now.Add(time.Hour) }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	record, err := rehydrated.lookupSession("child")
	if err != nil {
		t.Fatalf("lookupSession(child) error = %v", err)
	}
	if record.hidden != true {
		t.Fatal("child hidden = false, want true fixture")
	}
	listed, err := rehydrated.ListTeams(context.Background(), ListTeamsRequest{IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListTeams() error = %v", err)
	}
	if listed.TotalCount != 1 || listed.Roots[0].Status != TeamStatusCompleted {
		t.Fatalf("ListTeams() = %+v, want hidden child actor reconciled completed", listed)
	}
}

func TestSpawnTeamCreatesChildSessionAndActor(t *testing.T) {
	s := newStub(config.Load(), time.Now)
	parent, err := s.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/repo"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	res, err := s.SpawnTeam(context.Background(), testSpawnTeamRequest(parent.Session.SessionID, "reviewer", "review", "/repo"))
	if err != nil {
		t.Fatalf("SpawnTeam() error = %v", err)
	}
	if !res.OK || res.ActorID == "" || res.ChildSessionID == "" || res.Actor == nil {
		t.Fatalf("SpawnTeam() = %+v, want actor and child session", res)
	}
	listed, err := s.ListTeams(context.Background(), ListTeamsRequest{})
	if err != nil {
		t.Fatalf("ListTeams() error = %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Roots) != 1 || listed.Roots[0].ChildSessionID != res.ChildSessionID {
		t.Fatalf("ListTeams() = %+v, want spawned actor", listed)
	}
}

func TestSpawnTeamHidesChildSessionFromSessionList(t *testing.T) {
	s := newStub(config.Load(), time.Now)
	parent, err := s.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: "/repo"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	spawned, err := s.SpawnTeam(context.Background(), testSpawnTeamRequest(parent.Session.SessionID, "reviewer", "", "/repo"))
	if err != nil {
		t.Fatalf("SpawnTeam() error = %v", err)
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

func TestCloseTeamKillsChildRuntimeWithoutDeletingHistory(t *testing.T) {
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
	spawned, err := s.SpawnTeam(context.Background(), testSpawnTeamRequest(parent.Session.SessionID, "reviewer", "", "/repo"))
	if err != nil {
		t.Fatalf("SpawnTeam() error = %v", err)
	}
	if len(handles) != 2 {
		t.Fatalf("runtime starts = %d, want parent and child", len(handles))
	}
	if _, err := s.CloseTeam(context.Background(), CloseTeamRequest{ActorID: spawned.ActorID}); err != nil {
		t.Fatalf("CloseTeam() error = %v", err)
	}
	if handles[0].KillCalls() != 0 || handles[1].KillCalls() != 1 {
		t.Fatalf("kill calls = parent %d child %d, want 0 and 1", handles[0].KillCalls(), handles[1].KillCalls())
	}
	listed, err := s.ListTeams(context.Background(), ListTeamsRequest{IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListTeams() error = %v", err)
	}
	if listed.TotalCount != 1 || listed.Roots[0].Status != TeamStatusClosed {
		t.Fatalf("ListTeams(include closed) = %+v, want closed actor history", listed)
	}
}

func TestSpawnTeamAcceptsExternalParentSessionID(t *testing.T) {
	s := newStub(config.Load(), time.Now)
	spawned, err := s.SpawnTeam(context.Background(), testSpawnTeamRequest("external_parent", "reviewer", "", "/repo"))
	if err != nil {
		t.Fatalf("SpawnTeam() error = %v", err)
	}
	if spawned.Actor == nil || spawned.Actor.ParentSessionID != "external_parent" {
		t.Fatalf("SpawnTeam() = %+v, want actor linked to external parent", spawned)
	}
}

func TestTeamRegistryActiveNameConflictAllowsClosedRecreate(t *testing.T) {
	r := newTeamRegistry(time.Now)
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

func TestTeamStatusJSONSerialization(t *testing.T) {
	statuses := []TeamStatus{TeamStatusRunning, TeamStatusIdle, TeamStatusWaitingForParent, TeamStatusCompleted, TeamStatusFailed, TeamStatusClosed, TeamStatusUnknown}
	for _, status := range statuses {
		encoded, err := json.Marshal(TeamNode{ActorID: "actor", Status: status})
		if err != nil {
			t.Fatalf("Marshal(%q) error = %v", status, err)
		}
		if !strings.Contains(string(encoded), `"status":"`+string(status)+`"`) {
			t.Fatalf("Marshal(%q) = %s", status, encoded)
		}
	}
}

func TestTeamAskParentAbortReturnsTerminalResult(t *testing.T) {
	r := newTeamRegistry(time.Now)
	actor, err := r.spawn("parent", "", "child", "reviewer", "review", "pi", "", "/repo")
	if err != nil {
		t.Fatalf("spawn error = %v", err)
	}
	qid, err := r.askParent(actor.ActorID, "turn_1", "Continue?", "")
	if err != nil {
		t.Fatalf("askParent error = %v", err)
	}
	answerCh := make(chan teamParentAnswer, 1)
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

func TestTeamRegistryTreeAndClosedFilter(t *testing.T) {
	r := newTeamRegistry(time.Now)
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
	if got := countTeamNodes(visible); got != 2 {
		t.Fatalf("countTeamNodes(visible) = %d, want 2", got)
	}
	if got := countNonLeafTeamNodes(visible); got != 1 {
		t.Fatalf("countNonLeafTeamNodes(visible) = %d, want 1", got)
	}
	withClosed := r.snapshot(true)
	if got := countTeamNodes(withClosed); got != 3 {
		t.Fatalf("countTeamNodes(withClosed) = %d, want 3", got)
	}
}

func TestTeamAskParentAnswerAndReplay(t *testing.T) {
	r := newTeamRegistry(time.Now)
	actor, err := r.spawn("parent", "", "child", "reviewer", "review", "pi", "", "/repo")
	if err != nil {
		t.Fatalf("spawn error = %v", err)
	}
	qid, err := r.askParent(actor.ActorID, "turn_1", "Use A or B?", "context")
	if err != nil {
		t.Fatalf("askParent error = %v", err)
	}
	answerCh := make(chan teamParentAnswer, 1)
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
