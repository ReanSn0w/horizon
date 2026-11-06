package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var ErrInterrupted = errors.New("input interrupted")

// MessageEditor supplies one complete message when stdin is interactive.
// The terminal implementation is connected separately from input selection.
type MessageEditor interface {
	ReadMessage() (string, error)
}

func (a *App) readMessage(explicit optionalString) (string, error) {
	return a.readMessageMode(explicit, "text")
}

func (a *App) readMessageMode(explicit optionalString, mode string) (string, error) {
	var (
		message string
		err     error
	)
	switch {
	case explicit.Set:
		message = explicit.Value
	case !a.interactive():
		var data []byte
		data, err = io.ReadAll(a.in)
		message = string(data)
	case mode == "plain":
		return "", usage("plain mode requires -m or redirected stdin", nil)
	case a.editor != nil:
		message, err = a.editor.ReadMessage()
	default:
		return "", failure("interactive message editor is not available yet; use -m or redirected stdin", nil)
	}
	if err != nil {
		if errors.Is(err, ErrInterrupted) {
			return "", &Error{Code: ExitInterrupted, Message: "input interrupted", Cause: err}
		}
		return "", failure(fmt.Sprintf("read message: %v", err), err)
	}
	if strings.TrimSpace(message) == "" {
		return "", usage("message must not be empty", nil)
	}
	return message, nil
}

func isInteractive(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
