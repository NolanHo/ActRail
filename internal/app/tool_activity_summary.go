package app

import (
	"strconv"
	"strings"
)

const toolActivitySummaryDetailsKey = "tool_activity_summary"

type sessionToolActivitySummary struct {
	Operations         int      `json:"operations"`
	TotalTools         int      `json:"total_tools"`
	ToolCalls          int      `json:"tool_calls"`
	ToolResults        int      `json:"tool_results"`
	Running            int      `json:"running"`
	OK                 int      `json:"ok"`
	Failed             int      `json:"failed"`
	Reasoning          int      `json:"reasoning"`
	TodoSnapshots      int      `json:"todo_snapshots"`
	ProcessUpdates     int      `json:"process_updates"`
	SystemEvents       int      `json:"system_events"`
	StartedAt          float64  `json:"started_at,omitempty"`
	LastActivityAt     float64  `json:"last_activity_at,omitempty"`
	ElapsedSeconds     float64  `json:"elapsed_seconds,omitempty"`
	MaxToolCallSeconds float64  `json:"max_tool_call_seconds,omitempty"`
	RunningToolNames   []string `json:"running_tool_names,omitempty"`
	SummaryText        string   `json:"summary_text"`
	StatusText         string   `json:"status_text"`
}

type toolCallSummaryState struct {
	id        string
	name      string
	startedAt float64
	resultAt  float64
	failed    bool
}

func annotateHiddenToolActivitySummaries(visible []SessionMessage, all []SessionMessage) []SessionMessage {
	if len(visible) == 0 || len(all) == 0 {
		return visible
	}
	bySeq := hiddenToolActivitySummariesByAssistantSeq(all)
	if len(bySeq) == 0 {
		return visible
	}
	out := make([]SessionMessage, len(visible))
	copy(out, visible)
	for i := range out {
		summary, ok := bySeq[out[i].Seq]
		if !ok {
			continue
		}
		out[i].Details = cloneSessionMessageDetails(out[i].Details)
		out[i].Details[toolActivitySummaryDetailsKey] = summary
	}
	return out
}

func hiddenToolActivitySummariesByAssistantSeq(items []SessionMessage) map[uint64]sessionToolActivitySummary {
	summaries := make(map[uint64]sessionToolActivitySummary)
	segment := make([]SessionMessage, 0)
	for _, item := range items {
		kind := sessionMessageDisplayKind(item)
		if item.Role == "user" {
			segment = segment[:0]
			continue
		}
		if item.Role == "assistant" {
			if summary, ok := buildHiddenToolActivitySummary(segment, item.TS); ok {
				summaries[item.Seq] = summary
			}
			segment = segment[:0]
			continue
		}
		if sessionMessageIsActivityEvent(item, kind) {
			segment = append(segment, item)
		}
	}
	return summaries
}

func buildHiddenToolActivitySummary(events []SessionMessage, assistantTS float64) (sessionToolActivitySummary, bool) {
	if len(events) == 0 {
		return sessionToolActivitySummary{}, false
	}
	summary := sessionToolActivitySummary{Operations: len(events)}
	callsByID := make(map[string]*toolCallSummaryState)
	pendingWithoutID := make([]*toolCallSummaryState, 0)
	for i, event := range events {
		kind := sessionMessageDisplayKind(event)
		if event.TS > 0 {
			if summary.StartedAt == 0 || event.TS < summary.StartedAt {
				summary.StartedAt = event.TS
			}
			if event.TS > summary.LastActivityAt {
				summary.LastActivityAt = event.TS
			}
		}
		switch kind {
		case "reasoning":
			summary.Reasoning++
		case "todo_snapshot":
			summary.TodoSnapshots++
		case "custom_message":
			summary.ProcessUpdates++
		case "pi_event", "event", "error":
			summary.SystemEvents++
			if event.IsError {
				summary.Failed++
			}
		case "tool":
			summary.ToolCalls++
			id := strings.TrimSpace(event.ToolCallID)
			state := &toolCallSummaryState{
				id:        id,
				name:      sessionMessageToolName(event),
				startedAt: event.TS,
			}
			if id != "" {
				callsByID[id] = state
			} else {
				state.id = fallbackToolActivityID(i)
				pendingWithoutID = append(pendingWithoutID, state)
			}
		case "tool_result":
			summary.ToolResults++
			state := matchingToolCallSummaryState(event, callsByID, pendingWithoutID, i)
			state.resultAt = event.TS
			state.failed = event.IsError
			if state.name == "" {
				state.name = sessionMessageToolName(event)
			}
		}
	}
	calls := make([]*toolCallSummaryState, 0, len(callsByID)+len(pendingWithoutID))
	for _, call := range callsByID {
		calls = append(calls, call)
	}
	calls = append(calls, pendingWithoutID...)
	summary.TotalTools = len(calls)
	seenRunningNames := make(map[string]bool)
	for _, call := range calls {
		if call.resultAt > 0 {
			if call.failed {
				summary.Failed++
			} else {
				summary.OK++
			}
			if call.startedAt > 0 {
				elapsed := call.resultAt - call.startedAt
				if elapsed > summary.MaxToolCallSeconds {
					summary.MaxToolCallSeconds = elapsed
				}
			}
			continue
		}
		summary.Running++
		name := strings.TrimSpace(call.name)
		if name != "" && !seenRunningNames[name] {
			seenRunningNames[name] = true
			summary.RunningToolNames = append(summary.RunningToolNames, name)
		}
	}
	if summary.LastActivityAt == 0 && assistantTS > 0 {
		summary.LastActivityAt = assistantTS
	}
	if summary.StartedAt > 0 {
		end := summary.LastActivityAt
		if assistantTS > end {
			end = assistantTS
		}
		if end > summary.StartedAt {
			summary.ElapsedSeconds = end - summary.StartedAt
		}
	}
	summary.SummaryText = formatToolActivitySummaryText(summary)
	summary.StatusText = formatToolActivityStatusText(summary)
	return summary, summary.TotalTools > 0 || summary.Reasoning > 0 || summary.TodoSnapshots > 0 || summary.ProcessUpdates > 0 || summary.SystemEvents > 0
}

func matchingToolCallSummaryState(event SessionMessage, callsByID map[string]*toolCallSummaryState, pendingWithoutID []*toolCallSummaryState, index int) *toolCallSummaryState {
	id := strings.TrimSpace(event.ToolCallID)
	if id != "" {
		if call, ok := callsByID[id]; ok {
			return call
		}
		call := &toolCallSummaryState{id: id, name: sessionMessageToolName(event)}
		callsByID[id] = call
		return call
	}
	for _, call := range pendingWithoutID {
		if call.resultAt == 0 {
			return call
		}
	}
	call := &toolCallSummaryState{id: fallbackToolActivityID(index), name: sessionMessageToolName(event)}
	callsByID[call.id] = call
	return call
}

func sessionMessageIsActivityEvent(item SessionMessage, kind string) bool {
	switch kind {
	case "tool", "tool_result", "reasoning", "todo_snapshot", "custom_message", "pi_event", "event", "error":
		return true
	default:
		return false
	}
}

func sessionMessageDisplayKind(item SessionMessage) string {
	if item.IsError && strings.TrimSpace(item.Type) != "" {
		return strings.TrimSpace(item.Type)
	}
	if strings.TrimSpace(item.Role) != "" {
		return strings.TrimSpace(item.Role)
	}
	if strings.TrimSpace(item.Type) != "" {
		return strings.TrimSpace(item.Type)
	}
	return strings.TrimSpace(item.Kind)
}

func sessionMessageToolName(item SessionMessage) string {
	if name := strings.TrimSpace(item.Name); name != "" {
		return name
	}
	if name := strings.TrimSpace(item.Summary); name != "" {
		return name
	}
	if name := strings.TrimSpace(item.Text); name != "" {
		return name
	}
	return "tool"
}

func cloneSessionMessageDetails(details map[string]any) map[string]any {
	out := make(map[string]any, len(details)+1)
	for key, value := range details {
		out[key] = value
	}
	return out
}

func fallbackToolActivityID(index int) string {
	return "fallback:" + strconvItoa(index)
}

func formatToolActivitySummaryText(summary sessionToolActivitySummary) string {
	parts := make([]string, 0, 6)
	if summary.TotalTools > 0 {
		if summary.Running > 0 {
			parts = append(parts, "Running "+strconvItoa(summary.Running)+"/"+strconvItoa(summary.TotalTools)+" "+pluralTool(summary.TotalTools))
		} else {
			parts = append(parts, "Ran "+strconvItoa(summary.TotalTools)+" "+pluralTool(summary.TotalTools))
		}
	} else {
		parts = append(parts, "Activity")
	}
	if summary.OK > 0 {
		parts = append(parts, strconvItoa(summary.OK)+" ok")
	}
	if summary.Failed > 0 {
		parts = append(parts, strconvItoa(summary.Failed)+" failed")
	}
	if summary.Reasoning > 0 {
		parts = append(parts, strconvItoa(summary.Reasoning)+" reasoning")
	}
	if summary.TodoSnapshots > 0 {
		parts = append(parts, strconvItoa(summary.TodoSnapshots)+" todo")
	}
	if summary.ProcessUpdates > 0 {
		parts = append(parts, strconvItoa(summary.ProcessUpdates)+" process")
	}
	if summary.SystemEvents > 0 {
		parts = append(parts, strconvItoa(summary.SystemEvents)+" system")
	}
	if summary.ElapsedSeconds > 0 {
		parts = append(parts, formatToolActivityDuration(summary.ElapsedSeconds))
	}
	return strings.Join(parts, " · ")
}

func formatToolActivityStatusText(summary sessionToolActivitySummary) string {
	if summary.Running > 0 {
		if len(summary.RunningToolNames) > 0 {
			return "running: " + strings.Join(summary.RunningToolNames, ", ")
		}
		return "running " + strconvItoa(summary.Running)
	}
	if summary.Failed > 0 {
		return strconvItoa(summary.Failed) + " failed"
	}
	if summary.TotalTools > 0 {
		return strconvItoa(summary.OK) + " completed"
	}
	if summary.Operations > 0 {
		return strconvItoa(summary.Operations) + " operations"
	}
	return "complete"
}

func pluralTool(count int) string {
	if count == 1 {
		return "tool"
	}
	return "tools"
}

func formatToolActivityDuration(seconds float64) string {
	total := int(seconds)
	if total < 60 {
		return strconvItoa(total) + "s"
	}
	minutes := total / 60
	if minutes < 60 {
		if total%60 == 0 {
			return strconvItoa(minutes) + "m"
		}
		return strconvItoa(minutes) + "m" + strconvItoa(total%60) + "s"
	}
	hours := minutes / 60
	if minutes%60 == 0 {
		return strconvItoa(hours) + "h"
	}
	return strconvItoa(hours) + "h" + strconvItoa(minutes%60) + "m"
}

func strconvItoa(value int) string {
	return strconv.FormatInt(int64(value), 10)
}
