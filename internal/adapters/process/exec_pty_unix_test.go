//go:build unix

package process

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	ptylib "github.com/creack/pty"
)

func TestExecRunnerStartPTYInteractiveRoundTrip(t *testing.T) {
	runner := NewExecRunner()
	tmp := t.TempDir()
	ptyLog := tmp + "/session.log"
	cmd := helperCommand(t, "pty-echo")
	helperFlag, err := NewEnvVar("GO_WANT_HELPER_PROCESS", "1")
	if err != nil {
		t.Fatalf("NewEnvVar() error = %v", err)
	}
	env, err := InheritEnv(helperFlag)
	if err != nil {
		t.Fatalf("InheritEnv() error = %v", err)
	}
	ioSpec, err := PTYIO(PTYSize{Rows: 24, Cols: 80}, LogPaths{PTY: ptyLog})
	if err != nil {
		t.Fatalf("PTYIO() error = %v", err)
	}
	spec, err := NewLaunchSpec(cmd, tmp, env, ioSpec)
	if err != nil {
		t.Fatalf("NewLaunchSpec() error = %v", err)
	}
	handle, err := runner.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if handle.Stdout() != nil {
		t.Fatalf("Stdout() = %v, want nil in PTY mode", handle.Stdout())
	}
	if handle.Stderr() != nil {
		t.Fatalf("Stderr() = %v, want nil in PTY mode", handle.Stderr())
	}
	dev := handle.PTY()
	if dev == nil {
		t.Fatal("PTY() = nil, want live PTY handle")
	}
	defer dev.Close()
	reader := bufio.NewReader(dev)
	waitPTYLine(t, reader, 3*time.Second, "ready")
	if _, err := io.WriteString(dev, "ping\n"); err != nil {
		t.Fatalf("WriteString(ping) error = %v", err)
	}
	waitPTYLine(t, reader, 3*time.Second, "echo:ping")
	if err := dev.Resize(PTYSize{Rows: 30, Cols: 100}); err != nil {
		t.Fatalf("Resize() error = %v", err)
	}
	if _, err := io.WriteString(dev, "size\n"); err != nil {
		t.Fatalf("WriteString(size) error = %v", err)
	}
	waitPTYLine(t, reader, 3*time.Second, "size:30x100")
	if _, err := io.WriteString(dev, "exit\n"); err != nil {
		t.Fatalf("WriteString(exit) error = %v", err)
	}
	waitPTYLine(t, reader, 3*time.Second, "bye")
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, err := handle.Wait(waitCtx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !status.Successful() {
		t.Fatalf("Wait() status = %#v, want success", status)
	}
	logBytes, err := os.ReadFile(ptyLog)
	if err != nil {
		t.Fatalf("ReadFile(ptyLog) error = %v", err)
	}
	logText := normalizePTYText(string(logBytes))
	if !strings.Contains(logText, "echo:ping") {
		t.Fatalf("pty log = %q, want echo marker", logText)
	}
	if !strings.Contains(logText, "size:30x100") {
		t.Fatalf("pty log = %q, want resized terminal size", logText)
	}
}

func runPTYHelper() int {
	reader := bufio.NewReader(os.Stdin)
	_, _ = io.WriteString(os.Stdout, "ready\n")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return 0
			}
			_, _ = io.WriteString(os.Stderr, err.Error())
			return 91
		}
		switch strings.TrimSpace(line) {
		case "ping":
			_, _ = io.WriteString(os.Stdout, "echo:ping\n")
		case "size":
			size, err := ptylib.GetsizeFull(os.Stdin)
			if err != nil {
				_, _ = io.WriteString(os.Stderr, err.Error())
				return 92
			}
			_, _ = io.WriteString(os.Stdout, fmt.Sprintf("size:%dx%d\n", size.Rows, size.Cols))
		case "exit":
			_, _ = io.WriteString(os.Stdout, "bye\n")
			return 0
		default:
			_, _ = io.WriteString(os.Stdout, fmt.Sprintf("unknown:%s\n", strings.TrimSpace(line)))
		}
	}
}

func waitPTYLine(t *testing.T, reader *bufio.Reader, timeout time.Duration, want string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	seen := make([]string, 0, 4)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("ReadString() timeout waiting for %q after %q", want, strings.Join(seen, ", "))
		}
		line, err := readPTYLine(reader, remaining)
		if err != nil {
			t.Fatalf("ReadString() error = %v after %q", err, strings.Join(seen, ", "))
		}
		text := strings.TrimSpace(normalizePTYText(line))
		seen = append(seen, text)
		if text == want {
			return
		}
	}
}

func readPTYLine(reader *bufio.Reader, timeout time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		ch <- result{line: line, err: err}
	}()
	select {
	case res := <-ch:
		return res.line, res.err
	case <-time.After(timeout):
		return "", context.DeadlineExceeded
	}
}

func normalizePTYText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "")
}
