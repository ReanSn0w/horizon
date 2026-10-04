package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/decision"
	flags "github.com/umputun/go-flags"
)

const usage = `Usage: horizon decision <request.json|-> [-o result.json]

Send state and questions to the Jev decision provider and return a JSON response.

Arguments:
  request.json    Path to a UTF-8 JSON request file.
  -               Read the request from stdin.
  -o result.json  Write the response to a file instead of stdout (atomic, mode 0600).
  -h, --help      Show this help without loading configuration or using the network.

Example request.json:
{
  "state": {
    "proposal": "Release version 1.2",
    "tests_passed": true,
    "open_blockers": 0
  },
  "questions": {
    "ready": {
      "type": "noul",
      "instructions": "Is the release ready, considering test results and blocking issues?",
      "criteria": {
        "true": "Tests passed and no blocking issues remain.",
        "false": "Tests failed or blocking issues remain."
      }
    }
  }
}

Examples:
  horizon decision request.json
  horizon decision request.json -o result.json
  cat request.json | horizon decision -
  horizon --home /path/to/home decision request.json

Request format:
  state      Data to evaluate, in any JSON format.
  questions  A non-empty object of questions with unique names (such as ready).
  Each question requires type and non-empty instructions.
  Types: noul — truth score from 0 to 1; choice — select an option;
  score — a numeric rating. Optional criteria are passed to the provider.
  Other top-level fields are not allowed. Input is limited to 32 KiB;
  the serialized request including the configured model must also fit this limit.

Configuration and output:
  URL, API key and model come from decision.provider.url, decision.provider.key
  and decision.model in the selected home/config.yaml. Request overrides are
  not supported.
  Home: Horizon's global --home, then HORIZON_HOME, then ~/.horizon.
  The response contains id, model, provider, answers and usage. The example's
  ready score is in answers.ready.noul. Without -o, the response goes to stdout;
  diagnostics go to stderr. The plugin does not create agent sessions.

Exit codes: 0 — success; 2 — invalid arguments, input or configuration;
            1 — provider or output error; 130 — cancellation.
`

const maxInputBytes = 32 * 1024

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(code int, err error) int { fmt.Fprintf(stderr, "decision: %s\n", err); return code }
	if len(args) == 1 && args[0] == "horizon-plugin-metadata" {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"protocol_version": 1, "config_protocol_version": 1, "version": "1.0.0", "description": "Evaluate structured JSON questions with the decision provider"}); err != nil {
			return fail(1, err)
		}
		return 0
	}
	if len(args) == 1 && args[0] == "horizon-plugin-config-template" {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"config_protocol_version": 1, "section": nil}); err != nil {
			return fail(1, err)
		}
		return 0
	}
	input, output, err := parseArgs(args)
	if err != nil {
		var flagErr *flags.Error
		if errors.As(err, &flagErr) && flagErr.Type == flags.ErrHelp {
			if _, err := io.WriteString(stdout, usage); err != nil {
				return fail(1, err)
			}
			return 0
		}
		return fail(2, err)
	}
	reader := stdin
	if input != "-" {
		f, err := os.Open(input)
		if err != nil {
			return fail(2, err)
		}
		defer f.Close()
		reader = f
	}
	request, err := readRequest(reader)
	if err != nil {
		return fail(2, err)
	}
	home, err := config.ResolveHome("")
	if err != nil {
		return fail(2, err)
	}
	cfg, err := config.Load(home)
	// Configuration errors can include user-controlled YAML values. Keep diagnostics
	// field-oriented without echoing credentials from the file.
	if err != nil {
		var setup *config.SetupError
		if errors.As(err, &setup) {
			return fail(2, setup)
		}
		return fail(2, errors.New("cannot load config.yaml; check configuration format and values"))
	}
	endpoint, err := cfg.DecisionEndpoint()
	if err != nil {
		return fail(2, err)
	}
	client := decision.Client{Endpoint: endpoint, APIKey: cfg.Decision.Provider.Key, Model: cfg.Decision.Model}
	response, err := client.Decide(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return fail(130, errors.New("cancelled"))
		}
		return fail(1, errors.New(strings.ReplaceAll(err.Error(), cfg.Decision.Provider.Key, "[redacted]")))
	}
	data, err := json.Marshal(response)
	if err != nil {
		return fail(1, err)
	}
	data = append(data, '\n')
	if output != "" {
		err = writeAtomic(output, data)
	} else {
		var n int
		n, err = stdout.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		return fail(1, err)
	}
	return 0
}

func parseArgs(args []string) (string, string, error) {
	var options struct {
		Output []string `short:"o" description:"Write the response to a file"`
	}
	parser := flags.NewNamedParser("horizon decision", flags.HelpFlag|flags.PassDoubleDash)
	if _, err := parser.AddGroup("Output", "", &options); err != nil {
		return "", "", err
	}
	positional, err := parser.ParseArgs(args)
	if err != nil {
		return "", "", err
	}
	if len(options.Output) > 1 || len(options.Output) == 1 && options.Output[0] == "" {
		return "", "", errors.New("-o requires one output path")
	}
	if len(positional) != 1 || positional[0] == "" {
		return "", "", errors.New("expected an input path or - for stdin")
	}
	if len(options.Output) == 1 {
		return positional[0], options.Output[0], nil
	}
	return positional[0], "", nil
}

func readRequest(reader io.Reader) (decision.Request, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxInputBytes+1))
	if err != nil {
		return decision.Request{}, err
	}
	if len(data) > maxInputBytes {
		return decision.Request{}, errors.New("decision request is too large")
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
		return request, errors.New("decision questions are required")
	}
	for id, q := range request.Questions {
		if id == "" || q.Instructions == "" || (q.Type != "noul" && q.Type != "choice" && q.Type != "score") {
			return request, errors.New("invalid decision question")
		}
	}
	return request, nil
}

func writeAtomic(path string, data []byte) error {
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
	return os.Rename(file.Name(), path)
}
