package process

import (
	"context"
	"io"
	"os"
)

// Runner starts a validated child process and returns a reusable control handle.
type Runner interface {
	Start(context.Context, LaunchSpec) (Handle, error)
}

// PTY exposes the control surface required by PTY-backed launches.
type PTY interface {
	io.ReadWriteCloser
	Resize(PTYSize) error
}

// Handle owns the lifetime and output hooks for one child process.
type Handle interface {
	PID() int
	Spec() LaunchSpec
	Logs() LogPaths
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	PTY() PTY
	Signal(os.Signal) error
	Interrupt() error
	Kill() error
	Wait(context.Context) (ExitStatus, error)
}

// ExitStatus is the normalized exit result for a completed process.
type ExitStatus struct {
	Code   int
	Signal string
}

func (s ExitStatus) Successful() bool {
	return s.Code == 0 && s.Signal == ""
}
