package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReanSn0w/horizon/internal/decision"
	"github.com/ReanSn0w/horizon/internal/eventstream"
	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
	"github.com/ReanSn0w/horizon/internal/turninput"
)

type inboundFixture struct {
	pending []turninput.Message
	acked   map[string]string
	closed  bool
	failAck bool
}

func newInboundFixture(messages ...turninput.Message) *inboundFixture {
	return &inboundFixture{pending: messages, acked: map[string]string{}}
}

func (i *inboundFixture) add(t *testing.T, message turninput.Message) {
	t.Helper()
	if i.closed {
		t.Fatal("turn closed before all provider responses")
	}
	i.pending = append(i.pending, message)
}

func (i *inboundFixture) Next(ctx context.Context, _ string, closing bool) (*turninput.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(i.pending) > 0 {
		return &i.pending[0], nil
	}
	i.closed = i.closed || closing
	return nil, nil
}

func (i *inboundFixture) Acknowledge(_ context.Context, id, turn string) error {
	if i.failAck {
		i.failAck = false
		return errors.New("acknowledgment lost")
	}
	if len(i.pending) == 0 || i.pending[0].ID != id {
		return errors.New("unexpected input acknowledgment")
	}
	i.acked[id] = turn
	i.pending = i.pending[1:]
	return nil
}

type streamFunc func(context.Context, responses.Request) (*responses.Response, error)

func (f streamFunc) Stream(ctx context.Context, request responses.Request, before func() error, _ func(responses.Event)) (*responses.Response, error) {
	if err := before(); err != nil {
		return nil, err
	}
	return f(ctx, request)
}

type reviewFunc func(context.Context, decision.Command) (decision.Verdict, error)

func (f reviewFunc) Review(ctx context.Context, command decision.Command) (decision.Verdict, error) {
	return f(ctx, command)
}

func finalResponse(text string) *responses.Response {
	return &responses.Response{Status: "completed", Output: []json.RawMessage{
		json.RawMessage(fmt.Sprintf(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}`, text)),
	}}
}

func TestActiveInputBetweenStreamToolAndFinalization(t *testing.T) {
	in := newInboundFixture(journalInput("initial", 1, "sender", "initial input"))
	var requests []responses.Request
	client := streamFunc(func(_ context.Context, request responses.Request) (*responses.Response, error) {
		requests = append(requests, request)
		switch len(requests) {
		case 1:
			in.add(t, journalInput("during-stream", 2, "sender two", "during SSE"))
			return &responses.Response{Status: "completed", Output: []json.RawMessage{
				json.RawMessage(`{"type":"function_call","call_id":"check","name":"shell_exec","arguments":"{\"command\":\"printf inspected\",\"timeout_ms\":null,\"max_output_chars\":null}"}`),
			}}, nil
		case 2:
			in.add(t, journalInput("before-close", 4, "sender four", "arrived before close"))
		case 3:
			in.add(t, journalInput("followup", 5, "sender five", "arrived during continuation"))
		default:
			return finalResponse("final answer"), nil
		}
		return finalResponse("draft answer"), nil
	})
	runtime, cleanup := testRuntime(t, client)
	defer cleanup()
	runtime.Inbound = in
	runtime.Reviewer = reviewFunc(func(_ context.Context, _ decision.Command) (decision.Verdict, error) {
		in.add(t, journalInput("during-tool", 3, "sender three", "during tool"))
		if len(in.acked) != 1 {
			t.Fatalf("input was acknowledged before the tool finished: %+v", in.acked)
		}
		return decision.Verdict{Allowed: true}, nil
	})
	inputEvents := 0
	runtime.Publish = func(event eventstream.Event) {
		if event.Type == "input_received" || event.Type == "input_included" {
			inputEvents++
			fields := event.Data.(map[string]any)
			if len(fields) != 2 || fields["input_id"] == nil || fields["sequence"] == nil {
				t.Fatalf("input event exposes message content: %+v", event)
			}
		}
	}
	result, err := runtime.Run(context.Background(), "")
	if err != nil || result.Text != "final answer" || len(requests) != 4 || !in.closed {
		t.Fatalf("run did not continue within one turn: %+v %v, requests=%d closed=%t", result, err, len(requests), in.closed)
	}
	second := requests[1].Input
	if len(second) != 5 || !bytes.Contains(second[1], []byte(`"type":"function_call"`)) ||
		!bytes.Contains(second[2], []byte(`"type":"function_call_output"`)) ||
		!bytes.Contains(second[3], []byte("during SSE")) || !bytes.Contains(second[4], []byte("during tool")) {
		t.Fatalf("provider input split the tool pair or reordered messages: %s", second)
	}
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Turns) != 1 || len(stored.Turns[0].ToolCalls) != 1 || len(stored.Turns[0].Messages) != 6 || len(in.acked) != 5 {
		t.Fatalf("history duplicated a tool or lost an input: %+v", stored.Turns)
	}
	for id, turn := range in.acked {
		if turn != result.TurnID {
			t.Fatalf("message %s was acknowledged for a different turn: %s", id, turn)
		}
	}
	if inputEvents != 10 {
		t.Fatalf("input events=%d, want 10", inputEvents)
	}
}

func TestActiveInputRequestLimitLeavesUnprocessedMessageInBuffer(t *testing.T) {
	in := newInboundFixture(journalInput("initial", 1, "sender", "initial input"))
	client := streamFunc(func(_ context.Context, _ responses.Request) (*responses.Response, error) {
		in.add(t, journalInput("late", 2, "sender", "still waiting"))
		return finalResponse("draft answer"), nil
	})
	runtime, cleanup := testRuntime(t, client)
	defer cleanup()
	runtime.Inbound = in
	runtime.MaxRequests = 1
	_, err := runtime.Run(context.Background(), "")
	assertRuntimeCode(t, err, "request_limit_exceeded")
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(in.pending) != 1 || in.pending[0].ID != "late" || len(in.acked) != 1 || len(stored.Turns[0].Messages) != 1 {
		t.Fatalf("limit lost or prematurely acknowledged input: %v %+v", in.pending, stored.Turns)
	}
}

func TestActiveInputLostAcknowledgmentRecoversWithoutModelReplay(t *testing.T) {
	message := journalInput("lost-ack", 1, "sender", "saved before acknowledgment")
	in := newInboundFixture(message)
	in.failAck = true
	client := &fakeClient{}
	runtime, cleanup := testRuntime(t, client)
	defer cleanup()
	runtime.Inbound = in
	_, err := runtime.Run(context.Background(), "")
	assertRuntimeCode(t, err, "input_channel_failed")
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Turns) != 1 || stored.Turns[0].Status != session.StatusFailed || len(stored.Turns[0].Messages) != 1 || len(in.acked) != 0 {
		t.Fatalf("failed acknowledgment did not retain only the session copy: %+v", stored.Turns)
	}
	runtime.Session = stored
	result, err := runtime.Run(context.Background(), "")
	if err != nil || client.calls != 0 || result.TurnID != "" || !in.closed || in.acked[message.ID] != stored.Turns[0].ID {
		t.Fatalf("redelivery produced a new model request or turn: %+v %v", result, err)
	}
	after, err := runtime.Locked.Load()
	if err != nil || len(after.Turns) != 1 || len(after.Turns[0].Messages) != 1 {
		t.Fatalf("redelivery duplicated session history: %+v %v", after, err)
	}
}

func TestActiveInputAcknowledgmentRecoveryAfterToolEffect(t *testing.T) {
	initial := journalInput("original", 1, "sender", "make a marker")
	clarification := journalInput("later", 2, "sender two", "clarify the marker")
	in := newInboundFixture(initial)
	call := json.RawMessage(`{"type":"function_call","call_id":"create-marker","name":"shell_exec","arguments":"{\"command\":\"printf effect >> marker\",\"timeout_ms\":null,\"max_output_chars\":null}"}`)
	client := &fakeClient{outputs: [][]json.RawMessage{{call}}}
	runtime, cleanup := testRuntime(t, client)
	defer cleanup()
	runtime.Inbound = in
	runtime.Publish = func(e eventstream.Event) {
		if e.Type == "tool_completed" {
			in.add(t, clarification)
			in.failAck = true
		}
	}
	_, err := runtime.Run(context.Background(), "")
	assertRuntimeCode(t, err, "input_channel_failed")
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	turn := stored.Turns[0]
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ResultState != session.ToolResultKnown || len(turn.Messages) != 2 || client.calls != 1 {
		t.Fatalf("tool result or clarification was not saved before the failed ack: %+v", turn)
	}
	runtime.Session = stored
	if _, err := runtime.Run(context.Background(), ""); err != nil || client.calls != 1 || in.acked[clarification.ID] != turn.ID {
		t.Fatalf("ack recovery made a new model request: %v, calls=%d", err, client.calls)
	}

	// A genuinely new message can continue from history after reconciliation.
	runtime.Inbound = newInboundFixture(journalInput("new", 3, "sender", "continue explicitly"))
	client.outputs = [][]json.RawMessage{finalResponse("continued").Output}
	result, err := runtime.Run(context.Background(), "")
	if err != nil || result.Text != "continued" || client.calls != 2 {
		t.Fatalf("new input failed to continue: %+v %v calls=%d", result, err, client.calls)
	}
	input, _ := json.Marshal(client.requests[1].Input)
	if bytes.Count(input, []byte("clarify the marker")) != 1 || !bytes.Contains(input, []byte(`"type":"function_call_output"`)) {
		t.Fatalf("recovery lost or duplicated prior context: %s", input)
	}
	marker, err := os.ReadFile(filepath.Join(runtime.Workspace.Dir, "marker"))
	if err != nil || string(marker) != "effect" {
		t.Fatalf("tool side effect was repeated: %q %v", marker, err)
	}
}

func TestActiveInputCancellationKeepsPendingSeparate(t *testing.T) {
	in := newInboundFixture(journalInput("initial", 1, "sender", "initial input"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := streamFunc(func(ctx context.Context, _ responses.Request) (*responses.Response, error) {
		in.add(t, journalInput("pending", 2, "sender", "later input"))
		cancel()
		return nil, ctx.Err()
	})
	runtime, cleanup := testRuntime(t, client)
	defer cleanup()
	runtime.Inbound = in
	_, err := runtime.Run(ctx, "")
	assertRuntimeCode(t, err, "turn_cancelled")
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(in.pending) != 1 || len(in.acked) != 1 || stored.Turns[0].Status != session.StatusCancelled || len(stored.Turns[0].Messages) != 1 {
		t.Fatalf("cancel lost or acknowledged input: pending=%+v, history=%+v", in.pending, stored.Turns)
	}
}
