package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

type helperReadErrorDialerFunc func(context.Context, string, string) (net.Conn, error)

func (f helperReadErrorDialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func TestStaleHelperReadErrorDoesNotBreakCurrentCodexGeneration(t *testing.T) {
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	oldGeneration := mustHelperGenerationID(t, "g_old_helper_read_error")
	newGeneration := mustHelperGenerationID(t, "g_new_helper_read_error")
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper:   &runtimeIODHelper{generationID: newGeneration},
			codex:    newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-helper-read-error")
		record.transport = transportSnapshotAttached(newGeneration)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	svc.handleHelperReadError(sessionID, session.BackendCodex, oldGeneration, errors.New("old stream closed"))

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.GenerationID != newGeneration.String() || state.Transport.ResetRequired {
		t.Fatalf("SessionState() after stale read error = %+v, want current generation attached", state)
	}
	if state.RuntimeState != string(codexRuntimePhaseIdle) {
		t.Fatalf("SessionState().RuntimeState = %q, want idle", state.RuntimeState)
	}
}

func TestCurrentHelperReadErrorMarksCodexTransportResetRequired(t *testing.T) {
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_current_helper_read_error")
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper:   &runtimeIODHelper{generationID: generationID},
			codex:    newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-helper-read-error")
		record.transport = transportSnapshotAttached(generationID)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	svc.handleHelperReadError(sessionID, session.BackendCodex, generationID, errors.New("current stream closed"))

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateBroken || !state.Transport.ResetRequired || state.Transport.Reason != iod.GenerationBreakAttachLost.String() {
		t.Fatalf("SessionState() after current read error = %+v, want reset-required attach_lost", state)
	}
	if state.RuntimeState != string(codexRuntimePhaseFailed) || state.RuntimeStateReason != iod.GenerationBreakAttachLost.String() {
		t.Fatalf("SessionState() runtime = (%q, %q), want failed attach_lost", state.RuntimeState, state.RuntimeStateReason)
	}
}

func TestCurrentHelperReadErrorRedialsLiveCodexHelper(t *testing.T) {
	var hello iod.HelloPacket
	dialer := helperReadErrorDialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		clientConn, serverConn := net.Pipe()
		go func() {
			defer serverConn.Close()
			_ = json.NewEncoder(serverConn).Encode(hello)
		}()
		return clientConn, nil
	})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{IODDialer: dialer})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_current_helper_redial")
	proof, err := iod.NewHelloProof(os.Getpid(), nil, t.TempDir()+"/transport.wal", t.TempDir()+"/io", float64(time.Unix(1760000000, 0).UTC().Unix()))
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	hello, err = iod.NewHelloPacket(sessionID, generationID, 1, proof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	originalClientConn, originalServerConn := net.Pipe()
	defer originalServerConn.Close()
	svc.helpers.Set(sessionID, attachedHelper{
		Binding:      helperGenerationBinding{SessionID: sessionID, GenerationID: generationID},
		ManifestPath: t.TempDir() + "/generation-manifest.json",
		Manifest:     manifest,
		Hello:        hello,
		Client:       iodclient.NewClient(originalClientConn),
	})
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper:   &runtimeIODHelper{generationID: generationID},
			codex:    newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-helper-redial")
		record.transport = transportSnapshotAttached(generationID)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	result := svc.handleHelperReadError(sessionID, session.BackendCodex, generationID, errors.New("transient read reset"))
	if !result.reattached || result.client == nil || result.retry {
		t.Fatalf("handleHelperReadError() = %+v, want live redial", result)
	}
	defer result.client.Close()

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.ResetRequired || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState() after live redial = %+v, want attached current generation", state.Transport)
	}
	if state.RuntimeState == string(codexRuntimePhaseFailed) {
		t.Fatalf("SessionState().RuntimeState = %q, want non-failed", state.RuntimeState)
	}
}

func TestFreshHelperReadErrorRedialsFromRuntimeManifest(t *testing.T) {
	var hello iod.HelloPacket
	dialer := helperReadErrorDialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		clientConn, serverConn := net.Pipe()
		go func() {
			defer serverConn.Close()
			_ = json.NewEncoder(serverConn).Encode(hello)
		}()
		return clientConn, nil
	})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{IODDialer: dialer})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_fresh_helper_redial")
	runtimeDir := t.TempDir()
	proof, err := iod.NewHelloProof(os.Getpid(), nil, runtimeDir+"/transport.wal", runtimeDir+"/io", float64(time.Unix(1760000000, 0).UTC().Unix()))
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	hello, err = iod.NewHelloPacket(sessionID, generationID, 1, proof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	originalClientConn, originalServerConn := net.Pipe()
	defer originalServerConn.Close()
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper: &runtimeIODHelper{
				streamClient: iodclient.NewClient(originalClientConn),
				manifest:     manifest,
				sessionID:    sessionID,
				generationID: generationID,
				helperPID:    os.Getpid(),
				runtimeDir:   runtimeDir,
			},
			helperBinding: &RuntimeHelperBinding{GenerationID: generationID},
			codex:         newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-fresh-helper-redial")
		record.transport = transportSnapshotAttached(generationID)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	result := svc.handleHelperReadError(sessionID, session.BackendCodex, generationID, errors.New("fresh stream reset"))
	if !result.reattached || result.client == nil || result.retry {
		t.Fatalf("handleHelperReadError() = %+v, want live redial from runtime helper manifest", result)
	}
	defer result.client.Close()

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.ResetRequired || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState() after fresh helper redial = %+v, want attached current generation", state.Transport)
	}
	if state.RuntimeState == string(codexRuntimePhaseFailed) {
		t.Fatalf("SessionState().RuntimeState = %q, want non-failed", state.RuntimeState)
	}
}

func TestCurrentHelperReadLoopEOFMarksAttachLost(t *testing.T) {
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_current_helper_eof")
	clientConn, serverConn := net.Pipe()
	client := iodclient.NewClient(clientConn)
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper:   &runtimeIODHelper{generationID: generationID, streamClient: client},
			codex:    newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-helper-eof")
		record.transport = transportSnapshotAttached(generationID)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	done := make(chan struct{})
	go func() {
		svc.readRuntimeHelper(sessionID, session.BackendCodex, &runtimeIODHelper{
			generationID: generationID,
			streamClient: client,
		})
		close(done)
	}()
	_ = serverConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("current read loop did not exit after EOF")
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateBroken || !state.Transport.ResetRequired || state.Transport.Reason != iod.GenerationBreakAttachLost.String() {
		t.Fatalf("SessionState().Transport = %+v, want reset-required attach_lost after EOF", state.Transport)
	}
}

func TestFreshHelperEOFRedialsFromRuntimeManifest(t *testing.T) {
	var hello iod.HelloPacket
	dialer := helperReadErrorDialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		clientConn, serverConn := net.Pipe()
		go func() {
			defer serverConn.Close()
			_ = json.NewEncoder(serverConn).Encode(hello)
		}()
		return clientConn, nil
	})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{IODDialer: dialer})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_fresh_helper_eof")
	runtimeDir := t.TempDir()
	proof, err := iod.NewHelloProof(os.Getpid(), nil, runtimeDir+"/transport.wal", runtimeDir+"/io", float64(time.Unix(1760000000, 0).UTC().Unix()))
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	hello, err = iod.NewHelloPacket(sessionID, generationID, 1, proof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	originalClientConn, originalServerConn := net.Pipe()
	defer originalServerConn.Close()
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper: &runtimeIODHelper{
				streamClient: iodclient.NewClient(originalClientConn),
				manifest:     manifest,
				sessionID:    sessionID,
				generationID: generationID,
				helperPID:    os.Getpid(),
				runtimeDir:   runtimeDir,
			},
			helperBinding: &RuntimeHelperBinding{GenerationID: generationID},
			codex:         newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-fresh-helper-eof")
		record.transport = transportSnapshotAttached(generationID)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	result := svc.handleHelperReadError(sessionID, session.BackendCodex, generationID, io.EOF)
	if !result.reattached || result.client == nil || result.retry {
		t.Fatalf("handleHelperReadError(EOF) = %+v, want live redial from runtime helper manifest", result)
	}
	defer result.client.Close()

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.ResetRequired || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState() after fresh helper EOF redial = %+v, want attached current generation", state.Transport)
	}
}

func TestStaleHelperReadLoopDoesNotBreakRestartedSameGeneration(t *testing.T) {
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_restarted_same_generation")
	oldClientConn, oldServerConn := net.Pipe()
	currentClientConn, currentServerConn := net.Pipe()
	defer oldServerConn.Close()
	defer currentClientConn.Close()
	defer currentServerConn.Close()
	oldClient := iodclient.NewClient(oldClientConn)
	currentClient := iodclient.NewClient(currentClientConn)
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper:   &runtimeIODHelper{generationID: generationID, streamClient: currentClient},
			codex:    newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-restarted-same-generation")
		record.transport = transportSnapshotAttached(generationID)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	done := make(chan struct{})
	go func() {
		svc.readRuntimeHelper(sessionID, session.BackendCodex, &runtimeIODHelper{
			generationID: generationID,
			streamClient: oldClient,
		})
		close(done)
	}()
	_ = oldClient.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stale read loop did not exit")
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.ResetRequired || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState().Transport = %+v, want current helper still attached", state.Transport)
	}
}

func TestCurrentHelperReadErrorKeepsLiveHelperRecoverableWhenRedialTemporarilyFails(t *testing.T) {
	dialer := helperReadErrorDialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, errors.New("temporary dial failure")
	})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{IODDialer: dialer})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_current_helper_transient")
	proof, err := iod.NewHelloProof(os.Getpid(), nil, t.TempDir()+"/transport.wal", t.TempDir()+"/io", float64(time.Unix(1760000000, 0).UTC().Unix()))
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	hello, err := iod.NewHelloPacket(sessionID, generationID, 1, proof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	originalClientConn, originalServerConn := net.Pipe()
	defer originalClientConn.Close()
	defer originalServerConn.Close()
	svc.helpers.Set(sessionID, attachedHelper{
		Binding:      helperGenerationBinding{SessionID: sessionID, GenerationID: generationID},
		ManifestPath: t.TempDir() + "/generation-manifest.json",
		Manifest:     manifest,
		Hello:        hello,
		Client:       iodclient.NewClient(originalClientConn),
	})
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper:   &runtimeIODHelper{generationID: generationID},
			codex:    newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-helper-transient")
		record.transport = transportSnapshotAttached(generationID)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	result := svc.handleHelperReadError(sessionID, session.BackendCodex, generationID, errors.New("transient stream reset"))
	if !result.retry || result.reattached || result.client != nil {
		t.Fatalf("handleHelperReadError() = %+v, want retry without client", result)
	}

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateSilent || state.Transport.ResetRequired || state.Transport.Reason != "attach_lost_reconnecting" {
		t.Fatalf("SessionState().Transport = %+v, want silent reconnecting without reset", state.Transport)
	}
	if state.RuntimeState == string(codexRuntimePhaseFailed) || strings.TrimSpace(state.RuntimeStateReason) != "" {
		t.Fatalf("SessionState() runtime = (%q, %q), want non-failed reconnecting", state.RuntimeState, state.RuntimeStateReason)
	}
}

func TestBrokenAttachLostCanRedialLiveCodexHelper(t *testing.T) {
	var hello iod.HelloPacket
	dialer := helperReadErrorDialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		clientConn, serverConn := net.Pipe()
		go func() {
			defer serverConn.Close()
			_ = json.NewEncoder(serverConn).Encode(hello)
		}()
		return clientConn, nil
	})
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{IODDialer: dialer})
	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{AgentBackend: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessionID := mustSessionID(t, created.Session.SessionID)
	generationID := mustHelperGenerationID(t, "g_broken_helper_redial")
	proof, err := iod.NewHelloProof(os.Getpid(), nil, t.TempDir()+"/transport.wal", t.TempDir()+"/io", float64(time.Unix(1760000000, 0).UTC().Unix()))
	if err != nil {
		t.Fatalf("NewHelloProof() error = %v", err)
	}
	manifest, err := iod.NewGenerationManifest(sessionID, generationID, proof)
	if err != nil {
		t.Fatalf("NewGenerationManifest() error = %v", err)
	}
	hello, err = iod.NewHelloPacket(sessionID, generationID, 1, proof)
	if err != nil {
		t.Fatalf("NewHelloPacket() error = %v", err)
	}
	originalClientConn, originalServerConn := net.Pipe()
	defer originalServerConn.Close()
	svc.helpers.Set(sessionID, attachedHelper{
		Binding:      helperGenerationBinding{SessionID: sessionID, GenerationID: generationID},
		ManifestPath: t.TempDir() + "/generation-manifest.json",
		Manifest:     manifest,
		Hello:        hello,
		Client:       iodclient.NewClient(originalClientConn),
	})
	if _, ok, err := svc.registry.Update(sessionID, false, func(record *sessionRecord) error {
		record.runtime = sessionRuntime{
			protocol: runtimeProtocolCodexRPC,
			helper:   &runtimeIODHelper{generationID: generationID},
			codex:    newCodexRuntimeState(session.BackendCodex),
		}
		record.runtime.codex.markInitialized()
		record.runtime.codex.setThreadID("thread-broken-redial")
		record.transport = transportSnapshotBroken(generationID, iod.GenerationBreakAttachLost.String(), true)
		return nil
	}); err != nil || !ok {
		t.Fatalf("registry.Update() = (_, %v, %v), want ok", ok, err)
	}

	result := svc.handleHelperReadError(sessionID, session.BackendCodex, generationID, errors.New("retry existing broken attach"))
	if !result.reattached || result.client == nil || result.retry {
		t.Fatalf("handleHelperReadError() = %+v, want reattached client", result)
	}
	defer result.client.Close()

	state, err := svc.SessionState(context.Background(), SessionStateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SessionState() error = %v", err)
	}
	if state.Transport.State != SessionTransportStateAttached || state.Transport.ResetRequired || state.Transport.GenerationID != generationID.String() {
		t.Fatalf("SessionState().Transport = %+v, want attached without reset", state.Transport)
	}
	if state.RuntimeState == string(codexRuntimePhaseFailed) {
		t.Fatalf("SessionState().RuntimeState = %q, want non-failed", state.RuntimeState)
	}
}
