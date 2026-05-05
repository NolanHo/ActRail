package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"actrail/internal/adapters/process"
	"actrail/internal/app"
	"actrail/internal/config"
)

type testPTY struct {
	bytes.Buffer
}

func (p *testPTY) Close() error                 { return nil }
func (p *testPTY) Resize(process.PTYSize) error { return nil }

func TestTeamProtocolE2EWithDefaultActRailClient(t *testing.T) {
	var childPTY *testPTY
	runner := &process.FakeRunner{HandleBuild: func(spec process.LaunchSpec) process.Handle {
		h := process.NewFakeHandle(spec)
		pty := &testPTY{}
		h.SetPTY(pty)
		childPTY = pty
		return h
	}}
	svc := app.NewStubForTest(config.Load(), time.Now, app.RuntimeConfig{Runner: runner})
	parent, err := svc.CreateSession(context.Background(), app.CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	h := newTestRouter(config.Load(), svc)
	server := httptest.NewServer(h)
	defer server.Close()

	spawn := map[string]any{
		"protocol":        "pi.team.v1",
		"type":            "team.spawn",
		"requestId":       "req_spawn",
		"parentSessionId": parent.Session.SessionID,
		"name":            "worker",
		"role":            "review",
		"cwd":             t.TempDir(),
		"initialPrompt":   "inspect",
	}
	spawnBody, _ := json.Marshal(spawn)
	res, err := http.Post(server.URL+"/team/command", "application/json", bytes.NewReader(spawnBody))
	if err != nil {
		t.Fatalf("POST /team/command error = %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /team/command status = %d", res.StatusCode)
	}
	var commandResult struct {
		RequestID      string `json:"requestId"`
		ActorID        string `json:"actorId"`
		ChildSessionID string `json:"childSessionId"`
		Status         string `json:"status"`
		TurnID         string `json:"turnId"`
	}
	if err := json.NewDecoder(res.Body).Decode(&commandResult); err != nil {
		t.Fatalf("decode command result: %v", err)
	}
	if commandResult.RequestID != "req_spawn" || commandResult.ActorID == "" || commandResult.ChildSessionID == "" || commandResult.TurnID == "" {
		t.Fatalf("command result = %+v, want actor, child session, turn", commandResult)
	}
	if childPTY != nil && childPTY.String() != "" && !strings.Contains(childPTY.String(), "inspect") {
		t.Fatalf("child runtime input = %q, want initial prompt when runtime uses PTY", childPTY.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/team/events?actorId="+commandResult.ActorID, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	eventRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /team/events error = %v", err)
	}
	defer eventRes.Body.Close()
	if eventRes.StatusCode != http.StatusOK {
		t.Fatalf("GET /team/events status = %d", eventRes.StatusCode)
	}
	scanner := bufio.NewScanner(eventRes.Body)
	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 3 {
		lineCh := make(chan string, 1)
		go func() {
			if scanner.Scan() {
				lineCh <- scanner.Text()
				return
			}
			lineCh <- ""
		}()
		select {
		case <-deadline:
			cancel()
			t.Fatalf("timed out waiting for team events; seen=%v", seen)
		case line := <-lineCh:
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event struct {
				Protocol string `json:"protocol"`
				Type     string `json:"type"`
				ActorID  string `json:"actorId"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatalf("decode event %q: %v", line, err)
			}
			if event.Protocol != "pi.team.v1" || event.ActorID != commandResult.ActorID {
				t.Fatalf("event = %+v, want pi.team.v1 actor %s", event, commandResult.ActorID)
			}
			seen[event.Type] = true
		}
	}
	for _, typ := range []string{"team.started", "team.turn_started", "team.status"} {
		if !seen[typ] {
			t.Fatalf("missing event type %s in %v", typ, seen)
		}
	}
}

func boolPtr(v bool) *bool { return &v }
