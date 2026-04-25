//go:build !unix

package process

import "os"

func signalName(state *os.ProcessState) string {
	return ""
}
