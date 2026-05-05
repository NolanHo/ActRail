package app

import (
	"strings"

	"actrail/internal/domain/session"
)

func (s *Stub) appendSubagentMessageDeltaEvent(sessionID session.SessionID, turnID, role, delta string) {
	if strings.TrimSpace(delta) == "" {
		return
	}
	if strings.TrimSpace(role) != "assistant" {
		return
	}
	s.subagents.appendTeamEventForSession(sessionID, "subagent.output_delta", turnID, "", delta, SubagentStatusRunning)
}

func (s *Stub) appendSubagentMessageCommitEvent(sessionID session.SessionID, turnID string, msg SessionMessage) {
	if msg.Kind == "tool" {
		s.subagents.appendTeamEventForSession(sessionID, "subagent.tool_call", turnID, msg.ToolCallID, firstNonEmptyString(msg.Name, msg.Summary, msg.Text), SubagentStatusRunning)
		return
	}
	if msg.Kind == "tool_result" {
		status := SubagentStatusRunning
		if msg.IsError {
			status = SubagentStatusFailed
		}
		s.subagents.appendTeamEventForSession(sessionID, "subagent.tool_result", turnID, msg.ToolCallID, firstNonEmptyString(msg.Name, msg.Summary, msg.Text), status)
		return
	}
	if msg.Kind == "error" || msg.IsError {
		s.subagents.appendTeamEventForSession(sessionID, "subagent.error", turnID, "", msg.Text, SubagentStatusFailed)
		return
	}
	if msg.Role == "assistant" && msg.Kind == "message" {
		s.subagents.appendTeamEventForSession(sessionID, "subagent.turn_result", turnID, "", msg.Text, SubagentStatusCompleted)
	}
}
