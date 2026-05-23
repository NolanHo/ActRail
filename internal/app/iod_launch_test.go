package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
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
	"github.com/gorilla/websocket"
)

const (
	fakePIChildEnv          = "ACTRAIL_P3A_FAKE_PI_CHILD"
	fakePIChildLogEnv       = "ACTRAIL_P3A_FAKE_PI_CHILD_LOG"
	fakePIChildMarkEnv      = "ACTRAIL_P2_FAKE_PI_CHILD_MARK"
	fakeCodexSessionPathEnv = "ACTRAIL_FAKE_CODEX_SESSION_PATH"
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

func (a iodLaunchAdapter) CommandArgs(opts agent.Options) ([]string, error) {
	args := append([]string(nil), a.args...)
	if a.backend == session.BackendCodex && opts.ListenURL() != "" && len(args) > 0 {
		args = append([]string{args[0], "--listen", opts.ListenURL()}, args[1:]...)
	}
	return args, nil
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

func TestP3AFakeCodexUnixAppServer(t *testing.T) {
	if os.Getenv(fakePIChildEnv) != "codex-unix" {
		return
	}
	if len(os.Args) == 0 {
		t.Fatal("missing fake Codex socket path")
	}
	socketPath := strings.TrimSpace(os.Args[len(os.Args)-1])
	if socketPath == "" {
		t.Fatal("empty fake Codex socket path")
	}
	logPath := strings.TrimSpace(os.Getenv(fakePIChildLogEnv))
	if logPath == "" {
		t.Fatal("missing fake Codex log path")
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen(%q) error = %v", socketPath, err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	connCh := make(chan *websocket.Conn, 1)
	errCh := make(chan error, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		connCh <- conn
	})}
	go func() {
		if err := server.Serve(listener); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			errCh <- err
		}
	}()
	var conn *websocket.Conn
	select {
	case conn = <-connCh:
	case err := <-errCh:
		t.Fatalf("fake Codex websocket accept error = %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake Codex websocket connection")
	}
	defer conn.Close()
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(%q) error = %v", logPath, err)
	}
	defer file.Close()
	threadID := "codex-thread-1"
	sessionPath := strings.TrimSpace(os.Getenv(fakeCodexSessionPathEnv))
	turn := 0
	for {
		messageType, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return
			}
			t.Fatalf("fake Codex websocket read error = %v", err)
		}
		if messageType != websocket.TextMessage {
			continue
		}
		line := string(msg)
		if _, err := file.WriteString(line + "\n"); err != nil {
			t.Fatalf("WriteString(%q) error = %v", logPath, err)
		}
		if strings.Contains(line, `"method":"initialize"`) {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"id":"initialize-1","result":{"userAgent":"actrail-test"}}`)); err != nil {
				t.Fatalf("fake Codex write initialize response error = %v", err)
			}
		}
		if strings.Contains(line, `"method":"thread/start"`) {
			if sessionPath != "" {
				if err := appendFakeCodexSessionLine(sessionPath, `{"timestamp":"2026-05-08T15:58:02.545Z","type":"session_meta","payload":{"id":"codex-thread-1","cwd":"/tmp/fake-codex"}}`); err != nil {
					t.Fatalf("append fake Codex session meta error = %v", err)
				}
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"id":"thread-start-2","result":{"thread":{"id":"codex-thread-1"}}}`)); err != nil {
				t.Fatalf("fake Codex write thread response error = %v", err)
			}
			threadStarted := `{"method":"thread/started","params":{"thread":{"id":"codex-thread-1"}}}`
			if sessionPath != "" {
				encodedPath, err := json.Marshal(sessionPath)
				if err != nil {
					t.Fatalf("json.Marshal(sessionPath) error = %v", err)
				}
				threadStarted = fmt.Sprintf(`{"method":"thread/started","params":{"thread":{"id":"codex-thread-1","path":%s}}}`, encodedPath)
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(threadStarted)); err != nil {
				t.Fatalf("fake Codex write thread notification error = %v", err)
			}
		}
		if strings.Contains(line, `"method":"turn/start"`) {
			turn++
			turnID := fmt.Sprintf("codex-turn-%d", turn)
			if sessionPath != "" {
				prompt := fakeCodexTurnText(line)
				if prompt == "" {
					prompt = "Implement P6 Codex transport"
				}
				if err := appendFakeCodexSessionLine(sessionPath, fmt.Sprintf(`{"timestamp":"2026-05-08T15:58:03.000Z","type":"event_msg","payload":{"type":"user_message","message":%s}}`, mustJSONQuote(t, prompt))); err != nil {
					t.Fatalf("append fake Codex user message error = %v", err)
				}
				if err := appendFakeCodexSessionLine(sessionPath, `{"timestamp":"2026-05-08T15:58:04.000Z","type":"event_msg","payload":{"type":"agent_message","message":"IOD history reached ActRail.","phase":"final_answer"}}`); err != nil {
					t.Fatalf("append fake Codex assistant message error = %v", err)
				}
				if err := appendFakeCodexSessionLine(sessionPath, `{"timestamp":"2026-05-08T15:58:05.000Z","type":"event_msg","payload":{"type":"task_complete"}}`); err != nil {
					t.Fatalf("append fake Codex task_complete error = %v", err)
				}
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"id":"turn-start-3","result":{"turn":{"id":"%s","status":"inProgress","error":null}}}`, turnID))); err != nil {
				t.Fatalf("fake Codex write turn response error = %v", err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"method":"turn/started","params":{"threadId":"%s","turn":{"id":"%s","status":"inProgress","error":null}}}`, threadID, turnID))); err != nil {
				t.Fatalf("fake Codex write turn notification error = %v", err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"%s","turn":{"id":"%s","status":"completed","error":null}}}`, threadID, turnID))); err != nil {
				t.Fatalf("fake Codex write turn completed notification error = %v", err)
			}
		}
		if strings.Contains(line, `"method":"turn/interrupt"`) {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"id":"turn-interrupt-4","result":{}}`)); err != nil {
				t.Fatalf("fake Codex write interrupt response error = %v", err)
			}
		}
		if err := file.Sync(); err != nil {
			t.Fatalf("Sync(%q) error = %v", logPath, err)
		}
	}
}

func appendFakeCodexSessionLine(path string, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.WriteString(line + "\n"); err != nil {
		return err
	}
	return file.Sync()
}

func fakeCodexTurnText(line string) string {
	var packet struct {
		Params struct {
			Input []struct {
				Text string `json:"text"`
			} `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &packet); err != nil {
		return ""
	}
	for _, item := range packet.Params.Input {
		if text := strings.TrimSpace(item.Text); text != "" {
			return text
		}
	}
	return ""
}

func mustJSONQuote(t *testing.T, text string) string {
	t.Helper()
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("json.Marshal(%q) error = %v", text, err)
	}
	return string(encoded)
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
	t.Setenv(fakePIChildEnv, "codex-unix")
	t.Setenv("ACTRAIL_TEST_BINARY", os.Args[0])
	cwd := filepath.Join(t.TempDir(), "cwd-codex-helper")
	childPath := writeFakeCodexAppServerScript(t)
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
	paths, err := iod.NewGenerationPaths(cfg.Storage.IODRuntimeRoot(), sessionID, binding.GenerationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	waitForChildLogLines(t, childLog, []string{
		"cwd=" + cwd,
		"argv[0]=app-server",
		"argv[1]=--listen",
		"argv[2]=unix://" + paths.ChildSocketPath,
		"argv[3]=--model",
		"argv[4]=gpt-4.1",
		"env[" + fakePIChildMarkEnv + "]=codex-forwarded-via-helper",
	})

	_ = manifest
}

func TestCodexIODControl(t *testing.T) {
	cfg := persistentTestConfig(t)
	childLog := filepath.Join(t.TempDir(), "child.log")
	t.Setenv(fakePIChildLogEnv, childLog)
	t.Setenv(fakePIChildMarkEnv, "codex-jsonrpc-via-helper")
	t.Setenv(fakePIChildEnv, "codex-unix")
	t.Setenv("ACTRAIL_TEST_BINARY", os.Args[0])
	cwd := filepath.Join(t.TempDir(), "cwd-codex-control")
	childPath := writeFakeCodexAppServerScript(t)
	runtimeCfg := realIODHelperRuntimeConfigForBackend(t, session.BackendCodex, []string{"app-server", "--model", "gpt-4.1"}, func(session.Backend) (string, error) {
		return childPath, nil
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, runtimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	_, sessionID, record, binding, manifest := createIODBackedSessionForBackend(t, svc, cfg, "codex", cwd)
	defer deleteSessionIfPresent(t, svc, sessionID)
	paths, err := iod.NewGenerationPaths(cfg.Storage.IODRuntimeRoot(), sessionID, binding.GenerationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}

	if err := record.runtime.EnsureCodexThread(context.Background()); err != nil {
		t.Fatalf("EnsureCodexThread() error = %v", err)
	}
	waitForAppCondition(t, func() bool {
		_, threadID, _ := record.runtime.codex.snapshot()
		return threadID == "codex-thread-1"
	})
	waitForIODCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		return err == nil && state.Transport.State == SessionTransportStateAttached
	})
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
	if !interrupted.Busy {
		t.Fatalf("Interrupt() = %+v, want busy true while Codex interrupt is pending", interrupted)
	}
	waitForChildLogLines(t, childLog, []string{
		"cwd=" + cwd,
		"argv[0]=app-server",
		"argv[1]=--listen",
		"argv[2]=unix://" + paths.ChildSocketPath,
		"argv[3]=--model",
		"argv[4]=gpt-4.1",
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
		`"effort":"high"`,
		`"text":"Implement P6 Codex transport"`,
		`"method":"turn/interrupt"`,
		`"id":"turn-interrupt-4"`,
		`"turnId":"codex-turn-1"`,
	})
	assertNoCodexWAL(t, manifest.WALPath)
}

func TestCodexIODSessionHistoryEndToEnd(t *testing.T) {
	cfg := persistentTestConfig(t)
	childLog := filepath.Join(t.TempDir(), "child.log")
	sessionPath := filepath.Join(t.TempDir(), "codex", "rollout-codex-thread-1.jsonl")
	t.Setenv(fakePIChildLogEnv, childLog)
	t.Setenv(fakePIChildMarkEnv, "codex-history-via-helper")
	t.Setenv(fakePIChildEnv, "codex-unix")
	t.Setenv(fakeCodexSessionPathEnv, sessionPath)
	t.Setenv("ACTRAIL_TEST_BINARY", os.Args[0])
	cwd := filepath.Join(t.TempDir(), "cwd-codex-history")
	childPath := writeFakeCodexAppServerScript(t)
	runtimeCfg := realIODHelperRuntimeConfigForBackend(t, session.BackendCodex, []string{"app-server", "--model", "gpt-4.1"}, func(session.Backend) (string, error) {
		return childPath, nil
	})
	svc, err := NewPersistentStubForTest(cfg, func() time.Time { return time.Unix(1760000000, 0).UTC() }, runtimeCfg)
	if err != nil {
		t.Fatalf("NewPersistentStubForTest() error = %v", err)
	}
	_, sessionID, record, binding, manifest := createIODBackedSessionForBackend(t, svc, cfg, "codex", cwd)
	defer deleteSessionIfPresent(t, svc, sessionID)
	paths, err := iod.NewGenerationPaths(cfg.Storage.IODRuntimeRoot(), sessionID, binding.GenerationID)
	if err != nil {
		t.Fatalf("NewGenerationPaths() error = %v", err)
	}
	if err := record.runtime.EnsureCodexThread(context.Background()); err != nil {
		t.Fatalf("EnsureCodexThread() error = %v", err)
	}
	waitForIODCondition(t, func() bool {
		state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
		if err != nil {
			return false
		}
		_, threadID, _ := record.runtime.codex.snapshot()
		return threadID == "codex-thread-1" && state.RuntimeState != string(codexRuntimePhaseThreadStarting)
	})
	waitForChildLogLines(t, childLog, []string{
		"cwd=" + cwd,
		"argv[0]=app-server",
		"argv[1]=--listen",
		"argv[2]=unix://" + paths.ChildSocketPath,
		"env[" + fakePIChildMarkEnv + "]=codex-history-via-helper",
	})
	sent, err := svc.Send(context.Background(), SendRequest{SessionID: sessionID, Text: "Check IOD history"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !sent.Busy {
		t.Fatalf("Send() = %+v, want busy true while fake Codex turn is accepted", sent)
	}
	waitForIODCondition(t, func() bool {
		response, err := svc.SessionMessages(context.Background(), SessionMessagesRequest{SessionID: sessionID, Limit: 20, IncludeToolEvents: true})
		if err != nil {
			return false
		}
		if response.TailSeq < 3 {
			return false
		}
		var sawUser, sawAssistant bool
		for _, item := range response.Items {
			if item.Role == "user" && item.Text == "Check IOD history" && strings.HasPrefix(item.EventID, "codex:event:user:") {
				sawUser = true
			}
			if item.Role == "assistant" && item.Text == "IOD history reached ActRail." && stringValue(item.Details["phase"]) == "final_answer" && strings.HasPrefix(item.EventID, "codex:event:assistant:") {
				sawAssistant = true
			}
		}
		return sawUser && sawAssistant
	})
	waitForIODCondition(t, func() bool {
		history, err := record.runtime.helper.sessionHistory(context.Background())
		return err == nil && history.SourcePath == sessionPath && history.TaskComplete && history.IndexedCount == 2
	})
	history, err := record.runtime.helper.sessionHistory(context.Background())
	if err != nil {
		t.Fatalf("sessionHistory() error = %v", err)
	}
	if got := history.SourcePath; got != sessionPath {
		t.Fatalf("helper history path = %q, want %q", got, sessionPath)
	}
	if !history.TaskComplete || history.IndexedCount != 2 {
		t.Fatalf("sessionHistory() = %+v, want task_complete with two indexed display messages", history)
	}
	assertNoCodexWAL(t, manifest.WALPath)
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
	_, err = svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "pi", PIAgentGRPC: boolPtr(false), CWD: t.TempDir()})
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
		t.Fatalf("Send() = %+v, want busy true immediately after Pi RPC send", sent)
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
	req := CreateSessionRequest{AgentBackend: backend, CWD: cwd}
	if strings.EqualFold(backend, "pi") {
		req.PIAgentGRPC = boolPtr(false)
	}
	created, err := svc.CreateSession(context.Background(), req)
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

func assertNoCodexWAL(t *testing.T, walPath string) {
	t.Helper()
	if _, err := os.Stat(walPath); !os.IsNotExist(err) {
		t.Fatalf("Codex helper WAL stat error = %v, want no WAL", err)
	}
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
		"sock=\"\"\n" +
		"prev=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--listen\" ]; then sock=\"${arg#unix://}\"; fi\n" +
		"  prev=\"$arg\"\n" +
		"done\n" +
		"if [ -z \"$sock\" ]; then echo 'missing --listen unix:// socket' >> \"$log\"; exit 2; fi\n" +
		"exec \"$ACTRAIL_TEST_BINARY\" -test.run '^TestP3AFakeCodexUnixAppServer$' -- \"$sock\"\n"
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
