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
	"sort"
	"strconv"
	"strings"
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
	PIAgentGRPCReadyTimeout time.Duration
	IODRuntimeRoot          string
	UseIODHelper            bool
	CodexDangerousBypass    *bool
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
	CodexThreadID   string
	PIAgentGRPC     bool
	AttachOnly      bool
	ForceNewIOD     bool
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
	piAgentGRPCReadyTimeout time.Duration
	codexDangerousBypass    bool
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
	defaultHelperGeneration = "g_current"

	helperFlagSessionID          = "-session-id"
	helperFlagGenerationID       = "-generation-id"
	helperFlagRuntimeRoot        = "-runtime-root"
	helperFlagChildCWD           = "-child-cwd"
	helperFlagChildEnvMode       = "-child-env-mode"
	helperFlagChildEnv           = "-child-env"
	helperFlagChildIOMode        = "-child-io-mode"
	helperFlagSessionHistoryPath = "-session-history-path"
	helperFlagCodexThreadID      = "-codex-thread-id"
)

const defaultHelperReadyTimeout = 30 * time.Second

type sessionRuntime struct {
	launchSpec           process.LaunchSpec
	handle               process.Handle
	protocol             runtimeProtocol
	codex                *codexRuntimeState
	helper               *runtimeIODHelper
	attachedExistingIOD  bool
	piAgentGRPC          *piagentgrpc.Client
	piAgentGRPCReady     <-chan error
	helperBinding        *RuntimeHelperBinding
	currentHelperBinding func(session.SessionID) (*RuntimeHelperBinding, error)
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
var errCodexThreadNotReady = errors.New("codex thread is not ready")

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
	readyTimeout := cfg.PIAgentGRPCReadyTimeout
	if readyTimeout <= 0 {
		readyTimeout = defaultHelperReadyTimeout
	}
	codexDangerousBypass := true
	if cfg.CodexDangerousBypass != nil {
		codexDangerousBypass = *cfg.CodexDangerousBypass
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
		piAgentGRPCReadyTimeout: readyTimeout,
		codexDangerousBypass:    codexDangerousBypass,
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
	if backend == session.BackendCodex {
		if path := strings.TrimSpace(os.Getenv("ACTRAIL_CODEX_BIN")); path != "" {
			return path, nil
		}
		if path, err := exec.LookPath("codex"); err == nil {
			return path, nil
		}
		if path, ok := firstExecutableCodexPath(); ok {
			return path, nil
		}
	}
	return backend.String(), nil
}

func firstExecutableCodexPath() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", false
	}
	candidates := []string{filepath.Join(home, ".local", "bin", "codex")}
	nvmMatches, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "codex"))
	sort.Strings(nvmMatches)
	for i := len(nvmMatches) - 1; i >= 0; i-- {
		candidates = append(candidates, nvmMatches[i])
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
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
	return iod.NewGenerationID(defaultHelperGeneration)
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
			return sessionRuntime{}, invalidLaunchCWD(err)
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
		readyCtx, cancel = context.WithTimeout(ctx, l.piAgentGRPCReadyTimeout)
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
		return sessionRuntime{}, invalidLaunchCWD(err)
	}
	if handle == nil {
		return sessionRuntime{}, fmt.Errorf("start runtime %q: nil process handle", req.Backend)
	}
	return sessionRuntime{
		launchSpec:           launchSpec,
		handle:               handle,
		protocol:             runtimeProtocolForBackend(req.Backend),
		codex:                newCodexRuntimeStateWithResumeThread(req.Backend, req.CodexThreadID),
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
	if !req.ForceNewIOD || req.Backend == session.BackendCodex {
		if runtime, ok := l.attachExistingIODHelper(ctx, req, runtimeRoot); ok {
			return runtime, nil
		}
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
	if err := shutdownRuntimeGenerationFromManifest(ctx, paths.ManifestPath, req.SessionID, generationID); err != nil {
		return sessionRuntime{}, err
	}
	if err := removeRuntimeGenerationArtifacts(paths.RuntimeDir); err != nil {
		return sessionRuntime{}, err
	}
	if err := paths.EnsureDir(); err != nil {
		return sessionRuntime{}, err
	}
	childLaunchSpec, err := l.childLaunchSpecForGeneration(req, &paths)
	if err != nil {
		return sessionRuntime{}, err
	}
	helperLaunchSpec, err := l.helperLaunchSpec(req, helperBinPath, generationID, paths, childLaunchSpec)
	if err != nil {
		return sessionRuntime{}, err
	}
	handle, err := l.runner.Start(ctx, helperLaunchSpec)
	if err != nil {
		return sessionRuntime{}, invalidLaunchCWD(err)
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
		codex:         newCodexRuntimeStateWithResumeThread(req.Backend, req.CodexThreadID),
		helper:        helper,
		helperBinding: binding,
		currentHelperBinding: func(session.SessionID) (*RuntimeHelperBinding, error) {
			resolved := *binding
			return &resolved, nil
		},
	}, verifyLaunchMatchesManifest(paths, manifest)
}

func (l processRuntimeLauncher) attachExistingIODHelper(ctx context.Context, req runtimeLaunchRequest, runtimeRoot string) (sessionRuntime, bool) {
	index, err := iodclient.DiscoverManifestIndex(runtimeRoot)
	if err != nil {
		return sessionRuntime{}, false
	}
	var preferred *iod.GenerationID
	if l.currentHelperBinding != nil {
		if binding, err := l.currentHelperBinding(req.SessionID); err == nil && binding != nil && binding.GenerationID != "" {
			generationID := binding.GenerationID
			preferred = &generationID
		}
	}
	if preferred == nil && req.Backend != session.BackendCodex {
		return sessionRuntime{}, false
	}
	for _, discovered := range index.Candidates(req.SessionID, preferred) {
		if req.Backend != session.BackendCodex && preferred != nil && discovered.Manifest.GenerationID != *preferred {
			continue
		}
		runtime, err := l.attachIODManifest(ctx, req, discovered)
		if err == nil {
			return runtime, true
		}
	}
	return sessionRuntime{}, false
}

func (l processRuntimeLauncher) attachIODManifest(ctx context.Context, req runtimeLaunchRequest, discovered iodclient.DiscoveredManifest) (sessionRuntime, error) {
	if discovered.Manifest.SessionID != req.SessionID {
		return sessionRuntime{}, fmt.Errorf("iod manifest session id = %q, want %q", discovered.Manifest.SessionID, req.SessionID)
	}
	if err := verifyCodexIODManifestForAttach(req, discovered.Manifest); err != nil {
		return sessionRuntime{}, err
	}
	client, hello, err := l.attachHelper(ctx, discovered.Manifest)
	if err != nil {
		return sessionRuntime{}, err
	}
	generationID := discovered.Manifest.GenerationID
	helper := &runtimeIODHelper{
		streamClient: client,
		dialer:       l.dialer,
		manifest:     discovered.Manifest,
		sessionID:    req.SessionID,
		generationID: generationID,
		helperPID:    hello.HelperPID,
		childPID:     copyIntPtr(hello.ChildPID),
		buildDate:    hello.IODBuildDate,
		gitSHA:       hello.IODGitSHA,
		startTS:      hello.StartTS,
		runtimeDir:   filepath.Dir(discovered.Path),
	}
	if err := l.verifyAttachedCodexIODThread(ctx, req, helper); err != nil {
		_ = client.Close()
		return sessionRuntime{}, err
	}
	codex := newCodexRuntimeStateWithResumeThread(req.Backend, req.CodexThreadID)
	if codex != nil && strings.TrimSpace(req.CodexThreadID) != "" {
		codex.attachInitializedThread(req.CodexThreadID)
	}
	binding := &RuntimeHelperBinding{GenerationID: generationID}
	return sessionRuntime{
		protocol:            runtimeProtocolForBackend(req.Backend),
		codex:               codex,
		helper:              helper,
		attachedExistingIOD: true,
		helperBinding:       binding,
		currentHelperBinding: func(session.SessionID) (*RuntimeHelperBinding, error) {
			resolved := *binding
			return &resolved, nil
		},
	}, nil
}

func verifyCodexIODManifestForAttach(req runtimeLaunchRequest, manifest iod.GenerationManifest) error {
	if req.Backend != session.BackendCodex {
		return nil
	}
	threadID := strings.TrimSpace(req.CodexThreadID)
	requestedPath := filepath.Clean(strings.TrimSpace(req.SessionPath))
	manifestPath := filepath.Clean(strings.TrimSpace(manifest.SessionHistoryPath))
	if threadID == "" && requestedPath == "." {
		return nil
	}
	if requestedPath != "." && manifestPath != "." && manifestPath != requestedPath {
		return fmt.Errorf("attached codex IOD history %q does not match requested history %q", manifestPath, requestedPath)
	}
	if threadID != "" && manifestPath != "." && !codexSourcePathMatchesSessionID(manifestPath, threadID) {
		return fmt.Errorf("attached codex IOD history %q does not match requested thread %q", manifestPath, threadID)
	}
	if req.ForceNewIOD && threadID != "" && manifestPath == "." {
		return fmt.Errorf("attached codex IOD has no session history path for requested thread %q", threadID)
	}
	return nil
}

func (l processRuntimeLauncher) verifyAttachedCodexIODThread(ctx context.Context, req runtimeLaunchRequest, helper *runtimeIODHelper) error {
	if req.Backend != session.BackendCodex || strings.TrimSpace(req.CodexThreadID) == "" || helper == nil {
		return nil
	}
	historyCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	packet, err := helper.sessionHistory(historyCtx)
	if err != nil {
		if req.ForceNewIOD {
			return fmt.Errorf("verify attached codex IOD history: %w", err)
		}
		return nil
	}
	sourcePath := strings.TrimSpace(packet.SourcePath)
	if sourcePath == "" {
		if req.ForceNewIOD && strings.TrimSpace(helper.manifest.SessionHistoryPath) == "" {
			return fmt.Errorf("attached codex IOD has no session history source for requested thread %q", strings.TrimSpace(req.CodexThreadID))
		}
		return nil
	}
	if codexSourcePathMatchesSessionID(sourcePath, req.CodexThreadID) {
		return nil
	}
	return fmt.Errorf("attached codex IOD history %q does not match requested thread %q", sourcePath, strings.TrimSpace(req.CodexThreadID))
}

func (l processRuntimeLauncher) childLaunchSpec(req runtimeLaunchRequest) (process.LaunchSpec, error) {
	return l.childLaunchSpecForGeneration(req, nil)
}

func (l processRuntimeLauncher) childLaunchSpecForGeneration(req runtimeLaunchRequest, paths *iod.GenerationPaths) (process.LaunchSpec, error) {
	binPath, err := l.resolveBinPath(req.Backend)
	if err != nil {
		return process.LaunchSpec{}, err
	}
	grpcSocketPath := ""
	if req.PIAgentGRPC {
		grpcSocketPath = piagentgrpc.SocketPathForTarget(l.PIAgentGRPCTarget(req.SessionID))
	}
	listenURL := ""
	if req.Backend == session.BackendCodex && paths != nil {
		listenURL = "unix://" + paths.ChildSocketPath
	}
	dangerousBypass := req.Backend == session.BackendCodex && l.codexDangerousBypass
	options, err := agent.NewOptionsWithRuntimeFlags(req.Provider, req.Model, req.ReasoningEffort, req.SessionPath, grpcSocketPath, listenURL, dangerousBypass)
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

func (l processRuntimeLauncher) helperLaunchSpec(req runtimeLaunchRequest, helperBinPath string, generationID iod.GenerationID, paths iod.GenerationPaths, childLaunchSpec process.LaunchSpec) (process.LaunchSpec, error) {
	commandArgs := []string{
		helperFlagSessionID, req.SessionID.String(),
		helperFlagGenerationID, generationID.String(),
		helperFlagRuntimeRoot, strings.TrimSpace(l.iodRuntimeRoot),
		helperFlagChildCWD, childLaunchSpec.CWD().String(),
		helperFlagChildEnvMode, string(childLaunchSpec.Environment().Mode()),
	}
	if req.Backend == session.BackendPI {
		// Legacy compatibility path: helper-backed Pi still delivers commands
		// over child stdin. New command state-machine and health/ensure work
		// should target Pi gRPC UDS or another ackable request/response channel.
		commandArgs = append(commandArgs, helperFlagChildIOMode, string(iod.ChildIOModeStdio))
	} else if req.Backend == session.BackendCodex {
		commandArgs = append(commandArgs, helperFlagChildIOMode, string(iod.ChildIOModeUnix))
	}
	if sessionPath := strings.TrimSpace(req.SessionPath); sessionPath != "" {
		commandArgs = append(commandArgs, helperFlagSessionHistoryPath, sessionPath)
	}
	if req.Backend == session.BackendCodex {
		if threadID := strings.TrimSpace(req.CodexThreadID); threadID != "" {
			commandArgs = append(commandArgs, helperFlagCodexThreadID, threadID)
		}
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
	ioSpec, err := process.PipeIO(process.LogPaths{Stdout: paths.HelperStdoutPath, Stderr: paths.HelperStderrPath})
	if err != nil {
		return process.LaunchSpec{}, err
	}
	return process.NewLaunchSpec(command, req.CWD, l.env, ioSpec, process.Detached())
}

func (l processRuntimeLauncher) waitForHelperReady(ctx context.Context, handle process.Handle, paths iod.GenerationPaths) (iod.GenerationManifest, iod.HelloPacket, *iodclient.Client, error) {
	readyCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		readyCtx, cancel = context.WithTimeout(ctx, defaultHelperReadyTimeout)
		defer cancel()
	}
	exitCh := make(chan helperExitResult, 1)
	go func() {
		status, err := handle.Wait(context.Background())
		exitCh <- helperExitResult{status: status, err: err}
	}()
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
		case result := <-exitCh:
			return iod.GenerationManifest{}, iod.HelloPacket{}, nil, fmt.Errorf("iod helper exited before ready: %s", formatHelperExitResult(result))
		case <-readyCtx.Done():
			return iod.GenerationManifest{}, iod.HelloPacket{}, nil, readyCtx.Err()
		case <-ticker.C:
		}
	}
}

type helperExitResult struct {
	status process.ExitStatus
	err    error
}

func formatHelperExitResult(result helperExitResult) string {
	parts := []string{}
	if result.status.Signal != "" {
		parts = append(parts, "signal "+result.status.Signal)
	} else {
		parts = append(parts, fmt.Sprintf("exit code %d", result.status.Code))
	}
	if result.err != nil {
		parts = append(parts, result.err.Error())
	}
	return strings.Join(parts, ": ")
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

func invalidLaunchCWD(err error) error {
	var cwdErr *process.WorkingDirError
	if !errors.As(err, &cwdErr) {
		return err
	}
	message := cwdErr.Error()
	if errors.Is(cwdErr.Err, os.ErrNotExist) {
		message = fmt.Sprintf("working directory %q does not exist", cwdErr.Dir)
	}
	return Invalid("cwd", message)
}

func (r sessionRuntime) UsesPIAgentGRPC() bool {
	return r.piAgentGRPC != nil
}

func (r sessionRuntime) PendingPIAgentGRPCReady() bool {
	return r.piAgentGRPC != nil && r.piAgentGRPCReady != nil
}

func (r sessionRuntime) PendingCodexThread() bool {
	if r.protocol != runtimeProtocolCodexRPC || r.codex == nil {
		return false
	}
	_, threadID, _ := r.codex.snapshot()
	return strings.TrimSpace(threadID) == ""
}

func (r sessionRuntime) PendingCodexResumeThreadID() string {
	if r.protocol != runtimeProtocolCodexRPC || r.codex == nil {
		return ""
	}
	return strings.TrimSpace(r.codex.pendingResumeThreadID())
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

func (r sessionRuntime) RequestCodexThreadState(ctx context.Context) error {
	if r.protocol != runtimeProtocolCodexRPC {
		return nil
	}
	if r.codex == nil {
		return errRuntimeInputUnavailable
	}
	if err := r.EnsureCodexThread(ctx); err != nil {
		return err
	}
	_, threadID, _ := r.codex.snapshot()
	if threadID == "" {
		return errCodexThreadNotReady
	}
	request := map[string]any{
		"method": "thread/read",
		"id":     r.codex.nextRequestID("thread-read"),
		"params": map[string]any{
			"threadId":     threadID,
			"includeTurns": true,
		},
	}
	return r.writeCodexCommand(ctx, request)
}

func (r sessionRuntime) WaitCodexThreadReady(ctx context.Context) error {
	if r.protocol != runtimeProtocolCodexRPC {
		return nil
	}
	if r.codex == nil {
		return errRuntimeInputUnavailable
	}
	ticker := time.NewTicker(codexRuntimePollInterval)
	defer ticker.Stop()
	for {
		_, threadID, _ := r.codex.snapshot()
		if strings.TrimSpace(threadID) != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return errCodexThreadNotReady
		case <-ticker.C:
		}
	}
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
		if r.codex == nil {
			return errRuntimeInputUnavailable
		}
		_, threadID, _ := r.codex.snapshot()
		if threadID == "" {
			return errRuntimeInputUnavailable
		}
		request := map[string]any{
			"method": "turn/start",
			"id":     r.codex.nextRequestID("turn-start"),
			"params": map[string]any{
				"threadId": threadID,
				"effort":   agent.CodexDefaultReasoningEffort(),
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
	if r.codex == nil {
		return errRuntimeInputUnavailable
	}
	for _, request := range r.codex.bootstrapRequests() {
		if err := r.writeCodexCommand(ctx, request); err != nil {
			return err
		}
	}
	return nil
}

func (r sessionRuntime) EnsureCodexThreadStarted(ctx context.Context) error {
	if r.protocol != runtimeProtocolCodexRPC {
		return nil
	}
	if r.codex == nil {
		return errRuntimeInputUnavailable
	}
	request := r.codex.threadStartRequest()
	if request == nil {
		return nil
	}
	return r.writeCodexCommand(ctx, request)
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

func (r sessionRuntime) canWriteInput() bool {
	if r.helper != nil {
		return true
	}
	return r.handle != nil && r.handle.PTY() != nil
}

func (r sessionRuntime) Interrupt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.protocol == runtimeProtocolCodexRPC {
		if r.codex == nil {
			return nil
		}
		_, threadID, turnID := r.codex.snapshot()
		activity := r.codex.activity()
		if turnID == "" {
			if phaseIsTurnActive(activity.Phase) {
				r.codex.requestInterrupt()
			}
			return nil
		}
		r.codex.requestInterrupt()
		if threadID == "" {
			return nil
		}
		return r.sendCodexInterrupt(ctx, threadID, turnID)
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

func (r sessionRuntime) FlushCodexPendingInterrupt(ctx context.Context) error {
	if r.protocol != runtimeProtocolCodexRPC || r.codex == nil {
		return nil
	}
	threadID, turnID, ok := r.codex.pendingInterruptCommand()
	if !ok {
		return nil
	}
	return r.sendCodexInterrupt(ctx, threadID, turnID)
}

func (r sessionRuntime) sendCodexInterrupt(ctx context.Context, threadID, turnID string) error {
	if r.protocol != runtimeProtocolCodexRPC || r.codex == nil {
		return nil
	}
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
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
	r.codex.markInterruptSent(turnID)
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

func (r sessionRuntime) CloseHelperStream() {
	if r.helper == nil || r.helper.streamClient == nil {
		return
	}
	_ = r.helper.streamClient.Close()
}

func (r sessionRuntime) ReleaseAttachedHelperRollback() {
	if !r.attachedExistingIOD {
		_ = r.Kill(context.Background())
		_ = r.CleanupHelperArtifacts()
		return
	}
	r.CloseHelperStream()
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
		if runtime.protocol == runtimeProtocolCodexRPC && runtime.codex != nil {
			_, threadID, _ := runtime.codex.snapshot()
			if strings.TrimSpace(threadID) != "" {
				runtime.codex = newCodexRuntimeStateWithResumeThread(backend, threadID)
			}
		}
		return runtime
	}
	binding := &RuntimeHelperBinding{GenerationID: attachment.Binding.GenerationID, LastReplayOffset: attachment.Binding.LastReplayOffset}
	runtime.helperBinding = binding
	runtime.helper = runtimeIODHelperFromAttachment(attachment, s.helperDialer)
	runtime.attachedExistingIOD = true
	runtime.currentHelperBinding = func(session.SessionID) (*RuntimeHelperBinding, error) {
		resolved := *binding
		return &resolved, nil
	}
	return runtime
}

func (s *Stub) runtimeForRecord(record sessionRecord) sessionRuntime {
	runtime := s.runtimeForSession(record.identity.SessionID(), record.identity.Backend(), record.runtime)
	if record.identity.Backend() != session.BackendCodex || runtime.codex == nil {
		return runtime
	}
	threadID := strings.TrimSpace(record.importedBackendSessionID)
	if threadID == "" {
		return runtime
	}
	_, currentThreadID, _ := runtime.codex.snapshot()
	if strings.TrimSpace(currentThreadID) == "" {
		if runtime.codex.pendingResumeThreadID() != "" {
			return runtime
		}
		runtime.codex = newCodexRuntimeStateWithResumeThread(record.identity.Backend(), threadID)
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
		buildDate:    attachment.Hello.IODBuildDate,
		gitSHA:       attachment.Hello.IODGitSHA,
		startTS:      attachment.Hello.StartTS,
		runtimeDir:   filepath.Dir(attachment.ManifestPath),
	}
}
