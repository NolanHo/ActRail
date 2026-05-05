package app

import (
	"strings"

	"actrail/internal/domain/session"
)

func (s *Stub) appendTeamMessageDeltaEvent(sessionID session.SessionID, turnID, role, delta string) {
	if strings.TrimSpace(delta) == "" {
		return
	}
	if strings.TrimSpace(role) != "assistant" {
		return
	}
	s.teams.appendTeamEventForSession(sessionID, "team.output_delta", turnID, "", delta, TeamStatusRunning)
}

func (s *Stub) appendTeamMessageCommitEvent(sessionID session.SessionID, turnID string, msg SessionMessage) {
	if msg.Kind == "tool" {
		s.teams.appendTeamEventForSession(sessionID, "team.tool_call", turnID, msg.ToolCallID, firstNonEmptyString(msg.Name, msg.Summary, msg.Text), TeamStatusRunning)
		return
	}
	if msg.Kind == "tool_result" {
		status := TeamStatusRunning
		if msg.IsError {
			status = TeamStatusFailed
		}
		s.teams.appendTeamEventForSession(sessionID, "team.tool_result", turnID, msg.ToolCallID, firstNonEmptyString(msg.Name, msg.Summary, msg.Text), status)
		return
	}
	if msg.Kind == "error" || msg.IsError {
		s.teams.appendTeamEventForSession(sessionID, "team.error", turnID, "", msg.Text, TeamStatusFailed)
		return
	}
	if msg.Role == "assistant" && msg.Kind == "message" {
		s.teams.appendTeamEventForSession(sessionID, "team.turn_result", turnID, "", msg.Text, TeamStatusCompleted)
	}
}
