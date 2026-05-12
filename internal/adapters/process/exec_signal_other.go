//go:build !unix

package process

import "os"

func signalProcess(proc *os.Process, _ LaunchSpec, sig os.Signal) error {
	if proc == nil {
		return os.ErrProcessDone
	}
	return proc.Signal(sig)
}

func killProcess(proc *os.Process, spec LaunchSpec) error {
	return signalProcess(proc, spec, os.Kill)
}

func cleanupProcess(_ *os.Process, _ LaunchSpec) {}
