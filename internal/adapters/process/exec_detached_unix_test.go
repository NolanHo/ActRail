//go:build unix

package process

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestExecRunnerDetachedStartsOutsideParentProcessGroup(t *testing.T) {
	runner := NewExecRunner()
	tmp := t.TempDir()
	cmd, err := NewCommand("sleep", "5")
	if err != nil {
		t.Fatalf("NewCommand() error = %v", err)
	}
	env, err := InheritEnv()
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	ioSpec, err := PipeIO(LogPaths{})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	spec, err := NewLaunchSpec(cmd, tmp, env, ioSpec, Detached())
	if err != nil {
		t.Fatalf("NewLaunchSpec() error = %v", err)
	}
	handle, err := runner.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = handle.Kill() }()
	if handle.Stdout() != nil || handle.Stderr() != nil {
		t.Fatalf("detached handle stdio = (%v, %v), want nil pipes", handle.Stdout(), handle.Stderr())
	}
	parentPGID, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid(parent) error = %v", err)
	}
	childPGID, err := syscall.Getpgid(handle.PID())
	if err != nil {
		t.Fatalf("Getpgid(child) error = %v", err)
	}
	if childPGID == parentPGID {
		t.Fatalf("child process group = parent process group %d, want detached group", parentPGID)
	}
	if childPGID != handle.PID() {
		t.Fatalf("child process group = %d, want process pid %d", childPGID, handle.PID())
	}
	if err := handle.Kill(); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := handle.Wait(waitCtx); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}
