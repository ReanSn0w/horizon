package cli

import (
	"errors"
	"fmt"
)

const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitUsage       = 2
	ExitInterrupted = 130
)

// Error carries a process exit code without coupling handlers to os.Exit.
type Error struct {
	Code    int
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return fmt.Sprintf("horizon exited with code %d", e.Code)
}

func (e *Error) Unwrap() error { return e.Cause }

func failure(message string, cause error) error {
	return &Error{Code: ExitFailure, Message: message, Cause: cause}
}

func usage(message string, cause error) error {
	return &Error{Code: ExitUsage, Message: message, Cause: cause}
}

func exitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var commandError *Error
	if errors.As(err, &commandError) {
		return commandError.Code
	}
	return ExitFailure
}
