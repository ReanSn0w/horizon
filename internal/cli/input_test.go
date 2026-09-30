package cli

import (
	"errors"
	"strings"
	"testing"
)

type testEditor struct {
	message string
	err     error
	calls   int
}

func (editor *testEditor) ReadMessage() (string, error) {
	editor.calls++
	return editor.message, editor.err
}

func TestReadMessagePriority(t *testing.T) {
	editor := &testEditor{message: "editor"}
	app := New(strings.NewReader("stdin"), &strings.Builder{}, &strings.Builder{})
	app.interactive = func() bool { return true }
	app.editor = editor

	message, err := app.readMessage(optionalString{Value: "explicit\n", Set: true})
	if err != nil || message != "explicit\n" {
		t.Fatalf("explicit message = %q, %v", message, err)
	}
	if editor.calls != 0 {
		t.Fatalf("editor called %d times for explicit message", editor.calls)
	}

	app.interactive = func() bool { return false }
	message, err = app.readMessage(optionalString{})
	if err != nil || message != "stdin" {
		t.Fatalf("stdin message = %q, %v", message, err)
	}
	if editor.calls != 0 {
		t.Fatalf("editor called %d times for redirected stdin", editor.calls)
	}

	app.interactive = func() bool { return true }
	message, err = app.readMessage(optionalString{})
	if err != nil || message != "editor" || editor.calls != 1 {
		t.Fatalf("editor message = %q, calls=%d, err=%v", message, editor.calls, err)
	}
}

func TestReadMessageErrors(t *testing.T) {
	app := New(strings.NewReader("ignored"), &strings.Builder{}, &strings.Builder{})
	if _, err := app.readMessage(optionalString{Set: true, Value: " \n\t"}); exitCode(err) != ExitUsage {
		t.Fatalf("empty explicit message code = %d, error=%v", exitCode(err), err)
	}

	app.interactive = func() bool { return true }
	app.editor = &testEditor{err: ErrInterrupted}
	if _, err := app.readMessage(optionalString{}); exitCode(err) != ExitInterrupted {
		t.Fatalf("interrupted editor code = %d, error=%v", exitCode(err), err)
	}

	app.editor = &testEditor{err: errors.New("terminal failed")}
	if _, err := app.readMessage(optionalString{}); exitCode(err) != ExitFailure {
		t.Fatalf("failed editor code = %d, error=%v", exitCode(err), err)
	}
}

func TestReadMessagePlain(t *testing.T) {
	editor := &testEditor{message: "unexpected"}
	app := New(strings.NewReader("pipe"), &strings.Builder{}, &strings.Builder{})
	app.editor = editor
	app.interactive = func() bool { return true }
	if _, err := app.readMessageMode(optionalString{}, "plain"); exitCode(err) != ExitUsage {
		t.Fatalf("error=%v", err)
	}
	if got, err := app.readMessageMode(optionalString{Set: true, Value: "explicit"}, "plain"); err != nil || got != "explicit" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	app.interactive = func() bool { return false }
	if got, err := app.readMessageMode(optionalString{}, "plain"); err != nil || got != "pipe" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if editor.calls != 0 {
		t.Fatal("plain opened editor")
	}
}
