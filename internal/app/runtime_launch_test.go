package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"actrail/internal/adapters/agent"
	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
	piagentv1 "actrail/proto/pi/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type launchAdapter struct {
	backend session.Backend
	args    []string
}

type fakePiAgentServer struct {
	piagentv1.UnimplementedPiAgentServer
	states func(context.Context) (*piagentv1.SessionState, error)
}

func (s fakePiAgentServer) GetState(ctx context.Context, _ *piagentv1.GetStateRequest) (*piagentv1.SessionState, error) {
	if s.states != nil {
		return s.states(ctx)
	}
	return &piagentv1.SessionState{SessionId: "pi-grpc-test", RuntimeState: piagentv1.RuntimeState_RUNTIME_STATE_READY, RuntimeStatusMessage: "ready"}, nil
}

func (fakePiAgentServer) Prompt(context.Context, *piagentv1.PromptRequest) (*piagentv1.CommandAck, error) {
	return &piagentv1.CommandAck{}, nil
}

func (a launchAdapter) Backend() session.Backend {
	return a.backend
}

func (a launchAdapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (a launchAdapter) ValidateOptions(agent.Options) error {
	return nil
}

func (a launchAdapter) CommandArgs(agent.Options) ([]string, error) {
	return append([]string(nil), a.args...), nil
}

func TestCreateSessionLaunchesRuntimeThroughInjectedCatalogAndRunner(t *testing.T) {
	catalog, err := agent.NewCatalog(launchAdapter{backend: session.BackendPI, args: []string{"serve", "--session"}})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPID(321)
	runner := &process.FakeRunner{NextHandle: handle}
	now := time.Unix(1760000000, 0).UTC()
	svc := newStubWithRuntime(config.Load(), func() time.Time { return now }, RuntimeConfig{
		Catalog: catalog,
		Runner:  runner,
		ResolveBinPath: func(backend session.Backend) (string, error) {
			if backend != session.BackendPI {
				t.Fatalf("ResolveBinPath() backend = %q, want %q", backend, session.BackendPI)
			}
			return "/tmp/custom-pi", nil
		},
	})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend: "pi",
		PIAgentGRPC:  boolPtr(false),
		CWD:          "/root/code/ActRail",
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if !created.OK || created.Session == nil {
		t.Fatalf("CreateSession() = %+v, want ok session payload", created)
	}
	if len(runner.Starts) != 1 {
		t.Fatalf("len(runner.Starts) = %d, want 1", len(runner.Starts))
	}
	start := runner.Starts[0]
	if start.Command().Path() != "/tmp/custom-pi" {
		t.Fatalf("launch command path = %q, want %q", start.Command().Path(), "/tmp/custom-pi")
	}
	if got := start.Command().Args(); len(got) != 2 || got[0] != "serve" || got[1] != "--session" {
		t.Fatalf("launch args = %#v, want [serve --session]", got)
	}
	if start.CWD().String() != "/root/code/ActRail" {
		t.Fatalf("launch cwd = %q, want %q", start.CWD().String(), "/root/code/ActRail")
	}
	wantMode := process.IOModePipes
	if preferRuntimePTY() {
		wantMode = process.IOModePTY
	}
	if start.IO().Mode() != wantMode {
		t.Fatalf("launch io mode = %q, want %q", start.IO().Mode(), wantMode)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	record, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatal("Lookup() ok = false, want true")
	}
	if record.runtime.PID() != 321 {
		t.Fatalf("record.runtime.PID() = %d, want 321", record.runtime.PID())
	}
	if record.runtime.launchSpec.Command().Path() != "/tmp/custom-pi" {
		t.Fatalf("record.runtime.launchSpec.Command().Path() = %q, want %q", record.runtime.launchSpec.Command().Path(), "/tmp/custom-pi")
	}
}

func TestCreateSessionDefaultsPIAgentToGRPC(t *testing.T) {
	catalog := agent.DefaultCatalog()
	runner := &process.FakeRunner{}
	grpcServer := grpc.NewServer()
	piagentv1.RegisterPiAgentServer(grpcServer, fakePiAgentServer{})
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	go func() { _ = grpcServer.Serve(listener) }()
	dialTargets := make(chan string, 4)
	allowDial := make(chan struct{})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Catalog: catalog,
		Runner:  runner,
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/custom-pi", nil
		},
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(ctx context.Context, target string) (*grpc.ClientConn, error) {
			select {
			case dialTargets <- target:
			default:
			}
			select {
			case <-allowDial:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}), grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
	})
	type createResult struct {
		created CreateSessionResponse
		err     error
	}
	createDone := make(chan createResult, 1)
	go func() {
		created, err := svc.CreateSession(context.Background(), CreateSessionRequest{
			AgentBackend: "pi",
			CWD:          "/root/code/ActRail",
		})
		createDone <- createResult{created: created, err: err}
	}()
	var result createResult
	select {
	case result = <-createDone:
	case <-dialTargets:
		select {
		case result = <-createDone:
		case <-time.After(200 * time.Millisecond):
			close(allowDial)
			t.Fatal("CreateSession blocked on pi agent grpc readiness")
		}
	case <-time.After(200 * time.Millisecond):
		close(allowDial)
		t.Fatal("CreateSession did not return")
	}
	if result.err != nil {
		t.Fatalf("CreateSession() error = %v", result.err)
	}
	created := result.created
	if created.Session == nil {
		t.Fatal("CreateSession().Session = nil")
	}
	if len(runner.Starts) != 1 {
		t.Fatalf("len(runner.Starts) = %d, want 1", len(runner.Starts))
	}
	if !runner.Starts[0].Detached() {
		t.Fatal("grpc launch Detached() = false, want true so server restart does not kill runtime")
	}
	if runner.Starts[0].IO().Mode() != process.IOModePipes {
		t.Fatalf("grpc launch IO mode = %q, want %q so server-owned PTY close does not kill runtime", runner.Starts[0].IO().Mode(), process.IOModePipes)
	}
	args := runner.Starts[0].Command().Args()
	wantPrefix := []string{"--mode", "grpc", "--grpc-socket", "/tmp/custom-pi-agent.sock"}
	if !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("launch args prefix = %#v, want %#v", args[:len(wantPrefix)], wantPrefix)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	record, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatal("Lookup() ok = false")
	}
	if record.runtime.helper != nil {
		t.Fatal("record.runtime.helper != nil; grpc path must not use IOD helper")
	}
	if record.runtime.piAgentGRPC == nil {
		t.Fatal("record.runtime.piAgentGRPC = nil")
	}
	if created.Session.TransportState != SessionTransportStateStarting.String() || !created.Session.PendingStartup {
		t.Fatalf("CreateSession().Session transport = (%q, pending=%v), want starting pending", created.Session.TransportState, created.Session.PendingStartup)
	}
	close(allowDial)
	if err := record.runtime.WaitForPIAgentGRPCReady(context.Background()); err != nil {
		t.Fatalf("WaitForPIAgentGRPCReady() error = %v", err)
	}
	var resolvedTarget string
	select {
	case resolvedTarget = <-dialTargets:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for grpc dial")
	}
	if resolvedTarget != "unix:///tmp/custom-pi-agent.sock" {
		t.Fatalf("grpc target = %q, want custom target", resolvedTarget)
	}
	listed, err := svc.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("len(ListSessions().Items) = %d, want 1", len(listed.Items))
	}
	if listed.Items[0].IOD == nil || listed.Items[0].IOD.Mode != "grpc" {
		t.Fatalf("ListSessions().Items[0].IOD = %+v, want grpc mode", listed.Items[0].IOD)
	}
}

func TestCreateSessionCanOptOutOfPIAgentGRPC(t *testing.T) {
	runner := &process.FakeRunner{}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})
	useGRPC := false
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir(), PIAgentGRPC: &useGRPC})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	record, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatalf("Lookup(%q) ok = false", sessionID)
	}
	if record.runtime.piAgentGRPC != nil {
		t.Fatal("record.runtime.piAgentGRPC != nil, want std/IOD mode")
	}
	if record.runtime.helper == nil && record.runtime.handle == nil {
		t.Fatal("record.runtime has no std transport")
	}
	if created.Session.TransportState != string(SessionTransportStateAttached) {
		t.Fatalf("CreateSession().Session.TransportState = %q, want attached", created.Session.TransportState)
	}
}

func TestCreateSessionAttachesAfterTransientPIAgentGRPCStartupDialErrors(t *testing.T) {
	runner := &process.FakeRunner{}
	grpcServer := grpc.NewServer()
	var calls atomic.Int32
	piagentv1.RegisterPiAgentServer(grpcServer, fakePiAgentServer{states: func(context.Context) (*piagentv1.SessionState, error) {
		if calls.Add(1) <= 2 {
			return nil, errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: Error while dialing: dial unix /tmp/pi-agent/actrail/s_1.sock: connect: no such file or directory"`)
		}
		return &piagentv1.SessionState{SessionId: "pi-grpc-test", RuntimeState: piagentv1.RuntimeState_RUNTIME_STATE_READY, RuntimeStatusMessage: "ready"}, nil
	}})
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	go func() { _ = grpcServer.Serve(listener) }()
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Runner:                  runner,
		PIAgentGRPCReadyTimeout: time.Second,
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
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	assertCtx, cancelAssert := context.WithTimeout(context.Background(), time.Second)
	defer cancelAssert()
	for {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			t.Fatalf("SessionState() error = %v", err)
		}
		if state.Transport.State == SessionTransportStateAttached {
			return
		}
		select {
		case <-assertCtx.Done():
			t.Fatalf("transport state = %q, want attached", state.Transport.State)
		case <-time.After(helperReadyPollInterval):
		}
	}
}

func TestCreateSessionMarksPIAgentGRPCFailedWhenStartupDialNeverRecovers(t *testing.T) {
	runner := &process.FakeRunner{}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Runner:                  runner,
		PIAgentGRPCReadyTimeout: 50 * time.Millisecond,
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/custom-pi", nil
		},
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return nil, errors.New(`rpc error: code = Unavailable desc = connection error: desc = "transport: Error while dialing: dial unix /tmp/pi-agent/actrail/s_1.sock: connect: no such file or directory"`)
		},
	})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	assertCtx, cancelAssert := context.WithTimeout(context.Background(), time.Second)
	defer cancelAssert()
	for {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			t.Fatalf("SessionState() error = %v", err)
		}
		if state.Transport.State == SessionTransportStateFailed {
			if !strings.Contains(state.Transport.Reason, "wait for pi agent grpc") {
				t.Fatalf("failed transport reason = %q, want readiness timeout", state.Transport.Reason)
			}
			return
		}
		select {
		case <-assertCtx.Done():
			t.Fatalf("transport state = %q, want failed", state.Transport.State)
		case <-time.After(helperReadyPollInterval):
		}
	}
}

func TestCreateSessionMarksPIAgentGRPCHardDialFailureImmediately(t *testing.T) {
	runner := &process.FakeRunner{}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Runner:                  runner,
		PIAgentGRPCReadyTimeout: time.Second,
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/custom-pi", nil
		},
		PIAgentGRPCTarget: "unix:///tmp/custom-pi-agent.sock",
		PIAgentGRPCDialer: func(context.Context, string) (*grpc.ClientConn, error) {
			return nil, context.Canceled
		},
	})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	assertCtx, cancelAssert := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelAssert()
	for {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			t.Fatalf("SessionState() error = %v", err)
		}
		if state.Transport.State == SessionTransportStateFailed {
			if !strings.Contains(state.Transport.Reason, "context canceled") {
				t.Fatalf("failed transport reason = %q, want canceled error", state.Transport.Reason)
			}
			return
		}
		select {
		case <-assertCtx.Done():
			t.Fatalf("transport state = %q, want failed", state.Transport.State)
		case <-time.After(helperReadyPollInterval):
		}
	}
}

func TestCreateSessionDoesNotStoreMetadataWhenLaunchFails(t *testing.T) {
	runner := &process.FakeRunner{StartErr: context.DeadlineExceeded}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	_, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend: "pi",
		PIAgentGRPC:  boolPtr(false),
		CWD:          "/root/code/ActRail",
	})
	if err == nil {
		t.Fatal("CreateSession() error = nil, want launch failure")
	}
	if items := svc.registry.List(); len(items) != 0 {
		t.Fatalf("len(registry.List()) = %d, want 0 after failed launch", len(items))
	}
}

func TestHelperLaunchSpecEncodesTransparentChildLaunchContract(t *testing.T) {
	envVarA, err := process.NewEnvVar("ACTRAIL_ALPHA", "one")
	if err != nil {
		t.Fatalf("NewEnvVar(ACTRAIL_ALPHA) error = %v", err)
	}
	envVarB, err := process.NewEnvVar("ACTRAIL_BRAVO", "two=2")
	if err != nil {
		t.Fatalf("NewEnvVar(ACTRAIL_BRAVO) error = %v", err)
	}
	childEnv, err := process.ReplaceEnv(envVarA, envVarB)
	if err != nil {
		t.Fatalf("ReplaceEnv() error = %v", err)
	}
	ioSpec, err := process.PipeIO(process.LogPaths{})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	launcherEnv, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	launcher := processRuntimeLauncher{iodRuntimeRoot: "/tmp/actrail-data/runtime/iod", env: launcherEnv}
	sessionID := mustSessionID(t, "s_helper_contract")
	generationID, err := iod.NewGenerationID("g_helper_contract")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	piChild := mustLaunchSpecForHelperContractTest(t, "/tmp/pi", []string{"--mode", "rpc"}, "/tmp/project-pi", childEnv, ioSpec)
	paths, err := iod.NewGenerationPaths(launcher.iodRuntimeRoot, sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	codexChild := mustLaunchSpecForHelperContractTest(t, "/tmp/codex", []string{"-c", `model_reasoning_effort="high"`, "app-server", "--listen", "unix://" + paths.ChildSocketPath}, "/tmp/project-codex", childEnv, ioSpec)

	piSpec, err := launcher.helperLaunchSpec(runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendPI, CWD: "/tmp/project-pi"}, "/tmp/actrail-iod", generationID, piChild)
	if err != nil {
		t.Fatalf("helperLaunchSpec(pi) error = %v", err)
	}
	codexSpec, err := launcher.helperLaunchSpec(runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: "/tmp/project-codex", CodexThreadID: "thread-helper-contract"}, "/tmp/actrail-iod", generationID, codexChild)
	if err != nil {
		t.Fatalf("helperLaunchSpec(codex) error = %v", err)
	}

	assertHelperLaunchContract(t, piSpec.Command().Args(), piChild, iod.ChildIOModeStdio)
	assertHelperLaunchContract(t, codexSpec.Command().Args(), codexChild, iod.ChildIOModeUnix, "thread-helper-contract")
	if !reflect.DeepEqual(piSpec.Environment().Vars(), launcherEnv.Vars()) {
		t.Fatalf("helper environment vars = %#v, want launcher env vars %#v", piSpec.Environment().Vars(), launcherEnv.Vars())
	}
	if piSpec.Environment().Mode() != launcherEnv.Mode() {
		t.Fatalf("helper environment mode = %q, want %q", piSpec.Environment().Mode(), launcherEnv.Mode())
	}
	if !piSpec.Detached() || !codexSpec.Detached() {
		t.Fatalf("helper launch detached = (%t, %t), want both true", piSpec.Detached(), codexSpec.Detached())
	}
	if codexSpec.CWD().String() != "/tmp/project-codex" {
		t.Fatalf("codex helper cwd = %q, want %q", codexSpec.CWD().String(), "/tmp/project-codex")
	}
}

func TestCodexChildLaunchSpecUsesUniqueIODSocketPerGeneration(t *testing.T) {
	launcher := processRuntimeLauncher{iodRuntimeRoot: "/tmp/actrail-data/runtime/iod", catalog: agent.DefaultCatalog(), codexDangerousBypass: true}
	env, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	launcher.env = env
	ioSpec, err := process.PipeIO(process.LogPaths{})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	launcher.io = ioSpec
	launcher.resolveBinPath = func(backend session.Backend) (string, error) {
		if backend != session.BackendCodex {
			t.Fatalf("ResolveBinPath() backend = %q, want codex", backend)
		}
		return "/tmp/codex", nil
	}

	firstSession := mustSessionID(t, "s_codex_socket_1")
	secondSession := mustSessionID(t, "s_codex_socket_2")
	generationID, err := iod.NewGenerationID("g_codex_socket")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	firstPaths, err := iod.NewGenerationPaths(launcher.iodRuntimeRoot, firstSession, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths(first) error = %v", err)
	}
	secondPaths, err := iod.NewGenerationPaths(launcher.iodRuntimeRoot, secondSession, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths(second) error = %v", err)
	}

	firstSpec, err := launcher.childLaunchSpecForGeneration(runtimeLaunchRequest{SessionID: firstSession, Backend: session.BackendCodex, CWD: "/tmp/project"}, &firstPaths)
	if err != nil {
		t.Fatalf("childLaunchSpecForGeneration(first) error = %v", err)
	}
	secondSpec, err := launcher.childLaunchSpecForGeneration(runtimeLaunchRequest{SessionID: secondSession, Backend: session.BackendCodex, CWD: "/tmp/project"}, &secondPaths)
	if err != nil {
		t.Fatalf("childLaunchSpecForGeneration(second) error = %v", err)
	}

	firstListen := listenURLFromArgs(firstSpec.Command().Args())
	secondListen := listenURLFromArgs(secondSpec.Command().Args())
	if firstListen == "" || secondListen == "" {
		t.Fatalf("listen urls = (%q, %q), want both set", firstListen, secondListen)
	}
	if firstListen == secondListen {
		t.Fatalf("listen url = %q for both sessions, want unique per session", firstListen)
	}
	if firstListen != "unix://"+firstPaths.ChildSocketPath || secondListen != "unix://"+secondPaths.ChildSocketPath {
		t.Fatalf("listen urls = (%q, %q), want (%q, %q)", firstListen, secondListen, "unix://"+firstPaths.ChildSocketPath, "unix://"+secondPaths.ChildSocketPath)
	}
}

func listenURLFromArgs(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--listen" {
			return args[i+1]
		}
	}
	return ""
}

func TestNewRuntimeLauncherResolvesIODRuntimeRootToAbsolutePath(t *testing.T) {
	launcher := newRuntimeLauncher(RuntimeConfig{IODRuntimeRoot: "./data/runtime/iod", UseIODHelper: true})
	processLauncher, ok := launcher.(processRuntimeLauncher)
	if !ok {
		t.Fatalf("launcher type = %T, want processRuntimeLauncher", launcher)
	}
	if !filepath.IsAbs(processLauncher.iodRuntimeRoot) {
		t.Fatalf("iod runtime root = %q, want absolute path", processLauncher.iodRuntimeRoot)
	}
	want, err := filepath.Abs("./data/runtime/iod")
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	if processLauncher.iodRuntimeRoot != want {
		t.Fatalf("iod runtime root = %q, want %q", processLauncher.iodRuntimeRoot, want)
	}
}

func TestDefaultRuntimeGenerationIDUsesStableCurrentSlot(t *testing.T) {
	sessionID := mustSessionID(t, "s_stable_generation")
	first, err := defaultRuntimeGenerationID(sessionID)
	if err != nil {
		t.Fatalf("defaultRuntimeGenerationID(first) error = %v", err)
	}
	second, err := defaultRuntimeGenerationID(sessionID)
	if err != nil {
		t.Fatalf("defaultRuntimeGenerationID(second) error = %v", err)
	}
	if first.String() != defaultHelperGeneration || second != first {
		t.Fatalf("default generation ids = (%q, %q), want stable %q", first, second, defaultHelperGeneration)
	}
}

func TestRuntimeLauncherCleansStableGenerationArtifactsBeforeStartingNewHelper(t *testing.T) {
	sessionID := mustSessionID(t, "s_clean_stable_generation")
	runtimeRoot := t.TempDir()
	generationID, err := defaultRuntimeGenerationID(sessionID)
	if err != nil {
		t.Fatalf("defaultRuntimeGenerationID() error = %v", err)
	}
	paths, err := iod.NewGenerationPaths(runtimeRoot, sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	staleFile := filepath.Join(paths.RuntimeDir, "stale.sock")
	if err := os.MkdirAll(paths.RuntimeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", paths.RuntimeDir, err)
	}
	if err := os.WriteFile(staleFile, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", staleFile, err)
	}
	startErr := errors.New("stop before helper start")
	launcher := newRuntimeLauncher(RuntimeConfig{
		IODRuntimeRoot:          runtimeRoot,
		UseIODHelper:            true,
		Runner:                  &process.FakeRunner{StartErr: startErr},
		ResolveIODHelperBinPath: func() (string, error) { return "/tmp/actrail-iod", nil },
		ResolveBinPath:          func(session.Backend) (string, error) { return "/tmp/codex", nil },
	})

	_, err = launcher.Launch(context.Background(), runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: t.TempDir()})
	if !errors.Is(err, startErr) {
		t.Fatalf("Launch() error = %v, want %v", err, startErr)
	}
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want stale stable-generation artifact removed", staleFile, err)
	}
}

func TestRuntimeLauncherShutsDownStaleStableGenerationManifestBeforeReuse(t *testing.T) {
	sessionID := mustSessionID(t, "s_shutdown_stable_generation")
	runtimeRoot := filepath.Join("/tmp", fmt.Sprintf("arslot-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	generationID, err := defaultRuntimeGenerationID(sessionID)
	if err != nil {
		t.Fatalf("defaultRuntimeGenerationID() error = %v", err)
	}
	paths, err := iod.NewGenerationPaths(runtimeRoot, sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	if err := os.MkdirAll(paths.RuntimeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", paths.RuntimeDir, err)
	}
	proof, err := iod.NewHelloProof(999999, nil, paths.WALPath, paths.ControlSocketPath, 1760000006)
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	if err := iod.WriteGenerationManifest(paths.ManifestPath, manifest); err != nil {
		t.Fatalf("WriteGenerationManifest(%q) error = %v", paths.ManifestPath, err)
	}
	startErr := errors.New("stop before helper start")
	launcher := newRuntimeLauncher(RuntimeConfig{
		IODRuntimeRoot:          runtimeRoot,
		UseIODHelper:            true,
		Runner:                  &process.FakeRunner{StartErr: startErr},
		ResolveIODHelperBinPath: func() (string, error) { return "/tmp/actrail-iod", nil },
		ResolveBinPath:          func(session.Backend) (string, error) { return "/tmp/codex", nil },
	})

	_, err = launcher.Launch(context.Background(), runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: t.TempDir(), ForceNewIOD: true})
	if !errors.Is(err, startErr) {
		t.Fatalf("Launch() error = %v, want %v", err, startErr)
	}
	if _, err := os.Stat(paths.ManifestPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want stale stable-generation manifest removed", paths.ManifestPath, err)
	}
}

func TestDefaultRuntimeBinPathFindsCodexFromNVM(t *testing.T) {
	home := t.TempDir()
	codexPath := filepath.Join(home, ".nvm", "versions", "node", "v25.9.0", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(codex bin dir) error = %v", err)
	}
	if err := os.WriteFile(codexPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(codex) error = %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ACTRAIL_CODEX_BIN", "")

	got, err := defaultRuntimeBinPath(session.BackendCodex)
	if err != nil {
		t.Fatalf("defaultRuntimeBinPath(codex) error = %v", err)
	}
	if got != codexPath {
		t.Fatalf("defaultRuntimeBinPath(codex) = %q, want %q", got, codexPath)
	}
}

func TestRuntimeLauncherReturnsHelperExitBeforeReady(t *testing.T) {
	sessionID := mustSessionID(t, "s_helper_exit_before_ready")
	generationID := mustHelperGenerationID(t, "g_helper_exit_before_ready")
	paths, err := iod.NewGenerationPaths(t.TempDir(), sessionID, generationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetWaitResult(process.ExitStatus{Code: 2}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, _, _, err = (processRuntimeLauncher{}).waitForHelperReady(ctx, handle, paths)
	if err == nil {
		t.Fatal("waitForHelperReady() error = nil, want helper exit error")
	}
	if !strings.Contains(err.Error(), "iod helper exited before ready") || !strings.Contains(err.Error(), "exit code 2") {
		t.Fatalf("waitForHelperReady() error = %v, want helper exit code", err)
	}
}

func TestRuntimeLauncherCodexDangerousBypassDefaultsOnAndCanDisable(t *testing.T) {
	sessionID := mustSessionID(t, "s_codex_bypass")
	defaultLauncher := newRuntimeLauncher(RuntimeConfig{
		ResolveBinPath: func(session.Backend) (string, error) { return "/tmp/codex", nil },
	})
	spec, err := defaultLauncher.(processRuntimeLauncher).childLaunchSpec(runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: "/tmp/project"})
	if err != nil {
		t.Fatalf("childLaunchSpec(default) error = %v", err)
	}
	wantDefault := []string{"--dangerously-bypass-approvals-and-sandbox", "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`, "-c", `model_reasoning_effort="high"`, "app-server"}
	if !reflect.DeepEqual(spec.Command().Args(), wantDefault) {
		t.Fatalf("default codex args = %#v, want %#v", spec.Command().Args(), wantDefault)
	}

	disabled := false
	disabledLauncher := newRuntimeLauncher(RuntimeConfig{
		ResolveBinPath:       func(session.Backend) (string, error) { return "/tmp/codex", nil },
		CodexDangerousBypass: &disabled,
	})
	spec, err = disabledLauncher.(processRuntimeLauncher).childLaunchSpec(runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: "/tmp/project"})
	if err != nil {
		t.Fatalf("childLaunchSpec(disabled) error = %v", err)
	}
	wantDisabled := []string{"-c", `model_reasoning_effort="high"`, "app-server"}
	if !reflect.DeepEqual(spec.Command().Args(), wantDisabled) {
		t.Fatalf("disabled codex args = %#v, want %#v", spec.Command().Args(), wantDisabled)
	}
}

func TestCreateSessionHonorsCodexDangerousBypassConfig(t *testing.T) {
	cfg := config.Load()
	cfg.Launch.CodexDangerousBypass = false
	runner := &process.FakeRunner{}
	svc := newStubWithRuntime(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Runner: runner,
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/codex", nil
		},
	})
	if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if len(runner.Starts) != 1 {
		t.Fatalf("len(runner.Starts) = %d, want 1", len(runner.Starts))
	}
	want := []string{"-c", `model_reasoning_effort="high"`, "app-server"}
	if !reflect.DeepEqual(runner.Starts[0].Command().Args(), want) {
		t.Fatalf("codex args = %#v, want %#v", runner.Starts[0].Command().Args(), want)
	}
}

func TestCreateSessionCodexDangerousBypassDefaultsOnWithZeroConfig(t *testing.T) {
	runner := &process.FakeRunner{}
	svc := newStubWithRuntime(config.Config{}, func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{
		Runner: runner,
		ResolveBinPath: func(session.Backend) (string, error) {
			return "/tmp/codex", nil
		},
	})
	if _, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if len(runner.Starts) != 1 {
		t.Fatalf("len(runner.Starts) = %d, want 1", len(runner.Starts))
	}
	want := []string{"--dangerously-bypass-approvals-and-sandbox", "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`, "-c", `model_reasoning_effort="high"`, "app-server"}
	if !reflect.DeepEqual(runner.Starts[0].Command().Args(), want) {
		t.Fatalf("codex args = %#v, want %#v", runner.Starts[0].Command().Args(), want)
	}
}

func TestResolveCodexLaunchEnvCopiesAuthKeyIntoCRSAlias(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.codex) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, codexAuthPath), []byte(`{"OPENAI_API_KEY":"cr_test_key"}`), 0o644); err != nil {
		t.Fatalf("WriteFile(auth.json) error = %v", err)
	}
	base, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	resolved, err := resolveCodexLaunchEnv(base, func(string) (string, bool) { return "", false }, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatalf("resolveCodexLaunchEnv() error = %v", err)
	}
	openAIKey, ok := resolvedEnvValue(resolved, nil, "OPENAI_API_KEY")
	if !ok || openAIKey != "cr_test_key" {
		t.Fatalf("resolved OPENAI_API_KEY = %q, %v, want cr_test_key", openAIKey, ok)
	}
	crsKey, ok := resolvedEnvValue(resolved, nil, "CRS_OAI_KEY")
	if !ok || crsKey != "cr_test_key" {
		t.Fatalf("resolved CRS_OAI_KEY = %q, %v, want cr_test_key", crsKey, ok)
	}
}

func mustLaunchSpecForHelperContractTest(t *testing.T, path string, args []string, cwd string, env process.Environment, ioSpec process.IO) process.LaunchSpec {
	t.Helper()
	command, err := process.NewCommand(path, args...)
	if err != nil {
		t.Fatalf("NewCommand(%q) error = %v", path, err)
	}
	spec, err := process.NewLaunchSpec(command, cwd, env, ioSpec)
	if err != nil {
		t.Fatalf("NewLaunchSpec(%q) error = %v", path, err)
	}
	return spec
}

func assertHelperLaunchContract(t *testing.T, got []string, childSpec process.LaunchSpec, childIOMode iod.ChildIOMode, codexThreadID ...string) {
	t.Helper()
	wantPrefix := []string{
		helperFlagSessionID, "s_helper_contract",
		helperFlagGenerationID, "g_helper_contract",
		helperFlagRuntimeRoot, "/tmp/actrail-data/runtime/iod",
		helperFlagChildCWD, childSpec.CWD().String(),
		helperFlagChildEnvMode, string(childSpec.Environment().Mode()),
	}
	if childIOMode != "" {
		wantPrefix = append(wantPrefix, helperFlagChildIOMode, string(childIOMode))
	}
	if len(codexThreadID) > 0 && codexThreadID[0] != "" {
		wantPrefix = append(wantPrefix, helperFlagCodexThreadID, codexThreadID[0])
	}
	wantPrefix = append(wantPrefix,
		helperFlagChildEnv, "ACTRAIL_ALPHA=one",
		helperFlagChildEnv, "ACTRAIL_BRAVO=two=2",
		"--",
		childSpec.Command().Path(),
	)
	want := append(wantPrefix, childSpec.Command().Args()...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("helper launch args = %#v, want %#v", got, want)
	}
}
