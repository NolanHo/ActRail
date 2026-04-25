package agent

import (
	"testing"

	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

func TestNewRequestNormalizesFields(t *testing.T) {
	foo, err := process.NewEnvVar("FOO", "bar")
	if err != nil {
		t.Fatalf("NewEnvVar() error = %v", err)
	}
	env, err := process.InheritEnv(foo)
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	ioSpec, err := process.PipeIO(process.LogPaths{Stdout: "/tmp/stdout.log", Stderr: "/tmp/stderr.log"})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	opts, err := NewOptions("  openrouter  ", "  google/gemini-2.5-pro  ", "  high  ")
	if err != nil {
		t.Fatalf("NewOptions() error = %v", err)
	}

	req, err := NewRequest(session.BackendPI, "  /root/.local/bin/pi  ", "/tmp/../tmp/work", env, ioSpec, opts)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	if req.BinPath().String() != "/root/.local/bin/pi" {
		t.Fatalf("bin path = %q, want %q", req.BinPath(), "/root/.local/bin/pi")
	}
	if req.CWD().String() != "/tmp/work" {
		t.Fatalf("cwd = %q, want %q", req.CWD(), "/tmp/work")
	}
	if req.Options().Provider() != "openrouter" {
		t.Fatalf("provider = %q, want %q", req.Options().Provider(), "openrouter")
	}
	if req.Options().Model() != "google/gemini-2.5-pro" {
		t.Fatalf("model = %q, want %q", req.Options().Model(), "google/gemini-2.5-pro")
	}
	if req.Options().ReasoningEffort() != "high" {
		t.Fatalf("reasoning = %q, want %q", req.Options().ReasoningEffort(), "high")
	}
}

func TestNewRequestRejectsInvalidBinPath(t *testing.T) {
	env, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	ioSpec, err := process.PipeIO(process.LogPaths{})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	opts, err := NewOptions("", "", "")
	if err != nil {
		t.Fatalf("NewOptions() error = %v", err)
	}

	if _, err := NewRequest(session.BackendPI, "  ", "/tmp", env, ioSpec, opts); err == nil {
		t.Fatal("NewRequest() error = nil, want error")
	}
}
