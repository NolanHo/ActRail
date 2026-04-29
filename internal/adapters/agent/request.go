package agent

import (
	"fmt"
	"strings"

	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

// BinPath is the validated executable path or name used for a backend launch.
type BinPath string

func NewBinPath(raw string) (BinPath, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("bin path is required")
	}
	if strings.ContainsRune(trimmed, 0) {
		return "", fmt.Errorf("bin path %q contains NUL", trimmed)
	}
	return BinPath(trimmed), nil
}

func (p BinPath) Validate() error {
	_, err := NewBinPath(string(p))
	return err
}

func (p BinPath) String() string {
	return string(p)
}

// Options holds normalized backend launch options that affect provider/model selection.
type Options struct {
	provider        string
	model           string
	reasoningEffort string
	sessionPath     string
}

func NewOptions(provider, model, reasoningEffort string) (Options, error) {
	return NewOptionsWithSessionPath(provider, model, reasoningEffort, "")
}

func NewOptionsWithSessionPath(provider, model, reasoningEffort, sessionPath string) (Options, error) {
	normalizedProvider, err := normalizeOptionValue("provider", provider)
	if err != nil {
		return Options{}, err
	}
	normalizedModel, err := normalizeOptionValue("model", model)
	if err != nil {
		return Options{}, err
	}
	normalizedReasoning, err := normalizeOptionValue("reasoning_effort", reasoningEffort)
	if err != nil {
		return Options{}, err
	}
	normalizedSessionPath, err := normalizeOptionValue("session_path", sessionPath)
	if err != nil {
		return Options{}, err
	}
	return Options{
		provider:        normalizedProvider,
		model:           normalizedModel,
		reasoningEffort: normalizedReasoning,
		sessionPath:     normalizedSessionPath,
	}, nil
}

func (o Options) Validate() error {
	_, err := NewOptionsWithSessionPath(o.provider, o.model, o.reasoningEffort, o.sessionPath)
	return err
}

func (o Options) Provider() string {
	return o.provider
}

func (o Options) Model() string {
	return o.model
}

func (o Options) ReasoningEffort() string {
	return o.reasoningEffort
}

func (o Options) SessionPath() string {
	return o.sessionPath
}

func normalizeOptionValue(label, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.ContainsRune(trimmed, 0) {
		return "", fmt.Errorf("%s %q contains NUL", label, trimmed)
	}
	return trimmed, nil
}

// Request is the validated backend-agnostic launch input for agent runtimes.
type Request struct {
	backend session.Backend
	binPath BinPath
	cwd     process.WorkingDir
	env     process.Environment
	io      process.IO
	options Options
}

func NewRequest(backend session.Backend, binPath, cwd string, env process.Environment, io process.IO, options Options) (Request, error) {
	if err := backend.Validate(); err != nil {
		return Request{}, err
	}
	path, err := NewBinPath(binPath)
	if err != nil {
		return Request{}, err
	}
	dir, err := process.NewWorkingDir(cwd)
	if err != nil {
		return Request{}, err
	}
	if err := env.Validate(); err != nil {
		return Request{}, err
	}
	if err := io.Validate(); err != nil {
		return Request{}, err
	}
	if err := options.Validate(); err != nil {
		return Request{}, err
	}
	return Request{
		backend: backend,
		binPath: path,
		cwd:     dir,
		env:     env,
		io:      io,
		options: options,
	}, nil
}

func (r Request) Validate() error {
	_, err := NewRequest(r.backend, r.binPath.String(), r.cwd.String(), r.env, r.io, r.options)
	return err
}

func (r Request) Backend() session.Backend {
	return r.backend
}

func (r Request) BinPath() BinPath {
	return r.binPath
}

func (r Request) CWD() process.WorkingDir {
	return r.cwd
}

func (r Request) Environment() process.Environment {
	return r.env
}

func (r Request) IO() process.IO {
	return r.io
}

func (r Request) Options() Options {
	return r.options
}
