//go:build unix

package process

import (
	"os/exec"
	"syscall"
)

func applyProcessAttrs(cmd *exec.Cmd, spec LaunchSpec) {
	if !spec.Detached() {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
