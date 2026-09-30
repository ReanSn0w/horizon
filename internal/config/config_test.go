package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validConfig = `
mode: unit
default_model: coding
models:
  coding:
    model: model-code
    compact_threshold: 100000
provider:
  url: https://example.test/v1
  key: super-secret
`

func TestLoadLimits(t *testing.T) {
	tests := []struct {
		name         string
		limits       string
		wantRequests int
		wantDuration time.Duration
		wantError    string
	}{
		{name: "defaults", wantRequests: 128, wantDuration: 120 * time.Minute},
		{name: "explicit zero", limits: "limits:\n  max_model_requests: 0\n  max_turn_duration: \"0\"\n", wantRequests: 0, wantDuration: 0},
		{name: "values", limits: "limits:\n  max_model_requests: 4\n  max_turn_duration: 45s\n", wantRequests: 4, wantDuration: 45 * time.Second},
		{name: "negative requests", limits: "limits:\n  max_model_requests: -1\n", wantError: "must not be negative"},
		{name: "negative duration", limits: "limits:\n  max_turn_duration: -1s\n", wantError: "must not be negative"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := writeConfig(t, validConfig+test.limits)
			cfg, err := Load(home)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Load() error = %v, want substring %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Limits.MaxModelRequests != test.wantRequests || cfg.Limits.MaxTurnDuration != test.wantDuration {
				t.Fatalf("Load() limits = %+v, want requests=%d duration=%v", cfg.Limits, test.wantRequests, test.wantDuration)
			}
		})
	}
}

func TestLoadSoulEnabled(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		want          bool
		wantError     bool
	}{
		{name: "omitted"},
		{name: "disabled", setting: "soul_enabled: false\n"},
		{name: "enabled", setting: "soul_enabled: true\n", want: true},
		{name: "invalid", setting: "soul_enabled: maybe\n", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, validConfig+tc.setting))
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "cannot unmarshal") {
					t.Fatalf("Load() error = %v", err)
				}
				return
			}
			if err != nil || cfg.SoulEnabled != tc.want {
				t.Fatalf("Load() soul_enabled = %t, error = %v", cfg.SoulEnabled, err)
			}
		})
	}
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name, contents, want string
	}{
		{"server", strings.Replace(validConfig, "mode: unit", "mode: server", 1), "not implemented"},
		{"unknown field", validConfig + "mystery: true\n", "field mystery not found"},
		{"missing threshold", strings.Replace(validConfig, "    compact_threshold: 100000\n", "", 1), "compact_threshold"},
		{"bad default", strings.Replace(validConfig, "default_model: coding", "default_model: absent", 1), "does not name"},
		{"bad URL", strings.Replace(validConfig, "https://example.test/v1", "file:///tmp/api", 1), "absolute HTTP(S)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.contents))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
			if strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("Load() exposed provider key: %v", err)
			}
		})
	}
}

func TestEndpointsAndModelSelection(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := cfg.Endpoint("responses/compact")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://example.test/v1/responses/compact" {
		t.Fatalf("Endpoint() = %q", endpoint)
	}
	name, model, err := cfg.SelectModel("")
	if err != nil || name != "coding" || model.Model != "model-code" {
		t.Fatalf("SelectModel(default) = %q, %+v, %v", name, model, err)
	}
	if _, _, err := cfg.SelectModel("absent"); err == nil || !strings.Contains(err.Error(), "horizon models") {
		t.Fatalf("SelectModel(absent) error = %v", err)
	}
}

func TestDecisionProviderIsSeparateAndRequiredForAgent(t *testing.T) {
	config := validConfig + "decision:\n  provider:\n    url: https://openrouter.ai/api\n    key: decision-secret\n  model: typesafe/jev-1.13\n"
	cfg, err := Load(writeConfig(t, config))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RequireDecision(); err != nil {
		t.Fatal(err)
	}
	endpoint, err := cfg.DecisionEndpoint()
	if err != nil || endpoint != "https://openrouter.ai/api/alpha/decisions" || cfg.Provider.Key != "super-secret" || cfg.Decision.Provider.Key != "decision-secret" {
		t.Fatalf("decision endpoint=%q provider=%+v error=%v", endpoint, cfg.Decision.Provider, err)
	}
	old, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if err := old.RequireDecision(); err == nil || !strings.Contains(err.Error(), "decision.provider.key") {
		t.Fatalf("old config decision error=%v", err)
	}
}

func TestResolveHome(t *testing.T) {
	got, err := ResolveHome("relative/home")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs("relative/home")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ResolveHome(relative) = %q, want %q", got, want)
	}
	got, err = ResolveHome("")
	if err != nil {
		t.Fatal(err)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(userHome, ".horizon") {
		t.Fatalf("ResolveHome(default) = %q", got)
	}
}

func TestResolveHomeFromEnvironment(t *testing.T) {
	environmentHome := filepath.Join(t.TempDir(), "from-env")
	t.Setenv("HORIZON_HOME", environmentHome)
	got, err := ResolveHome("")
	if err != nil || got != environmentHome {
		t.Fatalf("ResolveHome() = %q, %v; want %q", got, err, environmentHome)
	}
	explicitHome := filepath.Join(t.TempDir(), "explicit")
	got, err = ResolveHome(explicitHome)
	if err != nil || got != explicitHome {
		t.Fatalf("ResolveHome(explicit) = %q, %v; want %q", got, err, explicitHome)
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestSetupDiagnostics(t *testing.T) {
	template, err := os.ReadFile("../bootstrap/assets/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{
		string(template),
		strings.Replace(validConfig, "super-secret", "replace-with-api-key", 1),
		strings.Replace(validConfig, "model-code", "replace-with-model-name", 1),
	} {
		home := writeConfig(t, contents)
		_, err := Load(home)
		var setup *SetupError
		if !errors.As(err, &setup) || len(setup.Fields) == 0 || !strings.Contains(err.Error(), filepath.Join(home, "config.yaml")) {
			t.Fatalf("setup error=%v", err)
		}
	}
	filled := strings.Replace(string(template), `url: ""`, "url: https://example.test/v1", 1)
	filled = strings.Replace(filled, `key: ""`, "key: secret", 1)
	filled = strings.Replace(filled, `model: ""`, "model: model-code", 1)
	if _, err := Load(writeConfig(t, filled)); err != nil {
		t.Fatal(err)
	}
}
