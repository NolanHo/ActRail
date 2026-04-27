package iod

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"actrail/internal/domain/session"
)

const (
	defaultManifestFilename = "generation-manifest.json"
	defaultWALFilename      = "transport.wal"
	defaultSocketFilename   = "io"
)

// GenerationPaths groups the per-generation runtime artifacts owned by one helper.
type GenerationPaths struct {
	RuntimeDir        string
	ManifestPath      string
	WALPath           string
	ControlSocketPath string
}

func NewGenerationPaths(root string, sessionID session.SessionID, generationID GenerationID) (GenerationPaths, error) {
	if err := sessionID.Validate(); err != nil {
		return GenerationPaths{}, err
	}
	if sessionID.IsHistorical() {
		return GenerationPaths{}, fmt.Errorf("session id %q cannot use historical replay identity", sessionID)
	}
	if err := generationID.Validate(); err != nil {
		return GenerationPaths{}, err
	}
	base := strings.TrimSpace(root)
	if base == "" {
		return GenerationPaths{}, fmt.Errorf("generation root is required")
	}
	base = filepath.Clean(base)
	runtimeDir := filepath.Join(base, sessionID.String(), generationID.String())
	paths := GenerationPaths{
		RuntimeDir:        runtimeDir,
		ManifestPath:      filepath.Join(runtimeDir, defaultManifestFilename),
		WALPath:           filepath.Join(runtimeDir, defaultWALFilename),
		ControlSocketPath: filepath.Join(runtimeDir, defaultSocketFilename),
	}
	if err := paths.Validate(); err != nil {
		return GenerationPaths{}, err
	}
	return paths, nil
}

func (p GenerationPaths) Validate() error {
	for _, item := range []struct {
		label string
		value string
	}{
		{label: "runtime dir", value: p.RuntimeDir},
		{label: "manifest path", value: p.ManifestPath},
		{label: "wal path", value: p.WALPath},
		{label: "control socket path", value: p.ControlSocketPath},
	} {
		if strings.TrimSpace(item.value) == "" {
			return fmt.Errorf("%s is required", item.label)
		}
	}
	return nil
}

func (p GenerationPaths) EnsureDir() error {
	if err := p.Validate(); err != nil {
		return err
	}
	return os.MkdirAll(p.RuntimeDir, 0o755)
}

func WriteGenerationManifest(path string, manifest GenerationManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("manifest path is required")
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal generation manifest: %w", err)
	}
	dir := filepath.Dir(trimmed)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create manifest dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create manifest temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.Write(append(encoded, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write generation manifest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync generation manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close generation manifest temp file: %w", err)
	}
	if err := os.Rename(tmpPath, trimmed); err != nil {
		return fmt.Errorf("rename generation manifest: %w", err)
	}
	return nil
}

func ReadGenerationManifest(path string) (GenerationManifest, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return GenerationManifest{}, fmt.Errorf("manifest path is required")
	}
	body, err := os.ReadFile(trimmed)
	if err != nil {
		return GenerationManifest{}, fmt.Errorf("read generation manifest: %w", err)
	}
	var manifest GenerationManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return GenerationManifest{}, fmt.Errorf("decode generation manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return GenerationManifest{}, err
	}
	return manifest, nil
}
