package app

import (
	"context"
	"net"
	"testing"
	"time"

	"actrail/internal/adapters/agent"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
	piagentv1 "actrail/proto/pi/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type commandPiAgentServer struct {
	piagentv1.UnimplementedPiAgentServer
	executedName string
	executedArgs string
}

func (s *commandPiAgentServer) GetState(context.Context, *piagentv1.GetStateRequest) (*piagentv1.SessionState, error) {
	return &piagentv1.SessionState{SessionId: "pi-grpc-commands-test"}, nil
}

func (s *commandPiAgentServer) ListCommands(context.Context, *piagentv1.ListCommandsRequest) (*piagentv1.ListCommandsResponse, error) {
	return &piagentv1.ListCommandsResponse{Commands: []*piagentv1.SlashCommand{
		{
			Name:        "review",
			Description: "Review current diff",
			Source:      "prompt",
			SourceInfo: &piagentv1.SourceInfo{
				Path:    "/tmp/review.md",
				Source:  "project",
				Scope:   "project",
				Origin:  "top-level",
				BaseDir: "/tmp",
			},
		},
	}}, nil
}

func (s *commandPiAgentServer) ExecuteCommand(_ context.Context, req *piagentv1.ExecuteCommandRequest) (*piagentv1.CommandAck, error) {
	s.executedName = req.GetName()
	s.executedArgs = req.GetArgs()
	return &piagentv1.CommandAck{}, nil
}

func newGRPCCommandSessionFixture(t *testing.T) (*Stub, session.SessionID, *commandPiAgentServer) {
	t.Helper()
	server := &commandPiAgentServer{}
	grpcServer := grpc.NewServer()
	piagentv1.RegisterPiAgentServer(grpcServer, server)
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	go func() { _ = grpcServer.Serve(listener) }()

	runner := &process.FakeRunner{}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Catalog: agent.DefaultCatalog(),
		Runner:  runner,
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/custom-pi", nil
		},
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}), grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
	})
	useGRPC := true
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend: "pi",
		CWD:          "/root/code/ActRail",
		PIAgentGRPC:  &useGRPC,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if created.Session == nil {
		t.Fatal("CreateSession().Session = nil")
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	return svc, sessionID, server
}

func TestSessionCommandsIncludesPIAgentGRPCCommands(t *testing.T) {
	svc, sessionID, _ := newGRPCCommandSessionFixture(t)

	resp, err := svc.SessionCommands(context.Background(), SessionCommandsRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionCommands() error = %v", err)
	}
	var found *SessionCommand
	for i := range resp.Commands {
		if resp.Commands[i].Name == "review" {
			found = &resp.Commands[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("commands = %+v, want review", resp.Commands)
	}
	if found.Source != "prompt" || found.Description != "Review current diff" {
		t.Fatalf("review command = %+v", *found)
	}
	if found.SourceInfo["path"] != "/tmp/review.md" || found.SourceInfo["base_dir"] != "/tmp" {
		t.Fatalf("source info = %+v", found.SourceInfo)
	}
}

func TestExecuteSessionCommandUsesPIAgentGRPC(t *testing.T) {
	svc, sessionID, server := newGRPCCommandSessionFixture(t)

	resp, err := svc.ExecuteSessionCommand(context.Background(), ExecuteSessionCommandRequest{SessionID: sessionID, Name: "/review", Args: "current diff"})
	if err != nil {
		t.Fatalf("ExecuteSessionCommand() error = %v", err)
	}
	if !resp.OK || resp.Message != "executed by runtime" {
		t.Fatalf("ExecuteSessionCommand() = %+v", resp)
	}
	if server.executedName != "review" || server.executedArgs != "current diff" {
		t.Fatalf("executed = (%q, %q)", server.executedName, server.executedArgs)
	}
}
