package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/session"
)

func TestHostChannelRequiresExplicitExtension(t *testing.T) {
	t.Setenv(TurnChannelEnvironment, `{"extension":"telegram","route":"-10"}`)
	channel, err := ReadTurnChannel()
	if err != nil || channel.Extension != "telegram" || channel.Route != "-10" {
		t.Fatalf("channel = %#v, error = %v", channel, err)
	}
	_, err = PrepareForTurn(context.Background(), t.TempDir(), session.Workspace{}, "read", nil, true, nil, TurnPolicy{Channel: channel})
	if err == nil || !strings.Contains(err.Error(), "requires that extension") {
		t.Fatalf("missing extension error = %v", err)
	}
	if env := Environment([]string{TurnChannelEnvironment + "=secret"}, "/home", "horizon", "read"); len(env) != 3 {
		t.Fatalf("subprocess inherited host context: %v", env)
	}
	t.Setenv(TurnChannelEnvironment, `{"extension":"telegram","route":"-10","permission":true}`)
	if _, err = ReadTurnChannel(); err == nil {
		t.Fatal("unexpected host fields accepted")
	}
}

func TestCommunicationPermissionIsIndependentOfFileAccess(t *testing.T) {
	for _, access := range []string{"read", "write", "full"} {
		for _, allowed := range []bool{false, true} {
			request := Request{Access: access, Communication: allowed}
			if ToolAllowed(request, CommunicationEffect) != allowed {
				t.Errorf("communicate with access %s grant %t", access, allowed)
			}
			for _, effect := range []string{"write_home", "write_workspace"} {
				if ToolAllowed(request, effect) != (access != "read") {
					t.Errorf("%s with access %s grant %t", effect, access, allowed)
				}
			}
		}
	}
	_, err := PrepareForTurn(context.Background(), t.TempDir(), session.Workspace{}, "read", []string{"telegram"}, true, nil,
		TurnPolicy{Channel: &TurnChannel{Extension: "telegram", Route: "-10"}, Communication: []string{"other"}})
	if err == nil || !strings.Contains(err.Error(), "agent_communication") {
		t.Fatalf("ungranted route error = %v", err)
	}
}
