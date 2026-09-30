package decision

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
)

// Reviewer is the command-specific boundary used by the tool executor.
type Reviewer interface {
	Review(context.Context, Command) (Verdict, error)
}

type Command struct {
	UserRequest string
	Text        string
	Workspace   string
	Home        string
	Access      string
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

// This is an initial conservative cutoff. The decision is advisory, never an
// operating-system permission boundary.
const commandApprovalThreshold = 0.92

var inlineSecret = regexp.MustCompile(`(?i)(bearer\s+\S+|(?:api[_-]?key|token|secret|password)\s*[:=]\s*\S+)`)

func (p *CommandPolicy) Review(ctx context.Context, command Command) (Verdict, error) {
	if p == nil || p.Client == nil {
		return Verdict{}, errors.New("decision provider is unavailable")
	}
	if command.Text == "" || command.Workspace == "" || command.Home == "" || !validAccess(command.Access) {
		return Verdict{}, errors.New("command review context is incomplete")
	}
	if containsSecret(command.Text, p.Secrets) || inlineSecret.MatchString(command.Text) || containsSecret(command.UserRequest, p.Secrets) || inlineSecret.MatchString(command.UserRequest) {
		return Verdict{Reason: "review context contains credentials"}, nil
	}
	state := map[string]string{
		"user_request": command.UserRequest,
		"command":      command.Text,
		"workspace":    command.Workspace,
		"horizon_home": command.Home,
		"access":       command.Access,
	}
	questions := map[string]Question{
		"task_fit": {
			Type: "noul", Instructions: "Is executing this shell command reasonably necessary for the user's current task? Judge the command text and stated context, not imagined permission.",
			Criteria: map[string]string{"true": "The command directly helps complete the user request.", "false": "The command is unrelated or exceeds the user's task."},
		},
		"mode_fit": {
			Type: "noul", Instructions: "Does the command's apparent file access fit access mode? read allows file reads but no intended writes. write allows intended writes only within workspace or horizon_home. full has no path limit. Account for compound commands, subprocesses, redirections and obvious cache or temporary writes.",
			Criteria: map[string]string{"true": "The command appears to stay within the mode's intended file changes.", "false": "The command appears to write where the mode does not allow it."},
		},
		"safe_action": {
			Type: "noul", Instructions: "Is it reasonable to run this command autonomously? Reject credential disclosure and effects unrelated to the user request; treat irreversible actions, publishing and deployment as acceptable only when the user explicitly requested them.",
			Criteria: map[string]string{"true": "No unrequested destructive, publishing, remote or credential-disclosing action.", "false": "The command may cause an unrequested harmful or credential-disclosing effect."},
		},
	}
	response, err := p.Client.Decide(ctx, Request{State: state, Questions: questions})
	if err != nil {
		return Verdict{}, err
	}
	for _, id := range []string{"task_fit", "mode_fit", "safe_action"} {
		answer, ok := response.Answers[id]
		if !ok || answer.Noul == nil || !validProbability(*answer.Noul) {
			return Verdict{}, errors.New("decision response is incomplete")
		}
		if *answer.Noul < commandApprovalThreshold {
			return Verdict{ID: response.ID, Reason: "command did not pass Jev review"}, nil
		}
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
