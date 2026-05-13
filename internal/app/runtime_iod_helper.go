package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

const helperStopTimeout = 3 * time.Second

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
	commandFunc  func(context.Context, iod.CommandName, json.RawMessage) error
}

func (h *runtimeIODHelper) command(ctx context.Context, name iod.CommandName, payload json.RawMessage) error {
	if h == nil || h.streamClient == nil {
		return errRuntimeInputUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.commandFunc != nil {
		return h.commandFunc(ctx, name, payload)
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
	hello, err := client.Hello(ctx)
	if err != nil {
		return iod.SessionHistoryResponsePacket{}, err
	}
	if err := iodclient.VerifyHelloProof(h.manifest, hello); err != nil {
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
	shutdownCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		shutdownCtx, cancel = context.WithTimeout(ctx, helperStopTimeout)
		defer cancel()
	}
	verified, err := h.verifyShutdownTarget(shutdownCtx)
	if err != nil {
		return err
	}
	if !verified {
		return nil
	}
	if _, err := os.FindProcess(h.helperPID); err != nil {
		return fmt.Errorf("find iod helper pid %d: %w", h.helperPID, err)
	}
	if err := signalHelperPID(h.helperPID, syscall.SIGINT); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("signal iod helper pid %d: %w", h.helperPID, err)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !processPIDAlive(h.helperPID) {
			cleanupHelperProcessGroup(h.helperPID)
			return nil
		}
		select {
		case <-shutdownCtx.Done():
			if err := signalHelperPID(h.helperPID, syscall.SIGKILL); err != nil {
				if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
					return nil
				}
				return fmt.Errorf("kill iod helper pid %d: %w", h.helperPID, err)
			}
			return nil
		case <-ticker.C:
		}
	}
}

func signalHelperPID(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-pid, sig); err == nil || !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return syscall.Kill(pid, sig)
}

func cleanupHelperProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return
	}
}

func (h *runtimeIODHelper) verifyShutdownTarget(ctx context.Context) (bool, error) {
	if h == nil {
		return false, nil
	}
	client, err := iodclient.DialContext(ctx, h.manifest.ControlSocketPath, h.dialer)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return false, nil
		}
		return false, fmt.Errorf("verify iod helper shutdown target: %w", err)
	}
	defer client.Close()
	hello, err := client.Hello(ctx)
	if err != nil {
		return false, fmt.Errorf("verify iod helper shutdown target hello: %w", err)
	}
	if err := iodclient.VerifyHelloProof(h.manifest, hello); err != nil {
		return false, fmt.Errorf("verify iod helper shutdown target proof: %w", err)
	}
	return true, nil
}

func processPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	return true
}

func shutdownIODManifest(ctx context.Context, manifest iod.GenerationManifest) error {
	helper := &runtimeIODHelper{
		manifest:     manifest,
		sessionID:    manifest.SessionID,
		generationID: manifest.GenerationID,
		helperPID:    manifest.HelperPID,
		childPID:     manifest.ChildPID,
	}
	return helper.shutdown(ctx)
}

func shutdownRuntimeGenerationFromManifest(ctx context.Context, manifestPath string, sessionID session.SessionID, generationID iod.GenerationID) error {
	trimmed := strings.TrimSpace(manifestPath)
	if trimmed == "" {
		return nil
	}
	raw, err := os.ReadFile(trimmed)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read helper manifest %q before stable generation reuse: %w", trimmed, err)
	}
	var manifest iod.GenerationManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("decode helper manifest %q before stable generation reuse: %w", trimmed, err)
	}
	if manifest.SessionID != sessionID || manifest.GenerationID != generationID {
		return nil
	}
	_ = shutdownIODManifest(ctx, manifest)
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
