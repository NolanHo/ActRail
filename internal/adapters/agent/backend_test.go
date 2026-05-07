package agent

import (
	"errors"
	"reflect"
	"testing"

	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

type stubAdapter struct {
	backend      session.Backend
	capabilities Capabilities
}

func (a stubAdapter) Backend() session.Backend { return a.backend }

func (a stubAdapter) Capabilities() Capabilities { return a.capabilities }

func (a stubAdapter) ValidateOptions(Options) error { return nil }

func (a stubAdapter) CommandArgs(Options) ([]string, error) { return nil, nil }

func TestNewCatalogRejectsDuplicateBackend(t *testing.T) {
	_, err := NewCatalog(
		stubAdapter{backend: session.BackendPI},
		stubAdapter{backend: session.BackendPI},
	)
	if err == nil {
		t.Fatal("NewCatalog() error = nil, want error")
	}
}

func TestDefaultCatalogExposesBackendCapabilities(t *testing.T) {
	catalog := DefaultCatalog()

	pi, err := catalog.Adapter(session.BackendPI)
	if err != nil {
		t.Fatalf("Adapter(pi) error = %v", err)
	}
	if got, want := pi.Capabilities(), (Capabilities{Provider: true, Model: true, ReasoningEffort: true}); got != want {
		t.Fatalf("pi capabilities = %+v, want %+v", got, want)
	}

	codex, err := catalog.Adapter(session.BackendCodex)
	if err != nil {
		t.Fatalf("Adapter(codex) error = %v", err)
	}
	if got, want := codex.Capabilities(), (Capabilities{Provider: true, Model: true, ReasoningEffort: false}); got != want {
		t.Fatalf("codex capabilities = %+v, want %+v", got, want)
	}
}

func TestCatalogValidateRequestRejectsCodexReasoning(t *testing.T) {
	req := mustRequest(t, session.BackendCodex, Options{reasoningEffort: "high"})

	err := DefaultCatalog().ValidateRequest(req)
	if err == nil {
		t.Fatal("ValidateRequest() error = nil, want error")
	}
	var unsupported UnsupportedOptionError
	if !errors.As(err, &unsupported) {
		t.Fatalf("ValidateRequest() error = %T, want UnsupportedOptionError", err)
	}
	if unsupported.Option != "reasoning_effort" {
		t.Fatalf("unsupported option = %q, want reasoning_effort", unsupported.Option)
	}
}

func TestCatalogValidateRequestRejectsUnknownPIThinkingLevel(t *testing.T) {
	req := mustRequest(t, session.BackendPI, Options{reasoningEffort: "extreme"})

	err := DefaultCatalog().ValidateRequest(req)
	if err == nil {
		t.Fatal("ValidateRequest() error = nil, want error")
	}
	if got, want := err.Error(), `backend "pi" reasoning_effort "extreme" is not supported`; got != want {
		t.Fatalf("ValidateRequest() error = %q, want %q", got, want)
	}
}

func TestPICommandArgsIncludeProviderModelThinking(t *testing.T) {
	opts, err := NewOptions("openrouter", "google/gemini-2.5-pro", "high")
	if err != nil {
		t.Fatalf("NewOptions() error = %v", err)
	}
	args, err := (piAdapter{}).CommandArgs(opts)
	if err != nil {
		t.Fatalf("CommandArgs() error = %v", err)
	}
	want := []string{"--mode", "rpc", "--provider", "openrouter", "--model", "google/gemini-2.5-pro", "--thinking", "high"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("CommandArgs() = %#v, want %#v", args, want)
	}
}

func TestPICommandArgsUseGRPCModeWithSocket(t *testing.T) {
	opts, err := NewOptionsWithTransport("openai", "gpt-5.5", "high", "/tmp/session.jsonl", "/tmp/pi-agent/agent.sock")
	if err != nil {
		t.Fatalf("NewOptionsWithTransport() error = %v", err)
	}
	args, err := (piAdapter{}).CommandArgs(opts)
	if err != nil {
		t.Fatalf("CommandArgs() error = %v", err)
	}
	want := []string{"--mode", "grpc", "--grpc-socket", "/tmp/pi-agent/agent.sock", "--provider", "openai", "--model", "gpt-5.5", "--thinking", "high", "--session", "/tmp/session.jsonl"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("CommandArgs() = %#v, want %#v", args, want)
	}
}

func TestCodexCommandArgsUseConfigOverrideForProvider(t *testing.T) {
	opts, err := NewOptionsWithRuntimeFlags("openrouter", "gpt-5", "", "", "", "", true)
	if err != nil {
		t.Fatalf("NewOptionsWithRuntimeFlags() error = %v", err)
	}
	args, err := (codexAdapter{}).CommandArgs(opts)
	if err != nil {
		t.Fatalf("CommandArgs() error = %v", err)
	}
	want := []string{"--dangerously-bypass-approvals-and-sandbox", "-c", `model_provider="openrouter"`, "-c", `model_reasoning_effort="high"`, "--model", "gpt-5", "app-server"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("CommandArgs() = %#v, want %#v", args, want)
	}
}

func TestCodexCommandArgsIncludeListenURL(t *testing.T) {
	opts, err := NewOptionsWithRuntimeFlags("openai", "gpt-5", "", "", "", "unix:///tmp/actrail/child.sock", true)
	if err != nil {
		t.Fatalf("NewOptionsWithRuntimeFlags() error = %v", err)
	}
	args, err := (codexAdapter{}).CommandArgs(opts)
	if err != nil {
		t.Fatalf("CommandArgs() error = %v", err)
	}
	want := []string{"--dangerously-bypass-approvals-and-sandbox", "-c", `model_provider="openai"`, "-c", `model_reasoning_effort="high"`, "--model", "gpt-5", "app-server", "--listen", "unix:///tmp/actrail/child.sock"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("CommandArgs() = %#v, want %#v", args, want)
	}
}

func TestCodexCommandArgsCanDisableDangerousBypass(t *testing.T) {
	opts, err := NewOptionsWithRuntimeFlags("openai", "gpt-5", "", "", "", "unix:///tmp/actrail/child.sock", false)
	if err != nil {
		t.Fatalf("NewOptionsWithRuntimeFlags() error = %v", err)
	}
	args, err := (codexAdapter{}).CommandArgs(opts)
	if err != nil {
		t.Fatalf("CommandArgs() error = %v", err)
	}
	want := []string{"-c", `model_provider="openai"`, "-c", `model_reasoning_effort="high"`, "--model", "gpt-5", "app-server", "--listen", "unix:///tmp/actrail/child.sock"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("CommandArgs() = %#v, want %#v", args, want)
	}
}

func mustRequest(t *testing.T, backend session.Backend, opts Options) Request {
	t.Helper()
	env, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	ioSpec, err := process.PipeIO(process.LogPaths{})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	req, err := NewRequest(backend, "/usr/local/bin/agent", "/tmp", env, ioSpec, opts)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	return req
}
