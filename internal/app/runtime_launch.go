package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"actrail/internal/adapters/agent"
	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

type runtimeCatalog interface {
	LaunchSpec(agent.Request) (process.LaunchSpec, error)
}

type runtimeLauncher interface {
	Launch(context.Context, runtimeLaunchRequest) (sessionRuntime, error)
}

type RuntimeConfig struct {
	Catalog        runtimeCatalog
	Runner         process.Runner
	ResolveBinPath func(session.Backend) (string, error)
}

type runtimeLaunchRequest struct {
	Backend         session.Backend
	CWD             string
	Provider        string
	Model           string
	ReasoningEffort string
}

type processRuntimeLauncher struct {
	catalog        runtimeCatalog
	runner         process.Runner
	resolveBinPath func(session.Backend) (string, error)
	env            process.Environment
	io             process.IO
}

type sessionRuntime struct {
	launchSpec process.LaunchSpec
	handle     process.Handle
}

var errRuntimeInputUnavailable = errors.New("session runtime input is unavailable")

func newRuntimeLauncher(cfg RuntimeConfig) runtimeLauncher {
	env, err := process.InheritEnv()
	if err != nil {
		panic(err)
	}
	ioSpec, err := defaultRuntimeIO()
	if err != nil {
		panic(err)
	}
	catalog := cfg.Catalog
	if catalog == nil {
		catalog = agent.DefaultCatalog()
	}
	runner := cfg.Runner
	if runner == nil {
		runner = process.NewExecRunner()
	}
	resolveBinPath := cfg.ResolveBinPath
	if resolveBinPath == nil {
		resolveBinPath = defaultRuntimeBinPath
	}
	return processRuntimeLauncher{
		catalog:        catalog,
		runner:         runner,
		resolveBinPath: resolveBinPath,
		env:            env,
		io:             ioSpec,
	}
}

func defaultRuntimeIO() (process.IO, error) {
	if preferRuntimePTY() {
		return process.PTYIO(process.PTYSize{Rows: 24, Cols: 80}, process.LogPaths{})
	}
	return process.PipeIO(process.LogPaths{})
}

func defaultRuntimeBinPath(backend session.Backend) (string, error) {
	if err := backend.Validate(); err != nil {
		return "", err
	}
	return backend.String(), nil
}

func (l processRuntimeLauncher) Launch(ctx context.Context, req runtimeLaunchRequest) (sessionRuntime, error) {
	if err := ctx.Err(); err != nil {
		return sessionRuntime{}, err
	}
	if err := req.Backend.Validate(); err != nil {
		return sessionRuntime{}, err
	}
	binPath, err := l.resolveBinPath(req.Backend)
	if err != nil {
		return sessionRuntime{}, err
	}
	options, err := agent.NewOptions(req.Provider, req.Model, req.ReasoningEffort)
	if err != nil {
		return sessionRuntime{}, err
	}
	launchReq, err := agent.NewRequest(req.Backend, binPath, req.CWD, l.env, l.io, options)
	if err != nil {
		return sessionRuntime{}, err
	}
	launchSpec, err := l.catalog.LaunchSpec(launchReq)
	if err != nil {
		return sessionRuntime{}, err
	}
	handle, err := l.runner.Start(ctx, launchSpec)
	if err != nil {
		return sessionRuntime{}, err
	}
	if handle == nil {
		return sessionRuntime{}, fmt.Errorf("start runtime %q: nil process handle", req.Backend)
	}
	return sessionRuntime{launchSpec: launchSpec, handle: handle}, nil
}

func (r sessionRuntime) WriteInput(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.handle == nil || r.handle.PTY() == nil {
		return errRuntimeInputUnavailable
	}
	payload := text
	if !strings.HasSuffix(payload, "\n") {
		payload += "\n"
	}
	if _, err := io.WriteString(r.handle.PTY(), payload); err != nil {
		return fmt.Errorf("write runtime input: %w", err)
	}
	return nil
}

func (r sessionRuntime) Interrupt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.handle == nil {
		return fmt.Errorf("interrupt runtime: nil process handle")
	}
	if err := r.handle.Interrupt(); err != nil {
		return fmt.Errorf("interrupt runtime: %w", err)
	}
	return nil
}

func (r sessionRuntime) Kill(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.handle == nil {
		return nil
	}
	if err := r.handle.Kill(); err != nil {
		return fmt.Errorf("kill runtime: %w", err)
	}
	return nil
}

func (r sessionRuntime) PID() int {
	if r.handle == nil {
		return 0
	}
	return r.handle.PID()
}
