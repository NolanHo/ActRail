package app

import (
	"context"
	"testing"
	"time"

	"actrail/internal/config"
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

func TestSubagentNodeCountsAndClosedFilter(t *testing.T) {
	nodes := []SubagentNode{{
		ActorID: "lead",
		Status:  SubagentStatusRunning,
		Children: []SubagentNode{
			{ActorID: "leaf", Status: SubagentStatusIdle},
			{ActorID: "closed", Status: SubagentStatusClosed},
		},
	}}
	visible := filterClosedSubagentNodes(nodes, false)
	if got := countSubagentNodes(visible); got != 2 {
		t.Fatalf("countSubagentNodes(visible) = %d, want 2", got)
	}
	if got := countNonLeafSubagentNodes(visible); got != 1 {
		t.Fatalf("countNonLeafSubagentNodes(visible) = %d, want 1", got)
	}
	withClosed := filterClosedSubagentNodes(nodes, true)
	if got := countSubagentNodes(withClosed); got != 3 {
		t.Fatalf("countSubagentNodes(withClosed) = %d, want 3", got)
	}
}
