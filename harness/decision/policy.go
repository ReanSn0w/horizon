package decision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Reviewer is the command-specific boundary used by the tool executor.
type Reviewer interface {
	Review(context.Context, Command) (Verdict, error)
}

type Command struct {
	Text      string
	Workspace string
	Home      string
	Access    string
}

type Verdict struct {
	Allowed bool
	ID      string
	Reason  string
}

type Decider interface {
	Decide(context.Context, Request) (Response, error)
}

type CommandPolicy struct {
	Client  Decider
	Secrets []string
}

// These advisory model scores are not operating-system permission boundaries.
const (
	maxWriteProbabilityInRead    = 0.20
	maxOutsideProbabilityInWrite = 0.20
)

var inlineSecret = regexp.MustCompile(`(?i)(bearer\s+\S+|(?:api[_-]?key|token|secret|password)\s*[:=]\s*\S+)`)

func (p *CommandPolicy) Review(ctx context.Context, command Command) (Verdict, error) {
	if command.Text == "" || command.Workspace == "" || command.Home == "" || !validAccess(command.Access) {
		return Verdict{}, errors.New("command review context is incomplete")
	}
	if command.Access == "full" {
		return Verdict{Allowed: true}, nil
	}
	if p == nil || p.Client == nil {
		return Verdict{}, errors.New("decision provider is unavailable")
	}
	if containsSecret(command.Text, p.Secrets) || inlineSecret.MatchString(command.Text) {
		return Verdict{Reason: "review context contains credentials"}, nil
	}
	state := map[string]string{
		"command":      command.Text,
		"workspace":    command.Workspace,
		"horizon_home": command.Home,
	}
	questions := map[string]Question{
		"file_write": {
			Type: "noul", Instructions: "Does `command` apparently intend to create, modify, or delete local filesystem content? Count explicit writes through subprocesses, shell redirections, scripts, and clear cache or temporary writes. Do not infer a write from ordinary inspection, listing, or help commands. Judge intended local file effects, not remote effects.",
			Criteria: map[string]string{"true": "The command apparently writes, modifies, or deletes local filesystem content.", "false": "The command is read-only or has no apparent local file write."},
		},
		"outside_write": {
			Type: "noul", Instructions: "Does `command` apparently intend to write, modify, or delete local filesystem content outside both `workspace` and `horizon_home`? Relative paths start in `workspace`. Include explicit subprocesses, redirections, and clear cache or temporary writes. A read-only command has no outside write. Judge local file paths only, not remote effects.",
			Criteria: map[string]string{"true": "The command apparently writes outside both allowed directory trees.", "false": "All apparent local file writes remain within the allowed directory trees, or there is no apparent local file write."},
		},
	}
	response, err := p.Client.Decide(ctx, Request{State: state, Questions: questions})
	if err != nil {
		return Verdict{}, err
	}
	for _, id := range []string{"file_write", "outside_write"} {
		answer, ok := response.Answers[id]
		if !ok || answer.Noul == nil || !validProbability(*answer.Noul) {
			return Verdict{}, errors.New("decision response is incomplete")
		}
	}
	fileWrite := *response.Answers["file_write"].Noul
	outsideWrite := *response.Answers["outside_write"].Noul
	if command.Access == "read" && fileWrite > maxWriteProbabilityInRead {
		return Verdict{ID: response.ID, Reason: fmt.Sprintf("read mode: possible file write (Jev score %.2f)", fileWrite)}, nil
	}
	if command.Access == "write" && outsideWrite > maxOutsideProbabilityInWrite {
		return Verdict{ID: response.ID, Reason: fmt.Sprintf("write mode: possible file write outside workspace or Horizon home (Jev score %.2f)", outsideWrite)}, nil
	}
	return Verdict{Allowed: true, ID: response.ID}, nil
}

func validAccess(value string) bool {
	return value == "read" || value == "write" || value == "full"
}

func containsSecret(command string, known []string) bool {
	for _, value := range known {
		if len(value) >= 4 && strings.Contains(command, value) {
			return true
		}
	}
	for _, pair := range os.Environ() {
		name, value, found := strings.Cut(pair, "=")
		if !found || len(value) < 4 {
			continue
		}
		upper := strings.ToUpper(name)
		if (strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL")) && strings.Contains(command, value) {
			return true
		}
	}
	return false
}
