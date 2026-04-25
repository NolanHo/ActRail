package process

import (
	"reflect"
	"testing"
)

func TestNewLaunchSpecNormalizesFields(t *testing.T) {
	cmd, err := NewCommand("  /bin/echo  ", "hello")
	if err != nil {
		t.Fatalf("NewCommand() error = %v", err)
	}
	foo, err := NewEnvVar("FOO", "bar")
	if err != nil {
		t.Fatalf("NewEnvVar() error = %v", err)
	}
	env, err := InheritEnv(foo)
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	io, err := PipeIO(LogPaths{Stdout: "/tmp/stdout.log", Stderr: "/tmp/stderr.log"})
	if err != nil {
		t.Fatalf("PipeIO() error = %v", err)
	}
	launch, err := NewLaunchSpec(cmd, "/tmp/../tmp/work", env, io)
	if err != nil {
		t.Fatalf("NewLaunchSpec() error = %v", err)
	}
	if launch.Command().Path() != "/bin/echo" {
		t.Fatalf("command path = %q, want %q", launch.Command().Path(), "/bin/echo")
	}
	if launch.CWD().String() != "/tmp/work" {
		t.Fatalf("cwd = %q, want %q", launch.CWD(), "/tmp/work")
	}
	if launch.IO().Logs().Stdout != "/tmp/stdout.log" {
		t.Fatalf("stdout log = %q, want %q", launch.IO().Logs().Stdout, "/tmp/stdout.log")
	}
}

func TestNewWorkingDirRejectsRelativePath(t *testing.T) {
	if _, err := NewWorkingDir("relative/path"); err == nil {
		t.Fatal("NewWorkingDir() error = nil, want error")
	}
}

func TestNewEnvironmentRejectsDuplicateNames(t *testing.T) {
	foo1, _ := NewEnvVar("FOO", "1")
	foo2, _ := NewEnvVar("FOO", "2")
	if _, err := InheritEnv(foo1, foo2); err == nil {
		t.Fatal("InheritEnv() error = nil, want error")
	}
}

func TestEnvironmentResolveInheritOverridesParent(t *testing.T) {
	foo, _ := NewEnvVar("FOO", "child")
	bar, _ := NewEnvVar("BAR", "set")
	env, err := InheritEnv(foo, bar)
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	got := env.Resolve([]string{"FOO=parent", "KEEP=1"})
	want := []string{"FOO=child", "KEEP=1", "BAR=set"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}
}

func TestEnvironmentResolveReplaceDropsParent(t *testing.T) {
	foo, _ := NewEnvVar("FOO", "child")
	env, err := ReplaceEnv(foo)
	if err != nil {
		t.Fatalf("ReplaceEnv() error = %v", err)
	}
	got := env.Resolve([]string{"KEEP=1"})
	want := []string{"FOO=child"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}
}

func TestPTYIORequiresSize(t *testing.T) {
	if _, err := PTYIO(PTYSize{}, LogPaths{PTY: "/tmp/session.log"}); err == nil {
		t.Fatal("PTYIO() error = nil, want error")
	}
}

func TestPipeIORejectsPTYLog(t *testing.T) {
	if _, err := PipeIO(LogPaths{PTY: "/tmp/session.log"}); err == nil {
		t.Fatal("PipeIO() error = nil, want error")
	}
}
