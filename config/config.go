package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultMaxModelRequests = 128
	DefaultMaxInputTokens   = 2000000
	DefaultMaxTurnDuration  = 120 * time.Minute
)

type Config struct {
	AgentPlugins   []string
	Plugins        map[string]yaml.Node
	DisabledSkills []string
	SoulEnabled    bool
	Mode           string
	DefaultModel   string
	Models         map[string]Model
	Providers      map[string]Provider
	DecisionModel  string
	Memory         Memory
	Limits         Limits
}

type Model struct {
	Provider         string
	Model            string
	Reasoning        string
	Score            *int
	CompactThreshold int
}

type Provider struct {
	URL string `yaml:"url"`
	Key string `yaml:"key"`
}

type Memory struct {
	Enabled         bool
	Threshold       int
	Interval        time.Duration
	Model           string
	NoteLimit       int
	SummaryLimit    int
	ContextLimit    int
	APITimeout      time.Duration
	RetryInterval   time.Duration
	MaxPendingNotes int
	MaxPendingBytes int
}

type Limits struct {
	MaxModelRequests int
	MaxInputTokens   uint64
	MaxTurnDuration  time.Duration
}

type rawConfig struct {
	AgentPlugins   []string             `yaml:"agent_plugins"`
	Plugins        map[string]yaml.Node `yaml:"plugins"`
	DisabledSkills disabledIDs          `yaml:"disabled_skills"`
	SoulEnabled    bool                 `yaml:"soul_enabled"`
	Mode           string               `yaml:"mode"`
	DefaultModel   string               `yaml:"default_model"`
	Models         map[string]rawModel  `yaml:"models"`
	Providers      map[string]Provider  `yaml:"providers"`
	DecisionModel  string               `yaml:"decision_model"`
	Memory         rawMemoryNode        `yaml:"memory"`
	Limits         rawLimits            `yaml:"limits"`
}

type rawMemory struct {
	Enabled         *bool  `yaml:"enabled"`
	Threshold       int    `yaml:"threshold"`
	Interval        string `yaml:"interval"`
	Model           string `yaml:"model"`
	NoteLimit       int    `yaml:"note_limit"`
	SummaryLimit    int    `yaml:"summary_limit"`
	ContextLimit    int    `yaml:"context_limit"`
	APITimeout      string `yaml:"api_timeout"`
	RetryInterval   string `yaml:"retry_interval"`
	MaxPendingNotes int    `yaml:"max_pending_notes"`
	MaxPendingBytes int    `yaml:"max_pending_bytes"`
}

type rawMemoryNode struct {
	Node *yaml.Node
}

func (value *rawMemoryNode) UnmarshalYAML(node *yaml.Node) error {
	copy := *node
	value.Node = &copy
	return nil
}

type rawModel struct {
	Provider         string `yaml:"provider"`
	Model            string `yaml:"model"`
	Reasoning        string `yaml:"reasoning"`
	Score            *int   `yaml:"score"`
	CompactThreshold int    `yaml:"compact_threshold"`
}

type rawLimits struct {
	MaxModelRequests *int    `yaml:"max_model_requests"`
	MaxInputTokens   *int64  `yaml:"max_input_tokens"`
	MaxTurnDuration  *string `yaml:"max_turn_duration"`
}

func ResolveHome(value string) (string, error) {
	if value == "" {
		value = os.Getenv("HORIZON_HOME")
	}
	if value == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		value = filepath.Join(home, ".horizon")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve Horizon home %q: %w", value, err)
	}
	return filepath.Clean(abs), nil
}

func Load(home string) (Config, error) {
	path := filepath.Join(home, "config.yaml")
	raw, _, err := readConfig(path)
	if err != nil {
		return Config{}, err
	}
	cfg, err := validate(raw)
	if err != nil {
		return Config{}, fmt.Errorf("configuration %q: %w", path, err)
	}
	return cfg, nil
}

func validate(raw rawConfig) (Config, error) {
	if len(raw.AgentPlugins) > 16 {
		return Config{}, errors.New("agent_plugins supports at most 16 plugins")
	}
	seen := map[string]bool{}
	legacyMemoryEnabled := false
	for _, name := range raw.AgentPlugins {
		if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(name) || seen[name] {
			return Config{}, fmt.Errorf("invalid or duplicate agent_plugins name %q", name)
		}
		seen[name] = true
		legacyMemoryEnabled = legacyMemoryEnabled || name == "memory"
	}
	if raw.Mode != "unit" {
		if raw.Mode == "server" {
			return Config{}, errors.New("configuration mode server is not implemented")
		}
		return Config{}, fmt.Errorf("configuration mode must be unit, got %q", raw.Mode)
	}
	var missing []string
	if len(raw.Providers) == 0 {
		missing = append(missing, "providers")
	}
	for name, provider := range raw.Providers {
		if strings.TrimSpace(name) == "" {
			return Config{}, errors.New("provider name must not be empty")
		}
		if strings.TrimSpace(provider.URL) == "" {
			missing = append(missing, "providers."+name+".url")
		}
		if strings.TrimSpace(provider.Key) == "" || provider.Key == "replace-with-api-key" {
			missing = append(missing, "providers."+name+".key")
		}
	}
	for name, model := range raw.Models {
		if strings.TrimSpace(model.Model) == "" || strings.TrimSpace(model.Model) == "replace-with-model-name" {
			missing = append(missing, "models."+name+".model")
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return Config{}, &SetupError{Fields: missing}
	}
	for name, provider := range raw.Providers {
		u, err := url.Parse(provider.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Config{}, fmt.Errorf("providers.%s.url must be an absolute HTTP(S) base URL without credentials, query or fragment", name)
		}
	}
	if len(raw.Models) == 0 && raw.DefaultModel != "" {
		return Config{}, errors.New("default_model is set but models is empty")
	}
	if len(raw.Models) > 0 {
		if raw.DefaultModel == "" {
			return Config{}, errors.New("default_model is required when models are configured")
		}
		if _, ok := raw.Models[raw.DefaultModel]; !ok {
			return Config{}, fmt.Errorf("default_model %q does not name a configured model", raw.DefaultModel)
		}
	}

	models := make(map[string]Model, len(raw.Models))
	for name, model := range raw.Models {
		if strings.TrimSpace(name) == "" {
			return Config{}, errors.New("model profile name must not be empty")
		}
		if strings.TrimSpace(model.Model) == "" {
			return Config{}, fmt.Errorf("models.%s.model is required", name)
		}
		if model.CompactThreshold <= 0 {
			return Config{}, fmt.Errorf("models.%s.compact_threshold must be a positive integer", name)
		}
		if _, ok := raw.Providers[model.Provider]; !ok {
			return Config{}, fmt.Errorf("models.%s.provider %q does not name a configured provider", name, model.Provider)
		}
		models[name] = Model{
			Provider:         model.Provider,
			Model:            model.Model,
			Reasoning:        model.Reasoning,
			Score:            model.Score,
			CompactThreshold: model.CompactThreshold,
		}
	}
	memory, agentPlugins, err := validateMemory(raw, legacyMemoryEnabled, models)
	if err != nil {
		return Config{}, err
	}

	limits, err := parseLimits(raw.Limits)
	if err != nil {
		return Config{}, err
	}
	return Config{
		Plugins:        raw.Plugins,
		AgentPlugins:   agentPlugins,
		DisabledSkills: append([]string(nil), raw.DisabledSkills...),
		SoulEnabled:    raw.SoulEnabled,
		Mode:           raw.Mode,
		DefaultModel:   raw.DefaultModel,
		Models:         models,
		Providers:      raw.Providers,
		DecisionModel:  raw.DecisionModel,
		Memory:         memory,
		Limits:         limits,
	}, nil
}

// RequireDecision validates the decision profile before creating a read/write turn.
// Full access and local commands do not require a decision model.
func (c Config) RequireDecision() error {
	if strings.TrimSpace(c.DecisionModel) == "" {
		return &SetupError{Fields: []string{"decision_model"}}
	}
	if _, ok := c.Models[c.DecisionModel]; !ok {
		return fmt.Errorf("decision_model %q does not name a configured model", c.DecisionModel)
	}
	return nil
}

func parseLimits(raw rawLimits) (Limits, error) {
	requests := DefaultMaxModelRequests
	if raw.MaxModelRequests != nil {
		requests = *raw.MaxModelRequests
	}
	if requests < 0 {
		return Limits{}, errors.New("limits.max_model_requests must not be negative")
	}
	inputTokens := uint64(DefaultMaxInputTokens)
	if raw.MaxInputTokens != nil {
		if *raw.MaxInputTokens < 0 {
			return Limits{}, errors.New("limits.max_input_tokens must not be negative")
		}
		inputTokens = uint64(*raw.MaxInputTokens)
	}

	duration := DefaultMaxTurnDuration
	if raw.MaxTurnDuration != nil {
		if *raw.MaxTurnDuration == "0" {
			duration = 0
		} else {
			parsed, err := time.ParseDuration(*raw.MaxTurnDuration)
			if err != nil {
				return Limits{}, errors.New("limits.max_turn_duration must be a Go duration or 0")
			}
			if parsed < 0 {
				return Limits{}, errors.New("limits.max_turn_duration must not be negative")
			}
			duration = parsed
		}
	}
	return Limits{MaxModelRequests: requests, MaxInputTokens: inputTokens, MaxTurnDuration: duration}, nil
}

func (c Config) Endpoint(providerName, resource string) (string, error) {
	provider, ok := c.Providers[providerName]
	if !ok {
		return "", fmt.Errorf("unknown provider %q", providerName)
	}
	base, err := url.Parse(provider.URL)
	if err != nil {
		return "", errors.New("provider URL is invalid")
	}
	return url.JoinPath(base.String(), resource)
}

func (c Config) SelectModel(name string) (string, Model, error) {
	if name == "" {
		name = c.DefaultModel
	}
	model, ok := c.Models[name]
	if !ok {
		return "", Model{}, fmt.Errorf("unknown model profile %q; run 'horizon models' to list configured profiles", name)
	}
	return name, model, nil
}

// SetupError identifies editable fields that still need user configuration.
type SetupError struct{ Fields []string }

func (e *SetupError) Error() string {
	return "заполните данные провайдера и модели: " + strings.Join(e.Fields, ", ") +
		"; проверьте models.<профиль>.compact_threshold для выбранной модели"
}

// ProviderKeys supplies all configured API secrets for redaction and command review.
func (c Config) ProviderKeys() []string {
	keys := make([]string, 0, len(c.Providers))
	for _, provider := range c.Providers {
		if provider.Key != "" {
			keys = append(keys, provider.Key)
		}
	}
	return keys
}
