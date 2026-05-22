package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

const (
	helperStopTimeout             = 3 * time.Second
	helperMissingChildAliveReason = "helper_missing_child_alive"
)

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
	historyFunc  func(context.Context) (iod.SessionHistoryResponsePacket, error)
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
	if h.historyFunc != nil {
		return h.historyFunc(ctx)
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
		if cleaned, err := cleanupOrphanChildFromManifest(shutdownCtx, h.manifest); err != nil {
			return err
		} else if cleaned {
			return nil
		}
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

func cleanupOrphanChildFromManifest(ctx context.Context, manifest iod.GenerationManifest) (bool, error) {
	if manifest.HelperPID <= 0 || manifest.ChildPID == nil || *manifest.ChildPID <= 0 {
		return false, nil
	}
	if manifest.ChildPID != nil && *manifest.ChildPID == manifest.HelperPID {
		return false, nil
	}
	if processPIDAlive(manifest.HelperPID) {
		return false, nil
	}
	childPID := *manifest.ChildPID
	if !processPIDAlive(childPID) {
		return false, nil
	}
	signaled, err := signalOrphanChild(manifest.HelperPID, childPID)
	if err != nil {
		return false, err
	}
	if !signaled {
		return false, nil
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !processPIDAlive(childPID) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-ticker.C:
		}
	}
}

func cleanupOrphanChildrenForSocket(ctx context.Context, childSocketPath string, keepHelperPID int) (int, error) {
	trimmed := strings.TrimSpace(childSocketPath)
	if trimmed == "" {
		return 0, nil
	}
	matches, err := orphanChildCandidatesForSocket(trimmed, keepHelperPID)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, manifest := range matches {
		ok, err := cleanupOrphanChildFromManifest(ctx, manifest)
		if err != nil {
			return cleaned, err
		}
		if ok {
			cleaned++
		}
	}
	return cleaned, nil
}

func orphanChildCandidatesForSocket(childSocketPath string, keepHelperPID int) ([]iod.GenerationManifest, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	matches := make([]iod.GenerationManifest, 0)
	seenGroups := make(map[int]struct{})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || !bytes.Contains(cmdline, []byte(childSocketPath)) || !bytes.Contains(cmdline, []byte("app-server")) {
			continue
		}
		pgid, err := syscall.Getpgid(pid)
		if err != nil || pgid <= 0 || pgid == keepHelperPID {
			continue
		}
		if _, ok := seenGroups[pgid]; ok {
			continue
		}
		seenGroups[pgid] = struct{}{}
		if processPIDAlive(pgid) {
			continue
		}
		childPID := pid
		proof, err := iod.NewHelloProof(pgid, &childPID, filepath.Join(filepath.Dir(childSocketPath), "transport.wal"), filepath.Join(filepath.Dir(childSocketPath), "io"), float64(time.Now().Unix()))
		if err != nil {
			continue
		}
		matches = append(matches, iod.GenerationManifest{HelloProof: proof})
	}
	return matches, nil
}

func (s *Stub) cleanupOrphanChildAsync(manifest iod.GenerationManifest) {
	go func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), helperStopTimeout)
		defer cancel()
		_, _ = cleanupOrphanChildFromManifest(cleanupCtx, manifest)
	}()
}

func signalOrphanChild(helperPID int, childPID int) (bool, error) {
	if childPID <= 0 {
		return false, os.ErrProcessDone
	}
	if helperPID <= 0 {
		return false, nil
	}
	if ok, err := childBelongsToDeadHelperGroup(helperPID, childPID); err != nil || !ok {
		return false, err
	}
	if err := syscall.Kill(-helperPID, syscall.SIGKILL); err == nil || errors.Is(err, syscall.ESRCH) {
		return true, nil
	} else {
		return false, fmt.Errorf("kill orphan child process group %d: %w", helperPID, err)
	}
}

func childBelongsToDeadHelperGroup(helperPID int, childPID int) (bool, error) {
	pgid, err := syscall.Getpgid(childPID)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, fmt.Errorf("get orphan child pgid %d: %w", childPID, err)
	}
	if pgid != helperPID {
		return false, nil
	}
	sid, err := processSessionID(childPID)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, fmt.Errorf("get orphan child sid %d: %w", childPID, err)
	}
	return sid == helperPID, nil
}

func processSessionID(pid int) (int, error) {
	if pid <= 0 {
		return 0, os.ErrProcessDone
	}
	sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, uintptr(pid), 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(sid), nil
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

func shutdownRuntimeGenerationProcesses(ctx context.Context, runtimeRoot string, sessionID session.SessionID, generationID iod.GenerationID) error {
	pids, err := runtimeGenerationProcessPIDs("/proc", runtimeRoot, sessionID, generationID)
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if pid == os.Getpid() {
			continue
		}
		if err := shutdownRuntimeGenerationPID(ctx, pid); err != nil {
			return err
		}
	}
	return nil
}

func runtimeGenerationProcessPIDs(procRoot string, runtimeRoot string, sessionID session.SessionID, generationID iod.GenerationID) ([]int, error) {
	trimmedProcRoot := strings.TrimSpace(procRoot)
	if trimmedProcRoot == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(trimmedProcRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read proc root %q: %w", trimmedProcRoot, err)
	}
	pids := make([]int, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(trimmedProcRoot, entry.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		if runtimeGenerationProcessMatches(splitProcCmdline(raw), runtimeRoot, sessionID, generationID) {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids, nil
}

func runtimeGenerationProcessMatches(args []string, runtimeRoot string, sessionID session.SessionID, generationID iod.GenerationID) bool {
	if len(args) == 0 {
		return false
	}
	argSessionID, ok := argValue(args, helperFlagSessionID)
	if !ok || argSessionID != sessionID.String() {
		return false
	}
	argGenerationID, ok := argValue(args, helperFlagGenerationID)
	if !ok || argGenerationID != generationID.String() {
		return false
	}
	argRuntimeRoot, ok := argValue(args, helperFlagRuntimeRoot)
	if !ok || !runtimeRootMatches(argRuntimeRoot, runtimeRoot) {
		return false
	}
	return true
}

func splitProcCmdline(raw []byte) []string {
	parts := bytes.Split(bytes.TrimRight(raw, "\x00"), []byte{0})
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		args = append(args, string(part))
	}
	return args
}

func argValue(args []string, flag string) (string, bool) {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}

func runtimeRootMatches(left, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	resolvedLeft, leftErr := filepath.EvalSymlinks(left)
	resolvedRight, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(resolvedLeft) == filepath.Clean(resolvedRight)
}

func shutdownRuntimeGenerationPID(ctx context.Context, pid int) error {
	if pid <= 0 || !processPIDAlive(pid) {
		return nil
	}
	shutdownCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		shutdownCtx, cancel = context.WithTimeout(ctx, helperStopTimeout)
		defer cancel()
	}
	if err := signalHelperPID(pid, syscall.SIGINT); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("signal stale iod helper pid %d: %w", pid, err)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !processPIDAlive(pid) {
			cleanupHelperProcessGroup(pid)
			return nil
		}
		select {
		case <-shutdownCtx.Done():
			if err := signalHelperPID(pid, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("kill stale iod helper pid %d: %w", pid, err)
			}
			return nil
		case <-ticker.C:
		}
	}
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
