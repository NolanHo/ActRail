package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/iodclient"
	"actrail/internal/domain/session"
)

type helperDialer = iodclient.Dialer

var (
	errHelperReplayGap         = errors.New("helper replay gap")
	errHelperReplayCorruptTail = errors.New("helper replay corrupt tail")
)

type helperGenerationBinding struct {
	SessionID        session.SessionID `json:"session_id"`
	GenerationID     iod.GenerationID  `json:"generation_id"`
	LastReplayOffset iod.WALOffset     `json:"last_replay_offset,omitempty"`
}

func (b helperGenerationBinding) Validate() error {
	if err := b.SessionID.Validate(); err != nil {
		return err
	}
	if b.SessionID.IsHistorical() {
		return fmt.Errorf("session %q cannot bind a current generation", b.SessionID)
	}
	if err := b.GenerationID.Validate(); err != nil {
		return err
	}
	return b.LastReplayOffset.ValidateState()
}

type helperBindingStore struct {
	root string
}

func newHelperBindingStore(dataDir string) helperBindingStore {
	return helperBindingStore{root: strings.TrimSpace(dataDir)}
}

func (s helperBindingStore) Load() (map[session.SessionID]helperGenerationBinding, error) {
	if strings.TrimSpace(s.root) == "" {
		return map[session.SessionID]helperGenerationBinding{}, nil
	}
	if _, err := os.Stat(s.root); err != nil {
		if os.IsNotExist(err) {
			return map[session.SessionID]helperGenerationBinding{}, nil
		}
		return nil, fmt.Errorf("stat helper binding root %q: %w", s.root, err)
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("read helper binding root %q: %w", s.root, err)
	}
	items := make(map[session.SessionID]helperGenerationBinding, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.root, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read helper binding %q: %w", path, err)
		}
		var binding helperGenerationBinding
		if err := json.Unmarshal(raw, &binding); err != nil {
			return nil, fmt.Errorf("decode helper binding %q: %w", path, err)
		}
		if err := binding.Validate(); err != nil {
			return nil, fmt.Errorf("validate helper binding %q: %w", path, err)
		}
		items[binding.SessionID] = binding
	}
	return items, nil
}

func (s helperBindingStore) Save(binding helperGenerationBinding) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(s.root) == "" {
		return fmt.Errorf("helper binding root is required")
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("mkdir helper binding root %q: %w", s.root, err)
	}
	path := s.path(binding.SessionID)
	payload, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal helper binding %q: %w", binding.SessionID, err)
	}
	payload = append(payload, '\n')
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0o644); err != nil {
		return fmt.Errorf("write helper binding %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename helper binding %q: %w", path, err)
	}
	return nil
}

func (s helperBindingStore) Delete(sessionID session.SessionID) error {
	if err := sessionID.Validate(); err != nil {
		return err
	}
	path := s.path(sessionID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove helper binding %q: %w", path, err)
	}
	return nil
}

func (s helperBindingStore) path(sessionID session.SessionID) string {
	return filepath.Join(s.root, sessionID.String()+".json")
}

type helperFenceReason string

const (
	helperFenceUnknownSession           helperFenceReason = "unknown_session"
	helperFenceCurrentGenerationUnbound helperFenceReason = "current_generation_unbound"
	helperFenceGenerationNotCurrent     helperFenceReason = "generation_not_current"
	helperFenceDuplicateHelper          helperFenceReason = "duplicate_helper"
	helperFenceAttachFailed             helperFenceReason = "attach_failed"
	helperFenceHelloProofMismatch       helperFenceReason = "hello_proof_mismatch"
	helperFenceReplayFailed             helperFenceReason = "replay_failed"
	helperFenceReplayGap                helperFenceReason = "replay_gap"
	helperFenceReplayCorruptTail        helperFenceReason = "replay_corrupt_tail"
)

type helperFence struct {
	SessionID    session.SessionID
	GenerationID iod.GenerationID
	ManifestPath string
	Reason       helperFenceReason
}

type attachedHelper struct {
	Binding      helperGenerationBinding
	ManifestPath string
	Manifest     iod.GenerationManifest
	Hello        iod.HelloPacket
	Client       *iodclient.Client
	ReplayFailed bool
	ReplayReason helperFenceReason
}

type helperRegistry struct {
	mu          sync.RWMutex
	attachments map[session.SessionID]attachedHelper
	fenced      []helperFence
}

func newHelperRegistry() *helperRegistry {
	return &helperRegistry{attachments: make(map[session.SessionID]attachedHelper)}
}

func (r *helperRegistry) replaceAll(attachments map[session.SessionID]attachedHelper, fenced []helperFence) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for sessionID, current := range r.attachments {
		if _, ok := attachments[sessionID]; ok {
			continue
		}
		_ = current.Client.Close()
	}
	r.attachments = make(map[session.SessionID]attachedHelper, len(attachments))
	for sessionID, attachment := range attachments {
		r.attachments[sessionID] = attachment
	}
	r.fenced = append([]helperFence(nil), fenced...)
}

func (r *helperRegistry) Attachment(sessionID session.SessionID) (attachedHelper, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	attachment, ok := r.attachments[sessionID]
	return attachment, ok
}

func (r *helperRegistry) Set(sessionID session.SessionID, attachment attachedHelper) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.attachments[sessionID]; ok && current.Client != attachment.Client {
		_ = current.Client.Close()
	}
	r.attachments[sessionID] = attachment
}

func (r *helperRegistry) Fenced() []helperFence {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]helperFence(nil), r.fenced...)
}

func (r *helperRegistry) Remove(sessionID session.SessionID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	attachment, ok := r.attachments[sessionID]
	if ok {
		_ = attachment.Client.Close()
		delete(r.attachments, sessionID)
	}
}

func (r *helperRegistry) RemoveIfGeneration(sessionID session.SessionID, generationID iod.GenerationID) {
	if generationID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	attachment, ok := r.attachments[sessionID]
	if !ok || attachment.Binding.GenerationID != generationID {
		return
	}
	_ = attachment.Client.Close()
	delete(r.attachments, sessionID)
}

func (s *Stub) bindCurrentGeneration(binding helperGenerationBinding) error {
	return s.helperBindings.Save(binding)
}

func (s *Stub) bindRuntimeCurrentGeneration(sessionID session.SessionID, runtime sessionRuntime) error {
	binding, err := runtime.CurrentHelperBinding(sessionID)
	if err != nil {
		return err
	}
	if binding == nil {
		if s.helpers != nil {
			s.helpers.Remove(sessionID)
		}
		return s.helperBindings.Delete(sessionID)
	}
	return s.bindCurrentGeneration(helperGenerationBinding{
		SessionID:        sessionID,
		GenerationID:     binding.GenerationID,
		LastReplayOffset: binding.LastReplayOffset,
	})
}

func (s *Stub) reattachSurvivingHelpers(ctx context.Context) error {
	if s == nil || s.helpers == nil {
		return nil
	}
	bindings, err := s.helperBindings.Load()
	if err != nil {
		return err
	}
	manifests, err := iodclient.DiscoverManifests(iodclient.RuntimeRoot(s.cfg.Storage.DataDir))
	if err != nil {
		return fmt.Errorf("discover helper manifests: %w", err)
	}
	grouped := make(map[session.SessionID][]iodclient.DiscoveredManifest)
	for _, discovered := range manifests {
		grouped[discovered.Manifest.SessionID] = append(grouped[discovered.Manifest.SessionID], discovered)
	}
	sessionIDs := make([]string, 0, len(grouped))
	for sessionID := range grouped {
		sessionIDs = append(sessionIDs, sessionID.String())
	}
	sort.Strings(sessionIDs)
	attachments := make(map[session.SessionID]attachedHelper)
	fenced := make([]helperFence, 0)
	for _, rawSessionID := range sessionIDs {
		sessionID, err := session.ParseSessionID(rawSessionID)
		if err != nil {
			return err
		}
		discovered := grouped[sessionID]
		if _, ok := s.registry.Lookup(sessionID); !ok {
			for _, item := range discovered {
				fenced = append(fenced, helperFenceFrom(item, helperFenceUnknownSession))
			}
			continue
		}
		record, ok := s.registry.Lookup(sessionID)
		if !ok {
			return fmt.Errorf("session %q not found while reattaching helper", sessionID)
		}
		binding, hasBinding := bindings[sessionID]
		var preferred *iod.GenerationID
		if hasBinding {
			generationID := binding.GenerationID
			preferred = &generationID
		}
		bound := false
		selectedGeneration := iod.GenerationID("")
		fenceStart := len(fenced)
		for _, item := range iodclient.NewManifestIndex(discovered).Candidates(sessionID, preferred) {
			candidateBinding := binding
			if !hasBinding || item.Manifest.GenerationID != binding.GenerationID {
				candidateBinding = helperGenerationBinding{
					SessionID:    sessionID,
					GenerationID: item.Manifest.GenerationID,
				}
			}
			if record.identity.Backend() != session.BackendCodex && !hasBinding {
				fenced = append(fenced, helperFenceFrom(item, helperFenceCurrentGenerationUnbound))
				continue
			}
			if record.identity.Backend() != session.BackendCodex && hasBinding && item.Manifest.GenerationID != binding.GenerationID {
				fenced = append(fenced, helperFenceFrom(item, helperFenceGenerationNotCurrent))
				continue
			}
			if bound {
				fenced = append(fenced, helperFenceFrom(item, helperFenceDuplicateHelper))
				continue
			}
			attachment, updatedBinding, reason, err := s.reattachHelper(ctx, record.identity.Backend(), candidateBinding, item)
			if err != nil {
				fenced = append(fenced, helperFenceFrom(item, reason))
				continue
			}
			attachments[sessionID] = attachment
			if record.identity.Backend() == session.BackendCodex {
				if err := s.applyReattachedCodexRuntime(sessionID, record, attachment); err != nil {
					_ = attachment.Client.Close()
					fenced = append(fenced, helperFenceFrom(item, helperFenceAttachFailed))
					continue
				}
			}
			bound = true
			selectedGeneration = updatedBinding.GenerationID
			if !hasBinding || updatedBinding.GenerationID != binding.GenerationID || updatedBinding.LastReplayOffset != binding.LastReplayOffset {
				if err := s.helperBindings.Save(updatedBinding); err != nil {
					_ = attachment.Client.Close()
					return err
				}
				bindings[sessionID] = updatedBinding
				binding = updatedBinding
				hasBinding = true
			}
		}
		if bound {
			for idx := fenceStart; idx < len(fenced); idx++ {
				if fenced[idx].GenerationID == selectedGeneration {
					fenced[idx].Reason = helperFenceDuplicateHelper
				} else if record.identity.Backend() == session.BackendCodex {
					fenced[idx].Reason = helperFenceGenerationNotCurrent
				}
			}
		}
	}
	s.helpers.replaceAll(attachments, fenced)
	if err := s.applyStartupTransportHealth(bindings, attachments, fenced); err != nil {
		return err
	}
	return nil
}

func (s *Stub) applyReattachedCodexRuntime(sessionID session.SessionID, record sessionRecord, attachment attachedHelper) error {
	if s == nil {
		return nil
	}
	if current, ok := s.registry.Lookup(sessionID); ok {
		record = current
	}
	runtime := s.runtimeForSession(sessionID, record.identity.Backend(), record.runtime)
	if runtime.protocol == runtimeProtocolCodexRPC {
		if runtime.codex != nil && strings.TrimSpace(record.importedBackendSessionID) != "" {
			runtime.codex.attachInitializedThread(record.importedBackendSessionID)
		}
		runtime.helperBinding = &RuntimeHelperBinding{GenerationID: attachment.Binding.GenerationID, LastReplayOffset: attachment.Binding.LastReplayOffset}
		runtime.helper = runtimeIODHelperFromAttachment(attachment, s.helperDialer)
		runtime.attachedExistingIOD = true
		binding := *runtime.helperBinding
		runtime.currentHelperBinding = func(session.SessionID) (*RuntimeHelperBinding, error) {
			resolved := binding
			return &resolved, nil
		}
	}
	transport := transportSnapshotAttached(attachment.Binding.GenerationID)
	if attachment.ReplayFailed {
		transport = transportSnapshotAttachedWithReason(attachment.Binding.GenerationID, "codex_replay_failed:"+string(attachment.ReplayReason))
	}
	if _, _, err := s.registry.SetRuntimeTransportMemory(sessionID, runtime, transport); err != nil {
		return err
	}
	return nil
}

func (s *Stub) applyStartupTransportHealth(bindings map[session.SessionID]helperGenerationBinding, attachments map[session.SessionID]attachedHelper, fenced []helperFence) error {
	if s == nil || s.registry == nil {
		return nil
	}
	fencedBySession := make(map[session.SessionID][]helperFence, len(fenced))
	for _, fence := range fenced {
		fencedBySession[fence.SessionID] = append(fencedBySession[fence.SessionID], fence)
	}
	for _, record := range s.registry.ListAll() {
		sessionID := record.identity.SessionID()
		if record.identity.Historical() || record.runtime.UsesPIAgentGRPC() {
			continue
		}
		_, hasAttachment := attachments[sessionID]
		if !hasAttachment && startupTransportAlreadyTerminal(record) {
			if record.identity.Backend() == session.BackendCodex {
				transport, ok, err := s.cleanupStartupCodexOrphans(sessionID, record)
				if err != nil {
					return err
				}
				if ok {
					if _, ok, err := s.registry.SetStartupTransport(sessionID, transport); err != nil {
						return err
					} else if !ok {
						return fmt.Errorf("session %q not found while reconciling startup transport", sessionID)
					}
				}
			}
			continue
		}
		transport, orphanCandidate := s.startupTransportForSession(sessionID, record, bindings, attachments, fencedBySession[sessionID])
		if transport.State == "" {
			continue
		}
		if _, ok, err := s.registry.SetStartupTransport(sessionID, transport); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("session %q not found while setting startup transport", sessionID)
		}
		if (transport.State == SessionTransportStateEnded || transport.State == SessionTransportStateBroken) && record.state.Busy() {
			if _, ok, err := s.registry.MarkRuntimeCompleted(sessionID); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("session %q not found while clearing startup busy state", sessionID)
			}
			if err := s.setRuntimeAgentRunning(sessionID, false); err != nil {
				return err
			}
		}
		if orphanCandidate != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), helperStopTimeout)
			_, _ = cleanupOrphanChildFromManifest(cleanupCtx, *orphanCandidate)
			_, _ = cleanupOrphanChildrenForSocket(cleanupCtx, childSocketPathForManifest(*orphanCandidate), orphanCandidate.HelperPID)
			cancel()
		}
	}
	return nil
}

func (s *Stub) cleanupStartupCodexOrphans(sessionID session.SessionID, record sessionRecord) (SessionTransportSnapshot, bool, error) {
	generationID := strings.TrimSpace(record.transport.GenerationID)
	if generationID == "" {
		if binding, err := record.runtime.CurrentHelperBinding(sessionID); err == nil && binding != nil {
			generationID = binding.GenerationID.String()
		}
	}
	if generationID == "" {
		return SessionTransportSnapshot{}, false, nil
	}
	helperGenerationID, err := iod.NewGenerationID(generationID)
	if err != nil {
		return SessionTransportSnapshot{}, false, nil
	}
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(s.cfg.Storage.DataDir), sessionID, helperGenerationID)
	manifest, err := iod.ReadGenerationManifest(manifestPath)
	if err != nil {
		return SessionTransportSnapshot{}, false, nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), helperStopTimeout)
	defer cancel()
	_, _ = cleanupOrphanChildFromManifest(cleanupCtx, manifest)
	_, _ = cleanupOrphanChildrenForSocket(cleanupCtx, childSocketPathForManifest(manifest), manifest.HelperPID)
	if startupCodexBrokenChildGone(record.transport, manifest) {
		return transportSnapshotEnded(helperGenerationID, "helper_not_running"), true, nil
	}
	return SessionTransportSnapshot{}, false, nil
}

func (s *Stub) reconcileClearedCodexMissingChildTransport(record sessionRecord) (sessionRecord, bool) {
	if s == nil || record.identity.Historical() || record.identity.Backend() != session.BackendCodex {
		return record, false
	}
	transport := record.transport
	if !startupCodexBrokenChildGone(transport, iod.GenerationManifest{}) {
		return record, false
	}
	generationID := strings.TrimSpace(transport.GenerationID)
	if generationID == "" {
		if binding, err := record.runtime.CurrentHelperBinding(record.identity.SessionID()); err == nil && binding != nil {
			generationID = binding.GenerationID.String()
		}
	}
	if generationID == "" {
		return record, false
	}
	helperGenerationID, err := iod.NewGenerationID(generationID)
	if err != nil {
		return record, false
	}
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(s.cfg.Storage.DataDir), record.identity.SessionID(), helperGenerationID)
	manifest, err := iod.ReadGenerationManifest(manifestPath)
	if err != nil || !startupCodexBrokenChildGone(transport, manifest) {
		return record, false
	}
	updatedTransport, ok, err := s.registry.SetTransport(record.identity.SessionID(), transportSnapshotEnded(helperGenerationID, "helper_not_running"))
	if err != nil || !ok {
		return record, false
	}
	if record.state.Busy() || record.runtimeAgentRunning {
		_, _, _ = s.registry.MarkRuntimeCompleted(record.identity.SessionID())
		_ = s.setRuntimeAgentRunning(record.identity.SessionID(), false)
	}
	updated, ok := s.registry.Lookup(record.identity.SessionID())
	if !ok {
		record.transport = updatedTransport
		return record, true
	}
	updated.runtime = s.runtimeForRecord(updated)
	return updated, true
}

func startupCodexBrokenChildGone(transport SessionTransportSnapshot, manifest iod.GenerationManifest) bool {
	if transport.State != SessionTransportStateBroken || strings.TrimSpace(transport.Reason) != helperMissingChildAliveReason {
		return false
	}
	if manifest.HelperPID > 0 && processPIDAlive(manifest.HelperPID) {
		return false
	}
	if manifest.ChildPID != nil && *manifest.ChildPID > 0 && processPIDAlive(*manifest.ChildPID) {
		return false
	}
	return true
}

func childSocketPathForManifest(manifest iod.GenerationManifest) string {
	controlSocketPath := strings.TrimSpace(manifest.ControlSocketPath)
	if controlSocketPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(controlSocketPath), "child.sock")
}

func startupTransportAlreadyTerminal(record sessionRecord) bool {
	transport := record.transport
	if record.identity.Backend() == session.BackendCodex && transport.State == SessionTransportStateBroken && strings.TrimSpace(transport.Reason) == string(helperFenceReplayFailed) {
		return false
	}
	return transport.ResetRequired || transport.State == SessionTransportStateBroken || transport.State == SessionTransportStateEnded
}

func startupTransportForSession(sessionID session.SessionID, bindings map[session.SessionID]helperGenerationBinding, attachments map[session.SessionID]attachedHelper, fences []helperFence) SessionTransportSnapshot {
	if attachment, ok := attachments[sessionID]; ok {
		if attachment.ReplayFailed {
			return transportSnapshotAttachedWithReason(attachment.Binding.GenerationID, "codex_replay_failed:"+string(attachment.ReplayReason))
		}
		return transportSnapshotAttached(attachment.Binding.GenerationID)
	}
	if binding, ok := bindings[sessionID]; ok {
		if transport, ok := startupTransportFromFences(binding.GenerationID, fences); ok {
			return transport
		}
		return transportSnapshotEnded(binding.GenerationID, "helper_not_running")
	}
	if transport, ok := startupTransportFromFences("", fences); ok {
		return transport
	}
	return SessionTransportSnapshot{State: SessionTransportStateEnded, Reason: "helper_binding_missing"}
}

func (s *Stub) startupTransportForSession(sessionID session.SessionID, record sessionRecord, bindings map[session.SessionID]helperGenerationBinding, attachments map[session.SessionID]attachedHelper, fences []helperFence) (SessionTransportSnapshot, *iod.GenerationManifest) {
	transport := startupTransportForSession(sessionID, bindings, attachments, fences)
	if transport.State == SessionTransportStateEnded && transport.Reason == "helper_not_running" && record.identity.Backend() == session.BackendCodex {
		if replacement, manifest, ok := s.helperMissingCodexChildTransportWithGeneration(sessionID, strings.TrimSpace(transport.GenerationID)); ok {
			return replacement, manifest
		}
	}
	return transport, nil
}

func (s *Stub) helperMissingCodexChildTransport(record sessionRecord) (SessionTransportSnapshot, bool) {
	if record.identity.Historical() || record.identity.Backend() != session.BackendCodex {
		return SessionTransportSnapshot{}, false
	}
	if record.runtime.helper != nil || record.runtime.piAgentGRPC != nil || record.runtime.handle != nil {
		return SessionTransportSnapshot{}, false
	}
	transport := record.transport
	if transport.State != SessionTransportStateAttached && !(transport.State == SessionTransportStateEnded && transport.Reason == "helper_not_running") {
		return SessionTransportSnapshot{}, false
	}
	generationID := strings.TrimSpace(transport.GenerationID)
	if generationID == "" {
		if binding, err := record.runtime.CurrentHelperBinding(record.identity.SessionID()); err == nil && binding != nil {
			generationID = binding.GenerationID.String()
		}
	}
	transport, _, ok := s.helperMissingCodexChildTransportWithGeneration(record.identity.SessionID(), generationID)
	return transport, ok
}

func (s *Stub) helperMissingCodexChildTransportWithGeneration(sessionID session.SessionID, generationID string) (SessionTransportSnapshot, *iod.GenerationManifest, bool) {
	if s == nil || strings.TrimSpace(generationID) == "" {
		return SessionTransportSnapshot{}, nil, false
	}
	helperGenerationID, err := iod.NewGenerationID(generationID)
	if err != nil {
		return SessionTransportSnapshot{}, nil, false
	}
	manifestPath := iodclient.GenerationManifestPath(iodclient.RuntimeRoot(s.cfg.Storage.DataDir), sessionID, helperGenerationID)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return SessionTransportSnapshot{}, nil, false
	}
	var manifest iod.GenerationManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return SessionTransportSnapshot{}, nil, false
	}
	if manifest.SessionID != sessionID || manifest.GenerationID != helperGenerationID || manifest.ChildPID == nil {
		return SessionTransportSnapshot{}, nil, false
	}
	if !processPIDAlive(*manifest.ChildPID) {
		return SessionTransportSnapshot{}, nil, false
	}
	return SessionTransportSnapshot{
		GenerationID:  helperGenerationID.String(),
		State:         SessionTransportStateBroken,
		ResetRequired: true,
		Reason:        helperMissingChildAliveReason,
	}, &manifest, true
}

func startupTransportFromFences(generationID iod.GenerationID, fences []helperFence) (SessionTransportSnapshot, bool) {
	for _, fence := range fences {
		if generationID != "" && fence.GenerationID != generationID {
			continue
		}
		if transport, ok := transportSnapshotFromFence(fence); ok {
			return transport, true
		}
	}
	return SessionTransportSnapshot{}, false
}

func (s *Stub) reattachHelper(ctx context.Context, backend session.Backend, binding helperGenerationBinding, discovered iodclient.DiscoveredManifest) (attachedHelper, helperGenerationBinding, helperFenceReason, error) {
	client, err := iodclient.DialContext(ctx, discovered.Manifest.ControlSocketPath, s.helperDialer)
	if err != nil {
		return attachedHelper{}, binding, helperFenceAttachFailed, err
	}
	hello, err := client.Hello(ctx)
	if err != nil {
		_ = client.Close()
		return attachedHelper{}, binding, helperFenceAttachFailed, err
	}
	if err := iodclient.VerifyHelloProof(discovered.Manifest, hello); err != nil {
		_ = client.Close()
		return attachedHelper{}, binding, helperFenceHelloProofMismatch, err
	}

	updatedBinding := binding
	replayFailed := false
	replayReason := helperFenceReason("")
	if s != nil {
		replayAfterOffset := binding.LastReplayOffset
		if record, ok := s.registry.Lookup(binding.SessionID); ok {
			replayAfterOffset = helperReplayAfterOffset(record, binding)
		}
		replayState := newHelperReplayState(replayAfterOffset, func(packet iod.ReplayItemPacket) error {
			record, ok := s.registry.Lookup(binding.SessionID)
			if !ok {
				return fmt.Errorf("session %q not found while replaying helper WAL", binding.SessionID)
			}
			return s.applyRuntimeHelperPacketTrusted(binding.SessionID, record.identity.Backend(), binding.GenerationID, packet)
		})
		request, err := iod.NewReplayRequestPacket(binding.SessionID, binding.GenerationID, replayAfterOffset)
		if err != nil {
			_ = client.Close()
			return attachedHelper{}, binding, helperFenceReplayFailed, err
		}
		done, err := client.Replay(ctx, request, replayState.accept)
		if err != nil {
			replayReason = replayFenceReason(err)
			if backend != session.BackendCodex {
				_ = client.Close()
				return attachedHelper{}, binding, replayReason, err
			}
			replayFailed = true
		} else if err := replayState.finish(done); err != nil {
			replayReason = replayFenceReason(err)
			if backend != session.BackendCodex {
				_ = client.Close()
				return attachedHelper{}, binding, replayReason, err
			}
			replayFailed = true
		} else {
			updatedBinding.LastReplayOffset = done.LastOffset
		}
		if replayFailed {
			_ = client.Close()
			client, hello, err = redialHelper(ctx, discovered.Manifest, s.helperDialer)
			if err != nil {
				return attachedHelper{}, binding, helperFenceAttachFailed, err
			}
		}
	}

	return attachedHelper{
		Binding:      updatedBinding,
		ManifestPath: discovered.Path,
		Manifest:     discovered.Manifest,
		Hello:        hello,
		Client:       client,
		ReplayFailed: replayFailed,
		ReplayReason: replayReason,
	}, updatedBinding, "", nil
}

func redialHelper(ctx context.Context, manifest iod.GenerationManifest, dialer iodclient.Dialer) (*iodclient.Client, iod.HelloPacket, error) {
	client, err := iodclient.DialContext(ctx, manifest.ControlSocketPath, dialer)
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

func helperReplayAfterOffset(record sessionRecord, binding helperGenerationBinding) iod.WALOffset {
	if record.identity.Backend() == session.BackendCodex {
		runtime := record.runtime
		if strings.TrimSpace(record.importedBackendSessionID) != "" {
			return binding.LastReplayOffset
		}
		if runtime.codex == nil {
			return 0
		}
		initialized, threadID, _ := runtime.codex.snapshot()
		if !initialized || strings.TrimSpace(threadID) == "" {
			return 0
		}
	}
	if record.transcript.TailSeq().Uint64() != 0 {
		return binding.LastReplayOffset
	}
	if record.state.Tail().Seq().Uint64() != 0 && record.identity.Backend() == session.BackendCodex {
		return 0
	}
	if _, partial := record.transcript.PartialAssistantTurn(); partial {
		return binding.LastReplayOffset
	}
	return 0
}

type helperReplayState struct {
	lastOffset iod.WALOffset
	project    func(iod.ReplayItemPacket) error
}

func newHelperReplayState(afterOffset iod.WALOffset, project func(iod.ReplayItemPacket) error) helperReplayState {
	return helperReplayState{lastOffset: afterOffset, project: project}
}

func (r *helperReplayState) accept(packet iod.ReplayItemPacket) error {
	expected := r.lastOffset + 1
	if packet.Item.WALOffset != expected {
		return fmt.Errorf("%w: got wal offset %d after %d", errHelperReplayGap, packet.Item.WALOffset, r.lastOffset)
	}
	if err := packet.Item.Fact.Validate(); err != nil {
		return fmt.Errorf("validate replay fact at wal offset %d: %w", packet.Item.WALOffset, err)
	}
	if r.project != nil {
		// Replay cursor continuity is guarded by the helper WAL. Projection is a
		// best-effort UI cache rebuild; stale Codex event shapes or interleaved
		// side events must not fence a surviving runtime.
		_ = r.project(packet)
	}
	r.lastOffset = packet.Item.WALOffset
	return nil
}

func (r helperReplayState) finish(done iod.ReplayDonePacket) error {
	if done.CorruptTail {
		return fmt.Errorf("%w after wal offset %d", errHelperReplayCorruptTail, done.AfterOffset)
	}
	if done.LastOffset != r.lastOffset {
		return fmt.Errorf("%w: replay done last offset %d does not match accepted offset %d", errHelperReplayGap, done.LastOffset, r.lastOffset)
	}
	return nil
}

func replayFenceReason(err error) helperFenceReason {
	var helperErr iodclient.HelperError
	switch {
	case errors.Is(err, errHelperReplayCorruptTail):
		return helperFenceReplayCorruptTail
	case errors.As(err, &helperErr) && helperErr.Packet.Code == iod.ErrorReplayCorruptTail:
		return helperFenceReplayCorruptTail
	case errors.Is(err, errHelperReplayGap):
		return helperFenceReplayGap
	default:
		return helperFenceReplayFailed
	}
}

func helperFenceFrom(discovered iodclient.DiscoveredManifest, reason helperFenceReason) helperFence {
	return helperFence{
		SessionID:    discovered.Manifest.SessionID,
		GenerationID: discovered.Manifest.GenerationID,
		ManifestPath: discovered.Path,
		Reason:       reason,
	}
}
