package cli

import (
	"reflect"
	"testing"
)

func TestDefaultResumeArgs(t *testing.T) {
	for _, tt := range []struct{ args, want []string }{
		{nil, []string{"resume"}},
		{[]string{"--home", "models"}, []string{"--home", "models", "resume"}},
		{[]string{"--home=/tmp/home", "-v", "-m", "models"}, []string{"--home=/tmp/home", "-v", "resume", "-m", "models"}},
		{[]string{"--message=resume"}, []string{"resume", "--message=resume"}},
		{[]string{"--model", "chatting"}, []string{"resume", "--model", "chatting"}},
		{[]string{"--access", "read", "-m", "inspect"}, []string{"resume", "--access", "read", "-m", "inspect"}},
		{[]string{"--", "resume"}, []string{"resume", "--", "resume"}},
		{[]string{"--help"}, []string{"--help"}},
		{[]string{"--home", "x", "--help"}, []string{"--home", "x", "--help"}},
		{[]string{"resume", "--help"}, []string{"resume", "--help"}},
		{[]string{"unknown"}, []string{"unknown"}},
		{[]string{"sessions", "list"}, []string{"sessions", "list"}},
		{[]string{"--home"}, []string{"--home"}},
	} {
		if got := defaultResumeArgs(tt.args); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: got %q, want %q", tt.args, got, tt.want)
		}
	}
}
