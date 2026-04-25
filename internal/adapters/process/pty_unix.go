//go:build unix

package process

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	ptylib "github.com/creack/pty"
)

type osFilePTY struct {
	file *os.File
}

func startPTYProcess(cmd *exec.Cmd, size PTYSize) (PTY, error) {
	file, err := ptylib.StartWithSize(cmd, &ptylib.Winsize{Rows: size.Rows, Cols: size.Cols})
	if err != nil {
		return nil, err
	}
	return &osFilePTY{file: file}, nil
}

func (p *osFilePTY) Read(buf []byte) (int, error) {
	return p.file.Read(buf)
}

func (p *osFilePTY) Write(buf []byte) (int, error) {
	return p.file.Write(buf)
}

func (p *osFilePTY) Close() error {
	if p.file == nil {
		return nil
	}
	return p.file.Close()
}

func (p *osFilePTY) Resize(size PTYSize) error {
	if err := size.Validate(); err != nil {
		return err
	}
	if p.file == nil {
		return fmt.Errorf("pty is not available")
	}
	if err := ptylib.Setsize(p.file, &ptylib.Winsize{Rows: size.Rows, Cols: size.Cols}); err != nil {
		return fmt.Errorf("resize pty: %w", err)
	}
	return nil
}

var _ PTY = (*osFilePTY)(nil)
var _ io.ReadWriteCloser = (*osFilePTY)(nil)
