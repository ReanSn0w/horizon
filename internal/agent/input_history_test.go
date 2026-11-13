package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
	"github.com/ReanSn0w/horizon/internal/turninput"
)

func newJournalTurn(t *testing.T, runtime *Runtime) string {
	t.Helper()
	id, err := session.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Locked.StartTurn(session.Turn{ID: id, Status: session.StatusActive, StartedAt: time.Now().UTC(), Model: runtime.Profile}); err != nil {
		t.Fatal(err)
	}
	return id
}

func journalInput(id string, sequence uint64, author, text string) turninput.Message {
	return turninput.Message{ID: id, Sequence: sequence, Source: "adapter", Route: "route", Author: author, Text: text}
}

func saveJournalInput(t *testing.T, runtime *Runtime, turn string, message turninput.Message) {
	t.Helper()
	if got, added, err := runtime.Locked.RecordInbound(turn, message); got != turn || !added || err != nil {
		t.Fatalf("record inbound returned turn=%q, added=%t, err=%v", got, added, err)
	}
}

func TestRecoverInboundChronologyAcrossKnownAndUnknownTools(t *testing.T) {
	runtime, cleanup := testRuntime(t, &fakeClient{})
	defer cleanup()
	id := newJournalTurn(t, runtime)
	saveJournalInput(t, runtime, id, journalInput("initial", 1, "first sender", "initial request"))
	knownCall := json.RawMessage(`{"type":"function_call","call_id":"known","name":"skill_read","arguments":"{\"name\":\"skill\"}"}`)
	knownReasoning := json.RawMessage(`{"type":"reasoning","id":"retained-reasoning","encrypted_content":"opaque-known"}`)
	if err := runtime.Locked.AppendAPIItems(id, []json.RawMessage{knownReasoning, knownCall}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := runtime.Locked.RecordToolCall(id, session.ToolCall{CallID: "known", Name: "skill_read", Arguments: json.RawMessage(`{"name":"skill"}`), StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Locked.RecordToolResult(id, "known", json.RawMessage(`{"ok":true,"data":"known result"}`), at, nil); err != nil {
		t.Fatal(err)
	}
	steering := journalInput("clarification", 2, "another sender", "clarification after tool")
	saveJournalInput(t, runtime, id, steering)
	unknownCall := json.RawMessage(`{"type":"function_call","call_id":"unknown","name":"shell_exec","arguments":"{\"command\":\"touch marker\"}"}`)
	unknownReasoning := json.RawMessage(`{"type":"reasoning","id":"discarded-reasoning","encrypted_content":"opaque-unknown"}`)
	if err := runtime.Locked.AppendAPIItems(id, []json.RawMessage{unknownReasoning, unknownCall}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Locked.RecordToolCall(id, session.ToolCall{CallID: "unknown", Name: "shell_exec", Arguments: json.RawMessage(`{"command":"touch marker"}`), StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if recovered, err := runtime.Locked.RecoverInterruptedTurn(at); !recovered || err != nil {
		t.Fatalf("recover interrupted turn: %t %v", recovered, err)
	}
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	input, err := BuildInput(stored)
	if err != nil {
		t.Fatal(err)
	}
	if len(input) != 6 || !bytes.Contains(input[0], []byte("initial request")) ||
		!sameJournalItem(input[1], knownReasoning) || !sameJournalItem(input[2], knownCall) ||
		!bytes.Contains(input[3], []byte(`"type":"function_call_output"`)) ||
		!bytes.Contains(input[4], []byte("another sender")) || !bytes.Contains(input[4], []byte("clarification after tool")) ||
		!bytes.Contains(input[5], []byte("unknown outcome")) {
		t.Fatalf("bad recovery order or missing unknown warning: %s", input)
	}

	// Simulate a daemon crash after the session write and before acknowledgment.
	nextTurn := newJournalTurn(t, runtime)
	got, added, err := runtime.Locked.RecordInbound(nextTurn, steering)
	if got != id || added || err != nil {
		t.Fatalf("redelivered input moved into another turn: %s, %t, %v", got, added, err)
	}
	steering.Author = "different author"
	if _, _, err := runtime.Locked.RecordInbound(nextTurn, steering); err == nil {
		t.Fatal("an existing ID accepted a different author")
	}
}

func TestInboundHistorySurvivesCompactionAndFork(t *testing.T) {
	runtime, cleanup := testRuntime(t, &fakeClient{})
	defer cleanup()
	id := newJournalTurn(t, runtime)
	before := journalInput("before", 1, "sender", "before compaction")
	after := journalInput("after", 2, "another sender", "after compaction")
	saveJournalInput(t, runtime, id, before)
	compact := json.RawMessage(`{"type":"compaction","id":"compact","encrypted_content":"opaque"}`)
	if err := runtime.Locked.AppendAPIItems(id, []json.RawMessage{compact}); err != nil {
		t.Fatal(err)
	}
	saveJournalInput(t, runtime, id, after)
	final := json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"final"}]}`)
	window := []json.RawMessage{compact, inboundUserMessage(after.Author, after.Text), final}
	if err := runtime.Locked.CompleteTurnWithCompaction(id, session.StatusCompleted, nil, window,
		&session.Compaction{ID: "compact", ModelProfile: runtime.ProfileName, Items: []json.RawMessage{compact}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	input, err := BuildInput(stored)
	if err != nil || len(input) != 3 || !sameJournalItem(input[1], window[1]) || !sameJournalItem(input[2], final) {
		t.Fatalf("compacted checkpoint lost messages appended to its window: %s, %v", input, err)
	}
	manual := json.RawMessage(`{"type":"compaction","id":"manual","encrypted_content":"opaque-manual"}`)
	if result, err := Compact(context.Background(), fakeCompactClient{response: &responses.Response{Output: []json.RawMessage{manual}}},
		runtime.Locked, stored, "instructions", 0, nil); err != nil || !result.Compacted {
		t.Fatalf("manual compaction: %+v %v", result, err)
	}
	if err := runtime.Locked.Close(); err != nil {
		t.Fatal(err)
	}
	forked, err := runtime.Store.Fork(runtime.Workspace, stored.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []turninput.Message{before, after} {
		if got, err := forked.FindInbound(message); got != id || err != nil {
			t.Fatalf("fork lost inbound journal entry %s: %s %v", message.ID, got, err)
		}
	}
	if in := forked.Turns[0].Messages[1].Inbound; in.AfterAPIItems != 1 || in.AfterToolCalls != 0 {
		t.Fatalf("fork lost inbound position: %+v", in)
	}
	input, err = BuildInput(forked)
	if err != nil || len(input) != 1 || !sameJournalItem(input[0], manual) {
		t.Fatalf("fork lost the compacted working window: %s, %v", input, err)
	}
}

func sameJournalItem(a, b json.RawMessage) bool {
	var compactA, compactB bytes.Buffer
	return json.Compact(&compactA, a) == nil && json.Compact(&compactB, b) == nil && bytes.Equal(compactA.Bytes(), compactB.Bytes())
}

func TestFailedCompactionTurnRetainsInboundAfterOpaqueOutput(t *testing.T) {
	runtime, cleanup := testRuntime(t, &fakeClient{})
	defer cleanup()
	id := newJournalTurn(t, runtime)
	saveJournalInput(t, runtime, id, journalInput("before", 1, "sender", "first message"))
	compact := json.RawMessage(`{"type":"compaction","id":"failed-compact","encrypted_content":"opaque"}`)
	opaque := json.RawMessage(`{"type":"future-response-item","opaque":{"keep":"entire object"}}`)
	if err := runtime.Locked.AppendAPIItems(id, []json.RawMessage{compact, opaque}); err != nil {
		t.Fatal(err)
	}
	saveJournalInput(t, runtime, id, journalInput("after", 2, "sender", "later message"))
	if err := runtime.Locked.CompleteTurn(id, session.StatusFailed, &session.TurnError{Code: "test", Message: "stopped"}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	stored, err := runtime.Locked.Load()
	if err != nil {
		t.Fatal(err)
	}
	input, err := BuildInput(stored)
	if err != nil || len(input) != 4 || !bytes.Contains(input[0], []byte("first message")) ||
		!sameJournalItem(input[1], opaque) || !bytes.Contains(input[2], []byte("later message")) ||
		!bytes.Contains(input[3], []byte("did not complete successfully")) {
		t.Fatalf("failed compacted turn lost chronology: %s %v", input, err)
	}
}
