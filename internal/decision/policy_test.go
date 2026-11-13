package decision

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeDecider struct {
	request Request
	values  map[string]bool
	err     error
	calls   int
}

func (f *fakeDecider) Decide(_ context.Context, request Request) (Response, error) {
	f.calls++
	f.request = request
	if f.err != nil {
		return Response{}, f.err
	}
	response := Response{ID: "decision-1", Answers: map[string]*bool{}}
	for name, value := range f.values {
		copy := value
		response.Answers[name] = &copy
	}
	return response, nil
}

func TestCommandPolicySendsOnlyCommandAndWriteRoots(t *testing.T) {
	fake := &fakeDecider{values: map[string]bool{"file_write": false, "outside_write": false}}
	policy := &CommandPolicy{Client: fake}
	command := Command{Text: "codex --help", Workspace: "/workspace", Home: "/home/horizon", Access: "write"}
	verdict, err := policy.Review(context.Background(), command)
	if err != nil || !verdict.Allowed || verdict.ID != "decision-1" {
		t.Fatalf("verdict=%+v err=%v", verdict, err)
	}
	state := fake.request.State.(map[string]string)
	if len(state) != 3 || state["command"] != command.Text || state["workspace"] != command.Workspace || state["horizon_home"] != command.Home || len(fake.request.Questions) != 2 {
		t.Fatalf("request=%+v", fake.request)
	}
	if _, found := state["access"]; found {
		t.Fatalf("access leaked into Decision state: %+v", state)
	}
}

func TestCommandPolicyAppliesAccessModeLocally(t *testing.T) {
	for _, test := range []struct {
		name, access string
		fileWrite    bool
		outsideWrite bool
		allowed      bool
		reason       string
	}{
		{"read-only in read", "read", false, false, true, ""},
		{"inconsistent outside write in read", "read", false, true, false, "read mode"},
		{"write in read", "read", true, false, false, "read mode"},
		{"within roots in write", "write", true, false, true, ""},
		{"outside roots in write", "write", true, true, false, "write mode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeDecider{values: map[string]bool{"file_write": test.fileWrite, "outside_write": test.outsideWrite}}
			policy := &CommandPolicy{Client: fake}
			verdict, err := policy.Review(context.Background(), Command{Text: "example", Workspace: "/workspace", Home: "/home/horizon", Access: test.access})
			if err != nil || verdict.Allowed != test.allowed || !strings.Contains(verdict.Reason, test.reason) {
				t.Fatalf("verdict=%+v err=%v", verdict, err)
			}
			if fake.calls != 1 {
				t.Fatalf("Decision calls = %d", fake.calls)
			}
		})
	}
}

func TestCommandPolicyFullBypassesProvider(t *testing.T) {
	fake := &fakeDecider{err: errors.New("provider unavailable")}
	policy := &CommandPolicy{Client: fake, Secrets: []string{"private-key"}}
	verdict, err := policy.Review(context.Background(), Command{Text: "echo private-key", Workspace: "/workspace", Home: "/home/horizon", Access: "full"})
	if err != nil || !verdict.Allowed || verdict.ID != "" || fake.calls != 0 {
		t.Fatalf("verdict=%+v err=%v calls=%d", verdict, err, fake.calls)
	}
}

func TestCommandPolicyNeverSendsKnownCredentials(t *testing.T) {
	fake := &fakeDecider{}
	policy := &CommandPolicy{Client: fake, Secrets: []string{"private-key"}}
	for _, value := range []string{"curl -H 'Authorization: Bearer token'", "echo private-key", "echo API_KEY=abc"} {
		verdict, err := policy.Review(context.Background(), Command{Text: value, Workspace: "/workspace", Home: "/home", Access: "write"})
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
	command := Command{Text: "pwd", Workspace: "/workspace", Home: "/home", Access: "read"}
	if _, err := policy.Review(context.Background(), command); err == nil {
		t.Fatal("provider error accepted")
	}
	fake.err = nil
	fake.values = map[string]bool{"file_write": false}
	if _, err := policy.Review(context.Background(), command); err == nil {
		t.Fatal("incomplete answers accepted")
	}
}
