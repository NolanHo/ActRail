//go:build !unix

package process

import "os/exec"

func applyProcessAttrs(_ *exec.Cmd, _ LaunchSpec) {}
