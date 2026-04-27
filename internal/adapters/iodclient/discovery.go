package iodclient

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"actrail/internal/adapters/iod"
	"actrail/internal/domain/session"
)

const ManifestFilename = "generation-manifest.json"

// DiscoveredManifest is one manifest file found on disk for helper discovery.
type DiscoveredManifest struct {
	Path     string
	Manifest iod.GenerationManifest
}

func RuntimeRoot(dataDir string) string {
	trimmed := strings.TrimSpace(dataDir)
	if trimmed == "" {
		return filepath.Join(".", "runtime", "iod")
	}
	return filepath.Join(trimmed, "runtime", "iod")
}

func GenerationDir(root string, sessionID session.SessionID, generationID iod.GenerationID) string {
	return filepath.Join(root, sessionID.String(), generationID.String())
}

func GenerationManifestPath(root string, sessionID session.SessionID, generationID iod.GenerationID) string {
	return filepath.Join(GenerationDir(root, sessionID, generationID), ManifestFilename)
}

func WriteGenerationManifest(path string, manifest iod.GenerationManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o644)
}

func DiscoverManifests(root string) ([]DiscoveredManifest, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return nil, nil
	}
	if _, err := os.Stat(trimmed); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	items := make([]DiscoveredManifest, 0)
	err := filepath.WalkDir(trimmed, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != ManifestFilename {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var manifest iod.GenerationManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return nil
		}
		if err := manifest.Validate(); err != nil {
			return nil
		}
		items = append(items, DiscoveredManifest{Path: path, Manifest: manifest})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		if left.Manifest.SessionID != right.Manifest.SessionID {
			return left.Manifest.SessionID.String() < right.Manifest.SessionID.String()
		}
		if left.Manifest.GenerationID != right.Manifest.GenerationID {
			return left.Manifest.GenerationID.String() < right.Manifest.GenerationID.String()
		}
		return left.Path < right.Path
	})
	return items, nil
}
