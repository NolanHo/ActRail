package session

import (
	"fmt"
	"strings"
)

// Backend identifies the runtime family that owns a session.
type Backend string

const (
	// BackendPI is deprecated. Keep support for legacy sessions and explicit
	// operator overrides, but do not extend Pi-specific launch/runtime paths.
	BackendPI    Backend = "pi"
	BackendCodex Backend = "codex"
)

// ParseBackend trims and normalizes a backend name.
func ParseBackend(raw string) (Backend, error) {
	backend := Backend(strings.ToLower(strings.TrimSpace(raw)))
	if err := backend.Validate(); err != nil {
		return "", err
	}
	return backend, nil
}

// Validate rejects unknown backends and empty values.
func (b Backend) Validate() error {
	switch b {
	case BackendPI, BackendCodex:
		return nil
	case "":
		return fmt.Errorf("backend is required")
	default:
		return fmt.Errorf("backend %q is not supported", string(b))
	}
}

func (b Backend) String() string {
	return string(b)
}
