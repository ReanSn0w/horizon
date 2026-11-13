package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ReanSn0w/horizon/internal/decision"
)

const maxDecisionInputBytes = 32 * 1024

type decisionCommand struct {
	app    *App
	global *globalOptions
	Output []string `short:"o" description:"write the response atomically to a file"`
}

func (*decisionCommand) allowArguments() {}

func (command *decisionCommand) Execute(args []string) error {
	if len(command.Output) > 1 || len(command.Output) == 1 && command.Output[0] == "" {
		return usage("decision: -o requires one output path", nil)
	}
	if len(args) != 1 || args[0] == "" {
		return usage("decision: expected an input path or - for stdin", nil)
	}

	reader := command.app.in
	if args[0] != "-" {
		file, err := os.Open(args[0])
		if err != nil {
			return usage("decision: "+err.Error(), err)
		}
		defer file.Close()
		reader = file
	}
	request, err := readDecisionRequest(reader)
	if err != nil {
		return usage("decision: "+err.Error(), err)
	}
	_, cfg, err := command.app.loadConfig(command.global)
	if err != nil {
		return err
	}
	if err := cfg.RequireDecision(); err != nil {
		return usage("decision: "+err.Error(), err)
	}
	profile := cfg.Models[cfg.DecisionModel]
	endpoint, err := cfg.Endpoint(profile.Provider, "responses")
	if err != nil {
		return usage("decision: "+err.Error(), err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client := decision.Client{Endpoint: endpoint, APIKey: cfg.Providers[profile.Provider].Key, Model: profile.Model, HTTPClient: http.DefaultClient}
	response, err := client.Decide(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return &Error{Code: ExitInterrupted, Message: "decision: cancelled", Cause: ctx.Err()}
		}
		return failure("decision: "+strings.ReplaceAll(err.Error(), cfg.Providers[profile.Provider].Key, "[redacted]"), err)
	}
	data, err := json.Marshal(response)
	if err != nil {
		return failure("decision: "+err.Error(), err)
	}
	data = append(data, '\n')
	if len(command.Output) == 1 {
		err = writeDecisionAtomic(command.Output[0], data)
	} else {
		var n int
		n, err = command.app.out.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		return failure("decision: "+err.Error(), err)
	}
	return nil
}

func readDecisionRequest(reader io.Reader) (decision.Request, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxDecisionInputBytes+1))
	if err != nil {
		return decision.Request{}, err
	}
	if len(data) > maxDecisionInputBytes {
		return decision.Request{}, errors.New("request is too large")
	}
	if !utf8.Valid(data) {
		return decision.Request{}, errors.New("request must be UTF-8 JSON")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return decision.Request{}, errors.New("request must be a JSON object")
	}
	var request decision.Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("invalid request JSON; expected state and questions")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return request, errors.New("request contains trailing data")
	}
	if len(request.Questions) == 0 {
		return request, errors.New("questions are required")
	}
	for id, question := range request.Questions {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(question.Instructions) == "" {
			return request, errors.New("invalid question")
		}
	}
	return request, nil
}

func writeDecisionAtomic(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".horizon-decision-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Chmod(file.Name(), 0600); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func decisionLongDescription() string {
	return fmt.Sprintf(`Evaluate structured JSON with the configured decision provider.

Input is one UTF-8 JSON object containing state and a non-empty questions object.
Each question has instructions and returns a boolean answer. Input is limited to %d KiB.
Use - for stdin and -o PATH to write the JSON response atomically.`, maxDecisionInputBytes/1024)
}
