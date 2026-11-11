// Package daemon implements the local Horizon supervisor protocol.
package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"time"
)

const Version = 1
const MaxFrame = 1024 * 1024

const (
	ActiveInputCapability = "active-input"
	ActiveDelivery        = "active"
	MaxInputBytes         = 128 * 1024
	MaxPendingInputs      = 32
)

// InputReceipt reports persistence separately from inclusion in a Horizon turn.
// It deliberately carries no input text or author.
type InputReceipt struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
	Source   string `json:"source"`
	Route    string `json:"route"`
	Status   string `json:"status"`
	Session  string `json:"session_id,omitempty"`
	Turn     string `json:"turn_id,omitempty"`
}

type Caller struct {
	Kind       string `json:"kind"`
	Source     string `json:"source"`
	Workspace  string `json:"workspace"`
	Session    string `json:"session"`
	Access     string `json:"access"`
	Credential string `json:"credential,omitempty"`
}
type Request struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Method      string `json:"method"`
	OperationID string `json:"operation_id,omitempty"`
	Caller      Caller `json:"caller"`
	TaskID      string `json:"task_id,omitempty"`
	Message     string `json:"message,omitempty"`
	Access      string `json:"access,omitempty"`
	Cursor      uint64 `json:"cursor,omitempty"`
	Offset      int    `json:"offset,omitempty"`
	WaitMS      int    `json:"wait_ms,omitempty"`
	Tree        bool   `json:"tree,omitempty"`
	Route       string `json:"route,omitempty"`
	Delivery    string `json:"delivery,omitempty"`
	Author      string `json:"author,omitempty"`
}
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Failure) Error() string { return e.Code + ": " + e.Message }

type Response struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *Failure        `json:"error,omitempty"`
}

func reply(r Request, v any, err error) Response {
	out := Response{Version: Version, ID: r.ID, OK: err == nil}
	if err != nil {
		var f *Failure
		if errors.As(err, &f) {
			out.Error = f
		} else {
			out.Error = &Failure{"internal_error", "daemon operation failed"}
		}
	} else {
		out.Data, _ = json.Marshal(v)
	}
	return out
}
func fail(code, message string) error { return &Failure{code, message} }
func Socket(home string) string       { return filepath.Join(home, "run", "daemon.sock") }
func frame(r io.Reader, v any) error {
	b, err := bufio.NewReader(io.LimitReader(r, MaxFrame+1)).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("incomplete JSON frame: %w", err)
	}
	if len(b) > MaxFrame {
		return errors.New("frame too large")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("extra JSON data")
	}
	return nil
}

// Call never retries. Once bytes may have been sent a transport error is unknown.
func Call(ctx context.Context, home string, r Request) (Response, error) {
	r.Version = Version
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", Socket(home))
	if err != nil {
		return Response{}, fail("daemon_unavailable", "cannot connect to daemon")
	}
	defer c.Close()
	deadline := time.Now().Add(35 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)
	done := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer done()
	if err = json.NewEncoder(c).Encode(r); err != nil {
		return Response{}, fail("outcome_unknown", "connection lost; look up operation_id before repeating")
	}
	var out Response
	if err = frame(c, &out); err != nil {
		return out, fail("outcome_unknown", "invalid or missing response; look up operation_id before repeating")
	}
	if out.Version != Version || out.ID != r.ID {
		return out, fail("protocol_mismatch", "incompatible daemon response")
	}
	if !out.OK {
		if out.Error == nil {
			return out, fail("protocol_mismatch", "missing error")
		}
		return out, out.Error
	}
	return out, nil
}
func Hello(ctx context.Context, home string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := Call(ctx, home, Request{ID: "hello", Method: "hello"})
	return err
}
