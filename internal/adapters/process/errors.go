package process

import "errors"

var ErrPTYUnsupported = errors.New("pty mode is not supported")
