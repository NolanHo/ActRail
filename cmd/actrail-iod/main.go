package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

var (
	buildDate = "dev"
	gitSHA    = "dev"
)

func main() {
	runtime.GOMAXPROCS(4)
	var (
		sessionRaw            = flag.String("session-id", "", "live session id")
		generationRaw         = flag.String("generation-id", "", "live generation id")
		runtimeRoot           = flag.String("runtime-root", "", "directory containing per-generation helper files")
		childCWDRaw           = flag.String("child-cwd", "", "child working directory; defaults to current working directory")
		legacyCWDRaw          = flag.String("cwd", "", "deprecated alias for -child-cwd")
		childEnvModeRaw       = flag.String("child-env-mode", string(process.EnvModeInherit), "child environment mode: inherit or replace")
		childIOModeRaw        = flag.String("child-io-mode", string(iod.ChildIOModePTY), "child IO mode: pty, stdio, or unix")
		sessionHistoryPathRaw = flag.String("session-history-path", "", "Pi session JSONL path for helper history cache")
		codexThreadIDRaw      = flag.String("codex-thread-id", "", "Codex thread id for helper-owned session history discovery")
		protocolVersion       = flag.Int("protocol-version", iod.DefaultProtocolVersion, "helper protocol version")
		rows                  = flag.Uint("pty-rows", 24, "initial PTY rows")
		cols                  = flag.Uint("pty-cols", 80, "initial PTY cols")
		showVersion           = flag.Bool("version", false, "print iod version and exit")
	)
	var childEnvRaw multiStringFlag
	flag.Var(&childEnvRaw, "child-env", "child environment entry in NAME=VALUE form; repeatable")
	flag.Parse()
	if *showVersion {
		fmt.Printf("actrail-iod build_date=%s git_sha=%s\n", buildDate, gitSHA)
		return
	}
	if flag.NArg() == 0 {
		failf("actrail-iod: child command is required after flags")
	}
	sessionID, err := session.ParseSessionID(*sessionRaw)
	if err != nil {
		failf("actrail-iod: parse -session-id: %v", err)
	}
	generationID, err := iod.NewGenerationID(*generationRaw)
	if err != nil {
		failf("actrail-iod: parse -generation-id: %v", err)
	}
	paths, err := iod.NewGenerationPaths(*runtimeRoot, sessionID, generationID)
	if err != nil {
		failf("actrail-iod: build generation paths: %v", err)
	}
	childCWD := strings.TrimSpace(*childCWDRaw)
	if childCWD == "" {
		childCWD = strings.TrimSpace(*legacyCWDRaw)
	}
	cwd, err := iod.ResolveHelperCWD(childCWD)
	if err != nil {
		failf("actrail-iod: resolve child cwd: %v", err)
	}
	command, err := process.NewCommand(flag.Arg(0), flag.Args()[1:]...)
	if err != nil {
		failf("actrail-iod: child command: %v", err)
	}
	env, err := parseChildEnvironment(*childEnvModeRaw, childEnvRaw)
	if err != nil {
		failf("actrail-iod: parse child environment: %v", err)
	}
	childIOMode, err := iod.ParseChildIOMode(*childIOModeRaw)
	if err != nil {
		failf("actrail-iod: parse -child-io-mode: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := iod.RunHelper(ctx, iod.HelperOptions{
		SessionID:          sessionID,
		GenerationID:       generationID,
		Paths:              paths,
		Command:            command,
		CWD:                cwd,
		Environment:        env,
		PTYSize:            process.PTYSize{Rows: uint16(*rows), Cols: uint16(*cols)},
		ChildIOMode:        childIOMode,
		ProtocolVersion:    *protocolVersion,
		SessionHistoryPath: *sessionHistoryPathRaw,
		CodexThreadID:      *codexThreadIDRaw,
		BuildDate:          buildDate,
		GitSHA:             gitSHA,
	}); err != nil {
		failf("actrail-iod: %v", err)
	}
}

type multiStringFlag []string

func (f *multiStringFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *multiStringFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func parseChildEnvironment(modeRaw string, entries []string) (process.Environment, error) {
	vars := make([]process.EnvVar, 0, len(entries))
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			return process.Environment{}, fmt.Errorf("child env entry %q must be NAME=VALUE", entry)
		}
		item, err := process.NewEnvVar(name, value)
		if err != nil {
			return process.Environment{}, err
		}
		vars = append(vars, item)
	}
	switch strings.ToLower(strings.TrimSpace(modeRaw)) {
	case string(process.EnvModeInherit):
		return process.InheritEnv(vars...)
	case string(process.EnvModeReplace):
		return process.ReplaceEnv(vars...)
	default:
		return process.Environment{}, fmt.Errorf("unsupported child env mode %q", modeRaw)
	}
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
