//go:build !unix

package process

import "os/exec"

func startPTYProcess(_ *exec.Cmd, _ PTYSize) (PTY, error) {
	return nil, ErrPTYUnsupported
}
