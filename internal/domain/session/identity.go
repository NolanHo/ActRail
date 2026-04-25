package session

import (
	"fmt"
	"strings"
)

const historicalSessionPrefix = "history:"

// DurableID is the stable session identity used for history and resume.
type DurableID string

func NewDurableID(raw string) (DurableID, error) {
	value, err := normalizeRouteToken(raw, "durable session id")
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(value, historicalSessionPrefix) {
		return "", fmt.Errorf("durable session id %q uses reserved %q prefix", value, historicalSessionPrefix)
	}
	return DurableID(value), nil
}

func (id DurableID) Validate() error {
	_, err := NewDurableID(string(id))
	return err
}

func (id DurableID) String() string {
	return string(id)
}

// SessionID is the canonical route-visible session identifier.
type SessionID string

func ParseSessionID(raw string) (SessionID, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("session id is required")
	}
	if strings.HasPrefix(value, historicalSessionPrefix) {
		if _, err := ParseHistoricalSessionID(value); err != nil {
			return "", err
		}
		return SessionID(value), nil
	}
	if _, err := NewDurableID(value); err != nil {
		return "", err
	}
	return SessionID(value), nil
}

func (id SessionID) Validate() error {
	_, err := ParseSessionID(string(id))
	return err
}

func (id SessionID) String() string {
	return string(id)
}

func (id SessionID) IsHistorical() bool {
	return strings.HasPrefix(string(id), historicalSessionPrefix)
}

// RuntimeID is the live runtime identifier for a running session instance.
type RuntimeID string

func NewRuntimeID(raw string) (RuntimeID, error) {
	value, err := normalizeRouteToken(raw, "runtime id")
	if err != nil {
		return "", err
	}
	return RuntimeID(value), nil
}

func (id RuntimeID) Validate() error {
	_, err := NewRuntimeID(string(id))
	return err
}

func (id RuntimeID) String() string {
	return string(id)
}

// ThreadID links the session to backend-specific durable thread state.
type ThreadID string

func NewThreadID(raw string) (ThreadID, error) {
	value, err := normalizeRouteToken(raw, "thread id")
	if err != nil {
		return "", err
	}
	return ThreadID(value), nil
}

func (id ThreadID) Validate() error {
	_, err := NewThreadID(string(id))
	return err
}

func (id ThreadID) String() string {
	return string(id)
}

func normalizeRouteToken(raw, label string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if !isRouteToken(value) {
		return "", fmt.Errorf("%s %q must use only letters, digits, underscores, or hyphens", label, value)
	}
	return value, nil
}

func isRouteToken(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// HistoricalRef is the durable identity behind a synthetic historical session id.
type HistoricalRef struct {
	Backend Backend
	Durable DurableID
}

func NewHistoricalSessionID(backend Backend, durable DurableID) (SessionID, error) {
	if err := backend.Validate(); err != nil {
		return "", err
	}
	if err := durable.Validate(); err != nil {
		return "", err
	}
	return SessionID(historicalSessionPrefix + backend.String() + ":" + durable.String()), nil
}

func ParseHistoricalSessionID(raw string) (HistoricalRef, error) {
	value := strings.TrimSpace(raw)
	if !strings.HasPrefix(value, historicalSessionPrefix) {
		return HistoricalRef{}, fmt.Errorf("session id %q is not historical", value)
	}
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 {
		return HistoricalRef{}, fmt.Errorf("historical session id %q is malformed", value)
	}
	backend, err := ParseBackend(parts[1])
	if err != nil {
		return HistoricalRef{}, err
	}
	durable, err := NewDurableID(parts[2])
	if err != nil {
		return HistoricalRef{}, err
	}
	return HistoricalRef{Backend: backend, Durable: durable}, nil
}

// Identity freezes the contract-level identity of one live or historical session view.
type Identity struct {
	sessionID  SessionID
	durableID  DurableID
	runtimeID  *RuntimeID
	threadID   *ThreadID
	backend    Backend
	historical bool
}

func NewLiveIdentity(sessionIDRaw, runtimeIDRaw, threadIDRaw, backendRaw string) (Identity, error) {
	sessionID, err := ParseSessionID(sessionIDRaw)
	if err != nil {
		return Identity{}, err
	}
	if sessionID.IsHistorical() {
		return Identity{}, fmt.Errorf("live identity cannot use historical session id %q", sessionID)
	}
	durableID, err := NewDurableID(sessionID.String())
	if err != nil {
		return Identity{}, err
	}
	runtimeID, err := NewRuntimeID(runtimeIDRaw)
	if err != nil {
		return Identity{}, err
	}
	threadID, err := NewThreadID(threadIDRaw)
	if err != nil {
		return Identity{}, err
	}
	backend, err := ParseBackend(backendRaw)
	if err != nil {
		return Identity{}, err
	}
	identity := Identity{
		sessionID:  sessionID,
		durableID:  durableID,
		runtimeID:  &runtimeID,
		threadID:   &threadID,
		backend:    backend,
		historical: false,
	}
	if err := identity.Validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func NewHistoricalIdentity(durableIDRaw, backendRaw string, threadIDRaw ...string) (Identity, error) {
	durableID, err := NewDurableID(durableIDRaw)
	if err != nil {
		return Identity{}, err
	}
	backend, err := ParseBackend(backendRaw)
	if err != nil {
		return Identity{}, err
	}
	sessionID, err := NewHistoricalSessionID(backend, durableID)
	if err != nil {
		return Identity{}, err
	}
	var threadID *ThreadID
	if len(threadIDRaw) > 0 && strings.TrimSpace(threadIDRaw[0]) != "" {
		parsedThreadID, err := NewThreadID(threadIDRaw[0])
		if err != nil {
			return Identity{}, err
		}
		threadID = &parsedThreadID
	}
	identity := Identity{
		sessionID:  sessionID,
		durableID:  durableID,
		runtimeID:  nil,
		threadID:   threadID,
		backend:    backend,
		historical: true,
	}
	if err := identity.Validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func (i Identity) Validate() error {
	if err := i.sessionID.Validate(); err != nil {
		return err
	}
	if err := i.durableID.Validate(); err != nil {
		return err
	}
	if err := i.backend.Validate(); err != nil {
		return err
	}
	if i.threadID != nil {
		if err := i.threadID.Validate(); err != nil {
			return err
		}
	}
	if i.historical {
		if i.runtimeID != nil {
			return fmt.Errorf("historical session %q cannot carry runtime id %q", i.sessionID, *i.runtimeID)
		}
		ref, err := ParseHistoricalSessionID(i.sessionID.String())
		if err != nil {
			return err
		}
		if ref.Backend != i.backend {
			return fmt.Errorf("historical session %q backend %q does not match identity backend %q", i.sessionID, ref.Backend, i.backend)
		}
		if ref.Durable != i.durableID {
			return fmt.Errorf("historical session %q durable id %q does not match identity durable id %q", i.sessionID, ref.Durable, i.durableID)
		}
		return nil
	}
	if i.sessionID.IsHistorical() {
		return fmt.Errorf("live session %q cannot use historical prefix", i.sessionID)
	}
	if i.runtimeID == nil {
		return fmt.Errorf("live session %q requires runtime id", i.sessionID)
	}
	if err := i.runtimeID.Validate(); err != nil {
		return err
	}
	if i.threadID == nil {
		return fmt.Errorf("live session %q requires thread id", i.sessionID)
	}
	if i.durableID.String() != i.sessionID.String() {
		return fmt.Errorf("live session durable id %q must match session id %q", i.durableID, i.sessionID)
	}
	return nil
}

func (i Identity) SessionID() SessionID {
	return i.sessionID
}

func (i Identity) DurableID() DurableID {
	return i.durableID
}

func (i Identity) Backend() Backend {
	return i.backend
}

func (i Identity) Historical() bool {
	return i.historical
}

func (i Identity) Live() bool {
	return !i.historical
}

func (i Identity) RuntimeID() (RuntimeID, bool) {
	if i.runtimeID == nil {
		return "", false
	}
	return *i.runtimeID, true
}

func (i Identity) ThreadID() (ThreadID, bool) {
	if i.threadID == nil {
		return "", false
	}
	return *i.threadID, true
}

func (i Identity) HTTPRouteKey() string {
	return i.sessionID.String()
}
