package agent

import (
	"fmt"
	"strconv"
	"strings"

	"actrail/internal/domain/session"
)

// Capabilities declares which launch-time options a backend adapter accepts.
type Capabilities struct {
	Provider        bool
	Model           bool
	ReasoningEffort bool
}

// Adapter translates validated launch options into backend-specific argv.
type Adapter interface {
	Backend() session.Backend
	Capabilities() Capabilities
	ValidateOptions(Options) error
	CommandArgs(Options) ([]string, error)
}

// Catalog resolves backend adapters and translates requests into process specs.
type Catalog struct {
	adapters map[session.Backend]Adapter
}

var defaultCatalog = mustCatalog(piAdapter{}, codexAdapter{})

func NewCatalog(adapters ...Adapter) (Catalog, error) {
	items := make(map[session.Backend]Adapter, len(adapters))
	for _, adapter := range adapters {
		if adapter == nil {
			return Catalog{}, fmt.Errorf("adapter is required")
		}
		backend := adapter.Backend()
		if err := backend.Validate(); err != nil {
			return Catalog{}, err
		}
		if _, exists := items[backend]; exists {
			return Catalog{}, fmt.Errorf("backend adapter %q is duplicated", backend)
		}
		items[backend] = adapter
	}
	return Catalog{adapters: items}, nil
}

func DefaultCatalog() Catalog {
	return defaultCatalog
}

func mustCatalog(adapters ...Adapter) Catalog {
	catalog, err := NewCatalog(adapters...)
	if err != nil {
		panic(err)
	}
	return catalog
}

func (c Catalog) Adapter(backend session.Backend) (Adapter, error) {
	if err := backend.Validate(); err != nil {
		return nil, err
	}
	adapter, ok := c.adapters[backend]
	if !ok {
		return nil, fmt.Errorf("backend adapter %q is not configured", backend)
	}
	return adapter, nil
}

func (c Catalog) ValidateRequest(req Request) error {
	if err := req.Validate(); err != nil {
		return err
	}
	adapter, err := c.Adapter(req.Backend())
	if err != nil {
		return err
	}
	return adapter.ValidateOptions(req.Options())
}

// UnsupportedOptionError reports a launch option that the selected backend does not accept.
type UnsupportedOptionError struct {
	Backend session.Backend
	Option  string
	Reason  string
}

func (e UnsupportedOptionError) Error() string {
	message := fmt.Sprintf("backend %q does not support %s", e.Backend, e.Option)
	if strings.TrimSpace(e.Reason) == "" {
		return message
	}
	return message + ": " + e.Reason
}

type piAdapter struct{}

func (piAdapter) Backend() session.Backend {
	return session.BackendPI
}

func (piAdapter) Capabilities() Capabilities {
	return Capabilities{Provider: true, Model: true, ReasoningEffort: true}
}

func (piAdapter) ValidateOptions(opts Options) error {
	if effort := opts.ReasoningEffort(); effort != "" && !isPIThinkingLevel(effort) {
		return fmt.Errorf("backend %q reasoning_effort %q is not supported", session.BackendPI, effort)
	}
	return nil
}

func (piAdapter) CommandArgs(opts Options) ([]string, error) {
	if err := (piAdapter{}).ValidateOptions(opts); err != nil {
		return nil, err
	}
	args := make([]string, 0, 10)
	if socketPath := opts.GRPCSocketPath(); socketPath != "" {
		args = append(args, "--mode", "grpc", "--grpc-socket", socketPath)
	} else {
		args = append(args, "--mode", "rpc")
	}
	if provider := opts.Provider(); provider != "" {
		args = append(args, "--provider", provider)
	}
	if model := opts.Model(); model != "" {
		args = append(args, "--model", model)
	}
	if effort := opts.ReasoningEffort(); effort != "" {
		args = append(args, "--thinking", effort)
	}
	if sessionPath := opts.SessionPath(); sessionPath != "" {
		args = append(args, "--session", sessionPath)
	}
	return args, nil
}

type codexAdapter struct{}

func (codexAdapter) Backend() session.Backend {
	return session.BackendCodex
}

func (codexAdapter) Capabilities() Capabilities {
	return Capabilities{Provider: true, Model: true, ReasoningEffort: false}
}

func (codexAdapter) ValidateOptions(opts Options) error {
	if opts.ReasoningEffort() != "" {
		return UnsupportedOptionError{
			Backend: session.BackendCodex,
			Option:  "reasoning_effort",
			Reason:  "codex CLI does not expose a stable launch flag for reasoning effort",
		}
	}
	return nil
}

func (codexAdapter) CommandArgs(opts Options) ([]string, error) {
	if err := (codexAdapter{}).ValidateOptions(opts); err != nil {
		return nil, err
	}
	args := make([]string, 0, 7)
	if opts.DangerousBypass() {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, "app-server")
	if listenURL := opts.ListenURL(); listenURL != "" {
		args = append(args, "--listen", listenURL)
	}
	if provider := opts.Provider(); provider != "" {
		args = append(args, "-c", tomlStringConfig("model_provider", provider))
	}
	if model := opts.Model(); model != "" {
		args = append(args, "--model", model)
	}
	return args, nil
}

func isPIThinkingLevel(value string) bool {
	switch value {
	case "off", "minimal", "low", "medium", "high", "xhigh":
		return true
	default:
		return false
	}
}

func tomlStringConfig(key, value string) string {
	return key + "=" + strconv.Quote(value)
}
