package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ReanSn0w/horizon/internal/turninput"
)

const (
	inputTaskEnv  = "HORIZON_INPUT_TASK"
	inputRouteEnv = "HORIZON_INPUT_ROUTE"
	inputFD       = 3
	inputTokenLen = 32
)

func inputCredentialPipe(token string) (*os.File, error) {
	if len(token) != inputTokenLen {
		return nil, errors.New("active input credential is missing")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	_, err = io.WriteString(writer, token)
	closeErr := writer.Close()
	if err != nil || closeErr != nil {
		reader.Close()
		return nil, errors.New("cannot pass the active input credential")
	}
	return reader, nil
}

type inputClient struct {
	home   string
	task   string
	route  string
	caller Caller
}

func HasTurnInput() bool {
	_, present := os.LookupEnv(inputTaskEnv)
	return present
}

// OpenTurnInput consumes the inherited descriptor before any agent tools or
// extensions can inherit it. Standalone resume has no input environment.
func OpenTurnInput(home, workspace, session string) (turninput.Channel, error) {
	task, present := os.LookupEnv(inputTaskEnv)
	if !present {
		return nil, nil
	}
	route := os.Getenv(inputRouteEnv)
	_ = os.Unsetenv(inputTaskEnv)
	_ = os.Unsetenv(inputRouteEnv)
	descriptor := os.NewFile(inputFD, "daemon-input")
	if descriptor == nil {
		return nil, errors.New("active input descriptor is missing")
	}
	defer descriptor.Close()
	info, err := descriptor.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 || !sessionID.MatchString(task) || session == "" || workspace == "" || route == "" {
		return nil, errors.New("invalid active input context")
	}
	token, err := io.ReadAll(io.LimitReader(descriptor, inputTokenLen+1))
	if err != nil || len(token) != inputTokenLen {
		return nil, errors.New("cannot read the active input credential")
	}
	return &inputClient{home: home, task: task, route: route, caller: Caller{
		Kind: "turn_input", Credential: string(token), Workspace: workspace, Session: session,
	}}, nil
}

func (c *inputClient) Next(ctx context.Context, turnID string, closing bool) (*turninput.Message, error) {
	method := "input_next"
	if closing {
		method = "input_close"
	}
	response, err := c.call(ctx, method, "", turnID)
	if err != nil {
		return nil, fmt.Errorf("read active input: %w", err)
	}
	var message *turninput.Message
	if err := json.Unmarshal(response.Data, &message); err != nil {
		return nil, errors.New("invalid active input response")
	}
	return message, nil
}

func (c *inputClient) Acknowledge(ctx context.Context, inputID, turnID string) error {
	if _, err := c.call(ctx, "input_ack", inputID, turnID); err != nil {
		return fmt.Errorf("acknowledge active input: %w", err)
	}
	return nil
}

func (c *inputClient) call(ctx context.Context, method, inputID, turnID string) (Response, error) {
	return Call(ctx, c.home, Request{ID: newID(), Method: method, Caller: c.caller,
		TaskID: c.task, Route: c.route, TurnID: turnID, InputID: inputID})
}
