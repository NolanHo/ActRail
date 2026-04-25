package process

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerStartAndWaitWithMirroredLogs(t *testing.T) {
	runner := NewExecRunner()
	tmp := t.TempDir()
	stdoutLog := tmp + "/stdout.log"
	stderrLog := tmp + "/stderr.log"
	cmd := helperCommand(t, "emit", "stdout-line", "stderr-line")
	helperFlag, err := NewEnvVar("GO_WANT_HELPER_PROCESS", "1")
	if err != nil {
		t.Fatalf("NewEnvVar() error = %v", err)
	}
	env, err := InheritEnv(helperFlag)
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	ioSpec, err := PipeIO(LogPaths{Stdout: stdoutLog, Stderr: stderrLog})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	spec, err := NewLaunchSpec(cmd, tmp, env, ioSpec)
	if err != nil {
		t.Fatalf("NewLaunchSpec() error = %v", err)
	}
	handle, err := runner.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer handle.Stdout().Close()
	defer handle.Stderr().Close()
	stdout, err := io.ReadAll(handle.Stdout())
	if err != nil {
		t.Fatalf("ReadAll(stdout) error = %v", err)
	}
	stderr, err := io.ReadAll(handle.Stderr())
	if err != nil {
		t.Fatalf("ReadAll(stderr) error = %v", err)
	}
	status, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if handle.PID() <= 0 {
		t.Fatalf("PID() = %d, want > 0", handle.PID())
	}
	if !status.Successful() {
		t.Fatalf("Wait() status = %#v, want success", status)
	}
	if string(stdout) != "stdout-line" {
		t.Fatalf("stdout = %q, want %q", stdout, "stdout-line")
	}
	if string(stderr) != "stderr-line" {
		t.Fatalf("stderr = %q, want %q", stderr, "stderr-line")
	}
	stdoutBytes, err := os.ReadFile(stdoutLog)
	if err != nil {
		t.Fatalf("ReadFile(stdoutLog) error = %v", err)
	}
	stderrBytes, err := os.ReadFile(stderrLog)
	if err != nil {
		t.Fatalf("ReadFile(stderrLog) error = %v", err)
	}
	if string(stdoutBytes) != "stdout-line" {
		t.Fatalf("stdout log = %q, want %q", stdoutBytes, "stdout-line")
	}
	if string(stderrBytes) != "stderr-line" {
		t.Fatalf("stderr log = %q, want %q", stderrBytes, "stderr-line")
	}
}

func TestExecRunnerReplaceEnvDropsParent(t *testing.T) {
	runner := NewExecRunner()
	t.Setenv("ACTRAIL_PARENT_MARKER", "present")
	tmp := t.TempDir()
	cmd := helperCommand(t, "env", "CHILD_ONLY", "ACTRAIL_PARENT_MARKER")
	child, err := NewEnvVar("CHILD_ONLY", "set")
	if err != nil {
		t.Fatalf("NewEnvVar() error = %v", err)
	}
	helperFlag, err := NewEnvVar("GO_WANT_HELPER_PROCESS", "1")
	if err != nil {
		t.Fatalf("NewEnvVar() error = %v", err)
	}
	env, err := ReplaceEnv(child, helperFlag)
	if err != nil {
		t.Fatalf("ReplaceEnv() error = %v", err)
	}
	ioSpec, err := PipeIO(LogPaths{})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	spec, err := NewLaunchSpec(cmd, tmp, env, ioSpec)
	if err != nil {
		t.Fatalf("NewLaunchSpec() error = %v", err)
	}
	handle, err := runner.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer handle.Stdout().Close()
	defer handle.Stderr().Close()
	stdout, err := io.ReadAll(handle.Stdout())
	if err != nil {
		t.Fatalf("ReadAll(stdout) error = %v", err)
	}
	stderr, err := io.ReadAll(handle.Stderr())
	if err != nil {
		t.Fatalf("ReadAll(stderr) error = %v", err)
	}
	status, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !status.Successful() {
		t.Fatalf("Wait() status = %#v, want success", status)
	}
	if string(stdout) != "set" {
		t.Fatalf("stdout = %q, want %q", stdout, "set")
	}
	if string(stderr) != "<missing>" {
		t.Fatalf("stderr = %q, want %q", stderr, "<missing>")
	}
}

func TestExecRunnerInterrupt(t *testing.T) {
	runner := NewExecRunner()
	tmp := t.TempDir()
	cmd := helperCommand(t, "trap-int")
	helperFlag, err := NewEnvVar("GO_WANT_HELPER_PROCESS", "1")
	if err != nil {
		t.Fatalf("NewEnvVar() error = %v", err)
	}
	env, err := InheritEnv(helperFlag)
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	ioSpec, err := PipeIO(LogPaths{})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	spec, err := NewLaunchSpec(cmd, tmp, env, ioSpec)
	if err != nil {
		t.Fatalf("NewLaunchSpec() error = %v", err)
	}
	handle, err := runner.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer handle.Stdout().Close()
	defer handle.Stderr().Close()
	reader := bufio.NewReader(handle.Stdout())
	ready, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString(stdout) error = %v", err)
	}
	if strings.TrimSpace(ready) != "ready" {
		t.Fatalf("ready line = %q, want %q", ready, "ready")
	}
	if err := handle.Interrupt(); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	stderr, err := io.ReadAll(handle.Stderr())
	if err != nil {
		t.Fatalf("ReadAll(stderr) error = %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, err := handle.Wait(waitCtx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Successful() {
		t.Fatalf("Wait() status = %#v, want interrupt exit", status)
	}
	if !strings.Contains(string(stderr), "got:interrupt") {
		t.Fatalf("stderr = %q, want interrupt marker", stderr)
	}
}

func TestExecRunnerRejectsMissingWorkingDirectory(t *testing.T) {
	runner := NewExecRunner()
	tmp := t.TempDir()
	cmd := helperCommand(t, "emit", "stdout", "stderr")
	helperFlag, err := NewEnvVar("GO_WANT_HELPER_PROCESS", "1")
	if err != nil {
		t.Fatalf("NewEnvVar() error = %v", err)
	}
	env, err := InheritEnv(helperFlag)
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	ioSpec, err := PipeIO(LogPaths{})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	spec, err := NewLaunchSpec(cmd, tmp+"/missing", env, ioSpec)
	if err != nil {
		t.Fatalf("NewLaunchSpec() error = %v", err)
	}
	_, err = runner.Start(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "stat working directory") {
		t.Fatalf("Start() error = %v, want working directory stat failure", err)
	}
}

func helperCommand(t *testing.T, mode string, args ...string) Command {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	argv := []string{"-test.run=TestExecRunnerHelperProcess", "--", mode}
	argv = append(argv, args...)
	cmd, err := NewCommand(bin, argv...)
	if err != nil {
		t.Fatalf("NewCommand() error = %v", err)
	}
	return cmd
}

func TestExecRunnerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := helperArgs(os.Args)
	if len(args) == 0 {
		_, _ = io.WriteString(os.Stderr, "missing helper mode")
		os.Exit(2)
	}
	switch args[0] {
	case "emit":
		_, _ = io.WriteString(os.Stdout, args[1])
		_, _ = io.WriteString(os.Stderr, args[2])
		os.Exit(0)
	case "env":
		_, _ = io.WriteString(os.Stdout, os.Getenv(args[1]))
		if value, ok := os.LookupEnv(args[2]); ok {
			_, _ = io.WriteString(os.Stderr, value)
		} else {
			_, _ = io.WriteString(os.Stderr, "<missing>")
		}
		os.Exit(0)
	case "trap-int":
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		defer signal.Stop(sigCh)
		_, _ = io.WriteString(os.Stdout, "ready\n")
		select {
		case sig := <-sigCh:
			_, _ = io.WriteString(os.Stderr, fmt.Sprintf("got:%s", sig))
			os.Exit(130)
		case <-time.After(5 * time.Second):
			_, _ = io.WriteString(os.Stderr, "timeout")
			os.Exit(99)
		}
	case "pty-echo":
		os.Exit(runPTYHelper())
	default:
		_, _ = io.WriteString(os.Stderr, fmt.Sprintf("unknown mode %q", args[0]))
		os.Exit(2)
	}
}

func helperArgs(argv []string) []string {
	for i, arg := range argv {
		if arg == "--" && i+1 < len(argv) {
			return argv[i+1:]
		}
	}
	return nil
}
