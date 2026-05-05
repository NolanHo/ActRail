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

type SubagentStatus string

const (
	SubagentStatusRunning          SubagentStatus = "running"
	SubagentStatusIdle             SubagentStatus = "idle"
	SubagentStatusWaitingForParent SubagentStatus = "waiting_for_parent"
	SubagentStatusCompleted        SubagentStatus = "completed"
	SubagentStatusFailed           SubagentStatus = "failed"
	SubagentStatusClosed           SubagentStatus = "closed"
	SubagentStatusUnknown          SubagentStatus = "unknown"
)

type ListSubagentsRequest struct {
	IncludeClosed bool
}

type ListSubagentsResponse struct {
	OK           bool           `json:"ok"`
	Roots        []SubagentNode `json:"roots"`
	TotalCount   int            `json:"total_count"`
	NonLeafCount int            `json:"non_leaf_count"`
}

type SubagentNode struct {
	ActorID         string              `json:"actor_id"`
	ChildSessionID  string              `json:"child_session_id"`
	ParentActorID   string              `json:"parent_actor_id,omitempty"`
	ParentSessionID string              `json:"parent_session_id"`
	Name            string              `json:"name"`
	Role            string              `json:"role,omitempty"`
	Status          SubagentStatus      `json:"status"`
	TurnID          string              `json:"turn_id,omitempty"`
	Question        *SubagentQuestion   `json:"question,omitempty"`
	LastEventID     string              `json:"last_event_id,omitempty"`
	LastEventTS     float64             `json:"last_event_ts,omitempty"`
	Model           string              `json:"model,omitempty"`
	CWD             string              `json:"cwd,omitempty"`
	Children        []SubagentNode      `json:"children,omitempty"`
	Messages        []SubagentThreadMsg `json:"messages,omitempty"`
}

type SubagentQuestion struct {
	QuestionID string  `json:"question_id"`
	TurnID     string  `json:"turn_id,omitempty"`
	Question   string  `json:"question"`
	Context    string  `json:"context,omitempty"`
	CreatedTS  float64 `json:"created_ts,omitempty"`
}

type SubagentThreadMsg struct {
	MessageID string  `json:"message_id"`
	Kind      string  `json:"kind"`
	Label     string  `json:"label"`
	Body      string  `json:"body"`
	TS        float64 `json:"ts,omitempty"`
	Meta      string  `json:"meta,omitempty"`
}

type SubagentEvent struct {
	EventID         string         `json:"event_id"`
	Type            string         `json:"type"`
	ActorID         string         `json:"actor_id"`
	ChildSessionID  string         `json:"child_session_id"`
	ParentActorID   string         `json:"parent_actor_id,omitempty"`
	ParentSessionID string         `json:"parent_session_id"`
	TurnID          string         `json:"turn_id,omitempty"`
	QuestionID      string         `json:"question_id,omitempty"`
	Message         string         `json:"message,omitempty"`
	Status          SubagentStatus `json:"status,omitempty"`
	TS              float64        `json:"ts"`
}

type TeamEvent struct {
	Protocol        string         `json:"protocol"`
	EventID         string         `json:"eventId"`
	Type            string         `json:"type"`
	ActorID         string         `json:"actorId"`
	ChildSessionID  string         `json:"childSessionId"`
	ParentSessionID string         `json:"parentSessionId"`
	Timestamp       int64          `json:"timestamp"`
	Name            string         `json:"name,omitempty"`
	Role            string         `json:"role,omitempty"`
	TurnID          string         `json:"turnId,omitempty"`
	QuestionID      string         `json:"questionId,omitempty"`
	Question        string         `json:"question,omitempty"`
	Context         string         `json:"context,omitempty"`
	Blocking        bool           `json:"blocking,omitempty"`
	Delta           string         `json:"delta,omitempty"`
	ToolCallID      string         `json:"toolCallId,omitempty"`
	ToolName        string         `json:"toolName,omitempty"`
	IsError         bool           `json:"isError,omitempty"`
	Status          SubagentStatus `json:"status,omitempty"`
	Output          string         `json:"output,omitempty"`
	Error           string         `json:"error,omitempty"`
	ExitCode        *int           `json:"exitCode,omitempty"`
	Reason          string         `json:"reason,omitempty"`
}

type SpawnSubagentRequest struct {
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

type PromptSubagentRequest struct {
	ActorID string `json:"actor_id"`
	Prompt  string `json:"prompt"`
}

type FollowupSubagentRequest struct {
	ActorID string `json:"actor_id"`
	Prompt  string `json:"prompt"`
}

type SendSubagentRequest struct {
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

type AnswerSubagentRequest struct {
	ActorID    string `json:"actor_id"`
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
}

type AbortSubagentRequest struct {
	ActorID string `json:"actor_id"`
	TurnID  string `json:"turn_id"`
}

type CloseSubagentRequest struct {
	ActorID string `json:"actor_id"`
}

type StatusSubagentRequest struct {
	ActorID string `json:"actor_id"`
}

type SubagentEventsRequest struct {
	ActorID      string `json:"actor_id"`
	AfterEventID string `json:"after_event_id"`
}

type SubagentCommandResponse struct {
	OK             bool           `json:"ok"`
	Actor          *SubagentNode  `json:"actor,omitempty"`
	ActorID        string         `json:"actor_id,omitempty"`
	ChildSessionID string         `json:"child_session_id,omitempty"`
	TurnID         string         `json:"turn_id,omitempty"`
	QuestionID     string         `json:"question_id,omitempty"`
	Status         SubagentStatus `json:"status,omitempty"`
}

type SubagentDeliveryResponse struct {
	OK       bool                      `json:"ok"`
	ActorID  string                    `json:"actor_id"`
	TurnID   string                    `json:"turn_id,omitempty"`
	Delivery string                    `json:"delivery"`
	Queue    SessionQueueSnapshot      `json:"queue"`
	UI       *SessionUIRequestSnapshot `json:"ui_request,omitempty"`
}

type SubagentEventsResponse struct {
	OK     bool            `json:"ok"`
	Events []SubagentEvent `json:"events"`
}

type subagentStore interface {
	ReplaceSubagentSnapshot(context.Context, sqlitestore.SubagentSnapshotRow) error
	ListSubagentSnapshots(context.Context) ([]sqlitestore.SubagentSnapshotRow, error)
}

type subagentRegistry struct {
	mu           sync.Mutex
	now          func() time.Time
	store        subagentStore
	nextActor    int64
	nextTurn     int64
	nextEvent    int64
	nextQuestion int64
	actors       map[string]*subagentActor
	order        []string
}

type subagentActor struct {
	ActorID         string
	ChildSessionID  session.SessionID
	ParentActorID   string
	ParentSessionID session.SessionID
	Name            string
	Role            string
	Status          SubagentStatus
	TurnID          string
	Question        *subagentQuestionState
	LastEventID     string
	LastEventTS     time.Time
	Model           string
	CWD             string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Messages        []SubagentThreadMsg
	Events          []SubagentEvent
}

type subagentParentAnswer struct {
	answer   string
	terminal string
}

type subagentQuestionState struct {
	SubagentQuestion
	answer       chan subagentParentAnswer
	done         bool
	answerText   string
	terminalText string
}

func newSubagentRegistry(now func() time.Time, stores ...subagentStore) *subagentRegistry {
	if now == nil {
		now = time.Now
	}
	var store subagentStore
	if len(stores) > 0 {
		store = stores[0]
	}
	return &subagentRegistry{now: now, store: store, actors: map[string]*subagentActor{}}
}

func (s *Stub) ListSubagents(_ context.Context, req ListSubagentsRequest) (ListSubagentsResponse, error) {
	roots := s.subagents.snapshot(req.IncludeClosed)
	return ListSubagentsResponse{OK: true, Roots: roots, TotalCount: countSubagentNodes(roots), NonLeafCount: countNonLeafSubagentNodes(roots)}, nil
}

func (s *Stub) SpawnSubagent(ctx context.Context, req SpawnSubagentRequest) (SubagentCommandResponse, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return SubagentCommandResponse{}, Invalid("name", "name required")
	}
	parentSessionID, err := s.subagents.resolveParentSession(req.ParentSessionID, req.ParentActorID)
	if err != nil {
		return SubagentCommandResponse{}, err
	}
	if strings.TrimSpace(req.ParentActorID) == "" {
		parent, ok := s.registry.Lookup(parentSessionID)
		if !ok {
			return SubagentCommandResponse{}, NotFound(fmt.Sprintf("parent session %q not found", parentSessionID))
		}
		if parent.hidden {
			return SubagentCommandResponse{}, Forbidden("parent_session_id refers to a generated child session; use parent_actor_id")
		}
	}
	if err := s.subagents.validateSpawn(parentSessionID, name); err != nil {
		return SubagentCommandResponse{}, err
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
		return SubagentCommandResponse{}, err
	}
	if created.Session == nil {
		return SubagentCommandResponse{}, Conflict("subagent child session was not created")
	}
	model := ""
	if req.Model != nil {
		model = strings.TrimSpace(*req.Model)
	}
	actor, err := s.subagents.spawn(parentSessionID, req.ParentActorID, created.Session.SessionID, name, req.Role, backend, model, cwd)
	if err != nil {
		return SubagentCommandResponse{}, err
	}
	if text := strings.TrimSpace(req.InitialPrompt); text != "" {
		if _, err := s.sendSubagentText(ctx, actor.ActorID, text, true); err != nil {
			s.subagents.markFailed(actor.ActorID, err.Error())
			return SubagentCommandResponse{}, err
		}
		actor = s.subagents.lookupNode(actor.ActorID)
	}
	return SubagentCommandResponse{OK: true, Actor: actor, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID, TurnID: actor.TurnID, Status: actor.Status}, nil
}

func (s *Stub) PromptSubagent(ctx context.Context, req PromptSubagentRequest) (SubagentCommandResponse, error) {
	actor, err := s.sendSubagentText(ctx, req.ActorID, req.Prompt, true)
	if err != nil {
		return SubagentCommandResponse{}, err
	}
	return SubagentCommandResponse{OK: true, Actor: actor, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID, TurnID: actor.TurnID, Status: actor.Status}, nil
}

func (s *Stub) FollowupSubagent(ctx context.Context, req FollowupSubagentRequest) (SubagentCommandResponse, error) {
	actor, err := s.sendSubagentText(ctx, req.ActorID, req.Prompt, true)
	if err != nil {
		return SubagentCommandResponse{}, err
	}
	return SubagentCommandResponse{OK: true, Actor: actor, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID, TurnID: actor.TurnID, Status: actor.Status}, nil
}

func (s *Stub) SendSubagent(ctx context.Context, req SendSubagentRequest) (SubagentDeliveryResponse, error) {
	actor, err := s.sendSubagentText(ctx, req.ActorID, req.Message, false)
	if err != nil {
		return SubagentDeliveryResponse{}, err
	}
	return SubagentDeliveryResponse{OK: true, ActorID: actor.ActorID, TurnID: actor.TurnID, Delivery: "live"}, nil
}

func (s *Stub) sendSubagentText(ctx context.Context, actorID, text string, newTurn bool) (*SubagentNode, error) {
	cleaned := strings.TrimSpace(text)
	if cleaned == "" {
		return nil, Invalid("text", "text required")
	}
	actor, turnID, err := s.subagents.beginSend(actorID, cleaned, newTurn)
	if err != nil {
		return nil, err
	}
	if _, err := s.Send(ctx, SendRequest{SessionID: actor.ChildSessionID, Text: cleaned}); err != nil {
		s.subagents.markFailed(actorID, err.Error())
		return nil, err
	}
	node := s.subagents.lookupNode(actorID)
	if node != nil {
		node.TurnID = turnID
	}
	return node, nil
}

func (s *Stub) AskParent(ctx context.Context, req AskParentRequest) (AskParentResponse, error) {
	questionID, err := s.subagents.askParent(req.ActorID, req.TurnID, req.Question, req.Context)
	if err != nil {
		return AskParentResponse{}, err
	}
	answer, err := s.subagents.waitForAnswer(ctx, req.ActorID, questionID)
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
	answer, err := s.subagents.waitForAnswer(ctx, req.ActorID, questionID)
	if err != nil {
		return AskParentResponse{}, err
	}
	return AskParentResponse{OK: true, ActorID: req.ActorID, QuestionID: questionID, Answer: answer.answer, Terminal: answer.terminal}, nil
}

func (s *Stub) AnswerSubagent(_ context.Context, req AnswerSubagentRequest) (SubagentCommandResponse, error) {
	actor, err := s.subagents.answer(req.ActorID, req.QuestionID, req.Answer)
	if err != nil {
		return SubagentCommandResponse{}, err
	}
	return SubagentCommandResponse{OK: true, Actor: actor, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID, TurnID: actor.TurnID, QuestionID: req.QuestionID, Status: actor.Status}, nil
}

func (s *Stub) AbortSubagent(ctx context.Context, req AbortSubagentRequest) (SubagentCommandResponse, error) {
	actor, err := s.subagents.get(req.ActorID)
	if err != nil {
		return SubagentCommandResponse{}, err
	}
	if _, err := s.Interrupt(ctx, InterruptRequest{SessionID: actor.ChildSessionID}); err != nil {
		s.subagents.markFailed(req.ActorID, err.Error())
		return SubagentCommandResponse{}, err
	}
	node, err := s.subagents.abort(req.ActorID, req.TurnID)
	if err != nil {
		return SubagentCommandResponse{}, err
	}
	return SubagentCommandResponse{OK: true, Actor: node, ActorID: node.ActorID, ChildSessionID: node.ChildSessionID, TurnID: node.TurnID, Status: node.Status}, nil
}

func (s *Stub) CloseSubagent(ctx context.Context, req CloseSubagentRequest) (SubagentCommandResponse, error) {
	actor, err := s.subagents.get(req.ActorID)
	if err != nil {
		return SubagentCommandResponse{}, err
	}
	record, err := s.lookupSession(actor.ChildSessionID)
	if err != nil {
		return SubagentCommandResponse{}, err
	}
	if err := record.runtime.Kill(ctx); err != nil {
		s.subagents.markFailed(req.ActorID, err.Error())
		return SubagentCommandResponse{}, err
	}
	if err := s.setRuntimeAgentRunning(actor.ChildSessionID, false); err != nil {
		return SubagentCommandResponse{}, err
	}
	s.helpers.Remove(actor.ChildSessionID)
	if err := s.helperBindings.Delete(actor.ChildSessionID); err != nil {
		return SubagentCommandResponse{}, err
	}
	node, err := s.subagents.close(req.ActorID)
	if err != nil {
		return SubagentCommandResponse{}, err
	}
	return SubagentCommandResponse{OK: true, Actor: node, ActorID: node.ActorID, ChildSessionID: node.ChildSessionID, TurnID: node.TurnID, Status: node.Status}, nil
}

func (s *Stub) StatusSubagent(_ context.Context, req StatusSubagentRequest) (SubagentCommandResponse, error) {
	actor := s.subagents.lookupNode(req.ActorID)
	if actor == nil {
		return SubagentCommandResponse{}, NotFound(fmt.Sprintf("subagent actor %q not found", strings.TrimSpace(req.ActorID)))
	}
	return SubagentCommandResponse{OK: true, Actor: actor, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID, TurnID: actor.TurnID, Status: actor.Status}, nil
}

func (s *Stub) SubagentEvents(_ context.Context, req SubagentEventsRequest) (SubagentEventsResponse, error) {
	events, err := s.subagents.eventsAfter(req.ActorID, req.AfterEventID)
	if err != nil {
		return SubagentEventsResponse{}, err
	}
	return SubagentEventsResponse{OK: true, Events: events}, nil
}

func (r *subagentRegistry) resolveParentSession(rawSession, rawActor string) (session.SessionID, error) {
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

func (r *subagentRegistry) validateSpawn(parentSession session.SessionID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.validateSpawnLocked(parentSession, name)
}

func (r *subagentRegistry) validateSpawnLocked(parentSession session.SessionID, name string) error {
	for _, actor := range r.actors {
		if actor.ParentSessionID == parentSession && actor.Name == name && actor.Status != SubagentStatusClosed {
			return Conflict(fmt.Sprintf("subagent %q already exists for parent %q", name, parentSession))
		}
	}
	return nil
}

func (r *subagentRegistry) spawn(parentSession session.SessionID, parentActorID, childSessionID, name, role, backend, model, cwd string) (*SubagentNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validateSpawnLocked(parentSession, name); err != nil {
		return nil, err
	}
	r.nextActor++
	actorID := fmt.Sprintf("actor_%d", r.nextActor)
	now := r.now()
	actor := &subagentActor{ActorID: actorID, ChildSessionID: session.SessionID(childSessionID), ParentActorID: strings.TrimSpace(parentActorID), ParentSessionID: parentSession, Name: name, Role: strings.TrimSpace(role), Status: SubagentStatusIdle, Model: model, CWD: cwd, LastEventTS: now, CreatedAt: now, UpdatedAt: now}
	r.actors[actorID] = actor
	r.order = append(r.order, actorID)
	r.appendEventLocked(actor, "subagent.started", "", "", "", actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		delete(r.actors, actorID)
		r.order = r.order[:len(r.order)-1]
		return nil, err
	}
	node := r.nodeLocked(actor)
	return &node, nil
}

func (r *subagentRegistry) beginSend(actorID, text string, newTurn bool) (*subagentActor, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil, "", err
	}
	if actor.Status == SubagentStatusClosed {
		return nil, "", Conflict("subagent is closed")
	}
	if newTurn || actor.TurnID == "" {
		r.nextTurn++
		actor.TurnID = fmt.Sprintf("turn_%d", r.nextTurn)
		r.appendEventLocked(actor, "subagent.turn_started", actor.TurnID, "", "", SubagentStatusRunning)
	}
	actor.Status = SubagentStatusRunning
	actor.Messages = append(actor.Messages, SubagentThreadMsg{MessageID: actor.LastEventID + ":prompt", Kind: "leader", Label: "parent", Body: text, TS: subagentTimestamp(r.now())})
	r.appendEventLocked(actor, "subagent.prompt", actor.TurnID, "", text, actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		return nil, "", err
	}
	return cloneSubagentActor(actor), actor.TurnID, nil
}

func (r *subagentRegistry) askParent(actorID, turnID, question, contextText string) (string, error) {
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
		return "", Conflict("subagent already has a pending question")
	}
	if turnID == "" {
		turnID = actor.TurnID
	}
	r.nextQuestion++
	questionID := fmt.Sprintf("question_%d", r.nextQuestion)
	q := &subagentQuestionState{SubagentQuestion: SubagentQuestion{QuestionID: questionID, TurnID: turnID, Question: cleaned, Context: strings.TrimSpace(contextText), CreatedTS: subagentTimestamp(r.now())}, answer: make(chan subagentParentAnswer, 1)}
	actor.Question = q
	actor.Status = SubagentStatusWaitingForParent
	actor.Messages = append(actor.Messages, SubagentThreadMsg{MessageID: questionID, Kind: "member", Label: actor.Name, Body: cleaned, TS: q.CreatedTS, Meta: "ask_parent"})
	r.appendEventLocked(actor, "subagent.question", turnID, questionID, cleaned, actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		return "", err
	}
	return questionID, nil
}

func (r *subagentRegistry) waitForAnswer(ctx context.Context, actorID, questionID string) (subagentParentAnswer, error) {
	r.mu.Lock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		r.mu.Unlock()
		return subagentParentAnswer{}, err
	}
	if actor.Question == nil || actor.Question.QuestionID != questionID {
		r.mu.Unlock()
		return subagentParentAnswer{}, NotFound("subagent question not found")
	}
	if actor.Question.done {
		answer := subagentParentAnswer{answer: actor.Question.answerText, terminal: actor.Question.terminalText}
		r.mu.Unlock()
		return answer, nil
	}
	ch := actor.Question.answer
	r.mu.Unlock()
	select {
	case answer := <-ch:
		return answer, nil
	case <-ctx.Done():
		return subagentParentAnswer{}, ctx.Err()
	}
}

func (r *subagentRegistry) answer(actorID, questionID, answer string) (*SubagentNode, error) {
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
		return nil, NotFound("subagent pending question not found")
	}
	actor.Question.done = true
	actor.Question.answerText = cleaned
	actor.Question.answer <- subagentParentAnswer{answer: cleaned}
	actor.Status = SubagentStatusRunning
	actor.Messages = append(actor.Messages, SubagentThreadMsg{MessageID: questionID + ":answer", Kind: "leader", Label: "parent", Body: cleaned, TS: subagentTimestamp(r.now()), Meta: "answer"})
	r.appendEventLocked(actor, "subagent.answer_accepted", actor.TurnID, questionID, cleaned, actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		return nil, err
	}
	node := r.nodeLocked(actor)
	return &node, nil
}

func (r *subagentRegistry) abort(actorID, turnID string) (*SubagentNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil, err
	}
	if turnID != "" && actor.TurnID != "" && actor.TurnID != turnID {
		return nil, Conflict("subagent turn id mismatch")
	}
	actor.Status = SubagentStatusIdle
	if actor.Question != nil && !actor.Question.done {
		actor.Question.done = true
		actor.Question.terminalText = "aborted"
		actor.Question.answer <- subagentParentAnswer{terminal: "aborted"}
	}
	r.appendEventLocked(actor, "subagent.aborted", actor.TurnID, "", "", actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		return nil, err
	}
	node := r.nodeLocked(actor)
	return &node, nil
}

func (r *subagentRegistry) close(actorID string) (*SubagentNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil, err
	}
	actor.Status = SubagentStatusClosed
	if actor.Question != nil && !actor.Question.done {
		actor.Question.done = true
		actor.Question.terminalText = "closed"
		actor.Question.answer <- subagentParentAnswer{terminal: "closed"}
	}
	r.appendEventLocked(actor, "subagent.closed", actor.TurnID, "", "", actor.Status)
	if err := r.persistActorLocked(actor); err != nil {
		return nil, err
	}
	node := r.nodeLocked(actor)
	return &node, nil
}

func (r *subagentRegistry) markFailed(actorID, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return
	}
	actor.Status = SubagentStatusFailed
	if actor.Question != nil && !actor.Question.done {
		actor.Question.done = true
		actor.Question.terminalText = "failed"
		actor.Question.answer <- subagentParentAnswer{terminal: "failed"}
	}
	r.appendEventLocked(actor, "subagent.error", actor.TurnID, "", message, actor.Status)
	_ = r.persistActorLocked(actor)
}

func (r *subagentRegistry) appendTeamEventForSession(sessionID session.SessionID, typ, turnID, questionID, message string, status SubagentStatus) {
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

func (r *subagentRegistry) snapshot(includeClosed bool) []SubagentNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	byParent := map[string][]SubagentNode{}
	roots := []SubagentNode{}
	for _, actorID := range r.order {
		actor := r.actors[actorID]
		if !includeClosed && actor.Status == SubagentStatusClosed {
			continue
		}
		node := r.nodeLocked(actor)
		if actor.ParentActorID == "" {
			roots = append(roots, node)
		} else {
			byParent[actor.ParentActorID] = append(byParent[actor.ParentActorID], node)
		}
	}
	var attach func(*SubagentNode)
	attach = func(node *SubagentNode) {
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

func (r *subagentRegistry) lookupNode(actorID string) *SubagentNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil
	}
	node := r.nodeLocked(actor)
	return &node
}

func (r *subagentRegistry) eventsAfter(actorID, after string) ([]SubagentEvent, error) {
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
			return nil, NotFound("subagent event cursor not found")
		}
	}
	return append([]SubagentEvent(nil), actor.Events[start:]...), nil
}

func (r *subagentRegistry) get(actorID string) (*subagentActor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	actor, err := r.getLocked(actorID)
	if err != nil {
		return nil, err
	}
	return cloneSubagentActor(actor), nil
}

func (r *subagentRegistry) getLocked(actorID string) (*subagentActor, error) {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return nil, Invalid("actor_id", "actor_id required")
	}
	actor, ok := r.actors[actorID]
	if !ok {
		return nil, NotFound(fmt.Sprintf("subagent actor %q not found", actorID))
	}
	return actor, nil
}

func (r *subagentRegistry) appendEventLocked(actor *subagentActor, typ, turnID, questionID, message string, status SubagentStatus) {
	r.nextEvent++
	now := r.now()
	event := SubagentEvent{EventID: fmt.Sprintf("event_%d", r.nextEvent), Type: typ, ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID.String(), ParentActorID: actor.ParentActorID, ParentSessionID: actor.ParentSessionID.String(), TurnID: turnID, QuestionID: questionID, Message: message, Status: status, TS: subagentTimestamp(now)}
	actor.LastEventID = event.EventID
	actor.LastEventTS = now
	actor.UpdatedAt = now
	actor.Events = append(actor.Events, event)
}

func (r *subagentRegistry) persistActorLocked(actor *subagentActor) error {
	if r.store == nil {
		return nil
	}
	return r.store.ReplaceSubagentSnapshot(context.Background(), durableSubagentSnapshotFromActor(actor))
}

func (r *subagentRegistry) rehydrate(snapshots []sqlitestore.SubagentSnapshotRow) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actors = map[string]*subagentActor{}
	r.order = []string{}
	r.nextActor = 0
	r.nextTurn = 0
	r.nextEvent = 0
	r.nextQuestion = 0
	for _, snapshot := range snapshots {
		actor := subagentActorFromDurableSnapshot(snapshot)
		r.actors[actor.ActorID] = actor
		r.order = append(r.order, actor.ActorID)
		r.observeActorCountersLocked(actor)
	}
	return nil
}

func (r *subagentRegistry) observeActorCountersLocked(actor *subagentActor) {
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

func (r *subagentRegistry) nodeLocked(actor *subagentActor) SubagentNode {
	var question *SubagentQuestion
	if actor.Question != nil && !actor.Question.done {
		q := actor.Question.SubagentQuestion
		question = &q
	}
	return SubagentNode{ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID.String(), ParentActorID: actor.ParentActorID, ParentSessionID: actor.ParentSessionID.String(), Name: actor.Name, Role: actor.Role, Status: actor.Status, TurnID: actor.TurnID, Question: question, LastEventID: actor.LastEventID, LastEventTS: subagentTimestamp(actor.LastEventTS), Model: actor.Model, CWD: actor.CWD, Messages: append([]SubagentThreadMsg(nil), actor.Messages...)}
}

func cloneSubagentActor(actor *subagentActor) *subagentActor {
	cloned := *actor
	cloned.Messages = append([]SubagentThreadMsg(nil), actor.Messages...)
	cloned.Events = append([]SubagentEvent(nil), actor.Events...)
	return &cloned
}

func durableSubagentSnapshotFromActor(actor *subagentActor) sqlitestore.SubagentSnapshotRow {
	var lastEventAt *time.Time
	if !actor.LastEventTS.IsZero() {
		lastEventAt = copyTimePtr(&actor.LastEventTS)
	}
	row := sqlitestore.SubagentActorRow{ActorID: actor.ActorID, ChildSessionID: actor.ChildSessionID.String(), ParentActorID: actor.ParentActorID, ParentSessionID: actor.ParentSessionID.String(), Name: actor.Name, Role: actor.Role, Status: string(actor.Status), TurnID: actor.TurnID, LastEventID: actor.LastEventID, LastEventAt: lastEventAt, Model: actor.Model, CWD: actor.CWD, CreatedAt: actor.CreatedAt, UpdatedAt: actor.UpdatedAt}
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
	events := make([]sqlitestore.SubagentEventRow, 0, len(actor.Events))
	for i, event := range actor.Events {
		events = append(events, sqlitestore.SubagentEventRow{ActorID: event.ActorID, Ordinal: i + 1, EventID: event.EventID, Type: event.Type, ChildSessionID: event.ChildSessionID, ParentActorID: event.ParentActorID, ParentSessionID: event.ParentSessionID, TurnID: event.TurnID, QuestionID: event.QuestionID, Message: event.Message, Status: string(event.Status), TS: event.TS})
	}
	messages := make([]sqlitestore.SubagentMessageRow, 0, len(actor.Messages))
	for i, msg := range actor.Messages {
		messages = append(messages, sqlitestore.SubagentMessageRow{ActorID: actor.ActorID, Ordinal: i + 1, MessageID: msg.MessageID, Kind: msg.Kind, Label: msg.Label, Body: msg.Body, TS: msg.TS, Meta: msg.Meta})
	}
	return sqlitestore.SubagentSnapshotRow{Actor: row, Events: events, Messages: messages}
}

func subagentActorFromDurableSnapshot(snapshot sqlitestore.SubagentSnapshotRow) *subagentActor {
	row := snapshot.Actor
	actor := &subagentActor{ActorID: row.ActorID, ChildSessionID: session.SessionID(row.ChildSessionID), ParentActorID: row.ParentActorID, ParentSessionID: session.SessionID(row.ParentSessionID), Name: row.Name, Role: row.Role, Status: SubagentStatus(row.Status), TurnID: row.TurnID, LastEventID: row.LastEventID, Model: row.Model, CWD: row.CWD, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.LastEventAt != nil {
		actor.LastEventTS = *row.LastEventAt
	}
	if row.QuestionID != "" {
		actor.Question = &subagentQuestionState{SubagentQuestion: SubagentQuestion{QuestionID: row.QuestionID, TurnID: row.QuestionTurnID, Question: row.Question, Context: row.QuestionContext, CreatedTS: row.QuestionCreatedTS}, answer: make(chan subagentParentAnswer, 1), done: row.QuestionDone, answerText: row.QuestionAnswer, terminalText: row.QuestionTerminal}
	}
	actor.Events = make([]SubagentEvent, 0, len(snapshot.Events))
	for _, event := range snapshot.Events {
		actor.Events = append(actor.Events, SubagentEvent{EventID: event.EventID, Type: event.Type, ActorID: event.ActorID, ChildSessionID: event.ChildSessionID, ParentActorID: event.ParentActorID, ParentSessionID: event.ParentSessionID, TurnID: event.TurnID, QuestionID: event.QuestionID, Message: event.Message, Status: SubagentStatus(event.Status), TS: event.TS})
	}
	actor.Messages = make([]SubagentThreadMsg, 0, len(snapshot.Messages))
	for _, msg := range snapshot.Messages {
		actor.Messages = append(actor.Messages, SubagentThreadMsg{MessageID: msg.MessageID, Kind: msg.Kind, Label: msg.Label, Body: msg.Body, TS: msg.TS, Meta: msg.Meta})
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

func countSubagentNodes(nodes []SubagentNode) int {
	total := 0
	for _, node := range nodes {
		total++
		total += countSubagentNodes(node.Children)
	}
	return total
}

func countNonLeafSubagentNodes(nodes []SubagentNode) int {
	total := 0
	for _, node := range nodes {
		if len(node.Children) > 0 {
			total++
		}
		total += countNonLeafSubagentNodes(node.Children)
	}
	return total
}

func subagentTimestamp(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return timestampSeconds(t)
}

func TeamEventsFromSubagentEvents(events []SubagentEvent, actor *SubagentNode) []TeamEvent {
	out := make([]TeamEvent, 0, len(events))
	for _, event := range events {
		out = append(out, TeamEventFromSubagentEvent(event, actor))
	}
	return out
}

func TeamEventFromSubagentEvent(event SubagentEvent, actor *SubagentNode) TeamEvent {
	teamType := mapSubagentEventType(event.Type)
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
		if event.Status == SubagentStatusFailed {
			team.Error = event.Message
			code := 1
			team.ExitCode = &code
		} else if event.Status == SubagentStatusCompleted {
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

func mapSubagentEventType(typ string) string {
	switch typ {
	case "subagent.started":
		return "team.started"
	case "subagent.turn_started":
		return "team.turn_started"
	case "subagent.prompt":
		return "team.status"
	case "subagent.question":
		return "team.question"
	case "subagent.answer_accepted":
		return "team.answer_accepted"
	case "subagent.output_delta":
		return "team.output_delta"
	case "subagent.tool_call":
		return "team.tool_call"
	case "subagent.tool_result":
		return "team.tool_result"
	case "subagent.turn_result":
		return "team.turn_result"
	case "subagent.error":
		return "team.error"
	case "subagent.aborted":
		return "team.aborted"
	case "subagent.closed":
		return "team.closed"
	default:
		return "team.status"
	}
}
