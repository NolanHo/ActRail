package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"actrail/internal/adapters/agent"
	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/piagentgrpc"
	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

type runtimeCatalog interface {
	LaunchSpec(agent.Request) (process.LaunchSpec, error)
}

type runtimeLauncher interface {
	Launch(context.Context, runtimeLaunchRequest) (sessionRuntime, error)
	AttachPIAgentGRPC(context.Context, runtimeLaunchRequest) (sessionRuntime, error)
	PIAgentGRPCTarget(session.SessionID) string
}

type RuntimeHelperBinding struct {
	GenerationID     iod.GenerationID
	LastReplayOffset iod.WALOffset
}

func (b RuntimeHelperBinding) Validate() error {
	if err := b.GenerationID.Validate(); err != nil {
		return err
	}
	return b.LastReplayOffset.ValidateState()
}

type RuntimeConfig struct {
	Catalog                 runtimeCatalog
	Runner                  process.Runner
	ResolveBinPath          func(session.Backend) (string, error)
	ResolveIODHelperBinPath func() (string, error)
	ResolveLaunchEnv        func(session.Backend, process.Environment) (process.Environment, error)
	IODDialer               iodclient.Dialer
	PIAgentGRPCDialer       piagentgrpc.Dialer
	PIAgentGRPCTarget       string
	IODRuntimeRoot          string
	UseIODHelper            bool
	NewGenerationID         func(session.SessionID) (iod.GenerationID, error)
	CurrentHelperBinding    func(session.SessionID) (*RuntimeHelperBinding, error)
}

type runtimeLaunchRequest struct {
	SessionID       session.SessionID
	Backend         session.Backend
	CWD             string
	Provider        string
	Model           string
	ReasoningEffort string
	SessionPath     string
	PIAgentGRPC     bool
	AttachOnly      bool
}

type processRuntimeLauncher struct {
	catalog                 runtimeCatalog
	runner                  process.Runner
	resolveBinPath          func(session.Backend) (string, error)
	resolveIODHelperBinPath func() (string, error)
	currentHelperBinding    func(session.SessionID) (*RuntimeHelperBinding, error)
	newGenerationID         func(session.SessionID) (iod.GenerationID, error)
	iodRuntimeRoot          string
	useIODHelper            bool
	dialer                  iodclient.Dialer
	piAgentGRPCDialer       piagentgrpc.Dialer
	piAgentGRPCTarget       string
	resolveLaunchEnv        func(session.Backend, process.Environment) (process.Environment, error)
	env                     process.Environment
	io                      process.IO
}

type runtimeProtocol string

const (
	runtimeProtocolTTY      runtimeProtocol = "tty"
	runtimeProtocolPIRPC    runtimeProtocol = "pi_rpc"
	runtimeProtocolCodexRPC runtimeProtocol = "codex_rpc"

	helperReadyPollInterval = 25 * time.Millisecond
	helperReadyTimeout      = 30 * time.Second
	helperStopTimeout       = 3 * time.Second

	helperFlagSessionID          = "-session-id"
	helperFlagGenerationID       = "-generation-id"
	helperFlagRuntimeRoot        = "-runtime-root"
	helperFlagChildCWD           = "-child-cwd"
	helperFlagChildEnvMode       = "-child-env-mode"
	helperFlagChildEnv           = "-child-env"
	helperFlagChildIOMode        = "-child-io-mode"
	helperFlagSessionHistoryPath = "-session-history-path"
)

type sessionRuntime struct {
	launchSpec           process.LaunchSpec
	handle               process.Handle
	protocol             runtimeProtocol
	codex                *codexRuntimeState
	helper               *runtimeIODHelper
	piAgentGRPC          *piagentgrpc.Client
	piAgentGRPCReady     <-chan error
	helperBinding        *RuntimeHelperBinding
	currentHelperBinding func(session.SessionID) (*RuntimeHelperBinding, error)
}

type codexRuntimeState struct {
	mu              sync.Mutex
	requestSeq      uint64
	initialized     bool
	initializeSent  bool
	threadStartSent bool
	threadID        string
	activeTurnID    string
}

type runtimeIODHelper struct {
	handle       process.Handle
	streamClient *iodclient.Client
	dialer       iodclient.Dialer
	manifest     iod.GenerationManifest
	sessionID    session.SessionID
	generationID iod.GenerationID
	helperPID    int
	childPID     *int
	buildDate    string
	gitSHA       string
	startTS      float64
	runtimeDir   string
	commandMu    sync.Mutex
	commandSeq   uint64
}

type piRPCPromptCommand struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type piRPCGetStateCommand struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type piRPCExtensionUIResponseCommand struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Value string `json:"value"`
}

type piRPCAbortCommand struct {
	Type string `json:"type"`
}

var errRuntimeInputUnavailable = errors.New("session runtime input is unavailable")

func newCodexRuntimeState(backend session.Backend) *codexRuntimeState {
	if backend != session.BackendCodex {
		return nil
	}
	return &codexRuntimeState{}
}

func (s *codexRuntimeState) nextRequestID(prefix string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestSeq++
	return fmt.Sprintf("%s-%d", strings.TrimSpace(prefix), s.requestSeq)
}

func (s *codexRuntimeState) bootstrapRequests() []any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := make([]any, 0, 2)
	if !s.initialized && !s.initializeSent {
		s.requestSeq++
		s.initializeSent = true
		requests = append(requests, map[string]any{
			"method": "initialize",
			"id":     fmt.Sprintf("initialize-%d", s.requestSeq),
			"params": map[string]any{
				"clientInfo":   map[string]any{"name": "actrail", "version": "0"},
				"capabilities": nil,
			},
		})
	}
	if s.threadID == "" && !s.threadStartSent {
		s.requestSeq++
		s.threadStartSent = true
		requests = append(requests, map[string]any{
			"method": "thread/start",
			"id":     fmt.Sprintf("thread-start-%d", s.requestSeq),
			"params": map[string]any{
				"experimentalRawEvents":  false,
				"persistExtendedHistory": false,
			},
		})
	}
	return requests
}

func (s *codexRuntimeState) markInitialized() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initialized = true
	s.initializeSent = false
}

func (s *codexRuntimeState) setThreadID(threadID string) {
	if s == nil {
		return
	}
	resolved := strings.TrimSpace(threadID)
	if resolved == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threadID = resolved
	s.threadStartSent = false
}

func (s *codexRuntimeState) setActiveTurnID(turnID string) {
	if s == nil {
		return
	}
	resolved := strings.TrimSpace(turnID)
	if resolved == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeTurnID = resolved
}

func (s *codexRuntimeState) clearActiveTurnID(turnID string) {
	if s == nil {
		return
	}
	resolved := strings.TrimSpace(turnID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if resolved == "" || s.activeTurnID == resolved {
		s.activeTurnID = ""
	}
}

func (s *codexRuntimeState) snapshot() (initialized bool, threadID, activeTurnID string) {
	if s == nil {
		return false, "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized, s.threadID, s.activeTurnID
}

func newRuntimeLauncher(cfg RuntimeConfig) runtimeLauncher {
	env, err := process.InheritEnv()
	if err != nil {
		panic(err)
	}
	ioSpec, err := defaultRuntimeIO()
	if err != nil {
		panic(err)
	}
	catalog := cfg.Catalog
	if catalog == nil {
		catalog = agent.DefaultCatalog()
	}
	runner := cfg.Runner
	if runner == nil {
		runner = process.NewExecRunner()
	}
	resolveBinPath := cfg.ResolveBinPath
	if resolveBinPath == nil {
		resolveBinPath = defaultRuntimeBinPath
	}
	resolveIODHelperBinPath := cfg.ResolveIODHelperBinPath
	if resolveIODHelperBinPath == nil {
		resolveIODHelperBinPath = defaultIODHelperBinPath
	}
	resolveLaunchEnv := cfg.ResolveLaunchEnv
	if resolveLaunchEnv == nil {
		resolveLaunchEnv = defaultResolveLaunchEnv
	}
	newGenerationID := cfg.NewGenerationID
	if newGenerationID == nil {
		newGenerationID = defaultRuntimeGenerationID
	}
	iodRuntimeRoot, err := resolveIODRuntimeRoot(cfg.IODRuntimeRoot)
	if err != nil {
		panic(err)
	}

	return processRuntimeLauncher{
		catalog:                 catalog,
		runner:                  runner,
		resolveBinPath:          resolveBinPath,
		resolveIODHelperBinPath: resolveIODHelperBinPath,
		currentHelperBinding:    cfg.CurrentHelperBinding,
		newGenerationID:         newGenerationID,
		iodRuntimeRoot:          iodRuntimeRoot,
		useIODHelper:            cfg.UseIODHelper,
		dialer:                  cfg.IODDialer,
		piAgentGRPCDialer:       cfg.PIAgentGRPCDialer,
		piAgentGRPCTarget:       cfg.PIAgentGRPCTarget,
		resolveLaunchEnv:        resolveLaunchEnv,
		env:                     env,
		io:                      ioSpec,
	}
}

func resolveIODRuntimeRoot(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	resolved, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve iod runtime root %q: %w", trimmed, err)
	}
	return resolved, nil
}

func defaultRuntimeIO() (process.IO, error) {
	if preferRuntimePTY() {
		return process.PTYIO(process.PTYSize{Rows: 24, Cols: 80}, process.LogPaths{})
	}
	return process.PipeIO(process.LogPaths{})
}

func defaultRuntimeBinPath(backend session.Backend) (string, error) {
	if err := backend.Validate(); err != nil {
		return "", err
	}
	return backend.String(), nil
}

func defaultIODHelperBinPath() (string, error) {
	if value := strings.TrimSpace(os.Getenv("ACTRAIL_IOD_BIN")); value != "" {
		return value, nil
	}
	exePath, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exePath), "actrail-iod")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("actrail-iod")
	if err != nil {
		return "", fmt.Errorf("resolve actrail-iod helper: %w", err)
	}
	return path, nil
}

func defaultRuntimeGenerationID(sessionID session.SessionID) (iod.GenerationID, error) {
	if err := sessionID.Validate(); err != nil {
		return "", err
	}
	return iod.NewGenerationID(fmt.Sprintf("g_%d", time.Now().UTC().UnixNano()))
}

func runtimeProtocolForBackend(backend session.Backend) runtimeProtocol {
	switch backend {
	case session.BackendPI:
		return runtimeProtocolPIRPC
	case session.BackendCodex:
		return runtimeProtocolCodexRPC
	default:
		return runtimeProtocolTTY
	}
}

func (l processRuntimeLauncher) Launch(ctx context.Context, req runtimeLaunchRequest) (sessionRuntime, error) {
	if err := ctx.Err(); err != nil {
		return sessionRuntime{}, err
	}
	if err := req.Backend.Validate(); err != nil {
		return sessionRuntime{}, err
	}
	if req.PIAgentGRPC {
		return l.launchPIAgentGRPC(ctx, req)
	}
	if l.useIODHelper {
		return l.launchViaIODHelper(ctx, req)
	}
	return l.launchDirect(ctx, req)
}

func (l processRuntimeLauncher) AttachPIAgentGRPC(ctx context.Context, req runtimeLaunchRequest) (sessionRuntime, error) {
	req.PIAgentGRPC = true
	req.AttachOnly = true
	runtime, err := l.launchPIAgentGRPC(ctx, req)
	if err != nil {
		return sessionRuntime{}, err
	}
	if err := runtime.WaitForPIAgentGRPCReady(ctx); err != nil {
		_ = runtime.Kill(context.Background())
		return sessionRuntime{}, err
	}
	return runtime, nil
}

func (l processRuntimeLauncher) PIAgentGRPCTarget(sessionID session.SessionID) string {
	target := strings.TrimSpace(l.piAgentGRPCTarget)
	if target != "" {
		return target
	}
	return piagentgrpc.TargetForSession(sessionID.String())
}

func (l processRuntimeLauncher) launchPIAgentGRPC(ctx context.Context, req runtimeLaunchRequest) (sessionRuntime, error) {
	if req.Backend != session.BackendPI {
		return sessionRuntime{}, fmt.Errorf("pi agent grpc requires pi backend")
	}
	target := l.PIAgentGRPCTarget(req.SessionID)
	client := piagentgrpc.New(target, l.piAgentGRPCDialer)
	launchReq := req
	launchReq.PIAgentGRPC = true
	launchSpec, err := l.childLaunchSpec(launchReq)
	if err != nil {
		return sessionRuntime{}, err
	}
	if !req.AttachOnly {
		launchSpec, err = detachedPipeLaunchSpec(launchSpec)
		if err != nil {
			return sessionRuntime{}, err
		}
	}
	var handle process.Handle
	if !req.AttachOnly {
		handle, err = l.runner.Start(ctx, launchSpec)
		if err != nil {
			_ = client.Close()
			return sessionRuntime{}, err
		}
		if handle == nil {
			_ = client.Close()
			return sessionRuntime{}, fmt.Errorf("start pi agent grpc runtime %q: nil process handle", req.SessionID)
		}
	}
	return sessionRuntime{
		launchSpec:           launchSpec,
		handle:               handle,
		protocol:             runtimeProtocolPIRPC,
		piAgentGRPC:          client,
		piAgentGRPCReady:     l.piAgentGRPCReady(client),
		currentHelperBinding: l.currentHelperBinding,
	}, nil
}

func (l processRuntimeLauncher) piAgentGRPCReady(client *piagentgrpc.Client) <-chan error {
	ready := make(chan error, 1)
	go func() {
		ready <- l.waitForPIAgentGRPCReady(context.Background(), client)
	}()
	return ready
}

func (l processRuntimeLauncher) waitForPIAgentGRPCReady(ctx context.Context, client *piagentgrpc.Client) error {
	readyCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		readyCtx, cancel = context.WithTimeout(ctx, helperReadyTimeout)
		defer cancel()
	}
	ticker := time.NewTicker(helperReadyPollInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		if state, err := client.GetState(readyCtx); err == nil {
			if state.RuntimeReady() {
				return nil
			}
			if state.RuntimeFailed() {
				return fmt.Errorf("pi agent grpc runtime failed: %s", firstNonEmptyString(state.RuntimeMessage(), "runtime failed"))
			}
			lastErr = fmt.Errorf("pi agent grpc runtime starting: %s", firstNonEmptyString(state.RuntimeMessage(), "runtime starting"))
		} else {
			lastErr = err
			if errors.Is(err, context.Canceled) {
				return fmt.Errorf("wait for pi agent grpc: %w", err)
			}
		}
		select {
		case <-readyCtx.Done():
			if lastErr != nil {
				return fmt.Errorf("wait for pi agent grpc: %w", lastErr)
			}
			return readyCtx.Err()
		case <-ticker.C:
		}
	}
}

func (l processRuntimeLauncher) launchDirect(ctx context.Context, req runtimeLaunchRequest) (sessionRuntime, error) {
	launchSpec, err := l.childLaunchSpec(req)
	if err != nil {
		return sessionRuntime{}, err
	}
	handle, err := l.runner.Start(ctx, launchSpec)
	if err != nil {
		return sessionRuntime{}, err
	}
	if handle == nil {
		return sessionRuntime{}, fmt.Errorf("start runtime %q: nil process handle", req.Backend)
	}
	return sessionRuntime{
		launchSpec:           launchSpec,
		handle:               handle,
		protocol:             runtimeProtocolForBackend(req.Backend),
		codex:                newCodexRuntimeState(req.Backend),
		currentHelperBinding: l.currentHelperBinding,
	}, nil
}

func (l processRuntimeLauncher) launchViaIODHelper(ctx context.Context, req runtimeLaunchRequest) (sessionRuntime, error) {
	if err := req.SessionID.Validate(); err != nil {
		return sessionRuntime{}, err
	}
	runtimeRoot := strings.TrimSpace(l.iodRuntimeRoot)
	if runtimeRoot == "" {
		return sessionRuntime{}, fmt.Errorf("iod runtime root is required")
	}
	childLaunchSpec, err := l.childLaunchSpec(req)
	if err != nil {
		return sessionRuntime{}, err
	}
	helperBinPath, err := l.resolveIODHelperBinPath()
	if err != nil {
		return sessionRuntime{}, err
	}
	generationID, err := l.newGenerationID(req.SessionID)
	if err != nil {
		return sessionRuntime{}, err
	}
	paths, err := iod.NewGenerationPaths(runtimeRoot, req.SessionID, generationID)
	if err != nil {
		return sessionRuntime{}, err
	}
	helperLaunchSpec, err := l.helperLaunchSpec(req, helperBinPath, generationID, childLaunchSpec)
	if err != nil {
		return sessionRuntime{}, err
	}
	handle, err := l.runner.Start(ctx, helperLaunchSpec)
	if err != nil {
		return sessionRuntime{}, err
	}
	if handle == nil {
		return sessionRuntime{}, fmt.Errorf("start iod helper for %q: nil process handle", req.SessionID)
	}
	manifest, hello, client, err := l.waitForHelperReady(ctx, handle, paths)
	if err != nil {
		helper := &runtimeIODHelper{handle: handle, sessionID: req.SessionID, generationID: generationID, runtimeDir: paths.RuntimeDir}
		_ = helper.shutdown(context.Background())
		_ = removeRuntimeGenerationArtifacts(paths.RuntimeDir)
		return sessionRuntime{}, err
	}
	helper := &runtimeIODHelper{
		handle:       handle,
		streamClient: client,
		dialer:       l.dialer,
		manifest:     manifest,
		sessionID:    req.SessionID,
		generationID: generationID,
		helperPID:    hello.HelperPID,
		childPID:     copyIntPtr(hello.ChildPID),
		buildDate:    hello.IODBuildDate,
		gitSHA:       hello.IODGitSHA,
		startTS:      hello.StartTS,
		runtimeDir:   paths.RuntimeDir,
	}
	binding := &RuntimeHelperBinding{GenerationID: generationID}
	return sessionRuntime{
		launchSpec:    childLaunchSpec,
		handle:        handle,
		protocol:      runtimeProtocolForBackend(req.Backend),
		codex:         newCodexRuntimeState(req.Backend),
		helper:        helper,
		helperBinding: binding,
		currentHelperBinding: func(session.SessionID) (*RuntimeHelperBinding, error) {
			resolved := *binding
			return &resolved, nil
		},
	}, verifyLaunchMatchesManifest(paths, manifest)
}

func (l processRuntimeLauncher) childLaunchSpec(req runtimeLaunchRequest) (process.LaunchSpec, error) {
	binPath, err := l.resolveBinPath(req.Backend)
	if err != nil {
		return process.LaunchSpec{}, err
	}
	grpcSocketPath := ""
	if req.PIAgentGRPC {
		grpcSocketPath = piagentgrpc.SocketPathForTarget(l.PIAgentGRPCTarget(req.SessionID))
	}
	options, err := agent.NewOptionsWithTransport(req.Provider, req.Model, req.ReasoningEffort, req.SessionPath, grpcSocketPath)
	if err != nil {
		return process.LaunchSpec{}, err
	}
	launchEnv := l.env
	if l.resolveLaunchEnv != nil {
		launchEnv, err = l.resolveLaunchEnv(req.Backend, l.env)
		if err != nil {
			return process.LaunchSpec{}, err
		}
	}
	launchReq, err := agent.NewRequest(req.Backend, binPath, req.CWD, launchEnv, l.io, options)
	if err != nil {
		return process.LaunchSpec{}, err
	}
	return l.catalog.LaunchSpec(launchReq)
}

func detachedPipeLaunchSpec(spec process.LaunchSpec) (process.LaunchSpec, error) {
	ioSpec, err := process.PipeIO(process.LogPaths{})
	if err != nil {
		return process.LaunchSpec{}, err
	}
	return process.NewLaunchSpec(spec.Command(), spec.CWD().String(), spec.Environment(), ioSpec, process.Detached())
}

func (l processRuntimeLauncher) helperLaunchSpec(req runtimeLaunchRequest, helperBinPath string, generationID iod.GenerationID, childLaunchSpec process.LaunchSpec) (process.LaunchSpec, error) {
	commandArgs := []string{
		helperFlagSessionID, req.SessionID.String(),
		helperFlagGenerationID, generationID.String(),
		helperFlagRuntimeRoot, strings.TrimSpace(l.iodRuntimeRoot),
		helperFlagChildCWD, childLaunchSpec.CWD().String(),
		helperFlagChildEnvMode, string(childLaunchSpec.Environment().Mode()),
	}
	if req.Backend == session.BackendPI {
		commandArgs = append(commandArgs, helperFlagChildIOMode, string(iod.ChildIOModeStdio))
	}
	if sessionPath := strings.TrimSpace(req.SessionPath); sessionPath != "" {
		commandArgs = append(commandArgs, helperFlagSessionHistoryPath, sessionPath)
	}
	for _, item := range childLaunchSpec.Environment().Vars() {
		commandArgs = append(commandArgs, helperFlagChildEnv, item.String())
	}
	commandArgs = append(commandArgs, "--", childLaunchSpec.Command().Path())
	commandArgs = append(commandArgs, childLaunchSpec.Command().Args()...)
	command, err := process.NewCommand(helperBinPath, commandArgs...)
	if err != nil {
		return process.LaunchSpec{}, err
	}
	ioSpec, err := process.PipeIO(process.LogPaths{})
	if err != nil {
		return process.LaunchSpec{}, err
	}
	return process.NewLaunchSpec(command, req.CWD, l.env, ioSpec, process.Detached())
}

func (l processRuntimeLauncher) waitForHelperReady(ctx context.Context, handle process.Handle, paths iod.GenerationPaths) (iod.GenerationManifest, iod.HelloPacket, *iodclient.Client, error) {
	readyCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		readyCtx, cancel = context.WithTimeout(ctx, helperReadyTimeout)
		defer cancel()
	}
	ticker := time.NewTicker(helperReadyPollInterval)
	defer ticker.Stop()
	for {
		manifest, err := iod.ReadGenerationManifest(paths.ManifestPath)
		if err == nil {
			client, hello, attachErr := l.attachHelper(readyCtx, manifest)
			if attachErr == nil {
				return manifest, hello, client, nil
			}
		}
		select {
		case <-readyCtx.Done():
			return iod.GenerationManifest{}, iod.HelloPacket{}, nil, readyCtx.Err()
		case <-ticker.C:
		}
	}
}

func (l processRuntimeLauncher) attachHelper(ctx context.Context, manifest iod.GenerationManifest) (*iodclient.Client, iod.HelloPacket, error) {
	client, err := iodclient.DialContext(ctx, manifest.ControlSocketPath, l.dialer)
	if err != nil {
		return nil, iod.HelloPacket{}, err
	}
	hello, err := client.Hello(ctx)
	if err != nil {
		_ = client.Close()
		return nil, iod.HelloPacket{}, err
	}
	if err := iodclient.VerifyHelloProof(manifest, hello); err != nil {
		_ = client.Close()
		return nil, iod.HelloPacket{}, err
	}
	return client, hello, nil
}

func verifyLaunchMatchesManifest(paths iod.GenerationPaths, manifest iod.GenerationManifest) error {
	if manifest.WALPath != paths.WALPath {
		return fmt.Errorf("helper wal path = %q, want %q", manifest.WALPath, paths.WALPath)
	}
	if manifest.ControlSocketPath != paths.ControlSocketPath {
		return fmt.Errorf("helper control socket path = %q, want %q", manifest.ControlSocketPath, paths.ControlSocketPath)
	}
	return nil
}

func copyIntPtr(raw *int) *int {
	if raw == nil {
		return nil
	}
	copied := *raw
	return &copied
}

func (r sessionRuntime) UsesPIAgentGRPC() bool {
	return r.piAgentGRPC != nil
}

func (r sessionRuntime) PendingPIAgentGRPCReady() bool {
	return r.piAgentGRPC != nil && r.piAgentGRPCReady != nil
}

func (r sessionRuntime) PIAgentGRPCState(ctx context.Context) (piagentgrpc.State, error) {
	if r.piAgentGRPC == nil {
		return piagentgrpc.State{}, nil
	}
	return r.piAgentGRPC.GetState(ctx)
}

func (r sessionRuntime) WaitForPIAgentGRPCReady(ctx context.Context) error {
	if r.piAgentGRPCReady == nil {
		return nil
	}
	select {
	case err := <-r.piAgentGRPCReady:
		return err
	case <-ctx.Done():
		go func() { <-r.piAgentGRPCReady }()
		return ctx.Err()
	}
}

func (r sessionRuntime) CurrentHelperBinding(sessionID session.SessionID) (*RuntimeHelperBinding, error) {
	if r.helperBinding != nil {
		resolved := *r.helperBinding
		if err := resolved.Validate(); err != nil {
			return nil, err
		}
		return &resolved, nil
	}
	if r.currentHelperBinding == nil {
		return nil, nil
	}
	binding, err := r.currentHelperBinding(sessionID)
	if err != nil || binding == nil {
		return binding, err
	}
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	return binding, nil
}

func (r sessionRuntime) RequestPIRPCStateSnapshot(ctx context.Context) (*piRPCStateSnapshot, error) {
	if r.piAgentGRPC == nil {
		return nil, errRuntimeInputUnavailable
	}
	state, err := r.piAgentGRPC.GetState(ctx)
	if err != nil {
		return nil, err
	}
	return piRPCStateSnapshotFromGRPC(state), nil
}

func (r sessionRuntime) RequestPIRPCState(ctx context.Context, id string) error {
	if r.protocol != runtimeProtocolPIRPC {
		return nil
	}
	commandID := strings.TrimSpace(id)
	if commandID == "" {
		return fmt.Errorf("pi rpc state request id is required")
	}
	if r.piAgentGRPC != nil {
		return nil
	}
	command := piRPCGetStateCommand{ID: commandID, Type: "get_state"}
	if r.helper != nil {
		encoded, err := json.Marshal(command)
		if err != nil {
			return fmt.Errorf("marshal runtime command: %w", err)
		}
		return r.helper.command(ctx, iod.CommandSend, encoded)
	}
	return r.writeRPCCommand(ctx, command)
}

func (r sessionRuntime) SendPrompt(ctx context.Context, text string) error {
	return r.sendPrompt(ctx, text, nil, false)
}

func (r sessionRuntime) SendFollowUp(ctx context.Context, text string) error {
	return r.sendPrompt(ctx, text, nil, true)
}

func (r sessionRuntime) SendPromptWithStaleCheck(ctx context.Context, text string, stale func() bool) error {
	return r.sendPrompt(ctx, text, stale, false)
}

func (r sessionRuntime) sendPrompt(ctx context.Context, text string, stale func() bool, followUp bool) error {
	payload := strings.TrimSpace(text)
	if payload == "" {
		return fmt.Errorf("runtime prompt is required")
	}
	if r.protocol == runtimeProtocolCodexRPC {
		_, threadID, _ := r.codex.snapshot()
		if threadID == "" {
			return errRuntimeInputUnavailable
		}
		request := map[string]any{
			"method": "turn/start",
			"id":     r.codex.nextRequestID("turn-start"),
			"params": map[string]any{
				"threadId": threadID,
				"input": []any{map[string]any{
					"type":          "text",
					"text":          payload,
					"text_elements": []any{},
				}},
			},
		}
		return r.writeCodexCommand(ctx, request)
	}
	if r.helper != nil {
		if stale != nil && stale() {
			return errRuntimeChanged
		}
		if r.protocol == runtimeProtocolPIRPC {
			encoded, err := json.Marshal(piRPCPromptCommand{Type: "prompt", Message: payload})
			if err != nil {
				return fmt.Errorf("marshal runtime command: %w", err)
			}
			return r.helper.command(ctx, iod.CommandSend, encoded)
		}
		return r.helper.command(ctx, iod.CommandSend, json.RawMessage(strconv.Quote(payload)))
	}
	if r.piAgentGRPC != nil {
		if followUp {
			return r.piAgentGRPC.FollowUp(ctx, payload)
		}
		return r.piAgentGRPC.Prompt(ctx, payload)
	}
	if r.protocol == runtimeProtocolPIRPC {
		return r.writeRPCCommand(ctx, piRPCPromptCommand{Type: "prompt", Message: payload})
	}
	return r.writeLine(ctx, payload)
}

func (r sessionRuntime) RespondUI(ctx context.Context, requestID, value string) error {
	resolvedID := strings.TrimSpace(requestID)
	if resolvedID == "" {
		return fmt.Errorf("runtime ui request id is required")
	}
	payload := strings.TrimSpace(value)
	if payload == "" {
		return fmt.Errorf("runtime ui response value is required")
	}
	if r.protocol == runtimeProtocolCodexRPC {
		return errRuntimeInputUnavailable
	}
	if r.helper != nil {
		if r.protocol == runtimeProtocolPIRPC {
			encoded, err := json.Marshal(piRPCExtensionUIResponseCommand{Type: "extension_ui_response", ID: resolvedID, Value: payload})
			if err != nil {
				return fmt.Errorf("marshal runtime command: %w", err)
			}
			return r.helper.command(ctx, iod.CommandUIResponseSubmit, encoded)
		}
		return r.helper.command(ctx, iod.CommandSend, json.RawMessage(strconv.Quote(payload)))
	}
	if r.piAgentGRPC != nil {
		return errRuntimeInputUnavailable
	}
	if r.protocol == runtimeProtocolPIRPC {
		return r.writeRPCCommand(ctx, piRPCExtensionUIResponseCommand{Type: "extension_ui_response", ID: resolvedID, Value: payload})
	}
	return r.writeLine(ctx, payload)
}

func (r sessionRuntime) writeLine(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	writer, err := r.inputWriter()
	if err != nil {
		return err
	}
	payload := text
	if !strings.HasSuffix(payload, "\n") {
		payload += "\n"
	}
	if _, err := io.WriteString(writer, payload); err != nil {
		return fmt.Errorf("write runtime input: %w", err)
	}
	return nil
}

func (r sessionRuntime) writeRPCCommand(ctx context.Context, command any) error {
	payload, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("marshal runtime command: %w", err)
	}
	return r.writeLine(ctx, string(payload))
}

func (r sessionRuntime) writeCodexCommand(ctx context.Context, command any) error {
	payload, err := json.Marshal(command)
	if err != nil {
		return fmt.Errorf("marshal runtime command: %w", err)
	}
	if r.helper != nil {
		return r.helper.command(ctx, iod.CommandSend, payload)
	}
	return r.writeLine(ctx, string(payload))
}

func (r sessionRuntime) EnsureCodexThread(ctx context.Context) error {
	if r.protocol != runtimeProtocolCodexRPC {
		return nil
	}
	for _, request := range r.codex.bootstrapRequests() {
		if err := r.writeCodexCommand(ctx, request); err != nil {
			return err
		}
	}
	return nil
}

func (r sessionRuntime) inputWriter() (io.Writer, error) {
	if r.helper != nil {
		return nil, errRuntimeInputUnavailable
	}
	if r.handle == nil || r.handle.PTY() == nil {
		return nil, errRuntimeInputUnavailable
	}
	return r.handle.PTY(), nil
}

func (r sessionRuntime) Interrupt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.protocol == runtimeProtocolCodexRPC {
		_, threadID, turnID := r.codex.snapshot()
		if threadID == "" || turnID == "" {
			return nil
		}
		request := map[string]any{
			"method": "turn/interrupt",
			"id":     r.codex.nextRequestID("turn-interrupt"),
			"params": map[string]any{"threadId": threadID, "turnId": turnID},
		}
		if err := r.writeCodexCommand(ctx, request); err != nil {
			return err
		}
		r.codex.clearActiveTurnID(turnID)
		return nil
	}
	if r.helper != nil {
		return r.helper.command(ctx, iod.CommandInterrupt, json.RawMessage(`{}`))
	}
	if r.piAgentGRPC != nil {
		return r.piAgentGRPC.Abort(ctx)
	}
	if r.protocol == runtimeProtocolPIRPC {
		return r.writeRPCCommand(ctx, piRPCAbortCommand{Type: "abort"})
	}
	if r.handle == nil {
		return fmt.Errorf("interrupt runtime: nil process handle")
	}
	if err := r.handle.Interrupt(); err != nil {
		return fmt.Errorf("interrupt runtime: %w", err)
	}
	return nil
}

func (r sessionRuntime) Kill(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.helper != nil {
		return r.helper.shutdown(ctx)
	}
	if r.piAgentGRPC != nil {
		_ = r.piAgentGRPC.Close()
	}
	if r.handle == nil {
		return nil
	}
	if err := r.handle.Kill(); err != nil {
		return fmt.Errorf("kill runtime: %w", err)
	}
	return nil
}

func (r sessionRuntime) PID() int {
	if r.helper != nil && r.helper.childPID != nil {
		return *r.helper.childPID
	}
	if r.piAgentGRPC != nil && r.handle != nil {
		return r.handle.PID()
	}
	if r.handle == nil {
		return 0
	}
	return r.handle.PID()
}

func (r sessionRuntime) CleanupHelperArtifacts() error {
	if r.helper == nil {
		return nil
	}
	return removeRuntimeGenerationArtifacts(r.helper.runtimeDir)
}

func (h *runtimeIODHelper) command(ctx context.Context, name iod.CommandName, payload json.RawMessage) error {
	if h == nil || h.streamClient == nil {
		return errRuntimeInputUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	h.commandMu.Lock()
	defer h.commandMu.Unlock()
	commandID, err := h.nextCommandID()
	if err != nil {
		return err
	}
	packet, err := iod.NewCommandPacket(h.sessionID, h.generationID, name, commandID, payload)
	if err != nil {
		return err
	}
	client, err := h.attachCommandClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	result, err := client.Command(ctx, packet)
	if err != nil {
		return err
	}
	if result.Rejected != nil {
		return helperRejectedCommandError(*result.Rejected)
	}
	if result.Accepted == nil {
		return fmt.Errorf("helper command %q returned no durable outcome", name)
	}
	return nil
}

func (h *runtimeIODHelper) attachCommandClient(ctx context.Context) (*iodclient.Client, error) {
	if h == nil {
		return nil, errRuntimeInputUnavailable
	}
	client, err := iodclient.DialContext(ctx, h.manifest.ControlSocketPath, h.dialer)
	if err != nil {
		return nil, err
	}
	hello, err := client.Hello(ctx)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := iodclient.VerifyHelloProof(h.manifest, hello); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (h *runtimeIODHelper) nextCommandID() (iod.CommandID, error) {
	h.commandSeq++
	return iod.NewCommandID(fmt.Sprintf("cmd_%d_%d", time.Now().UTC().UnixNano(), h.commandSeq))
}

func helperRejectedCommandError(packet iod.CommandRejectedPacket) error {
	message := fmt.Sprintf("helper rejected command %q", packet.CommandID)
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(packet.Payload, &payload); err == nil && strings.TrimSpace(payload.Reason) != "" {
		message += ": " + strings.TrimSpace(payload.Reason)
	}
	return errors.New(message)
}

func (h *runtimeIODHelper) sessionHistory(ctx context.Context) (iod.SessionHistoryResponsePacket, error) {
	if h == nil {
		return iod.SessionHistoryResponsePacket{}, errRuntimeInputUnavailable
	}
	client, err := iodclient.DialContext(ctx, h.manifest.ControlSocketPath, h.dialer)
	if err != nil {
		return iod.SessionHistoryResponsePacket{}, err
	}
	defer client.Close()
	if _, err := client.Hello(ctx); err != nil {
		return iod.SessionHistoryResponsePacket{}, err
	}
	request, err := iod.NewSessionHistoryRequestPacket(h.sessionID, h.generationID)
	if err != nil {
		return iod.SessionHistoryResponsePacket{}, err
	}
	return client.SessionHistory(ctx, request)
}

func (h *runtimeIODHelper) shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if h.streamClient != nil {
		defer func() { _ = h.streamClient.Close() }()
	}
	if h.handle != nil {
		shutdownCtx := ctx
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancel context.CancelFunc
			shutdownCtx, cancel = context.WithTimeout(ctx, helperStopTimeout)
			defer cancel()
		}
		_ = h.handle.Interrupt()
		_, err := h.handle.Wait(shutdownCtx)
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if err == nil {
				return nil
			}
		}
		if killErr := h.handle.Kill(); killErr != nil {
			if strings.Contains(killErr.Error(), "is not available") {
				return nil
			}
			return fmt.Errorf("kill iod helper: %w", killErr)
		}
		_, _ = h.handle.Wait(context.Background())
		return nil
	}
	if h.helperPID <= 0 {
		return nil
	}
	proc, err := os.FindProcess(h.helperPID)
	if err != nil {
		return fmt.Errorf("find iod helper pid %d: %w", h.helperPID, err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("signal iod helper pid %d: %w", h.helperPID, err)
	}
	return nil
}

func removeRuntimeGenerationArtifacts(runtimeDir string) error {
	trimmed := strings.TrimSpace(runtimeDir)
	if trimmed == "" {
		return nil
	}
	if err := os.RemoveAll(trimmed); err != nil {
		return fmt.Errorf("remove helper runtime dir %q: %w", trimmed, err)
	}
	sessionDir := filepath.Dir(trimmed)
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read helper session dir %q: %w", sessionDir, err)
	}
	if len(entries) == 0 {
		if err := os.Remove(sessionDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove helper session dir %q: %w", sessionDir, err)
		}
	}
	return nil
}

func (s *Stub) runtimeForSession(sessionID session.SessionID, backend session.Backend, runtime sessionRuntime) sessionRuntime {
	if runtime.protocol == "" {
		runtime.protocol = runtimeProtocolForBackend(backend)
	}
	if backend == session.BackendCodex && runtime.codex == nil {
		runtime.codex = newCodexRuntimeState(backend)
	}
	if runtime.helper != nil || runtime.piAgentGRPC != nil || runtime.handle != nil || s == nil || s.helpers == nil {
		return runtime
	}
	attachment, ok := s.helpers.Attachment(sessionID)
	if !ok {
		return runtime
	}
	binding := &RuntimeHelperBinding{GenerationID: attachment.Binding.GenerationID, LastReplayOffset: attachment.Binding.LastReplayOffset}
	runtime.helperBinding = binding
	runtime.helper = runtimeIODHelperFromAttachment(attachment, s.helperDialer)
	runtime.currentHelperBinding = func(session.SessionID) (*RuntimeHelperBinding, error) {
		resolved := *binding
		return &resolved, nil
	}
	return runtime
}

func runtimeIODHelperFromAttachment(attachment attachedHelper, dialer iodclient.Dialer) *runtimeIODHelper {
	return &runtimeIODHelper{
		streamClient: attachment.Client,
		dialer:       dialer,
		manifest:     attachment.Manifest,
		sessionID:    attachment.Binding.SessionID,
		generationID: attachment.Binding.GenerationID,
		helperPID:    attachment.Hello.HelperPID,
		childPID:     copyIntPtr(attachment.Hello.ChildPID),
		runtimeDir:   filepath.Dir(attachment.ManifestPath),
	}
}
