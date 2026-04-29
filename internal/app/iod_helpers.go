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

func (s *Stub) bindCurrentGeneration(binding helperGenerationBinding) error {
	return s.helperBindings.Save(binding)
}

func (s *Stub) bindRuntimeCurrentGeneration(sessionID session.SessionID, runtime sessionRuntime) error {
	binding, err := runtime.CurrentHelperBinding(sessionID)
	if err != nil || binding == nil {
		return err
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
		binding, ok := bindings[sessionID]
		if !ok {
			for _, item := range discovered {
				fenced = append(fenced, helperFenceFrom(item, helperFenceCurrentGenerationUnbound))
			}
			continue
		}
		bound := false
		for _, item := range discovered {
			if item.Manifest.GenerationID != binding.GenerationID {
				fenced = append(fenced, helperFenceFrom(item, helperFenceGenerationNotCurrent))
				continue
			}
			if bound {
				fenced = append(fenced, helperFenceFrom(item, helperFenceDuplicateHelper))
				continue
			}
			attachment, updatedBinding, reason, err := s.reattachHelper(ctx, binding, item)
			if err != nil {
				fenced = append(fenced, helperFenceFrom(item, reason))
				continue
			}
			attachments[sessionID] = attachment
			bound = true
			if updatedBinding.LastReplayOffset != binding.LastReplayOffset {
				if err := s.helperBindings.Save(updatedBinding); err != nil {
					_ = attachment.Client.Close()
					return err
				}
				bindings[sessionID] = updatedBinding
			}
		}
	}
	s.helpers.replaceAll(attachments, fenced)
	if err := s.applyStartupTransportHealth(bindings, attachments, fenced); err != nil {
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
	for _, record := range s.registry.List() {
		sessionID := record.identity.SessionID()
		if record.identity.Historical() || startupTransportAlreadyTerminal(record.transport) {
			continue
		}
		transport := startupTransportForSession(sessionID, bindings, attachments, fencedBySession[sessionID])
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
	}
	return nil
}

func startupTransportAlreadyTerminal(transport SessionTransportSnapshot) bool {
	return transport.ResetRequired || transport.State == SessionTransportStateBroken || transport.State == SessionTransportStateEnded
}

func startupTransportForSession(sessionID session.SessionID, bindings map[session.SessionID]helperGenerationBinding, attachments map[session.SessionID]attachedHelper, fences []helperFence) SessionTransportSnapshot {
	if attachment, ok := attachments[sessionID]; ok {
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

func (s *Stub) reattachHelper(ctx context.Context, binding helperGenerationBinding, discovered iodclient.DiscoveredManifest) (attachedHelper, helperGenerationBinding, helperFenceReason, error) {
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
	updatedBinding.LastReplayOffset = 0
	return attachedHelper{
		Binding:      updatedBinding,
		ManifestPath: discovered.Path,
		Manifest:     discovered.Manifest,
		Hello:        hello,
		Client:       client,
	}, updatedBinding, "", nil
}

type helperReplayState struct {
	lastOffset iod.WALOffset
	project    func(iod.ReplayItemPacket) error
}

func newHelperReplayState(afterOffset iod.WALOffset, project func(iod.ReplayItemPacket) error) helperReplayState {
	return helperReplayState{lastOffset: afterOffset, project: project}
}

func (s *helperReplayState) accept(packet iod.ReplayItemPacket) error {
	expected := s.lastOffset + 1
	if packet.Item.WALOffset != expected {
		return fmt.Errorf("%w: got wal offset %d after %d", errHelperReplayGap, packet.Item.WALOffset, s.lastOffset)
	}
	if err := packet.Item.Fact.Validate(); err != nil {
		return fmt.Errorf("validate replay fact at wal offset %d: %w", packet.Item.WALOffset, err)
	}
	if s.project != nil {
		if err := s.project(packet); err != nil {
			return fmt.Errorf("project replay fact at wal offset %d: %w", packet.Item.WALOffset, err)
		}
	}
	s.lastOffset = packet.Item.WALOffset
	return nil
}

func (s helperReplayState) finish(done iod.ReplayDonePacket) error {
	if done.CorruptTail {
		return fmt.Errorf("%w after wal offset %d", errHelperReplayCorruptTail, done.AfterOffset)
	}
	if done.LastOffset != s.lastOffset {
		return fmt.Errorf("%w: replay done last offset %d does not match accepted offset %d", errHelperReplayGap, done.LastOffset, s.lastOffset)
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
