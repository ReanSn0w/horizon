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
)

const usage = "Usage: horizon decision <request.json|-> [-o result.json]\n"
const maxInputBytes = 32 * 1024

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(code int, err error) int { fmt.Fprintf(stderr, "decision: %s\n", err); return code }
	if len(args) == 1 && args[0] == "horizon-plugin-metadata" {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"protocol_version": 1, "version": "1.0.0", "description": "Evaluate structured JSON questions with the decision provider"}); err != nil {
			return fail(1, err)
		}
		return 0
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		if _, err := io.WriteString(stdout, usage); err != nil {
			return fail(1, err)
		}
		return 0
	}
	input, output, err := parseArgs(args)
	if err != nil {
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
	var input, output string
	positional := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !positional && arg == "--" {
			positional = true
			continue
		}
		if !positional && arg == "-o" {
			if output != "" || i+1 == len(args) || args[i+1] == "" {
				return "", "", errors.New("-o requires one output path")
			}
			i++
			output = args[i]
			continue
		}
		if !positional && strings.HasPrefix(arg, "-") && arg != "-" {
			return "", "", fmt.Errorf("unknown option %q", arg)
		}
		if input != "" || arg == "" {
			return "", "", errors.New("expected exactly one input path")
		}
		input = arg
	}
	if input == "" {
		return "", "", errors.New("expected an input path or - for stdin")
	}
	return input, output, nil
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
