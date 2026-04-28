package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"actrail/internal/adapters/agent"
	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

const (
	fakePIChildEnv     = "ACTRAIL_P3A_FAKE_PI_CHILD"
	fakePIChildLogEnv  = "ACTRAIL_P3A_FAKE_PI_CHILD_LOG"
	fakePIChildMarkEnv = "ACTRAIL_P2_FAKE_PI_CHILD_MARK"
)

var (
	buildIODHelperOnce sync.Once
	buildIODHelperPath string
	buildIODHelperErr  error
)

type iodLaunchAdapter struct {
	backend session.Backend
	args    []string
}

func (a iodLaunchAdapter) Backend() session.Backend {
	return a.backend
}

func (a iodLaunchAdapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (a iodLaunchAdapter) ValidateOptions(agent.Options) error {
	return nil
}

func (a iodLaunchAdapter) CommandArgs(agent.Options) ([]string, error) {
	return append([]string(nil), a.args...), nil
}

func TestP3AFakePIChildProcess(t *testing.T) {
	if os.Getenv(fakePIChildEnv) != "1" {
		return
	}
	logPath := strings.TrimSpace(os.Getenv(fakePIChildLogEnv))
	if logPath == "" {
		t.Fatal("missing fake PI child log path")
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(%q) error = %v", logPath, err)
	}
	defer file.Close()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
		}
	}()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRuntimeLineBytes)
	for scanner.Scan() {
		if _, err := file.WriteString(scanner.Text() + "\n"); err != nil {
			t.Fatalf("WriteString(%q) error = %v", logPath, err)
		}
		if err := file.Sync(); err != nil {
			t.Fatalf("Sync(%q) error = %v", logPath, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error = %v", err)
	}
}

func TestCreateSessionViaIod(t *testing.T) {
	cfg := persistentTestConfig(t)
	svc := newPersistentIODHelperStub(t, cfg, filepath.Join(t.TempDir(), "child.log"))
	created, sessionID, record, binding, manifest := createIODBackedSession(t, svc, cfg, filepath.Join(t.TempDir(), "cwd-create"))
	defer deleteSessionIfPresent(t, svc, sessionID)

	if !created.OK || created.Session == nil {
		t.Fatalf("CreateSession() = %+v, want created session payload", created)
	}
	if record.runtime.helper == nil {
		t.Fatal("record.runtime.helper = nil, want iod-backed runtime")
	}
	if record.runtime.helper.handle == nil {
		t.Fatal("record.runtime.helper.handle = nil, want helper process handle")
	}
	if record.runtime.helper.handle.PTY() != nil {
		t.Fatal("helper process handle PTY() != nil, want pipe-backed helper launch")
	}
	if record.runtime.helper.handle.Spec().Command().Path() != builtIODHelperBinary(t) {
		t.Fatalf("helper command path = %q, want %q", record.runtime.helper.handle.Spec().Command().Path(), builtIODHelperBinary(t))
	}
	args := record.runtime.helper.handle.Spec().Command().Args()
	if !slices.Contains(args, "-session-id") || !slices.Contains(args, sessionID.String()) {
		t.Fatalf("helper args = %#v, want session id flag for %q", args, sessionID)
	}
	if !slices.Contains(args, "-generation-id") || !slices.Contains(args, binding.GenerationID.String()) {
		t.Fatalf("helper args = %#v, want generation id flag for %q", args, binding.GenerationID)
	}
	if manifest.GenerationID != binding.GenerationID {
		t.Fatalf("manifest generation id = %q, want %q", manifest.GenerationID, binding.GenerationID)
	}
	if manifest.HelperPID <= 0 {
		t.Fatalf("manifest helper pid = %d, want > 0", manifest.HelperPID)
	}
	if manifest.ChildPID == nil || *manifest.ChildPID <= 0 {
		t.Fatalf("manifest child pid = %#v, want > 0", manifest.ChildPID)
	}
	if _, err := os.Stat(manifest.ControlSocketPath); err != nil {
		t.Fatalf("Stat(%q) error = %v", manifest.ControlSocketPath, err)
	}
	bindingPath := svc.helperBindings.path(sessionID)
	if got, want := filepath.Dir(bindingPath), cfg.Storage.IODBindingsDir(); got != want {
		t.Fatalf("helper binding dir = %q, want %q", got, want)
	}
	if _, err := os.Stat(bindingPath); err != nil {
		t.Fatalf("Stat(%q) error = %v", bindingPath, err)
	}
}

func TestCreateCodexSessionViaIod(t *testing.T) {
	cfg := persistentTestConfig(t)
	childLog := filepath.Join(t.TempDir(), "child.log")
	t.Setenv(fakePIChildLogEnv, childLog)
	t.Setenv(fakePIChildMarkEnv, "codex-forwarded-via-helper")
	cwd := filepath.Join(t.TempDir(), "cwd-codex-helper")
	childPath := writeFakePIChildScript(t)
	runtimeCfg := realIODHelperRuntimeConfigForBackend(t, session.BackendCodex, []string{"app-server", "--model", "gpt-4.1"}, func(backend session.Backend) (string, error) {
		return childPath, nil
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, runtimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	created, sessionID, record, binding, manifest := createIODBackedSessionForBackend(t, svc, cfg, "codex", cwd)
	defer deleteSessionIfPresent(t, svc, sessionID)

	if !created.OK || created.Session == nil {
		t.Fatalf("CreateSession() = %+v, want created session payload", created)
	}
	if record.runtime.helper == nil {
		t.Fatal("record.runtime.helper = nil, want iod-backed runtime")
	}
	if record.runtime.protocol != runtimeProtocolCodexRPC {
		t.Fatalf("record.runtime.protocol = %q, want %q", record.runtime.protocol, runtimeProtocolCodexRPC)
	}
	waitForChildLogLines(t, childLog, []string{
		"cwd=" + cwd,
		"argv[0]=app-server",
		"argv[1]=--model",
		"argv[2]=gpt-4.1",
		"env[" + fakePIChildMarkEnv + "]=codex-forwarded-via-helper",
	})

	_ = binding
	_ = manifest
}

func TestCodexIODControl(t *testing.T) {
	cfg := persistentTestConfig(t)
	childLog := filepath.Join(t.TempDir(), "child.log")
	t.Setenv(fakePIChildLogEnv, childLog)
	t.Setenv(fakePIChildMarkEnv, "codex-jsonrpc-via-helper")
	cwd := filepath.Join(t.TempDir(), "cwd-codex-control")
	childPath := writeFakePIChildScript(t)
	runtimeCfg := realIODHelperRuntimeConfigForBackend(t, session.BackendCodex, []string{"app-server", "--model", "gpt-4.1"}, func(session.Backend) (string, error) {
		return childPath, nil
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, runtimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	_, sessionID, record, binding, manifest := createIODBackedSessionForBackend(t, svc, cfg, "codex", cwd)
	defer deleteSessionIfPresent(t, svc, sessionID)

	if err := record.runtime.EnsureCodexThread(context.Background()); err != nil {
		t.Fatalf("EnsureCodexThread() error = %v", err)
	}
	svc.noteCodexThreadID(sessionID, "codex-thread-1")
	sent, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "Implement P6 Codex transport"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !sent.Busy {
		t.Fatalf("Send() = %+v, want busy true", sent)
	}
	svc.noteCodexTurnID(sessionID, "codex-turn-1")
	interrupted, err := svc.Interrupt(context.Background(), InterruptRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	if interrupted.Busy {
		t.Fatalf("Interrupt() = %+v, want busy false", interrupted)
	}
	waitForChildLogLines(t, childLog, []string{
		"cwd=" + cwd,
		"argv[0]=app-server",
		"argv[1]=--model",
		"argv[2]=gpt-4.1",
		"env[" + fakePIChildMarkEnv + "]=codex-jsonrpc-via-helper",
	})
	waitForChildLogContains(t, childLog, []string{
		`"method":"initialize"`,
		`"id":"initialize-1"`,
		`"clientInfo":{"name":"actrail","version":"0"}`,
		`"method":"thread/start"`,
		`"id":"thread-start-2"`,
		`"experimentalRawEvents":false`,
		`"persistExtendedHistory":false`,
		`"method":"turn/start"`,
		`"id":"turn-start-3"`,
		`"threadId":"codex-thread-1"`,
		`"text":"Implement P6 Codex transport"`,
		`"method":"turn/interrupt"`,
		`"id":"turn-interrupt-4"`,
		`"turnId":"codex-turn-1"`,
	})
	waitForAcceptedCommands(t, manifest.WALPath, sessionID, binding.GenerationID, []iod.PacketKind{iod.PacketCommandSend})
}

func TestIODHelperForwardsChildArgvCWDAndEnvironment(t *testing.T) {
	cfg := persistentTestConfig(t)
	childLog := filepath.Join(t.TempDir(), "child.log")
	t.Setenv(fakePIChildLogEnv, childLog)
	t.Setenv(fakePIChildMarkEnv, "forwarded-via-helper")
	cwd := filepath.Join(t.TempDir(), "cwd-forwarding")
	childPath := writeFakePIChildScript(t)
	runtimeCfg := realIODHelperRuntimeConfig(t, []string{"--mode", "rpc", "--transport", "stdio"}, func(session.Backend) (string, error) {
		return childPath, nil
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, runtimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	_, sessionID, _, _, _ := createIODBackedSession(t, svc, cfg, cwd)
	defer deleteSessionIfPresent(t, svc, sessionID)

	waitForChildLogLines(t, childLog, []string{
		"cwd=" + cwd,
		"argv[0]=--mode",
		"argv[1]=rpc",
		"argv[2]=--transport",
		"argv[3]=stdio",
		"env[" + fakePIChildMarkEnv + "]=forwarded-via-helper",
	})
}

func TestCreateSessionRollback(t *testing.T) {
	cfg := persistentTestConfig(t)
	runtimeCfg := realIODHelperRuntimeConfig(t, nil, func(session.Backend) (string, error) {
		return filepath.Join(t.TempDir(), "missing-pi-child"), nil
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, runtimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	_, err = svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", CWD: t.TempDir()})
	if err == nil {
		t.Fatal("CreateSession() error = nil, want helper launch failure")
	}
	if items := svc.registry.List(); len(items) != 0 {
		t.Fatalf("len(registry.List()) = %d, want 0 after helper rollback", len(items))
	}
	bindings, err := svc.helperBindings.Load()
	if err != nil {
		t.Fatalf("helperBindings.Load() error = %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("helper bindings = %#v, want none", bindings)
	}
	runtimeRoot := iodclient.RuntimeRoot(cfg.Storage.DataDir)
	entries, err := os.ReadDir(runtimeRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir(%q) error = %v", runtimeRoot, err)
	}
	if len(entries) != 0 {
		t.Fatalf("runtime root entries = %#v, want none after rollback", entries)
	}
}

func TestLiveControlUsesIod(t *testing.T) {
	cfg := persistentTestConfig(t)
	childLog := filepath.Join(t.TempDir(), "child.log")
	svc := newPersistentIODHelperStub(t, cfg, childLog)
	_, sessionID, record, binding, manifest := createIODBackedSession(t, svc, cfg, filepath.Join(t.TempDir(), "cwd-control"))
	defer deleteSessionIfPresent(t, svc, sessionID)

	if record.runtime.handle.PTY() != nil {
		t.Fatal("record.runtime.handle.PTY() != nil, want helper process handle with no direct child PTY")
	}
	sent, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "Implement helper cutover"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !sent.Busy {
		t.Fatalf("Send() = %+v, want busy true", sent)
	}
	if err := svc.SetSessionUIRequest(sessionID, SessionUIRequestSnapshot{RequestID: "ask_1", Kind: "ask_user", Prompt: "Choose one"}); err != nil {
		t.Fatalf("SetSessionUIRequest() error = %v", err)
	}
	resolved, err := svc.RespondUI(context.Background(), UIResponseRequest{SessionID: sessionID, ResponseTo: "ask_1", Value: "A"})
	if err != nil {
		t.Fatalf("RespondUI() error = %v", err)
	}
	if resolved.ResolvedRequestID != "ask_1" {
		t.Fatalf("RespondUI() = %+v, want resolved ask_1", resolved)
	}
	interrupted, err := svc.Interrupt(context.Background(), InterruptRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	if interrupted.Busy {
		t.Fatalf("Interrupt() = %+v, want busy false", interrupted)
	}
	waitForChildLogLines(t, childLog, []string{
		`{"type":"prompt","message":"Implement helper cutover"}`,
		`{"type":"extension_ui_response","id":"ask_1","value":"A"}`,
	})
	waitForAcceptedCommands(t, manifest.WALPath, sessionID, binding.GenerationID, []iod.PacketKind{
		iod.PacketCommandSend,
		iod.PacketCommandUIResponseSubmit,
		iod.PacketCommandInterrupt,
	})
}

func TestCreateSessionProducesDiscoverableHelper(t *testing.T) {
	cfg := persistentTestConfig(t)
	svc := newPersistentIODHelperStub(t, cfg, filepath.Join(t.TempDir(), "child.log"))
	_, sessionID, _, binding, _ := createIODBackedSession(t, svc, cfg, filepath.Join(t.TempDir(), "cwd-discovery"))
	defer deleteSessionIfPresent(t, svc, sessionID)

	runtimeRoot := iodclient.RuntimeRoot(cfg.Storage.DataDir)
	manifestPath := iodclient.GenerationManifestPath(runtimeRoot, sessionID, binding.GenerationID)
	discovered, err := iodclient.DiscoverManifests(runtimeRoot)
	if err != nil {
		t.Fatalf("DiscoverManifests(%q) error = %v", runtimeRoot, err)
	}
	if !containsDiscoveredManifest(discovered, manifestPath, sessionID, binding.GenerationID) {
		t.Fatalf("DiscoverManifests() = %#v, want manifest %q", discovered, manifestPath)
	}
	rehydrated, err := NewPersistentStubForTest(cfg, func() time.Time { return time.Unix(1760003600, 0).UTC() }, RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewPersistentStubForTest(restart) error = %v", err)
	}
	attachment, ok := rehydrated.helpers.Attachment(sessionID)
	if !ok {
		t.Fatalf("helper attachment for %q not found", sessionID)
	}
	if attachment.Binding.GenerationID != binding.GenerationID {
		t.Fatalf("attachment generation id = %q, want %q", attachment.Binding.GenerationID, binding.GenerationID)
	}
	if attachment.ManifestPath != manifestPath {
		t.Fatalf("attachment manifest path = %q, want %q", attachment.ManifestPath, manifestPath)
	}
}

func newPersistentIODHelperStub(t *testing.T, cfg config.Config, childLogPath string) *Stub {
	t.Helper()
	t.Setenv(fakePIChildLogEnv, childLogPath)
	childPath := writeFakePIChildScript(t)
	runtimeCfg := realIODHelperRuntimeConfig(t, nil, func(session.Backend) (string, error) {
		return childPath, nil
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, runtimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	return svc
}

func realIODHelperRuntimeConfig(t *testing.T, adapterArgs []string, resolveChild func(session.Backend) (string, error)) RuntimeConfig {
	t.Helper()
	return realIODHelperRuntimeConfigForBackend(t, session.BackendPI, adapterArgs, resolveChild)
}

func realIODHelperRuntimeConfigForBackend(t *testing.T, backend session.Backend, adapterArgs []string, resolveChild func(session.Backend) (string, error)) RuntimeConfig {
	t.Helper()
	catalog, err := agent.NewCatalog(iodLaunchAdapter{backend: backend, args: adapterArgs})
	if err != nil {
		t.Fatalf("agent.NewCatalog() error = %v", err)
	}
	return RuntimeConfig{
		Catalog:      catalog,
		UseIODHelper: true,
		ResolveBinPath: func(resolvedBackend session.Backend) (string, error) {
			if resolvedBackend != backend {
				return "", fmt.Errorf("backend = %q, want %q", resolvedBackend, backend)
			}
			return resolveChild(resolvedBackend)
		},
		ResolveIODHelperBinPath: func() (string, error) {
			return builtIODHelperBinary(t), nil
		},
	}
}

func createIODBackedSession(t *testing.T, svc *Stub, cfg config.Config, cwd string) (CreateSessionResponse, session.SessionID, sessionRecord, *RuntimeHelperBinding, iod.GenerationManifest) {
	t.Helper()
	return createIODBackedSessionForBackend(t, svc, cfg, "pi", cwd)
}

func createIODBackedSessionForBackend(t *testing.T, svc *Stub, cfg config.Config, backend string, cwd string) (CreateSessionResponse, session.SessionID, sessionRecord, *RuntimeHelperBinding, iod.GenerationManifest) {
	t.Helper()
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", cwd, err)
	}
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: backend, CWD: cwd})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	record, err := svc.lookupSession(sessionID)
	if err != nil {
		t.Fatalf("lookupSession() error = %v", err)
	}
	binding, err := record.runtime.CurrentHelperBinding(sessionID)
	if err != nil {
		t.Fatalf("CurrentHelperBinding() error = %v", err)
	}
	if binding == nil {
		t.Fatal("CurrentHelperBinding() = nil, want live helper generation binding")
	}
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(cfg.Storage.DataDir), sessionID, binding.GenerationID)
	manifest, err := iod.ReadGenerationManifest(manifestPath)
	if err != nil {
		t.Fatalf("ReadGenerationManifest(%q) error = %v", manifestPath, err)
	}
	return created, sessionID, record, binding, manifest
}

func deleteSessionIfPresent(t *testing.T, svc *Stub, sessionID session.SessionID) {
	t.Helper()
	if svc == nil {
		return
	}
	if _, ok := svc.registry.Lookup(sessionID); !ok {
		return
	}
	deleted, err := svc.DeleteSession(context.Background(), DeleteSessionRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("DeleteSession(%q) error = %v", sessionID, err)
	}
	if !deleted.OK || !deleted.Removed {
		t.Fatalf("DeleteSession(%q) = %+v, want removed session", sessionID, deleted)
	}
}

func waitForChildLogLines(t *testing.T, path string, want []string) {
	t.Helper()
	waitForIODCondition(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		for _, item := range want {
			if !slices.Contains(lines, item) {
				return false
			}
		}
		return true
	})
}

func waitForChildLogContains(t *testing.T, path string, want []string) {
	t.Helper()
	waitForIODCondition(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		text := string(data)
		for _, item := range want {
			if !strings.Contains(text, item) {
				return false
			}
		}
		return true
	})
}

func waitForAcceptedCommands(t *testing.T, walPath string, sessionID session.SessionID, generationID iod.GenerationID, want []iod.PacketKind) {
	t.Helper()
	waitForIODCondition(t, func() bool {
		replay, err := iod.ReplayWAL(walPath, sessionID, generationID, 0)
		if err != nil {
			return false
		}
		kinds := make([]iod.PacketKind, 0, len(replay.Records))
		for _, record := range replay.Records {
			if record.Header.Class != iod.WALRecordCommandAccepted {
				continue
			}
			var payload struct {
				CommandKind iod.PacketKind `json:"command_kind"`
			}
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				return false
			}
			kinds = append(kinds, payload.CommandKind)
		}
		for _, item := range want {
			if !slices.Contains(kinds, item) {
				return false
			}
		}
		return true
	})
}

func waitForIODCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func writeFakePIChildScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-pi-child.sh")
	content := "#!/bin/sh\n" +
		"set -eu\n" +
		"trap '' INT\n" +
		"log=\"${" + fakePIChildLogEnv + ":?missing log path}\"\n" +
		": > \"$log\"\n" +
		"printf 'cwd=%s\\n' \"$(pwd)\" >> \"$log\"\n" +
		"i=0\n" +
		"for arg in \"$@\"; do\n" +
		"  printf 'argv[%s]=%s\\n' \"$i\" \"$arg\" >> \"$log\"\n" +
		"  i=$((i+1))\n" +
		"done\n" +
		"printf 'env[" + fakePIChildMarkEnv + "]=%s\\n' \"${" + fakePIChildMarkEnv + "-}\" >> \"$log\"\n" +
		"while IFS= read -r line; do\n" +
		"  printf '%s\\n' \"$line\" >> \"$log\"\n" +
		"done\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func writeFakeCodexAppServerScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-codex-app-server.sh")
	content := "#!/bin/sh\n" +
		"set -eu\n" +
		"trap '' INT\n" +
		"log=\"${" + fakePIChildLogEnv + ":?missing log path}\"\n" +
		": > \"$log\"\n" +
		"printf 'cwd=%s\\n' \"$(pwd)\" >> \"$log\"\n" +
		"i=0\n" +
		"for arg in \"$@\"; do\n" +
		"  printf 'argv[%s]=%s\\n' \"$i\" \"$arg\" >> \"$log\"\n" +
		"  i=$((i+1))\n" +
		"done\n" +
		"printf 'env[" + fakePIChildMarkEnv + "]=%s\\n' \"${" + fakePIChildMarkEnv + "-}\" >> \"$log\"\n" +
		"thread_id=codex-thread-1\n" +
		"turn=0\n" +
		"while IFS= read -r line; do\n" +
		"  printf '%s\\n' \"$line\" >> \"$log\"\n" +
		"  case \"$line\" in\n" +
		"    *\"method\":\"initialize\"*)\n" +
		"      printf '%s\\n' '{\"id\":\"initialize-1\",\"result\":{\"userAgent\":\"actrail-test\"}}'\n" +
		"      ;;\n" +
		"    *\"method\":\"thread/start\"*)\n" +
		"      printf '%s\\n' '{\"id\":\"thread-start-2\",\"result\":{\"thread\":{\"id\":\"codex-thread-1\"}}}'\n" +
		"      printf '%s\\n' '{\"method\":\"thread/started\",\"params\":{\"thread\":{\"id\":\"codex-thread-1\"}}}'\n" +
		"      ;;\n" +
		"    *\"method\":\"turn/start\"*)\n" +
		"      turn=$((turn+1))\n" +
		"      turn_id=codex-turn-$turn\n" +
		"      printf '{\"id\":\"turn-start-3\",\"result\":{\"turn\":{\"id\":\"%s\",\"status\":\"inProgress\",\"error\":null}}}\\n' \"$turn_id\"\n" +
		"      printf '{\"method\":\"turn/started\",\"params\":{\"threadId\":\"%s\",\"turn\":{\"id\":\"%s\",\"status\":\"inProgress\",\"error\":null}}}\\n' \"$thread_id\" \"$turn_id\"\n" +
		"      ;;\n" +
		"    *\"method\":\"turn/interrupt\"*)\n" +
		"      printf '%s\\n' '{\"id\":\"turn-interrupt-4\",\"result\":{}}'\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func containsDiscoveredManifest(items []iodclient.DiscoveredManifest, path string, sessionID session.SessionID, generationID iod.GenerationID) bool {
	for _, item := range items {
		if item.Path == path && item.Manifest.SessionID == sessionID && item.Manifest.GenerationID == generationID {
			return true
		}
	}
	return false
}

func builtIODHelperBinary(t *testing.T) string {
	t.Helper()
	buildIODHelperOnce.Do(func() {
		root, err := repoRoot()
		if err != nil {
			buildIODHelperErr = err
			return
		}
		dir, err := os.MkdirTemp("", "actrail-iod-test-")
		if err != nil {
			buildIODHelperErr = err
			return
		}
		buildIODHelperPath = filepath.Join(dir, "actrail-iod")
		cmd := exec.Command("/usr/local/go/bin/go", "build", "-o", buildIODHelperPath, "./cmd/actrail-iod")
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			buildIODHelperErr = fmt.Errorf("go build ./cmd/actrail-iod: %w\n%s", err, strings.TrimSpace(string(output)))
		}
	})
	if buildIODHelperErr != nil {
		t.Fatal(buildIODHelperErr)
	}
	return buildIODHelperPath
}

func repoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := cwd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
	}
	return "", fmt.Errorf("go.mod not found from %q", cwd)
}
