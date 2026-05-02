package app

import (
	"context"
	"time"
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

func (s *Stub) ListSubagents(_ context.Context, req ListSubagentsRequest) (ListSubagentsResponse, error) {
	roots := filterClosedSubagentNodes(nil, req.IncludeClosed)
	return ListSubagentsResponse{OK: true, Roots: roots, TotalCount: countSubagentNodes(roots), NonLeafCount: countNonLeafSubagentNodes(roots)}, nil
}

func filterClosedSubagentNodes(nodes []SubagentNode, includeClosed bool) []SubagentNode {
	if includeClosed {
		return cloneSubagentNodes(nodes)
	}
	out := make([]SubagentNode, 0, len(nodes))
	for _, node := range nodes {
		if node.Status == SubagentStatusClosed {
			continue
		}
		node.Children = filterClosedSubagentNodes(node.Children, false)
		node.Messages = append([]SubagentThreadMsg(nil), node.Messages...)
		out = append(out, node)
	}
	return out
}

func cloneSubagentNodes(nodes []SubagentNode) []SubagentNode {
	out := make([]SubagentNode, len(nodes))
	for i, node := range nodes {
		out[i] = node
		out[i].Children = cloneSubagentNodes(node.Children)
		out[i].Messages = append([]SubagentThreadMsg(nil), node.Messages...)
	}
	return out
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
