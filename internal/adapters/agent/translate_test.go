package agent

import (
	"reflect"
	"testing"

	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

func TestRequestLaunchSpecForPI(t *testing.T) {
	foo, err := process.NewEnvVar("FOO", "bar")
	if err != nil {
		t.Fatalf("NewEnvVar() error = %v", err)
	}
	env, err := process.InheritEnv(foo)
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	ioSpec, err := process.PipeIO(process.LogPaths{Stdout: "/tmp/pi.stdout.log", Stderr: "/tmp/pi.stderr.log"})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	opts, err := NewOptions("openrouter", "google/gemini-2.5-pro", "high")
	if err != nil {
		t.Fatalf("NewOptions() error = %v", err)
	}
	req, err := NewRequest(session.BackendPI, "/root/.local/bin/pi", "/tmp/work", env, ioSpec, opts)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	spec, err := req.LaunchSpec()
	if err != nil {
		t.Fatalf("LaunchSpec() error = %v", err)
	}

	if spec.Command().Path() != "/root/.local/bin/pi" {
		t.Fatalf("command path = %q, want %q", spec.Command().Path(), "/root/.local/bin/pi")
	}
	wantArgs := []string{"--mode", "rpc", "--provider", "openrouter", "--model", "google/gemini-2.5-pro", "--thinking", "high"}
	if !reflect.DeepEqual(spec.Command().Args(), wantArgs) {
		t.Fatalf("command args = %#v, want %#v", spec.Command().Args(), wantArgs)
	}
	if spec.CWD().String() != "/tmp/work" {
		t.Fatalf("cwd = %q, want %q", spec.CWD(), "/tmp/work")
	}
	if !reflect.DeepEqual(spec.Environment().Vars(), env.Vars()) {
		t.Fatalf("environment vars = %#v, want %#v", spec.Environment().Vars(), env.Vars())
	}
	if spec.IO().Logs().Stdout != "/tmp/pi.stdout.log" {
		t.Fatalf("stdout log = %q, want %q", spec.IO().Logs().Stdout, "/tmp/pi.stdout.log")
	}
}

func TestCatalogLaunchSpecForCodex(t *testing.T) {
	env, err := process.ReplaceEnv()
	if err != nil {
		t.Fatalf("ReplaceEnv() error = %v", err)
	}
	ioSpec, err := process.PipeIO(process.LogPaths{})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	opts, err := NewOptionsWithRuntimeFlags("openrouter", "gpt-5", "", "", "", "", true)
	if err != nil {
		t.Fatalf("NewOptionsWithRuntimeFlags() error = %v", err)
	}
	req, err := NewRequest(session.BackendCodex, "/usr/local/bin/codex", "/tmp/work", env, ioSpec, opts)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	spec, err := DefaultCatalog().LaunchSpec(req)
	if err != nil {
		t.Fatalf("LaunchSpec() error = %v", err)
	}

	if spec.Command().Path() != "/usr/local/bin/codex" {
		t.Fatalf("command path = %q, want %q", spec.Command().Path(), "/usr/local/bin/codex")
	}
	wantArgs := []string{"--dangerously-bypass-approvals-and-sandbox", "app-server", "-c", `model_provider="openrouter"`, "--model", "gpt-5"}
	if !reflect.DeepEqual(spec.Command().Args(), wantArgs) {
		t.Fatalf("command args = %#v, want %#v", spec.Command().Args(), wantArgs)
	}
	if spec.Environment().Mode() != process.EnvModeReplace {
		t.Fatalf("environment mode = %q, want %q", spec.Environment().Mode(), process.EnvModeReplace)
	}
}
