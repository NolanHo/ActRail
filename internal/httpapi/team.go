package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"actrail/internal/app"
)

type teamCommand struct {
	Protocol        string          `json:"protocol"`
	Type            string          `json:"type"`
	RequestID       string          `json:"requestId"`
	ParentSessionID string          `json:"parentSessionId"`
	ActorID         string          `json:"actorId"`
	Name            string          `json:"name"`
	Role            string          `json:"role"`
	CWD             string          `json:"cwd"`
	Model           *string         `json:"model"`
	SystemPrompt    string          `json:"systemPrompt"`
	InitialPrompt   string          `json:"initialPrompt"`
	Prompt          string          `json:"prompt"`
	QueuedMessages  []string        `json:"queuedMessages"`
	Message         string          `json:"message"`
	QuestionID      string          `json:"questionId"`
	Answer          string          `json:"answer"`
	RoleConfig      *teamRoleConfig `json:"roleConfig"`
}

type teamRoleConfig struct {
	Tools []string `json:"tools"`
}

type teamCommandResult struct {
	RequestID      string         `json:"requestId"`
	ActorID        string         `json:"actorId,omitempty"`
	ChildSessionID string         `json:"childSessionId,omitempty"`
	Status         app.TeamStatus `json:"status,omitempty"`
	TurnID         string         `json:"turnId,omitempty"`
	Error          string         `json:"error,omitempty"`
	ExitCode       *int           `json:"exitCode,omitempty"`
	Delivery       string         `json:"delivery,omitempty"`
}

func (r Router) teamCommand(w http.ResponseWriter, req *http.Request) {
	var cmd teamCommand
	if err := decodeJSONBody(req, &cmd); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json", "")
		return
	}
	if cmd.Protocol != "pi.team.v1" {
		writeAppError(w, app.Invalid("protocol", "protocol must be pi.team.v1"))
		return
	}
	if strings.TrimSpace(cmd.RequestID) == "" {
		writeAppError(w, app.Invalid("requestId", "requestId required"))
		return
	}
	payload, err := r.dispatchTeamCommand(req, cmd)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) dispatchTeamCommand(req *http.Request, cmd teamCommand) (teamCommandResult, error) {
	switch cmd.Type {
	case "team.spawn":
		res, err := r.app.SpawnTeam(req.Context(), app.SpawnTeamRequest{
			ParentSessionID: cmd.ParentSessionID,
			Name:            cmd.Name,
			Role:            cmd.Role,
			AgentBackend:    "pi",
			CWD:             cmd.CWD,
			Model:           cmd.Model,
			InitialPrompt:   cmd.InitialPrompt,
		})
		return teamResultFromTeamCommand(cmd.RequestID, res), err
	case "team.followup":
		prompt := strings.TrimSpace(strings.Join(cmd.QueuedMessages, "\n\n"))
		if prompt != "" {
			prompt += "\n\n" + cmd.Prompt
		} else {
			prompt = cmd.Prompt
		}
		res, err := r.app.FollowupTeam(req.Context(), app.FollowupTeamRequest{ActorID: cmd.ActorID, Prompt: prompt})
		return teamResultFromTeamCommand(cmd.RequestID, res), err
	case "team.send":
		res, err := r.app.SendTeam(req.Context(), app.SendTeamRequest{ActorID: cmd.ActorID, Message: cmd.Message})
		return teamCommandResult{RequestID: cmd.RequestID, ActorID: res.ActorID, TurnID: res.TurnID, Delivery: res.Delivery}, err
	case "team.answer":
		res, err := r.app.AnswerTeam(req.Context(), app.AnswerTeamRequest{ActorID: cmd.ActorID, QuestionID: cmd.QuestionID, Answer: cmd.Answer})
		return teamResultFromTeamCommand(cmd.RequestID, res), err
	case "team.abort":
		res, err := r.app.AbortTeam(req.Context(), app.AbortTeamRequest{ActorID: cmd.ActorID})
		return teamResultFromTeamCommand(cmd.RequestID, res), err
	case "team.close":
		res, err := r.app.CloseTeam(req.Context(), app.CloseTeamRequest{ActorID: cmd.ActorID})
		return teamResultFromTeamCommand(cmd.RequestID, res), err
	case "team.status", "team.subscribe":
		res, err := r.app.StatusTeam(req.Context(), app.StatusTeamRequest{ActorID: cmd.ActorID})
		return teamResultFromTeamCommand(cmd.RequestID, res), err
	default:
		return teamCommandResult{}, app.Invalid("type", fmt.Sprintf("unsupported team command %q", cmd.Type))
	}
}

func teamResultFromTeamCommand(requestID string, res app.TeamCommandResponse) teamCommandResult {
	return teamCommandResult{RequestID: requestID, ActorID: res.ActorID, ChildSessionID: res.ChildSessionID, Status: res.Status, TurnID: res.TurnID}
}

func (r Router) teamEventsJSON(w http.ResponseWriter, req *http.Request) {
	actorID := strings.TrimSpace(req.PathValue("actor_id"))
	afterEventID := strings.TrimSpace(req.URL.Query().Get("after_event_id"))
	if actorID == "" {
		writeAppError(w, app.Invalid("actorId", "actorId required"))
		return
	}
	payload, err := r.app.TeamEvents(req.Context(), app.TeamEventsRequest{ActorID: actorID, AfterEventID: afterEventID})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (r Router) teamEventsSSE(w http.ResponseWriter, req *http.Request) {
	actorID := strings.TrimSpace(req.URL.Query().Get("actorId"))
	afterEventID := strings.TrimSpace(req.URL.Query().Get("afterEventId"))
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unsupported", "streaming unsupported", "")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	enc := json.NewEncoder(w)
	for {
		var payload app.TeamEventsResponse
		var err error
		if actorID == "" {
			payload, err = r.app.TeamEventsAll(req.Context(), app.TeamEventsRequest{AfterEventID: afterEventID})
		} else {
			payload, err = r.app.TeamEvents(req.Context(), app.TeamEventsRequest{ActorID: actorID, AfterEventID: afterEventID})
		}
		if err != nil {
			_, _ = w.Write([]byte("event: error\n"))
			_, _ = w.Write([]byte("data: "))
			_ = enc.Encode(map[string]string{"error": err.Error()})
			_, _ = w.Write([]byte("\n"))
			flusher.Flush()
			return
		}
		if len(payload.Events) > 0 {
			var actor *app.TeamNode
			if actorID != "" {
				status, err := r.app.StatusTeam(req.Context(), app.StatusTeamRequest{ActorID: actorID})
				if err != nil {
					_, _ = w.Write([]byte("event: error\n"))
					_, _ = w.Write([]byte("data: "))
					_ = enc.Encode(map[string]string{"error": err.Error()})
					_, _ = w.Write([]byte("\n"))
					flusher.Flush()
					return
				}
				actor = status.Actor
			}
			for _, event := range app.TeamEventsFromStoredEvents(payload.Events, actor) {
				_, _ = w.Write([]byte("data: "))
				_ = enc.Encode(event)
				_, _ = w.Write([]byte("\n"))
				afterEventID = event.EventID
			}
			flusher.Flush()
		}
		select {
		case <-req.Context().Done():
			return
		case <-ticker.C:
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": heartbeat\n\n"))
			flusher.Flush()
		}
	}
}
