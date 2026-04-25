package ws

import (
	"errors"
	"fmt"
	"strings"
)

// CommandTarget handles client-originated session control commands after frame validation.
type CommandTarget interface {
	HandleSend(SendCommand) error
	HandleEnqueue(EnqueueCommand) error
	HandleInterrupt(InterruptCommand) error
	HandleUIResponse(UIResponseCommand) error
}

// CommandError maps command-target failures onto websocket error frames.
type CommandError struct {
	Code    ErrorCode
	Message string
	Field   string
	Err     error
}

func NewCommandError(code ErrorCode, message, field string) *CommandError {
	return &CommandError{Code: code, Message: message, Field: field}
}

func WrapCommandError(code ErrorCode, message, field string, err error) *CommandError {
	return &CommandError{Code: code, Message: message, Field: field, Err: err}
}

func (e *CommandError) Error() string {
	if e == nil {
		return ""
	}
	if msg := strings.TrimSpace(e.Message); msg != "" {
		return msg
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Code)
}

func (e *CommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func normalizeCommandError(err error) (ErrorCode, string, string) {
	if err == nil {
		return "", "", ""
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) {
		code := commandErr.Code
		if !validCommandErrorCode(code) {
			return ErrorCodeInternal, fmt.Sprintf("command target returned unsupported error code %q", code), commandErr.Field
		}
		msg := strings.TrimSpace(commandErr.Message)
		if msg == "" {
			msg = err.Error()
		}
		return code, msg, commandErr.Field
	}
	return ErrorCodeInternal, err.Error(), ""
}

func validCommandErrorCode(code ErrorCode) bool {
	switch code {
	case ErrorCodeUnauthorized,
		ErrorCodeNotFound,
		ErrorCodeInvalidRequest,
		ErrorCodeConflict,
		ErrorCodeUnsupported,
		ErrorCodeInternal,
		ErrorCodeTransportResetRequired:
		return true
	default:
		return false
	}
}
