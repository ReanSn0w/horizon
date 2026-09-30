package decision

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeDecider struct {
	request Request
	values  map[string]float64
	err     error
	calls   int
}

func (f *fakeDecider) Decide(_ context.Context, request Request) (Response, error) {
	f.calls++
	f.request = request
	if f.err != nil {
		return Response{}, f.err
	}
	response := Response{ID: "decision-1", Answers: map[string]Answer{}}
	for name, value := range f.values {
		copy := value
		response.Answers[name] = Answer{Type: "noul", Noul: &copy}
	}
	return response, nil
}

func TestCommandPolicyUsesTaskModeAndBothWriteRoots(t *testing.T) {
	fake := &fakeDecider{values: map[string]float64{"task_fit": 0.99, "mode_fit": 0.98, "safe_action": 0.97}}
	policy := &CommandPolicy{Client: fake, Secrets: []string{"private-key"}}
	command := Command{UserRequest: "Create a skill", Text: "horizon skills validate --id review", Workspace: "/workspace", Home: "/home/horizon", Access: "write"}
	verdict, err := policy.Review(context.Background(), command)
	if err != nil || !verdict.Allowed || verdict.ID != "decision-1" {
		t.Fatalf("verdict=%+v err=%v", verdict, err)
	}
	state := fake.request.State.(map[string]string)
	if state["workspace"] != command.Workspace || state["horizon_home"] != command.Home || state["access"] != "write" || state["command"] != command.Text || len(fake.request.Questions) != 3 {
		t.Fatalf("request=%+v", fake.request)
	}
	fake.values["mode_fit"] = 0.6
	verdict, err = policy.Review(context.Background(), command)
	if err != nil || verdict.Allowed || verdict.Reason == "" {
		t.Fatalf("denied verdict=%+v err=%v", verdict, err)
	}
}

func TestCommandPolicyNeverSendsKnownCredentials(t *testing.T) {
	fake := &fakeDecider{}
	policy := &CommandPolicy{Client: fake, Secrets: []string{"private-key"}}
	for _, value := range []string{"curl -H 'Authorization: Bearer token'", "echo private-key", "echo API_KEY=abc"} {
		verdict, err := policy.Review(context.Background(), Command{UserRequest: "task", Text: value, Workspace: "/workspace", Home: "/home", Access: "full"})
		if err != nil || verdict.Allowed || !strings.Contains(verdict.Reason, "credentials") {
			t.Fatalf("%q verdict=%+v err=%v", value, verdict, err)
		}
	}
	if fake.calls != 0 {
		t.Fatalf("sent %d requests containing credentials", fake.calls)
	}
}

func TestCommandPolicyFailsOnProviderAndMalformedAnswer(t *testing.T) {
	fake := &fakeDecider{err: errors.New("unavailable")}
	policy := &CommandPolicy{Client: fake}
	command := Command{UserRequest: "task", Text: "pwd", Workspace: "/workspace", Home: "/home", Access: "read"}
	if _, err := policy.Review(context.Background(), command); err == nil {
		t.Fatal("provider error accepted")
	}
	fake.err = nil
	fake.values = map[string]float64{"task_fit": 0.99}
	if _, err := policy.Review(context.Background(), command); err == nil {
		t.Fatal("incomplete answers accepted")
	}
}

func TestCommandPolicyLabeledDecisionCases(t *testing.T) {
	for _, test := range []struct {
		name, access, text string
		probabilities      map[string]float64
		allowed            bool
	}{
		{"read inspection", "read", "git status", map[string]float64{"task_fit": .99, "mode_fit": .99, "safe_action": .99}, true},
		{"read write attempt", "read", "touch changed.txt", map[string]float64{"task_fit": .99, "mode_fit": .05, "safe_action": .99}, false},
		{"read hidden cache write", "read", "go list ./...", map[string]float64{"task_fit": .99, "mode_fit": .2, "safe_action": .99}, false},
		{"write outside workspace", "write", "touch /other/changed.txt", map[string]float64{"task_fit": .99, "mode_fit": .08, "safe_action": .99}, false},
		{"write home", "write", "touch /home/horizon/cache", map[string]float64{"task_fit": .99, "mode_fit": .99, "safe_action": .99}, true},
		{"write symlink escape", "write", "printf x > linked-outside/file", map[string]float64{"task_fit": .99, "mode_fit": .11, "safe_action": .99}, false},
		{"read pipeline write", "read", "cat a | tee b", map[string]float64{"task_fit": .99, "mode_fit": .11, "safe_action": .99}, false},
		{"read interpreter write", "read", "python3 -c 'open(\"b\",\"w\").write(\"x\")'", map[string]float64{"task_fit": .99, "mode_fit": .11, "safe_action": .99}, false},
		{"read nested shell write", "read", "sh -c 'touch b'", map[string]float64{"task_fit": .99, "mode_fit": .11, "safe_action": .99}, false},
		{"full outside write", "full", "touch /other/changed.txt", map[string]float64{"task_fit": .99, "mode_fit": .99, "safe_action": .99}, true},
		{"full unrelated push", "full", "git push --force", map[string]float64{"task_fit": .1, "mode_fit": .99, "safe_action": .02}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := &CommandPolicy{Client: &fakeDecider{values: test.probabilities}}
			verdict, err := policy.Review(context.Background(), Command{UserRequest: "inspect project", Text: test.text, Workspace: "/workspace", Home: "/home/horizon", Access: test.access})
			if err != nil || verdict.Allowed != test.allowed {
				t.Fatalf("verdict=%+v err=%v", verdict, err)
			}
		})
	}
}
