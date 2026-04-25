package app

import (
	"context"
	"testing"
	"time"

	"actrail/internal/adapters/agent"
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
