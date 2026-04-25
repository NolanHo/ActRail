package process

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestFakeRunnerRecordsStarts(t *testing.T) {
	cmd, _ := NewCommand("/bin/echo", "hello")
	env, _ := InheritEnv()
	ioSpec, _ := PipeIO(LogPaths{})
	launch, err := NewLaunchSpec(cmd, "/tmp", env, ioSpec)
	if err != nil {
		t.Fatalf("NewLaunchSpec() error = %v", err)
	}
	fake := &FakeRunner{}
	handle, err := fake.Start(context.Background(), launch)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if handle.Spec().Command().Path() != "/bin/echo" {
		t.Fatalf("handle command path = %q, want %q", handle.Spec().Command().Path(), "/bin/echo")
	}
	if len(fake.Starts) != 1 {
		t.Fatalf("starts = %d, want 1", len(fake.Starts))
	}
}

func TestFakeHandleWaitHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handle := NewFakeHandle(LaunchSpec{})
	handle.waitCh = make(chan struct{})
	if _, err := handle.Wait(ctx); err == nil {
		t.Fatal("Wait() error = nil, want context cancellation")
	}
}

func TestFakeHandleTracksSignalsAndReaders(t *testing.T) {
	handle := NewFakeHandle(LaunchSpec{})
	handle.SetPID(123)
	handle.SetLogs(LogPaths{Stdout: "/tmp/stdout.log"})
	handle.SetStdout(io.NopCloser(strings.NewReader("out")))
	handle.SetStderr(io.NopCloser(strings.NewReader("err")))
	if handle.PID() != 123 {
		t.Fatalf("PID() = %d, want 123", handle.PID())
	}
	if handle.Logs().Stdout != "/tmp/stdout.log" {
		t.Fatalf("stdout log = %q, want %q", handle.Logs().Stdout, "/tmp/stdout.log")
	}
	if err := handle.Interrupt(); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	if handle.InterruptCalls() != 1 {
		t.Fatalf("InterruptCalls() = %d, want 1", handle.InterruptCalls())
	}
	if err := handle.Kill(); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	if handle.KillCalls() != 1 {
		t.Fatalf("KillCalls() = %d, want 1", handle.KillCalls())
	}
}
