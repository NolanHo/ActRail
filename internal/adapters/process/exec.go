package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// ExecRunner starts child processes through os/exec and optional PTY wiring.
type ExecRunner struct {
	lookPath func(string) (string, error)
	openLog  func(string) (io.WriteCloser, error)
	startPTY func(*exec.Cmd, PTYSize) (PTY, error)
}

func NewExecRunner() *ExecRunner {
	return &ExecRunner{
		lookPath: exec.LookPath,
		openLog:  openLogFile,
		startPTY: startPTYProcess,
	}
}

func (r *ExecRunner) Start(ctx context.Context, spec LaunchSpec) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := ensureWorkingDir(spec.CWD()); err != nil {
		return nil, err
	}
	path, err := r.resolvePath(spec.Command().Path())
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, spec.Command().Args()...)
	cmd.Dir = spec.CWD().String()
	cmd.Env = spec.Environment().Resolve(os.Environ())
	if spec.IO().Mode() == IOModePTY {
		return r.startWithPTY(spec, cmd)
	}
	return r.startWithPipes(spec, cmd)
}

func (r *ExecRunner) startWithPipes(spec LaunchSpec, cmd *exec.Cmd) (Handle, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	logs := spec.IO().Logs()
	stdoutMirror, err := r.openOptionalLog(logs.Stdout)
	if err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	stderrMirror, err := r.openOptionalLog(logs.Stderr)
	if err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		_ = closeWriteCloser(stdoutMirror)
		return nil, err
	}
	stdoutHook := wrapMirroredReadCloser(stdout, stdoutMirror)
	stderrHook := wrapMirroredReadCloser(stderr, stderrMirror)
	if err := cmd.Start(); err != nil {
		_ = stdoutHook.Close()
		_ = stderrHook.Close()
		return nil, fmt.Errorf("start command %q: %w", spec.Command().Path(), err)
	}
	handle := &execHandle{
		spec:   spec,
		pid:    cmd.Process.Pid,
		logs:   logs,
		stdout: stdoutHook,
		stderr: stderrHook,
		waitCh: make(chan waitResult, 1),
		proc:   cmd.Process,
		cmd:    cmd,
	}
	return handle, nil
}

func (r *ExecRunner) startWithPTY(spec LaunchSpec, cmd *exec.Cmd) (Handle, error) {
	logs := spec.IO().Logs()
	mirror, err := r.openOptionalLog(logs.PTY)
	if err != nil {
		return nil, err
	}
	size := spec.IO().PTYSize()
	dev, err := r.startPTY(cmd, *size)
	if err != nil {
		_ = closeWriteCloser(mirror)
		return nil, fmt.Errorf("start pty for command %q: %w", spec.Command().Path(), err)
	}
	handle := &execHandle{
		spec:   spec,
		pid:    cmd.Process.Pid,
		logs:   logs,
		pty:    wrapMirroredPTY(dev, mirror),
		waitCh: make(chan waitResult, 1),
		proc:   cmd.Process,
		cmd:    cmd,
	}
	return handle, nil
}

func (r *ExecRunner) resolvePath(path string) (string, error) {
	resolved, err := r.lookPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve command %q: %w", path, err)
	}
	return resolved, nil
}

func (r *ExecRunner) openOptionalLog(path string) (io.WriteCloser, error) {
	if path == "" {
		return nil, nil
	}
	w, err := r.openLog(path)
	if err != nil {
		return nil, fmt.Errorf("open log %q: %w", path, err)
	}
	return w, nil
}

func openLogFile(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

func ensureWorkingDir(dir WorkingDir) error {
	info, err := os.Stat(dir.String())
	if err != nil {
		return fmt.Errorf("stat working directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working directory %q is not a directory", dir)
	}
	return nil
}

type execHandle struct {
	spec      LaunchSpec
	pid       int
	logs      LogPaths
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	pty       PTY
	waitCh    chan waitResult
	cmd       *exec.Cmd
	waitStart sync.Once

	mu     sync.Mutex
	proc   *os.Process
	result *waitResult
}

type waitResult struct {
	status ExitStatus
	err    error
}

func (h *execHandle) PID() int {
	return h.pid
}

func (h *execHandle) Spec() LaunchSpec {
	return h.spec
}

func (h *execHandle) Logs() LogPaths {
	return h.logs
}

func (h *execHandle) Stdout() io.ReadCloser {
	return h.stdout
}

func (h *execHandle) Stderr() io.ReadCloser {
	return h.stderr
}

func (h *execHandle) PTY() PTY {
	return h.pty
}

func (h *execHandle) Signal(sig os.Signal) error {
	h.mu.Lock()
	proc := h.proc
	h.mu.Unlock()
	if proc == nil {
		return fmt.Errorf("process %d is not available", h.pid)
	}
	if err := proc.Signal(sig); err != nil {
		return fmt.Errorf("signal %d with %v: %w", h.pid, sig, err)
	}
	return nil
}

func (h *execHandle) Interrupt() error {
	return h.Signal(os.Interrupt)
}

func (h *execHandle) Kill() error {
	h.mu.Lock()
	proc := h.proc
	h.mu.Unlock()
	if proc == nil {
		return fmt.Errorf("process %d is not available", h.pid)
	}
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("kill %d: %w", h.pid, err)
	}
	return nil
}

func (h *execHandle) Wait(ctx context.Context) (ExitStatus, error) {
	h.mu.Lock()
	result := h.result
	waitCh := h.waitCh
	cmd := h.cmd
	h.mu.Unlock()
	if result != nil {
		return result.status, result.err
	}
	h.waitStart.Do(func() {
		go h.await(cmd)
	})
	select {
	case <-ctx.Done():
		return ExitStatus{}, ctx.Err()
	case result := <-waitCh:
		h.mu.Lock()
		h.result = &result
		h.proc = nil
		h.mu.Unlock()
		return result.status, result.err
	}
}

func (h *execHandle) await(cmd *exec.Cmd) {
	err := cmd.Wait()
	result := waitResult{status: ExitStatus{errCode(err, cmd.ProcessState), signalName(cmd.ProcessState)}, err: nil}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			result.err = fmt.Errorf("wait process %d: %w", h.pid, err)
		}
	}
	h.waitCh <- result
}

func errCode(waitErr error, state *os.ProcessState) int {
	if state != nil {
		return state.ExitCode()
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type mirroredReadCloser struct {
	src       io.ReadCloser
	mirror    io.WriteCloser
	closeOnce sync.Once
}

func wrapMirroredReadCloser(src io.ReadCloser, mirror io.WriteCloser) io.ReadCloser {
	if mirror == nil {
		return src
	}
	return &mirroredReadCloser{src: src, mirror: mirror}
}

func (r *mirroredReadCloser) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if n > 0 {
		if _, writeErr := r.mirror.Write(p[:n]); writeErr != nil {
			_ = r.closeMirror()
			return n, errors.Join(err, fmt.Errorf("mirror output: %w", writeErr))
		}
	}
	if errors.Is(err, io.EOF) {
		_ = r.closeMirror()
	}
	return n, err
}

func (r *mirroredReadCloser) Close() error {
	return errors.Join(r.src.Close(), r.closeMirror())
}

func (r *mirroredReadCloser) closeMirror() error {
	var err error
	r.closeOnce.Do(func() {
		err = closeWriteCloser(r.mirror)
	})
	return err
}

type mirroredPTY struct {
	src       PTY
	mirror    io.WriteCloser
	closeOnce sync.Once
}

func wrapMirroredPTY(src PTY, mirror io.WriteCloser) PTY {
	if mirror == nil {
		return src
	}
	return &mirroredPTY{src: src, mirror: mirror}
}

func (p *mirroredPTY) Read(buf []byte) (int, error) {
	n, err := p.src.Read(buf)
	if n > 0 {
		if _, writeErr := p.mirror.Write(buf[:n]); writeErr != nil {
			_ = p.closeMirror()
			return n, errors.Join(err, fmt.Errorf("mirror output: %w", writeErr))
		}
	}
	if errors.Is(err, io.EOF) {
		_ = p.closeMirror()
	}
	return n, err
}

func (p *mirroredPTY) Write(buf []byte) (int, error) {
	return p.src.Write(buf)
}

func (p *mirroredPTY) Close() error {
	return errors.Join(p.src.Close(), p.closeMirror())
}

func (p *mirroredPTY) Resize(size PTYSize) error {
	return p.src.Resize(size)
}

func (p *mirroredPTY) closeMirror() error {
	var err error
	p.closeOnce.Do(func() {
		err = closeWriteCloser(p.mirror)
	})
	return err
}

func closeWriteCloser(w io.WriteCloser) error {
	if w == nil {
		return nil
	}
	return w.Close()
}
