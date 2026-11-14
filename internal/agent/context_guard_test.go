package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/instructions"
	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
)

func TestLocalSummaryKeepsLatestUserInputAndToolSuffix(t *testing.T) {
	prior := responses.UserMessage("Earlier context")
	latest := responses.UserMessage("Inspect this photo now")
	call := json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"skill_read","arguments":"{}"}`)
	result := json.RawMessage(`{"type":"function_call_output","call_id":"call-1","output":"done"}`)
	summary := json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"Prior context was about a photo."}]}`)
	client := &fakeClient{outputs: [][]json.RawMessage{{summary}}}
	runtime := &Runtime{Client: client, Profile: session.ModelProfile{Model: "fixture"}, Instructions: &instructions.Snapshot{Prompt: "Send visible replies through Telegram."}}
	input := []json.RawMessage{prior, latest, call, result}
	window, compact, _, unsupported, err := runtime.compactWindow(context.Background(), input, func() error { return nil }, true, func(*responses.Response, error) {})
	if err != nil || !unsupported || compact == nil || !strings.HasPrefix(compact.ID, "local-") {
		t.Fatalf("local compaction = %+v, unsupported=%t, err=%v", compact, unsupported, err)
	}
	if len(client.requests) != 1 || len(client.requests[0].Input) != 1 || !bytes.Equal(client.requests[0].Input[0], prior) {
		t.Fatalf("summary request included the current user input: %+v", client.requests)
	}
	if len(window) != 4 || !bytes.Equal(window[1], latest) || !bytes.Equal(window[2], call) || !bytes.Equal(window[3], result) {
		t.Fatalf("latest input or tool suffix changed: %s", window)
	}
	if !bytes.Contains(window[0], []byte("Prior context was about a photo")) {
		t.Fatalf("missing summary: %s", window[0])
	}
}

func TestLocalSummaryKeepsRecentAttachmentAndFollowups(t *testing.T) {
	old := responses.UserMessage("Old dialogue")
	photo := responses.UserMessage(`{"attachments":[{"kind":"photo","state":"ready","path":"/workspace/attachments/photo.jpg"}]}`)
	followup := responses.UserMessage("Can you see it now?")
	current := responses.UserMessage("What about the photo?")
	input := []json.RawMessage{old, responses.UserMessage("Earlier question"), photo, followup, current}
	prior, recent := splitLocalSummaryInput(input)
	if len(prior) != 2 || len(recent) != 3 || !bytes.Equal(recent[0], photo) || !bytes.Equal(recent[2], current) {
		t.Fatalf("recent photo or followups lost: prior=%d recent=%d", len(prior), len(recent))
	}
}
