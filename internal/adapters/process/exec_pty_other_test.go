//go:build !unix

package process

import (
	"context"
	"errors"
	"testing"
)

func TestExecRunnerRejectsPTYMode(t *testing.T) {
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
	ioSpec, err := PTYIO(PTYSize{Rows: 24, Cols: 80}, LogPaths{PTY: tmp + "/session.log"})
	if err != nil {
		t.Fatalf("PTYIO() error = %v", err)
	}
	spec, err := NewLaunchSpec(cmd, tmp, env, ioSpec)
	if err != nil {
		t.Fatalf("NewLaunchSpec() error = %v", err)
	}
	_, err = runner.Start(context.Background(), spec)
	if !errors.Is(err, ErrPTYUnsupported) {
		t.Fatalf("Start() error = %v, want %v", err, ErrPTYUnsupported)
	}
}

func runPTYHelper() int {
	return 2
}
