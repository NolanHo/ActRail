package app

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"actrail/internal/adapters/agent"
	"actrail/internal/adapters/iod"
	"actrail/internal/adapters/process"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

type launchAdapter struct {
	backend session.Backend
	args    []string
}

func (a launchAdapter) Backend() session.Backend {
	return a.backend
}

func (a launchAdapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (a launchAdapter) ValidateOptions(agent.Options) error {
	return nil
}

func (a launchAdapter) CommandArgs(agent.Options) ([]string, error) {
	return append([]string(nil), a.args...), nil
}

func TestCreateSessionLaunchesRuntimeThroughInjectedCatalogAndRunner(t *testing.T) {
	catalog, err := agent.NewCatalog(launchAdapter{backend: session.BackendPI, args: []string{"serve", "--session"}})
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	handle := process.NewFakeHandle(process.LaunchSpec{})
	handle.SetPID(321)
	runner := &process.FakeRunner{NextHandle: handle}
	now := time.Unix(1760000000, 0).UTC()
	svc := newStubWithRuntime(config.Load(), func() time.Time { return now }, RuntimeConfig{
		Catalog: catalog,
		Runner:  runner,
		ResolveBinPath: func(backend session.Backend) (string, error) {
			if backend != session.BackendPI {
				t.Fatalf("ResolveBinPath() backend = %q, want %q", backend, session.BackendPI)
			}
			return "/tmp/custom-pi", nil
		},
	})

	created, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend: "pi",
		CWD:          "/root/code/ActRail",
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if !created.OK || created.Session == nil {
		t.Fatalf("CreateSession() = %+v, want ok session payload", created)
	}
	if len(runner.Starts) != 1 {
		t.Fatalf("len(runner.Starts) = %d, want 1", len(runner.Starts))
	}
	start := runner.Starts[0]
	if start.Command().Path() != "/tmp/custom-pi" {
		t.Fatalf("launch command path = %q, want %q", start.Command().Path(), "/tmp/custom-pi")
	}
	if got := start.Command().Args(); len(got) != 2 || got[0] != "serve" || got[1] != "--session" {
		t.Fatalf("launch args = %#v, want [serve --session]", got)
	}
	if start.CWD().String() != "/root/code/ActRail" {
		t.Fatalf("launch cwd = %q, want %q", start.CWD().String(), "/root/code/ActRail")
	}
	wantMode := process.IOModePipes
	if preferRuntimePTY() {
		wantMode = process.IOModePTY
	}
	if start.IO().Mode() != wantMode {
		t.Fatalf("launch io mode = %q, want %q", start.IO().Mode(), wantMode)
	}
	sessionID, err := session.ParseSessionID(created.Session.SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	record, ok := svc.registry.Lookup(sessionID)
	if !ok {
		t.Fatal("Lookup() ok = false, want true")
	}
	if record.runtime.PID() != 321 {
		t.Fatalf("record.runtime.PID() = %d, want 321", record.runtime.PID())
	}
	if record.runtime.launchSpec.Command().Path() != "/tmp/custom-pi" {
		t.Fatalf("record.runtime.launchSpec.Command().Path() = %q, want %q", record.runtime.launchSpec.Command().Path(), "/tmp/custom-pi")
	}
}

func TestCreateSessionDoesNotStoreMetadataWhenLaunchFails(t *testing.T) {
	runner := &process.FakeRunner{StartErr: context.DeadlineExceeded}
	svc := newStubWithRuntime(config.Load(), func() time.Time { return time.Unix(1760000000, 0).UTC() }, RuntimeConfig{Runner: runner})

	_, err := svc.CreateSession(context.Background(), CreateSessionRequest{
		AgentBackend: "pi",
		CWD:          "/root/code/ActRail",
	})
	if err == nil {
		t.Fatal("CreateSession() error = nil, want launch failure")
	}
	if items := svc.registry.List(); len(items) != 0 {
		t.Fatalf("len(registry.List()) = %d, want 0 after failed launch", len(items))
	}
}

func TestHelperLaunchSpecEncodesTransparentChildLaunchContract(t *testing.T) {
	envVarA, err := process.NewEnvVar("ACTRAIL_ALPHA", "one")
	if err != nil {
		t.Fatalf("NewEnvVar(ACTRAIL_ALPHA) error = %v", err)
	}
	envVarB, err := process.NewEnvVar("ACTRAIL_BRAVO", "two=2")
	if err != nil {
		t.Fatalf("NewEnvVar(ACTRAIL_BRAVO) error = %v", err)
	}
	childEnv, err := process.ReplaceEnv(envVarA, envVarB)
	if err != nil {
		t.Fatalf("ReplaceEnv() error = %v", err)
	}
	ioSpec, err := process.PipeIO(process.LogPaths{})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	launcherEnv, err := process.InheritEnv()
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	launcher := processRuntimeLauncher{iodRuntimeRoot: "/tmp/actrail-data/runtime/iod", env: launcherEnv}
	sessionID := mustSessionID(t, "s_helper_contract")
	generationID, err := iod.NewGenerationID("g_helper_contract")
	if err != nil {
		t.Fatalf("NewGenerationID() error = %v", err)
	}
	piChild := mustLaunchSpecForHelperContractTest(t, "/tmp/pi", []string{"--mode", "rpc"}, "/tmp/project-pi", childEnv, ioSpec)
	codexChild := mustLaunchSpecForHelperContractTest(t, "/tmp/codex", []string{"app-server", "--stdio"}, "/tmp/project-codex", childEnv, ioSpec)

	piSpec, err := launcher.helperLaunchSpec(runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendPI, CWD: "/tmp/project-pi"}, "/tmp/actrail-iod", generationID, piChild)
	if err != nil {
		t.Fatalf("helperLaunchSpec(pi) error = %v", err)
	}
	codexSpec, err := launcher.helperLaunchSpec(runtimeLaunchRequest{SessionID: sessionID, Backend: session.BackendCodex, CWD: "/tmp/project-codex"}, "/tmp/actrail-iod", generationID, codexChild)
	if err != nil {
		t.Fatalf("helperLaunchSpec(codex) error = %v", err)
	}

	assertHelperLaunchContract(t, piSpec.Command().Args(), piChild)
	assertHelperLaunchContract(t, codexSpec.Command().Args(), codexChild)
	if !reflect.DeepEqual(piSpec.Environment().Vars(), launcherEnv.Vars()) {
		t.Fatalf("helper environment vars = %#v, want launcher env vars %#v", piSpec.Environment().Vars(), launcherEnv.Vars())
	}
	if piSpec.Environment().Mode() != launcherEnv.Mode() {
		t.Fatalf("helper environment mode = %q, want %q", piSpec.Environment().Mode(), launcherEnv.Mode())
	}
	if codexSpec.CWD().String() != "/tmp/project-codex" {
		t.Fatalf("codex helper cwd = %q, want %q", codexSpec.CWD().String(), "/tmp/project-codex")
	}
}

func TestNewRuntimeLauncherResolvesIODRuntimeRootToAbsolutePath(t *testing.T) {
	launcher := newRuntimeLauncher(RuntimeConfig{IODRuntimeRoot: "./data/runtime/iod", UseIODHelper: true})
	processLauncher, ok := launcher.(processRuntimeLauncher)
	if !ok {
		t.Fatalf("launcher type = %T, want processRuntimeLauncher", launcher)
	}
	if !filepath.IsAbs(processLauncher.iodRuntimeRoot) {
		t.Fatalf("iod runtime root = %q, want absolute path", processLauncher.iodRuntimeRoot)
	}
	want, err := filepath.Abs("./data/runtime/iod")
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	if processLauncher.iodRuntimeRoot != want {
		t.Fatalf("iod runtime root = %q, want %q", processLauncher.iodRuntimeRoot, want)
	}
}

func mustLaunchSpecForHelperContractTest(t *testing.T, path string, args []string, cwd string, env process.Environment, ioSpec process.IO) process.LaunchSpec {
	t.Helper()
	command, err := process.NewCommand(path, args...)
	if err != nil {
		t.Fatalf("NewCommand(%q) error = %v", path, err)
	}
	spec, err := process.NewLaunchSpec(command, cwd, env, ioSpec)
	if err != nil {
		t.Fatalf("NewLaunchSpec(%q) error = %v", path, err)
	}
	return spec
}

func assertHelperLaunchContract(t *testing.T, got []string, childSpec process.LaunchSpec) {
	t.Helper()
	wantPrefix := []string{
		helperFlagSessionID, "s_helper_contract",
		helperFlagGenerationID, "g_helper_contract",
		helperFlagRuntimeRoot, "/tmp/actrail-data/runtime/iod",
		helperFlagChildCWD, childSpec.CWD().String(),
		helperFlagChildEnvMode, string(childSpec.Environment().Mode()),
		helperFlagChildEnv, "ACTRAIL_ALPHA=one",
		helperFlagChildEnv, "ACTRAIL_BRAVO=two=2",
		"--",
		childSpec.Command().Path(),
	}
	want := append(wantPrefix, childSpec.Command().Args()...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("helper launch args = %#v, want %#v", got, want)
	}
}
