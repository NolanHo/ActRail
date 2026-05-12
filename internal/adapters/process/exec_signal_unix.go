//go:build unix

package process

import (
	"os"
	"syscall"
)

func signalProcess(proc *os.Process, spec LaunchSpec, sig os.Signal) error {
	if proc == nil {
		return os.ErrProcessDone
	}
	if spec.Detached() {
		sysSig, ok := sig.(syscall.Signal)
		if !ok {
			return proc.Signal(sig)
		}
		return syscall.Kill(-proc.Pid, sysSig)
	}
	return proc.Signal(sig)
}

func killProcess(proc *os.Process, spec LaunchSpec) error {
	return signalProcess(proc, spec, os.Kill)
}

func cleanupProcess(proc *os.Process, spec LaunchSpec) {
	if proc == nil || !spec.Detached() {
		return
	}
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return
	}
}
