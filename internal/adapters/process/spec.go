package process

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Command is the validated argv model for a child process launch.
type Command struct {
	path string
	args []string
}

func NewCommand(path string, args ...string) (Command, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return Command{}, fmt.Errorf("command path is required")
	}
	if strings.ContainsRune(trimmed, 0) {
		return Command{}, fmt.Errorf("command path %q contains NUL", trimmed)
	}
	copied := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return Command{}, fmt.Errorf("command arg %d contains NUL", i)
		}
		copied[i] = arg
	}
	return Command{path: trimmed, args: copied}, nil
}

func (c Command) Validate() error {
	_, err := NewCommand(c.path, c.args...)
	return err
}

func (c Command) Path() string {
	return c.path
}

func (c Command) Args() []string {
	copied := make([]string, len(c.args))
	copy(copied, c.args)
	return copied
}

func (c Command) Argv() []string {
	argv := make([]string, 0, len(c.args)+1)
	argv = append(argv, c.path)
	argv = append(argv, c.args...)
	return argv
}

// WorkingDir is the validated cwd model for a child process launch.
type WorkingDir string

func NewWorkingDir(raw string) (WorkingDir, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("working directory is required")
	}
	cleaned := filepath.Clean(trimmed)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("working directory %q must be absolute", trimmed)
	}
	return WorkingDir(cleaned), nil
}

func (d WorkingDir) Validate() error {
	_, err := NewWorkingDir(string(d))
	return err
}

func (d WorkingDir) String() string {
	return string(d)
}

// EnvVar is one validated environment binding.
type EnvVar struct {
	name  string
	value string
}

func NewEnvVar(name, value string) (EnvVar, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return EnvVar{}, fmt.Errorf("environment variable name is required")
	}
	if strings.Contains(trimmed, "=") {
		return EnvVar{}, fmt.Errorf("environment variable name %q contains '='", trimmed)
	}
	if strings.ContainsRune(trimmed, 0) {
		return EnvVar{}, fmt.Errorf("environment variable name %q contains NUL", trimmed)
	}
	if strings.ContainsRune(value, 0) {
		return EnvVar{}, fmt.Errorf("environment variable %q value contains NUL", trimmed)
	}
	return EnvVar{name: trimmed, value: value}, nil
}

func (v EnvVar) Validate() error {
	_, err := NewEnvVar(v.name, v.value)
	return err
}

func (v EnvVar) Name() string {
	return v.name
}

func (v EnvVar) Value() string {
	return v.value
}

func (v EnvVar) String() string {
	return v.name + "=" + v.value
}

// EnvMode controls whether a launch inherits the parent process environment.
type EnvMode string

const (
	EnvModeInherit EnvMode = "inherit"
	EnvModeReplace EnvMode = "replace"
)

func parseEnvMode(raw EnvMode) (EnvMode, error) {
	mode := EnvMode(strings.ToLower(strings.TrimSpace(string(raw))))
	switch mode {
	case EnvModeInherit, EnvModeReplace:
		return mode, nil
	case "":
		return "", fmt.Errorf("environment mode is required")
	default:
		return "", fmt.Errorf("environment mode %q is not supported", raw)
	}
}

// Environment is the validated environment model for a launch.
type Environment struct {
	mode EnvMode
	vars []EnvVar
}

func InheritEnv(vars ...EnvVar) (Environment, error) {
	return newEnvironment(EnvModeInherit, vars)
}

func ReplaceEnv(vars ...EnvVar) (Environment, error) {
	return newEnvironment(EnvModeReplace, vars)
}

func newEnvironment(mode EnvMode, vars []EnvVar) (Environment, error) {
	parsedMode, err := parseEnvMode(mode)
	if err != nil {
		return Environment{}, err
	}
	seen := make(map[string]struct{}, len(vars))
	copied := make([]EnvVar, len(vars))
	for i, v := range vars {
		if err := v.Validate(); err != nil {
			return Environment{}, err
		}
		if _, exists := seen[v.Name()]; exists {
			return Environment{}, fmt.Errorf("environment variable %q is duplicated", v.Name())
		}
		seen[v.Name()] = struct{}{}
		copied[i] = v
	}
	return Environment{mode: parsedMode, vars: copied}, nil
}

func (e Environment) Validate() error {
	_, err := newEnvironment(e.mode, e.vars)
	return err
}

func (e Environment) Mode() EnvMode {
	return e.mode
}

func (e Environment) Vars() []EnvVar {
	copied := make([]EnvVar, len(e.vars))
	copy(copied, e.vars)
	return copied
}

func (e Environment) Resolve(parent []string) []string {
	resolved := make([]string, 0, len(parent)+len(e.vars))
	if e.mode == EnvModeInherit {
		resolved = append(resolved, parent...)
	}
	index := make(map[string]int, len(resolved)+len(e.vars))
	for i, item := range resolved {
		name, _, ok := strings.Cut(item, "=")
		if !ok || name == "" {
			continue
		}
		index[name] = i
	}
	for _, v := range e.vars {
		if pos, ok := index[v.Name()]; ok {
			resolved[pos] = v.String()
			continue
		}
		index[v.Name()] = len(resolved)
		resolved = append(resolved, v.String())
	}
	return resolved
}

// IOMode controls whether the process uses stdio pipes or a PTY.
type IOMode string

const (
	IOModePipes IOMode = "pipes"
	IOModePTY   IOMode = "pty"
)

func parseIOMode(raw IOMode) (IOMode, error) {
	mode := IOMode(strings.ToLower(strings.TrimSpace(string(raw))))
	switch mode {
	case IOModePipes, IOModePTY:
		return mode, nil
	case "":
		return "", fmt.Errorf("io mode is required")
	default:
		return "", fmt.Errorf("io mode %q is not supported", raw)
	}
}

// PTYSize is the initial terminal geometry for PTY launches.
type PTYSize struct {
	Rows uint16
	Cols uint16
}

func (s PTYSize) Validate() error {
	if s.Rows == 0 {
		return fmt.Errorf("pty rows must be greater than zero")
	}
	if s.Cols == 0 {
		return fmt.Errorf("pty cols must be greater than zero")
	}
	return nil
}

// LogPaths declares known log destinations for launch output.
type LogPaths struct {
	Stdout string
	Stderr string
	PTY    string
}

func (l LogPaths) validate(mode IOMode) (LogPaths, error) {
	cleaned := LogPaths{}
	seen := make(map[string]string, 3)
	for _, item := range []struct {
		label string
		value string
	}{
		{label: "stdout", value: l.Stdout},
		{label: "stderr", value: l.Stderr},
		{label: "pty", value: l.PTY},
	} {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		path := filepath.Clean(item.value)
		if !filepath.IsAbs(path) {
			return LogPaths{}, fmt.Errorf("%s log path %q must be absolute", item.label, item.value)
		}
		if other, exists := seen[path]; exists {
			return LogPaths{}, fmt.Errorf("%s log path %q duplicates %s", item.label, path, other)
		}
		seen[path] = item.label
		switch item.label {
		case "stdout":
			cleaned.Stdout = path
		case "stderr":
			cleaned.Stderr = path
		case "pty":
			cleaned.PTY = path
		}
	}
	if mode == IOModePipes && cleaned.PTY != "" {
		return LogPaths{}, fmt.Errorf("pty log path requires io mode %q", IOModePTY)
	}
	if mode == IOModePTY && (cleaned.Stdout != "" || cleaned.Stderr != "") {
		return LogPaths{}, fmt.Errorf("stdout/stderr log paths require io mode %q", IOModePipes)
	}
	return cleaned, nil
}

// IO configures the output transport for one launch.
type IO struct {
	mode    IOMode
	ptySize *PTYSize
	logs    LogPaths
}

func PipeIO(logs LogPaths) (IO, error) {
	validated, err := logs.validate(IOModePipes)
	if err != nil {
		return IO{}, err
	}
	return IO{mode: IOModePipes, logs: validated}, nil
}

func PTYIO(size PTYSize, logs LogPaths) (IO, error) {
	if err := size.Validate(); err != nil {
		return IO{}, err
	}
	validated, err := logs.validate(IOModePTY)
	if err != nil {
		return IO{}, err
	}
	copySize := size
	return IO{mode: IOModePTY, ptySize: &copySize, logs: validated}, nil
}

func (io IO) Validate() error {
	mode, err := parseIOMode(io.mode)
	if err != nil {
		return err
	}
	if _, err := io.logs.validate(mode); err != nil {
		return err
	}
	if mode == IOModePipes {
		if io.ptySize != nil {
			return fmt.Errorf("pty size requires io mode %q", IOModePTY)
		}
		return nil
	}
	if io.ptySize == nil {
		return fmt.Errorf("pty size is required for io mode %q", IOModePTY)
	}
	if err := io.ptySize.Validate(); err != nil {
		return err
	}
	return nil
}

func (io IO) Mode() IOMode {
	return io.mode
}

func (io IO) PTYSize() *PTYSize {
	if io.ptySize == nil {
		return nil
	}
	copySize := *io.ptySize
	return &copySize
}

func (io IO) Logs() LogPaths {
	return io.logs
}

// LaunchOption configures process launch behavior that is independent of argv.
type LaunchOption func(*LaunchSpec)

// Detached starts the process in a lifecycle boundary that does not inherit the parent's terminal session.
func Detached() LaunchOption {
	return func(spec *LaunchSpec) {
		if spec != nil {
			spec.detached = true
		}
	}
}

// LaunchSpec freezes the validated process launch contract for later runtime integration.
type LaunchSpec struct {
	command  Command
	cwd      WorkingDir
	env      Environment
	io       IO
	detached bool
}

func NewLaunchSpec(command Command, cwd string, env Environment, io IO, options ...LaunchOption) (LaunchSpec, error) {
	if err := command.Validate(); err != nil {
		return LaunchSpec{}, err
	}
	dir, err := NewWorkingDir(cwd)
	if err != nil {
		return LaunchSpec{}, err
	}
	if err := env.Validate(); err != nil {
		return LaunchSpec{}, err
	}
	if err := io.Validate(); err != nil {
		return LaunchSpec{}, err
	}
	spec := LaunchSpec{command: command, cwd: dir, env: env, io: io}
	for _, option := range options {
		if option != nil {
			option(&spec)
		}
	}
	return spec, nil
}

func (s LaunchSpec) Validate() error {
	options := []LaunchOption{}
	if s.detached {
		options = append(options, Detached())
	}
	_, err := NewLaunchSpec(s.command, s.cwd.String(), s.env, s.io, options...)
	return err
}

func (s LaunchSpec) Command() Command {
	return s.command
}

func (s LaunchSpec) CWD() WorkingDir {
	return s.cwd
}

func (s LaunchSpec) Environment() Environment {
	return s.env
}

func (s LaunchSpec) IO() IO {
	return s.io
}

func (s LaunchSpec) Detached() bool {
	return s.detached
}
