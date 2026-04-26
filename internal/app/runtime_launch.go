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
	"strings"
	"sync"
	"time"

	"actrail/internal/adapters/agent"
	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

type runtimeCatalog interface {
	LaunchSpec(agent.Request) (process.LaunchSpec, error)
}

type runtimeLauncher interface {
	Launch(context.Context, runtimeLaunchRequest) (sessionRuntime, error)
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
	IODDialer               iodclient.Dialer
	IODRuntimeRoot          string
	UseIODHelper            bool
	NewGenerationID         func(session.SessionID) (iod.GenerationID, error)
	CurrentHelperBinding    func(session.SessionID) (*RuntimeHelperBinding, error)
}

type runtimeLaunchRequest struct {
	SessionID        session.SessionID
	Backend          session.Backend
	CWD              string
	Provider         string
	Model            string
	ReasoningEffort  string
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
	env                     process.Environment
	io                      process.IO
}

type runtimeProtocol string

const (
	runtimeProtocolTTY   runtimeProtocol = "tty"
	runtimeProtocolPIRPC runtimeProtocol = "pi_rpc"

	helperReadyPollInterval = 25 * time.Millisecond
	helperReadyTimeout      = 5 * time.Second
	helperStopTimeout       = 3 * time.Second
)

type sessionRuntime struct {
	launchSpec            process.LaunchSpec
	handle                process.Handle
	protocol              runtimeProtocol
	helper                *runtimeIODHelper
	helperBinding         *RuntimeHelperBinding
	currentHelperBinding  func(session.SessionID) (*RuntimeHelperBinding, error)
}

type runtimeIODHelper struct {
	handle       process.Handle
	client       *iodclient.Client
	sessionID    session.SessionID
	generationID iod.GenerationID
	helperPID    int
	childPID     *int
	runtimeDir   string
	commandMu    sync.Mutex
	commandSeq   uint64
}

type piRPCPromptCommand struct {
	Type    string `json:"type"`
	Message string `json:"message"`
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
	newGenerationID := cfg.NewGenerationID
	if newGenerationID == nil {
		newGenerationID = defaultRuntimeGenerationID
	}
	return processRuntimeLauncher{
		catalog:                 catalog,
		runner:                  runner,
		resolveBinPath:          resolveBinPath,
		resolveIODHelperBinPath: resolveIODHelperBinPath,
		currentHelperBinding:    cfg.CurrentHelperBinding,
		newGenerationID:         newGenerationID,
		iodRuntimeRoot:          strings.TrimSpace(cfg.IODRuntimeRoot),
		useIODHelper:            cfg.UseIODHelper,
		dialer:                  cfg.IODDialer,
		env:                     env,
		io:                      ioSpec,
	}
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
	if backend == session.BackendPI {
		return runtimeProtocolPIRPC
	}
	return runtimeProtocolTTY
}

func (l processRuntimeLauncher) Launch(ctx context.Context, req runtimeLaunchRequest) (sessionRuntime, error) {
	if err := ctx.Err(); err != nil {
		return sessionRuntime{}, err
	}
	if err := req.Backend.Validate(); err != nil {
		return sessionRuntime{}, err
	}
	if l.useIODHelper && req.Backend == session.BackendPI {
		return l.launchViaIODHelper(ctx, req)
	}
	return l.launchDirect(ctx, req)
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
		client:       client,
		sessionID:    req.SessionID,
		generationID: generationID,
		helperPID:    hello.HelperPID,
		childPID:     copyIntPtr(hello.ChildPID),
		runtimeDir:   paths.RuntimeDir,
	}
	binding := &RuntimeHelperBinding{GenerationID: generationID}
	return sessionRuntime{
		launchSpec:    childLaunchSpec,
		handle:        handle,
		protocol:      runtimeProtocolForBackend(req.Backend),
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
	options, err := agent.NewOptions(req.Provider, req.Model, req.ReasoningEffort)
	if err != nil {
		return process.LaunchSpec{}, err
	}
	launchReq, err := agent.NewRequest(req.Backend, binPath, req.CWD, l.env, l.io, options)
	if err != nil {
		return process.LaunchSpec{}, err
	}
	return l.catalog.LaunchSpec(launchReq)
}

func (l processRuntimeLauncher) helperLaunchSpec(req runtimeLaunchRequest, helperBinPath string, generationID iod.GenerationID, childLaunchSpec process.LaunchSpec) (process.LaunchSpec, error) {
	commandArgs := []string{
		"-session-id", req.SessionID.String(),
		"-generation-id", generationID.String(),
		"-runtime-root", strings.TrimSpace(l.iodRuntimeRoot),
		"-cwd", req.CWD,
		childLaunchSpec.Command().Path(),
	}
	commandArgs = append(commandArgs, childLaunchSpec.Command().Args()...)
	command, err := process.NewCommand(helperBinPath, commandArgs...)
	if err != nil {
		return process.LaunchSpec{}, err
	}
	ioSpec, err := process.PipeIO(process.LogPaths{})
	if err != nil {
		return process.LaunchSpec{}, err
	}
	return process.NewLaunchSpec(command, req.CWD, l.env, ioSpec)
}

func (l processRuntimeLauncher) waitForHelperReady(ctx context.Context, handle process.Handle, paths iod.GenerationPaths) (iod.GenerationManifest, iod.HelloPacket, *iodclient.Client, error) {
	readyCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		readyCtx, cancel = context.WithTimeout(ctx, helperReadyTimeout)
		defer cancel()
	}
	exitCh := make(chan error, 1)
	if handle != nil {
		go func() {
			_, err := handle.Wait(context.Background())
			if err != nil {
				exitCh <- err
				return
			}
			exitCh <- errors.New("iod helper exited before control socket became ready")
		}()
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
		case err := <-exitCh:
			return iod.GenerationManifest{}, iod.HelloPacket{}, nil, err
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

func (r sessionRuntime) SendPrompt(ctx context.Context, text string) error {
	payload := strings.TrimSpace(text)
	if payload == "" {
		return fmt.Errorf("runtime prompt is required")
	}
	if r.helper != nil {
		if r.protocol != runtimeProtocolPIRPC {
			return errRuntimeInputUnavailable
		}
		encoded, err := json.Marshal(piRPCPromptCommand{Type: "prompt", Message: payload})
		if err != nil {
			return fmt.Errorf("marshal runtime command: %w", err)
		}
		return r.helper.command(ctx, iod.CommandSend, encoded)
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
	if r.helper != nil {
		if r.protocol != runtimeProtocolPIRPC {
			return errRuntimeInputUnavailable
		}
		encoded, err := json.Marshal(piRPCExtensionUIResponseCommand{Type: "extension_ui_response", ID: resolvedID, Value: payload})
		if err != nil {
			return fmt.Errorf("marshal runtime command: %w", err)
		}
		return r.helper.command(ctx, iod.CommandUIResponseSubmit, encoded)
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
	if r.helper != nil {
		return r.helper.command(ctx, iod.CommandInterrupt, json.RawMessage(`{}`))
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
	if h == nil || h.client == nil {
		return errRuntimeInputUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	commandID, err := h.nextCommandID()
	if err != nil {
		return err
	}
	packet, err := iod.NewCommandPacket(h.sessionID, h.generationID, name, commandID, payload)
	if err != nil {
		return err
	}
	h.commandMu.Lock()
	defer h.commandMu.Unlock()
	result, err := h.client.Command(ctx, packet)
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

func (h *runtimeIODHelper) shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if h.client != nil {
		defer func() { _ = h.client.Close() }()
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

func (s *Stub) runtimeForSession(sessionID session.SessionID, runtime sessionRuntime) sessionRuntime {
	if runtime.helper != nil || s == nil || s.helpers == nil {
		return runtime
	}
	attachment, ok := s.helpers.Attachment(sessionID)
	if !ok {
		return runtime
	}
	binding := &RuntimeHelperBinding{GenerationID: attachment.Binding.GenerationID, LastReplayOffset: attachment.Binding.LastReplayOffset}
	runtime.helperBinding = binding
	runtime.helper = runtimeIODHelperFromAttachment(attachment)
	runtime.currentHelperBinding = func(session.SessionID) (*RuntimeHelperBinding, error) {
		resolved := *binding
		return &resolved, nil
	}
	return runtime
}

func runtimeIODHelperFromAttachment(attachment attachedHelper) *runtimeIODHelper {
	return &runtimeIODHelper{
		client:       attachment.Client,
		sessionID:    attachment.Binding.SessionID,
		generationID: attachment.Binding.GenerationID,
		helperPID:    attachment.Hello.HelperPID,
		childPID:     copyIntPtr(attachment.Hello.ChildPID),
		runtimeDir:   filepath.Dir(attachment.ManifestPath),
	}
}
