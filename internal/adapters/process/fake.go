package process

import (
	"context"
	"io"
	"os"
	"sync"
)

// FakeRunner is a test double for launch orchestration.
type FakeRunner struct {
	mu          sync.Mutex
	StartErr    error
	Starts      []LaunchSpec
	NextHandle  Handle
	HandleBuild func(LaunchSpec) Handle
}

func (r *FakeRunner) Start(ctx context.Context, spec LaunchSpec) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.StartErr != nil {
		return nil, r.StartErr
	}
	r.Starts = append(r.Starts, spec)
	if r.HandleBuild != nil {
		return r.HandleBuild(spec), nil
	}
	if r.NextHandle != nil {
		return r.NextHandle, nil
	}
	return &FakeHandle{spec: spec}, nil
}

// FakeHandle is a controllable test double for a child-process handle.
type FakeHandle struct {
	mu             sync.Mutex
	spec           LaunchSpec
	pid            int
	logs           LogPaths
	stdout         io.ReadCloser
	stderr         io.ReadCloser
	pty            PTY
	waitStatus     ExitStatus
	waitErr        error
	waitCh         chan struct{}
	signals        []os.Signal
	interruptCalls int
	killCalls      int
}

func NewFakeHandle(spec LaunchSpec) *FakeHandle {
	return &FakeHandle{spec: spec}
}

func (h *FakeHandle) PID() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pid
}

func (h *FakeHandle) SetPID(pid int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pid = pid
}

func (h *FakeHandle) Spec() LaunchSpec {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.spec
}

func (h *FakeHandle) SetLogs(logs LogPaths) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = logs
}

func (h *FakeHandle) Logs() LogPaths {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logs
}

func (h *FakeHandle) SetStdout(stdout io.ReadCloser) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stdout = stdout
}

func (h *FakeHandle) Stdout() io.ReadCloser {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stdout
}

func (h *FakeHandle) SetStderr(stderr io.ReadCloser) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stderr = stderr
}

func (h *FakeHandle) Stderr() io.ReadCloser {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stderr
}

func (h *FakeHandle) SetPTY(pty PTY) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pty = pty
}

func (h *FakeHandle) PTY() PTY {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pty
}

func (h *FakeHandle) Signal(sig os.Signal) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.signals = append(h.signals, sig)
	return nil
}

func (h *FakeHandle) Signals() []os.Signal {
	h.mu.Lock()
	defer h.mu.Unlock()
	copied := make([]os.Signal, len(h.signals))
	copy(copied, h.signals)
	return copied
}

func (h *FakeHandle) Interrupt() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.interruptCalls++
	return nil
}

func (h *FakeHandle) InterruptCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.interruptCalls
}

func (h *FakeHandle) Kill() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.killCalls++
	return nil
}

func (h *FakeHandle) KillCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.killCalls
}

func (h *FakeHandle) SetWaitResult(status ExitStatus, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.waitStatus = status
	h.waitErr = err
	if h.waitCh == nil {
		h.waitCh = make(chan struct{})
	}
	select {
	case <-h.waitCh:
	default:
		close(h.waitCh)
	}
}

func (h *FakeHandle) Wait(ctx context.Context) (ExitStatus, error) {
	h.mu.Lock()
	waitCh := h.waitCh
	status := h.waitStatus
	err := h.waitErr
	h.mu.Unlock()
	if waitCh == nil {
		return status, err
	}
	select {
	case <-ctx.Done():
		return ExitStatus{}, ctx.Err()
	case <-waitCh:
		return status, err
	}
}
