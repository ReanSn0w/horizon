package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/instructions"
	"github.com/ReanSn0w/horizon/internal/responses"
	"github.com/ReanSn0w/horizon/internal/session"
)

// Opt-in: this makes three small paid requests to the configured provider.
func TestConfiguredContextGuardEconomy(t *testing.T) {
	if os.Getenv("HORIZON_COST_CHECK") != "1" {
		t.Skip("set HORIZON_COST_CHECK=1 to compare real provider usage")
	}
	home, err := config.ResolveHome("")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	name, model, err := cfg.SelectModel("")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := cfg.Endpoint("responses")
	compactEndpoint, _ := cfg.Endpoint("responses/compact")
	client := &responses.Client{ResponsesURL: endpoint, CompactURL: compactEndpoint, APIKey: cfg.Provider.Key, MaxAttempts: 1}
	runtime := &Runtime{Client: client, ProfileName: name, Profile: session.ModelProfile{Model: model.Model}, Instructions: &instructions.Snapshot{Prompt: "Reply briefly."}}
	input := []json.RawMessage{responses.UserMessage(strings.Repeat("alpha ", 15000) + "Reply with OK.")}
	baseline, err := client.Stream(context.Background(), responses.Request{Model: model.Model, Instructions: "Reply briefly.", Input: input, Tools: []responses.Tool{}, Store: false}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var summaryCost float64
	window, _, compactUsage, _, err := runtime.compactWindow(context.Background(), input, nil, false, func(response *responses.Response, err error) {
		if err == nil && response != nil {
			summaryCost = usageCost(response.Usage)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := client.Stream(context.Background(), responses.Request{Model: model.Model, Instructions: "Reply briefly.", Input: append(window, responses.UserMessage("Reply with OK.")), Tools: []responses.Tool{}, Store: false}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeTokens := responses.ParseUsage(baseline.Usage).InputTokens
	afterTokens := responses.ParseUsage(after.Usage).InputTokens
	if beforeTokens == nil || afterTokens == nil || compactUsage.InputTokens == nil {
		t.Fatal("provider did not return input token usage")
	}
	t.Logf("baseline_input=%d compact_input=%d after_input=%d three_repeats_without=%d with=%d", *beforeTokens, *compactUsage.InputTokens, *afterTokens, 3**beforeTokens, *compactUsage.InputTokens+3**afterTokens)
	t.Logf("estimated_three_repeat_cost_without=%.8f with=%.8f USD", 3*usageCost(baseline.Usage), summaryCost+3*usageCost(after.Usage))
	if *afterTokens >= *beforeTokens {
		t.Fatalf("compacted request did not reduce input: before=%d after=%d", *beforeTokens, *afterTokens)
	}
}

func usageCost(raw json.RawMessage) float64 {
	var value struct {
		Cost float64 `json:"cost"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.Cost
}
