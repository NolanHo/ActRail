//go:build unix

package app

func preferRuntimePTY() bool {
	return true
}
