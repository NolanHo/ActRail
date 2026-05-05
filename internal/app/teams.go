package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/domain/session"
)

type TeamStatus string

const (
	TeamStatusRunning          TeamStatus = "running"
	TeamStatusIdle             TeamStatus = "idle"
	TeamStatusWaitingForParent TeamStatus = "waiting_for_parent"
	TeamStatusCompleted        TeamStatus = "completed"
	TeamStatusFailed           TeamStatus = "failed"
	TeamStatusClosed           TeamStatus = "closed"
	TeamStatusUnknown          TeamStatus = "unknown"
)

type ListTeamsRequest struct {
	IncludeClosed bool
}

type ListTeamsResponse struct {
	OK           bool       `json:"ok"`
	Roots        []TeamNode `json:"roots"`
	TotalCount   int        `json:"total_count"`
	NonLeafCount int        `json:"non_leaf_count"`
}

type TeamNode struct {
	ActorID         string          `json:"actor_id"`
	ChildSessionID  string          `json:"child_session_id"`
	ParentActorID   string          `json:"parent_actor_id,omitempty"`
	ParentSessionID string          `json:"parent_session_id"`
	Name            string          `json:"name"`
	Role            string          `json:"role,omitempty"`
	Status          TeamStatus      `json:"status"`
	TurnID          string          `json:"turn_id,omitempty"`
	Question        *TeamQuestion   `json:"question,omitempty"`
	LastEventID     string          `json:"last_event_id,omitempty"`
	LastEventTS     float64         `json:"last_event_ts,omitempty"`
	Model           string          `json:"model,omitempty"`
	CWD             string          `json:"cwd,omitempty"`
	Children        []TeamNode      `json:"children,omitempty"`
	Messages        []TeamThreadMsg `json:"messages,omitempty"`
}

type TeamQuestion struct {
	QuestionID string  `json:"question_id"`
	TurnID     string  `json:"turn_id,omitempty"`
	Question   string  `json:"question"`
	Context    string  `json:"context,omitempty"`
	CreatedTS  float64 `json:"created_ts,omitempty"`
}

type TeamThreadMsg struct {
	MessageID string  `json:"message_id"`
	Kind      string  `json:"kind"`
	Label     string  `json:"label"`
	Body      string  `json:"body"`
	TS        float64 `json:"ts,omitempty"`
	Meta      string  `json:"meta,omitempty"`
}

type TeamStoredEvent struct {
	EventID         string     `json:"event_id"`
	Type            string     `json:"type"`
	ActorID         string     `json:"actor_id"`
	ChildSessionID  string     `json:"child_session_id"`
	ParentActorID   string     `json:"parent_actor_id,omitempty"`
	ParentSessionID string     `json:"parent_session_id"`
	TurnID          string     `json:"turn_id,omitempty"`
	QuestionID      string     `json:"question_id,omitempty"`
	Message         string     `json:"message,omitempty"`
	Status          TeamStatus `json:"status,omitempty"`
	TS              float64    `json:"ts"`
}

type TeamEvent struct {
	Protocol        string     `json:"protocol"`
	EventID         string     `json:"eventId"`
	Type            string     `json:"type"`
	ActorID         string     `json:"actorId"`
	ChildSessionID  string     `json:"childSessionId"`
	ParentSessionID string     `json:"parentSessionId"`
	Timestamp       int64      `json:"timestamp"`
	Name            string     `json:"name,omitempty"`
	Role            string     `json:"role,omitempty"`
	TurnID          string     `json:"turnId,omitempty"`
	QuestionID      string     `json:"questionId,omitempty"`
	Question        string     `json:"question,omitempty"`
	Context         string     `json:"context,omitempty"`
	Blocking        bool       `json:"blocking,omitempty"`
	Delta           string     `json:"delta,omitempty"`
	ToolCallID      string     `json:"toolCallId,omitempty"`
	ToolName        string     `json:"toolName,omitempty"`
	IsError         bool       `json:"isError,omitempty"`
	Status          TeamStatus `json:"status,omitempty"`
	Output          string     `json:"output,omitempty"`
	Error           string     `json:"error,omitempty"`
	ExitCode        *int       `json:"exitCode,omitempty"`
	Reason          string     `json:"reason,omitempty"`
}

type SpawnTeamRequest struct {
	ParentSessionID string  `json:"parent_session_id"`
	ParentActorID   string  `json:"parent_actor_id"`
	Name            string  `json:"name"`
	Role            string  `json:"role"`
	AgentBackend    string  `json:"agent_backend"`
	CWD             string  `json:"cwd"`
	Model           *string `json:"model"`
	Provider        *string `json:"provider"`
	InitialPrompt   string  `json:"initial_prompt"`
	PIAgentGRPC     *bool   `json:"pi_agent_grpc"`
}

type PromptTeamRequest struct {
	ActorID string `json:"actor_id"`
	Prompt  string `json:"prompt"`
}

type FollowupTeamRequest struct {
	ActorID string `json:"actor_id"`
	Prompt  string `json:"prompt"`
}

type SendTeamRequest struct {
	ActorID string `json:"actor_id"`
	Message string `json:"message"`
}

type AskParentRequest struct {
	ActorID    string `json:"actor_id"`
	TurnID     string `json:"turn_id"`
	QuestionID string `json:"question_id"`
	Question   string `json:"question"`
	Context    string `json:"context"`
}

type AskParentResponse struct {
	OK         bool   `json:"ok"`
	ActorID    string `json:"actor_id"`
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer,omitempty"`
	Terminal   string `json:"terminal,omitempty"`
}

type AnswerTeamRequest struct {
	ActorID    string `json:"actor_id"`
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
}

type AbortTeamRequest struct {
	ActorID string `json:"actor_id"`
	TurnID  string `json:"turn_id"`
}

type CloseTeamRequest struct {
	ActorID string `json:"actor_id"`
}

type StatusTeamRequest struct {
	ActorID string `json:"actor_id"`
}

type TeamEventsRequest struct {
	ActorID      string `json:"actor_id"`
	AfterEventID string `json:"after_event_id"`
}

type TeamCommandResponse struct {
	OK             bool       `json:"ok"`
	Actor          *TeamNode  `json:"actor,omitempty"`
	ActorID        string     `json:"actor_id,omitempty"`
	ChildSessionID string     `json:"child_session_id,omitempty"`
	TurnID         string     `json:"turn_id,omitempty"`
	QuestionID     string     `json:"question_id,omitempty"`
	Status         TeamStatus `json:"status,omitempty"`
}

type TeamDeliveryResponse struct {
	OK       bool                      `json:"ok"`
	ActorID  string                    `json:"actor_id"`
	TurnID   string                    `json:"turn_id,omitempty"`
	Delivery string                    `json:"delivery"`
	Queue    SessionQueueSnapshot      `json:"queue"`
	UI       *SessionUIRequestSnapshot `json:"ui_request,omitempty"`
}

type TeamEventsResponse struct {
	OK     bool              `json:"ok"`
	Events []TeamStoredEvent `json:"events"`
}

type teamStore interface {
	ReplaceTeamSnapshot(context.Context, sqlitestore.TeamSnapshotRow) error
	ListTeamSnapshots(context.Context) ([]sqlitestore.TeamSnapshotRow, error)
}

type teamRegistry struct {
	mu           sync.Mutex
	now          func() time.Time
	store        teamStore
	nextActor    int64
	nextTurn     int64
	nextEvent    int64
	nextQuestion int64
	actors       map[string]*teamActor
	order        []string
}

type teamActor struct {
	ActorID         string
	ChildSessionID  session.SessionID
	ParentActorID   string
	ParentSessionID session.SessionID
	Name            string
	Role            string
	Status          TeamStatus
	TurnID          string
	Question        *teamQuestionState
	LastEventID     string
	LastEventTS     time.Time
	Model           string
	CWD             string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Messages        []TeamThreadMsg
	Events          []TeamStoredEvent
}

type teamParentAnswer struct {
	answer   string
	terminal string
}

type teamQuestionState struct {
	TeamQuestion
	answer       chan teamParentAnswer
	done         bool
	answerText   string
	terminalText string
}

func newTeamRegistry(now func() time.Time, stores ...teamStore) *teamRegistry {
	if now == nil {
		now = time.Now
	}
	var store teamStore
	if len(stores) > 0 {
		store = stores[0]
	}
	return &teamRegistry{now: now, store: store, actors: map[string]*teamActor{}}
}

func (s *Stub) ListTeams(_ context.Context, req ListTeamsRequest) (ListTeamsResponse, error) {
	roots := s.teams.snapshot(req.IncludeClosed)
	return ListTeamsResponse{OK: true, Roots: roots, TotalCount: countTeamNodes(roots), NonLeafCount: countNonLeafTeamNodes(roots)}, nil
}

func (s *Stub) SpawnTeam(ctx context.Context, req SpawnTeamRequest) (TeamCommandResponse, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return TeamCommandResponse{}, Invalid("name", "name required")
	}
	parentSessionID, err := s.teams.resolveParentSession(req.ParentSessionID, req.ParentActorID)
	if err != nil {
		return TeamCommandResponse{}, err
	}
	if strings.TrimSpace(req.ParentActorID) == "" {
		parent, ok := s.registry.Lookup(parentSessionID)
		if !ok {
			return TeamCommandResponse{}, NotFound(fmt.Sprintf("parent session %q not found", parentSessionID))
		}
		if parent.hidden {
			return TeamCommandResponse{}, Forbidden("parent_session_id refers to a generated child session; use parent_actor_id")
		}
	}
	if err := s.teams.validateSpawn(parentSessionID, name); err != nil {
		return TeamCommandResponse{}, err
	}
	backend := strings.TrimSpace(req.AgentBackend)
	if backend == "" {
		backend = string(session.BackendPI)
	}
	cwd := strings.TrimSpace(req.CWD)
	if cwd == "" {
		cwd = "."
	}
	title := name
	created, err := s.CreateSession(ctx, CreateSessionRequest{AgentBackend: backend, CWD: cwd, Model: req.Model, Provider: req.Provider, Title: &title, PIAgentGRPC: req.PIAgentGRPC, Hidden: true})
	if err != nil {
		return TeamCommandResponse{}, err
	}
	if created.Session == nil {
		return TeamCommandResponse{}, Conflict("team child session was not created")
	}
	model := ""
	if req.Model != nil {
		model = strings.TrimSpace(*req.Model)
	}
	actor, err := s.teams.spawn(parentSessionID, req.ParentActorID, created.Session.SessionID, name, req.Role, backend, model, cwd)
	if err != nil {
		return TeamCommandResponse{}, err
	}
	if text := strings.TrimSpace(req.InitialPrompt); text != "" {
		if _, err := s.sendTeamText(ctx, actor.ActorID, text, true, true); err != nil {
			s.teams.markFailed(actor.ActorID, err.Error())
			return TeamCommandResponse{}, err
		}
		actor = s.teams.lookupNode(actor.ActorID)
	}
	return TeamCommandResponse{OK: true, Actor: actor, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID, TurnID: actor.TurnID, Status: actor.Status}, nil
}

func (s *Stub) PromptTeam(ctx context.Context, req PromptTeamRequest) (TeamCommandResponse, error) {
	actor, err := s.sendTeamText(ctx, req.ActorID, req.Prompt, true, false)
	if err != nil {
		return TeamCommandResponse{}, err
	}
	return TeamCommandResponse{OK: true, Actor: actor, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID, TurnID: actor.TurnID, Status: actor.Status}, nil
}

func (s *Stub) FollowupTeam(ctx context.Context, req FollowupTeamRequest) (TeamCommandResponse, error) {
	actor, err := s.sendTeamText(ctx, req.ActorID, req.Prompt, true, false)
	if err != nil {
		return TeamCommandResponse{}, err
	}
	return TeamCommandResponse{OK: true, Actor: actor, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID, TurnID: actor.TurnID, Status: actor.Status}, nil
}

func (s *Stub) SendTeam(ctx context.Context, req SendTeamRequest) (TeamDeliveryResponse, error) {
	actor, err := s.sendTeamText(ctx, req.ActorID, req.Message, false, false)
	if err != nil {
		return TeamDeliveryResponse{}, err
	}
	return TeamDeliveryResponse{OK: true, ActorID: actor.ActorID, TurnID: actor.TurnID, Delivery: "live"}, nil
}

func (s *Stub) sendTeamText(ctx context.Context, actorID, text string, newTurn bool, followUp bool) (*TeamNode, error) {
	cleaned := strings.TrimSpace(text)
	if cleaned == "" {
		return nil, Invalid("text", "text required")
	}
	actor, turnID, err := s.teams.beginSend(actorID, cleaned, newTurn)
	if err != nil {
		return nil, err
	}
	if _, err := s.send(ctx, SendRequest{SessionID: actor.ChildSessionID, Text: cleaned}, followUp); err != nil {
		s.teams.markFailed(actorID, err.Error())
		return nil, err
	}
	node := s.teams.lookupNode(actorID)
	if node != nil {
		node.TurnID = turnID
	}
	return node, nil
}

func (s *Stub) AskParent(ctx context.Context, req AskParentRequest) (AskParentResponse, error) {
	questionID, err := s.teams.askParent(req.ActorID, req.TurnID, req.Question, req.Context)
	if err != nil {
		return AskParentResponse{}, err
	}
	answer, err := s.teams.waitForAnswer(ctx, req.ActorID, questionID)
	if err != nil {
		return AskParentResponse{}, err
	}
	return AskParentResponse{OK: true, ActorID: req.ActorID, QuestionID: questionID, Answer: answer.answer, Terminal: answer.terminal}, nil
}

func (s *Stub) ResumeAskParent(ctx context.Context, req AskParentRequest) (AskParentResponse, error) {
	questionID := strings.TrimSpace(req.QuestionID)
	if questionID == "" {
		questionID = strings.TrimSpace(req.Question)
	}
	if questionID == "" {
		return AskParentResponse{}, Invalid("question_id", "question_id required")
	}
	answer, err := s.teams.waitForAnswer(ctx, req.ActorID, questionID)
	if err != nil {
		return AskParentResponse{}, err
	}
	return AskParentResponse{OK: true, ActorID: req.ActorID, QuestionID: questionID, Answer: answer.answer, Terminal: answer.terminal}, nil
}

func (s *Stub) AnswerTeam(_ context.Context, req AnswerTeamRequest) (TeamCommandResponse, error) {
	actor, err := s.teams.answer(req.ActorID, req.QuestionID, req.Answer)
	if err != nil {
		return TeamCommandResponse{}, err
	}
	return TeamCommandResponse{OK: true, Actor: actor, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID, TurnID: actor.TurnID, QuestionID: req.QuestionID, Status: actor.Status}, nil
}

func (s *Stub) AbortTeam(ctx context.Context, req AbortTeamRequest) (TeamCommandResponse, error) {
	actor, err := s.teams.get(req.ActorID)
	if err != nil {
		return TeamCommandResponse{}, err
	}
	if _, err := s.Interrupt(ctx, InterruptRequest{SessionID: actor.ChildSessionID}); err != nil {
		s.teams.markFailed(req.ActorID, err.Error())
		return TeamCommandResponse{}, err
	}
	node, err := s.teams.abort(req.ActorID, req.TurnID)
	if err != nil {
		return TeamCommandResponse{}, err
	}
	return TeamCommandResponse{OK: true, Actor: node, ActorID: node.ActorID, ChildSessionID: node.ChildSessionID, TurnID: node.TurnID, Status: node.Status}, nil
}

func (s *Stub) CloseTeam(ctx context.Context, req CloseTeamRequest) (TeamCommandResponse, error) {
	actor, err := s.teams.get(req.ActorID)
	if err != nil {
		return TeamCommandResponse{}, err
	}
	record, err := s.lookupSession(actor.ChildSessionID)
	if err != nil {
		return TeamCommandResponse{}, err
	}
	if err := record.runtime.Kill(ctx); err != nil {
		s.teams.markFailed(req.ActorID, err.Error())
		return TeamCommandResponse{}, err
	}
	if err := s.setRuntimeAgentRunning(actor.ChildSessionID, false); err != nil {
		return TeamCommandResponse{}, err
	}
	s.helpers.Remove(actor.ChildSessionID)
	if err := s.helperBindings.Delete(actor.ChildSessionID); err != nil {
		return TeamCommandResponse{}, err
	}
	node, err := s.teams.close(req.ActorID)
	if err != nil {
		return TeamCommandResponse{}, err
	}
	return TeamCommandResponse{OK: true, Actor: node, ActorID: node.ActorID, ChildSessionID: node.ChildSessionID, TurnID: node.TurnID, Status: node.Status}, nil
}

func (s *Stub) StatusTeam(_ context.Context, req StatusTeamRequest) (TeamCommandResponse, error) {
	actor := s.teams.lookupNode(req.ActorID)
	if actor == nil {
		return TeamCommandResponse{}, NotFound(fmt.Sprintf("team actor %q not found", strings.TrimSpace(req.ActorID)))
	}
	return TeamCommandResponse{OK: true, Actor: actor, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID, TurnID: actor.TurnID, Status: actor.Status}, nil
}

func (s *Stub) TeamEvents(_ context.Context, req TeamEventsRequest) (TeamEventsResponse, error) {
	events, err := s.teams.eventsAfter(req.ActorID, req.AfterEventID)
	if err != nil {
		return TeamEventsResponse{}, err
	}
	return TeamEventsResponse{OK: true, Events: events}, nil
}

func (r *teamRegistry) resolveParentSession(rawSession, rawActor string) (session.SessionID, error) {
	parentSession := session.SessionID(strings.TrimSpace(rawSession))
	parentActor := strings.TrimSpace(rawActor)
	if parentSession != "" {
		return parentSession, nil
	}
	if parentActor == "" {
		return "", Invalid("parent_session_id", "parent_session_id or parent_actor_id required")
	}
	actor, err := r.get(parentActor)
	if err != nil {
		return "", err
	}
	return actor.ChildSessionID, nil
}

func (r *teamRegistry) validateSpawn(parentSession session.SessionID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.validateSpawnLocked(parentSession, name)
}

func (r *teamRegistry) validateSpawnLocked(parentSession session.SessionID, name string) error {
	for _, actor := range r.actors {
		if actor.ParentSessionID == parentSession && actor.Name == name && actor.Status != TeamStatusClosed {
			return Conflict(fmt.Sprintf("team %q already exists for parent %q", name, parentSession))
		}
	}
	return nil
}

func (r *teamRegistry) spawn(parentSession session.SessionID, parentActorID, childSessionID, name, role, backend, model, cwd string) (*TeamNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validateSpawnLocked(parentSession, name); err != nil {
		return nil, err
	}
	r.nextActor++
	actorID := fmt.Sprintf("actor_%d", r.nextActor)
	now := r.now()
	actor := &teamActor{ActorID: actorID, ChildSessionID: session.SessionID(childSessionID), ParentActorID: strings.TrimSpace(parentActorID), ParentSessionID: parentSession, Name: name, Role: strings.TrimSpace(role), Status: TeamStatusIdle, Model: model, CWD: cwd, LastEventTS: now, CreatedAt: now, UpdatedAt: now}
	r.actors[actorID] = actor
	r.order = append(r.order, actorID)
	r.appendEventLocked(actor, "team.started", "", "", "", actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		delete(r.actors, actorID)
		r.order = r.order[:len(r.order)-1]
		return nil, err
	}
	node := r.nodeLocked(actor)
	return &node, nil
}

func (r *teamRegistry) beginSend(actorID, text string, newTurn bool) (*teamActor, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil, "", err
	}
	if actor.Status == TeamStatusClosed {
		return nil, "", Conflict("team is closed")
	}
	if newTurn || actor.TurnID == "" {
		r.nextTurn++
		actor.TurnID = fmt.Sprintf("turn_%d", r.nextTurn)
		r.appendEventLocked(actor, "team.turn_started", actor.TurnID, "", "", TeamStatusRunning)
	}
	actor.Status = TeamStatusRunning
	actor.Messages = append(actor.Messages, TeamThreadMsg{MessageID: actor.LastEventID + ":prompt", Kind: "leader", Label: "parent", Body: text, TS: teamTimestamp(r.now())})
	r.appendEventLocked(actor, "team.prompt", actor.TurnID, "", text, actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		return nil, "", err
	}
	return cloneTeamActor(actor), actor.TurnID, nil
}

func (r *teamRegistry) askParent(actorID, turnID, question, contextText string) (string, error) {
	cleaned := strings.TrimSpace(question)
	if cleaned == "" {
		return "", Invalid("question", "question required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return "", err
	}
	if actor.Question != nil && !actor.Question.done {
		return "", Conflict("team already has a pending question")
	}
	if turnID == "" {
		turnID = actor.TurnID
	}
	r.nextQuestion++
	questionID := fmt.Sprintf("question_%d", r.nextQuestion)
	q := &teamQuestionState{TeamQuestion: TeamQuestion{QuestionID: questionID, TurnID: turnID, Question: cleaned, Context: strings.TrimSpace(contextText), CreatedTS: teamTimestamp(r.now())}, answer: make(chan teamParentAnswer, 1)}
	actor.Question = q
	actor.Status = TeamStatusWaitingForParent
	actor.Messages = append(actor.Messages, TeamThreadMsg{MessageID: questionID, Kind: "member", Label: actor.Name, Body: cleaned, TS: q.CreatedTS, Meta: "ask_parent"})
	r.appendEventLocked(actor, "team.question", turnID, questionID, cleaned, actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		return "", err
	}
	return questionID, nil
}

func (r *teamRegistry) waitForAnswer(ctx context.Context, actorID, questionID string) (teamParentAnswer, error) {
	r.mu.Lock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		r.mu.Unlock()
		return teamParentAnswer{}, err
	}
	if actor.Question == nil || actor.Question.QuestionID != questionID {
		r.mu.Unlock()
		return teamParentAnswer{}, NotFound("team question not found")
	}
	if actor.Question.done {
		answer := teamParentAnswer{answer: actor.Question.answerText, terminal: actor.Question.terminalText}
		r.mu.Unlock()
		return answer, nil
	}
	ch := actor.Question.answer
	r.mu.Unlock()
	select {
	case answer := <-ch:
		return answer, nil
	case <-ctx.Done():
		return teamParentAnswer{}, ctx.Err()
	}
}

func (r *teamRegistry) answer(actorID, questionID, answer string) (*TeamNode, error) {
	cleaned := strings.TrimSpace(answer)
	if cleaned == "" {
		return nil, Invalid("answer", "answer required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil, err
	}
	if actor.Question == nil || actor.Question.QuestionID != questionID || actor.Question.done {
		return nil, NotFound("team pending question not found")
	}
	actor.Question.done = true
	actor.Question.answerText = cleaned
	actor.Question.answer <- teamParentAnswer{answer: cleaned}
	actor.Status = TeamStatusRunning
	actor.Messages = append(actor.Messages, TeamThreadMsg{MessageID: questionID + ":answer", Kind: "leader", Label: "parent", Body: cleaned, TS: teamTimestamp(r.now()), Meta: "answer"})
	r.appendEventLocked(actor, "team.answer_accepted", actor.TurnID, questionID, cleaned, actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		return nil, err
	}
	node := r.nodeLocked(actor)
	return &node, nil
}

func (r *teamRegistry) abort(actorID, turnID string) (*TeamNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil, err
	}
	if turnID != "" && actor.TurnID != "" && actor.TurnID != turnID {
		return nil, Conflict("team turn id mismatch")
	}
	actor.Status = TeamStatusIdle
	if actor.Question != nil && !actor.Question.done {
		actor.Question.done = true
		actor.Question.terminalText = "aborted"
		actor.Question.answer <- teamParentAnswer{terminal: "aborted"}
	}
	r.appendEventLocked(actor, "team.aborted", actor.TurnID, "", "", actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		return nil, err
	}
	node := r.nodeLocked(actor)
	return &node, nil
}

func (r *teamRegistry) close(actorID string) (*TeamNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil, err
	}
	actor.Status = TeamStatusClosed
	if actor.Question != nil && !actor.Question.done {
		actor.Question.done = true
		actor.Question.terminalText = "closed"
		actor.Question.answer <- teamParentAnswer{terminal: "closed"}
	}
	r.appendEventLocked(actor, "team.closed", actor.TurnID, "", "", actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		return nil, err
	}
	node := r.nodeLocked(actor)
	return &node, nil
}

func (r *teamRegistry) markFailed(actorID, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return
	}
	actor.Status = TeamStatusFailed
	if actor.Question != nil && !actor.Question.done {
		actor.Question.done = true
		actor.Question.terminalText = "failed"
		actor.Question.answer <- teamParentAnswer{terminal: "failed"}
	}
	r.appendEventLocked(actor, "team.error", actor.TurnID, "", message, actor.Status)
	_ = r.persistActorLocked(actor)
}

func (r *teamRegistry) appendTeamEventForSession(sessionID session.SessionID, typ, turnID, questionID, message string, status TeamStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, actor := range r.actors {
		if actor.ChildSessionID != sessionID {
			continue
		}
		if status != "" {
			actor.Status = status
		}
		r.appendEventLocked(actor, typ, turnID, questionID, message, actor.Status)
		_ = r.persistActorLocked(actor)
		return
	}
}

func (r *teamRegistry) snapshot(includeClosed bool) []TeamNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	byParent := map[string][]TeamNode{}
	roots := []TeamNode{}
	for _, actorID := range r.order {
		actor := r.actors[actorID]
		if !includeClosed && actor.Status == TeamStatusClosed {
			continue
		}
		node := r.nodeLocked(actor)
		if actor.ParentActorID == "" {
			roots = append(roots, node)
		} else {
			byParent[actor.ParentActorID] = append(byParent[actor.ParentActorID], node)
		}
	}
	var attach func(*TeamNode)
	attach = func(node *TeamNode) {
		children := byParent[node.ActorID]
		node.Children = children
		for i := range node.Children {
			attach(&node.Children[i])
		}
	}
	for i := range roots {
		attach(&roots[i])
	}
	return roots
}

func (r *teamRegistry) lookupNode(actorID string) *TeamNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil
	}
	node := r.nodeLocked(actor)
	return &node
}

func (r *teamRegistry) eventsAfter(actorID, after string) ([]TeamStoredEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil, err
	}
	start := 0
	if after != "" {
		found := false
		for i, event := range actor.Events {
			if event.EventID == after {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, NotFound("team event cursor not found")
		}
	}
	return append([]TeamStoredEvent(nil), actor.Events[start:]...), nil
}

func (r *teamRegistry) get(actorID string) (*teamActor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil, err
	}
	return cloneTeamActor(actor), nil
}

func (r *teamRegistry) getLocked(actorID string) (*teamActor, error) {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return nil, Invalid("actor_id", "actor_id required")
	}
	actor, ok := r.actors[actorID]
	if !ok {
		return nil, NotFound(fmt.Sprintf("team actor %q not found", actorID))
	}
	return actor, nil
}

func (r *teamRegistry) appendEventLocked(actor *teamActor, typ, turnID, questionID, message string, status TeamStatus) {
	r.nextEvent++
	now := r.now()
	event := TeamStoredEvent{EventID: fmt.Sprintf("event_%d", r.nextEvent), Type: typ, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID.String(), ParentActorID: actor.ParentActorID, ParentSessionID: actor.ParentSessionID.String(), TurnID: turnID, QuestionID: questionID, Message: message, Status: status, TS: teamTimestamp(now)}
	actor.LastEventID = event.EventID
	actor.LastEventTS = now
	actor.UpdatedAt = now
	actor.Events = append(actor.Events, event)
}

func (r *teamRegistry) persistActorLocked(actor *teamActor) error {
	if r.store == nil {
		return nil
	}
	return r.store.ReplaceTeamSnapshot(context.Background(), durableTeamSnapshotFromActor(actor))
}

func (r *teamRegistry) rehydrate(snapshots []sqlitestore.TeamSnapshotRow) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actors = map[string]*teamActor{}
	r.order = []string{}
	r.nextActor = 0
	r.nextTurn = 0
	r.nextEvent = 0
	r.nextQuestion = 0
	for _, snapshot := range snapshots {
		actor := teamActorFromDurableSnapshot(snapshot)
		r.actors[actor.ActorID] = actor
		r.order = append(r.order, actor.ActorID)
		r.observeActorCountersLocked(actor)
	}
	return nil
}

func (r *teamRegistry) observeActorCountersLocked(actor *teamActor) {
	r.nextActor = maxInt64(r.nextActor, parseCounterSuffix(actor.ActorID, "actor_"))
	r.nextTurn = maxInt64(r.nextTurn, parseCounterSuffix(actor.TurnID, "turn_"))
	if actor.Question != nil {
		r.nextQuestion = maxInt64(r.nextQuestion, parseCounterSuffix(actor.Question.QuestionID, "question_"))
	}
	for _, event := range actor.Events {
		r.nextEvent = maxInt64(r.nextEvent, parseCounterSuffix(event.EventID, "event_"))
		r.nextTurn = maxInt64(r.nextTurn, parseCounterSuffix(event.TurnID, "turn_"))
		r.nextQuestion = maxInt64(r.nextQuestion, parseCounterSuffix(event.QuestionID, "question_"))
	}
}

func (r *teamRegistry) nodeLocked(actor *teamActor) TeamNode {
	var question *TeamQuestion
	if actor.Question != nil && !actor.Question.done {
		q := actor.Question.TeamQuestion
		question = &q
	}
	return TeamNode{ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID.String(), ParentActorID: actor.ParentActorID, ParentSessionID: actor.ParentSessionID.String(), Name: actor.Name, Role: actor.Role, Status: actor.Status, TurnID: actor.TurnID, Question: question, LastEventID: actor.LastEventID, LastEventTS: teamTimestamp(actor.LastEventTS), Model: actor.Model, CWD: actor.CWD, Messages: append([]TeamThreadMsg(nil), actor.Messages...)}
}

func cloneTeamActor(actor *teamActor) *teamActor {
	cloned := *actor
	cloned.Messages = append([]TeamThreadMsg(nil), actor.Messages...)
	cloned.Events = append([]TeamStoredEvent(nil), actor.Events...)
	return &cloned
}

func durableTeamSnapshotFromActor(actor *teamActor) sqlitestore.TeamSnapshotRow {
	var lastEventAt *time.Time
	if !actor.LastEventTS.IsZero() {
		lastEventAt = copyTimePtr(&actor.LastEventTS)
	}
	row := sqlitestore.TeamActorRow{ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID.String(), ParentActorID: actor.ParentActorID, ParentSessionID: actor.ParentSessionID.String(), Name: actor.Name, Role: actor.Role, Status: string(actor.Status), TurnID: actor.TurnID, LastEventID: actor.LastEventID, LastEventAt: lastEventAt, Model: actor.Model, CWD: actor.CWD, CreatedAt: actor.CreatedAt, UpdatedAt: actor.UpdatedAt}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = actor.LastEventTS
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = actor.LastEventTS
	}
	if actor.Question != nil {
		row.QuestionID = actor.Question.QuestionID
		row.QuestionTurnID = actor.Question.TurnID
		row.Question = actor.Question.Question
		row.QuestionContext = actor.Question.Context
		row.QuestionCreatedTS = actor.Question.CreatedTS
		row.QuestionDone = actor.Question.done
		row.QuestionAnswer = actor.Question.answerText
		row.QuestionTerminal = actor.Question.terminalText
	}
	events := make([]sqlitestore.TeamEventRow, 0, len(actor.Events))
	for i, event := range actor.Events {
		events = append(events, sqlitestore.TeamEventRow{ActorID: event.ActorID, Ordinal: i + 1, EventID: event.EventID, Type: event.Type, ChildSessionID: event.ChildSessionID, ParentActorID: event.ParentActorID, ParentSessionID: event.ParentSessionID, TurnID: event.TurnID, QuestionID: event.QuestionID, Message: event.Message, Status: string(event.Status), TS: event.TS})
	}
	messages := make([]sqlitestore.TeamMessageRow, 0, len(actor.Messages))
	for i, msg := range actor.Messages {
		messages = append(messages, sqlitestore.TeamMessageRow{ActorID: actor.ActorID, Ordinal: i + 1, MessageID: msg.MessageID, Kind: msg.Kind, Label: msg.Label, Body: msg.Body, TS: msg.TS, Meta: msg.Meta})
	}
	return sqlitestore.TeamSnapshotRow{Actor: row, Events: events, Messages: messages}
}

func teamActorFromDurableSnapshot(snapshot sqlitestore.TeamSnapshotRow) *teamActor {
	row := snapshot.Actor
	actor := &teamActor{ActorID: row.ActorID, ChildSessionID: session.SessionID(row.ChildSessionID), ParentActorID: row.ParentActorID, ParentSessionID: session.SessionID(row.ParentSessionID), Name: row.Name, Role: row.Role, Status: TeamStatus(row.Status), TurnID: row.TurnID, LastEventID: row.LastEventID, Model: row.Model, CWD: row.CWD, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.LastEventAt != nil {
		actor.LastEventTS = *row.LastEventAt
	}
	if row.QuestionID != "" {
		actor.Question = &teamQuestionState{TeamQuestion: TeamQuestion{QuestionID: row.QuestionID, TurnID: row.QuestionTurnID, Question: row.Question, Context: row.QuestionContext, CreatedTS: row.QuestionCreatedTS}, answer: make(chan teamParentAnswer, 1), done: row.QuestionDone, answerText: row.QuestionAnswer, terminalText: row.QuestionTerminal}
	}
	actor.Events = make([]TeamStoredEvent, 0, len(snapshot.Events))
	for _, event := range snapshot.Events {
		actor.Events = append(actor.Events, TeamStoredEvent{EventID: event.EventID, Type: event.Type, ActorID: event.ActorID, ChildSessionID: event.ChildSessionID, ParentActorID: event.ParentActorID, ParentSessionID: event.ParentSessionID, TurnID: event.TurnID, QuestionID: event.QuestionID, Message: event.Message, Status: TeamStatus(event.Status), TS: event.TS})
	}
	actor.Messages = make([]TeamThreadMsg, 0, len(snapshot.Messages))
	for _, msg := range snapshot.Messages {
		actor.Messages = append(actor.Messages, TeamThreadMsg{MessageID: msg.MessageID, Kind: msg.Kind, Label: msg.Label, Body: msg.Body, TS: msg.TS, Meta: msg.Meta})
	}
	return actor
}

func parseCounterSuffix(raw, prefix string) int64 {
	if !strings.HasPrefix(raw, prefix) {
		return 0
	}
	var n int64
	_, _ = fmt.Sscanf(strings.TrimPrefix(raw, prefix), "%d", &n)
	return n
}

func maxInt64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

func countTeamNodes(nodes []TeamNode) int {
	total := 0
	for _, node := range nodes {
		total++
		total += countTeamNodes(node.Children)
	}
	return total
}

func countNonLeafTeamNodes(nodes []TeamNode) int {
	total := 0
	for _, node := range nodes {
		if len(node.Children) > 0 {
			total++
		}
		total += countNonLeafTeamNodes(node.Children)
	}
	return total
}

func teamTimestamp(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return timestampSeconds(t)
}

func TeamEventsFromStoredEvents(events []TeamStoredEvent, actor *TeamNode) []TeamEvent {
	out := make([]TeamEvent, 0, len(events))
	for _, event := range events {
		out = append(out, TeamEventFromStoredEvent(event, actor))
	}
	return out
}

func TeamEventFromStoredEvent(event TeamStoredEvent, actor *TeamNode) TeamEvent {
	teamType := mapTeamEventType(event.Type)
	team := TeamEvent{
		Protocol:        "pi.team.v1",
		EventID:         event.EventID,
		Type:            teamType,
		ActorID:         event.ActorID,
		ChildSessionID:  event.ChildSessionID,
		ParentSessionID: event.ParentSessionID,
		Timestamp:       int64(event.TS * 1000),
		TurnID:          event.TurnID,
		QuestionID:      event.QuestionID,
	}
	if actor != nil {
		team.Name = actor.Name
		team.Role = actor.Role
	}
	switch teamType {
	case "team.started", "team.status":
		team.Status = event.Status
	case "team.question":
		team.Question = event.Message
		team.Blocking = true
		if actor != nil && actor.Question != nil && actor.Question.QuestionID == event.QuestionID {
			team.Context = actor.Question.Context
		}
	case "team.answer_accepted":
	case "team.output_delta":
		team.Delta = event.Message
	case "team.tool_call", "team.tool_result":
		team.ToolCallID = event.QuestionID
		team.ToolName = event.Message
		if team.ToolName == "" {
			team.ToolName = teamType
		}
	case "team.turn_result":
		team.Status = event.Status
		team.Output = event.Message
		if event.Status == TeamStatusFailed {
			team.Error = event.Message
			code := 1
			team.ExitCode = &code
		} else if event.Status == TeamStatusCompleted {
			code := 0
			team.ExitCode = &code
		}
	case "team.error":
		team.Error = event.Message
	case "team.aborted", "team.closed":
		team.Reason = event.Message
	}
	return team
}

func mapTeamEventType(typ string) string {
	switch typ {
	case "team.started":
		return "team.started"
	case "team.turn_started":
		return "team.turn_started"
	case "team.prompt":
		return "team.status"
	case "team.question":
		return "team.question"
	case "team.answer_accepted":
		return "team.answer_accepted"
	case "team.output_delta":
		return "team.output_delta"
	case "team.tool_call":
		return "team.tool_call"
	case "team.tool_result":
		return "team.tool_result"
	case "team.turn_result":
		return "team.turn_result"
	case "team.error":
		return "team.error"
	case "team.aborted":
		return "team.aborted"
	case "team.closed":
		return "team.closed"
	default:
		return "team.status"
	}
}
