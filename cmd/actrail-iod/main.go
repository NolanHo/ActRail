package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

func main() {
	var (
		sessionRaw      = flag.String("session-id", "", "live session id")
		generationRaw   = flag.String("generation-id", "", "live generation id")
		runtimeRoot     = flag.String("runtime-root", "", "directory containing per-generation helper files")
		cwdRaw          = flag.String("cwd", "", "child working directory; defaults to current working directory")
		protocolVersion = flag.Int("protocol-version", iod.DefaultProtocolVersion, "helper protocol version")
		rows            = flag.Uint("pty-rows", 24, "initial PTY rows")
		cols            = flag.Uint("pty-cols", 80, "initial PTY cols")
	)
	flag.Parse()
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
	cwd, err := iod.ResolveHelperCWD(*cwdRaw)
	if err != nil {
		failf("actrail-iod: resolve -cwd: %v", err)
	}
	command, err := process.NewCommand(flag.Arg(0), flag.Args()[1:]...)
	if err != nil {
		failf("actrail-iod: child command: %v", err)
	}
	env, err := process.InheritEnv()
	if err != nil {
		failf("actrail-iod: inherit environment: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := iod.RunHelper(ctx, iod.HelperOptions{
		SessionID:       sessionID,
		GenerationID:    generationID,
		Paths:           paths,
		Command:         command,
		CWD:             cwd,
		Environment:     env,
		PTYSize:         process.PTYSize{Rows: uint16(*rows), Cols: uint16(*cols)},
		ProtocolVersion: *protocolVersion,
	}); err != nil {
		failf("actrail-iod: %v", err)
	}
}

func failf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
