package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultMaxModelRequests = 128
	DefaultMaxTurnDuration  = 120 * time.Minute
)

type Config struct {
	Mode         string
	DefaultModel string
	Models       map[string]Model
	Provider     Provider
	Limits       Limits
}

type Model struct {
	Model            string
	Reasoning        string
	Score            *int
	CompactThreshold int
}

type Provider struct {
	URL string
	Key string
}

type Limits struct {
	MaxModelRequests int
	MaxTurnDuration  time.Duration
}

type rawConfig struct {
	Mode         string              `yaml:"mode"`
	DefaultModel string              `yaml:"default_model"`
	Models       map[string]rawModel `yaml:"models"`
	Provider     Provider            `yaml:"provider"`
	Limits       rawLimits           `yaml:"limits"`
}

type rawModel struct {
	Model            string `yaml:"model"`
	Reasoning        string `yaml:"reasoning"`
	Score            *int   `yaml:"score"`
	CompactThreshold int    `yaml:"compact_threshold"`
}

type rawLimits struct {
	MaxModelRequests *int    `yaml:"max_model_requests"`
	MaxTurnDuration  *string `yaml:"max_turn_duration"`
}

func ResolveHome(value string) (string, error) {
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
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration %q: %w", path, err)
	}

	var raw rawConfig
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("parse configuration %q: %w", path, err)
	}
	cfg, err := validate(raw)
	if err != nil {
		return Config{}, fmt.Errorf("configuration %q: %w", path, err)
	}
	return cfg, nil
}

func validate(raw rawConfig) (Config, error) {
	if raw.Mode != "unit" {
		if raw.Mode == "server" {
			return Config{}, errors.New("configuration mode server is not implemented")
		}
		return Config{}, fmt.Errorf("configuration mode must be unit, got %q", raw.Mode)
	}
	var missing []string
	if strings.TrimSpace(raw.Provider.URL) == "" {
		missing = append(missing, "provider.url")
	}
	if strings.TrimSpace(raw.Provider.Key) == "" || strings.TrimSpace(raw.Provider.Key) == "replace-with-api-key" {
		missing = append(missing, "provider.key")
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
	providerURL, err := url.Parse(raw.Provider.URL)
	if err != nil || providerURL.Scheme == "" || providerURL.Host == "" {
		return Config{}, errors.New("provider.url must be an absolute HTTP(S) base URL")
	}
	if providerURL.Scheme != "http" && providerURL.Scheme != "https" {
		return Config{}, errors.New("provider.url must use http or https")
	}
	if providerURL.RawQuery != "" || providerURL.Fragment != "" {
		return Config{}, errors.New("provider.url must not contain a query or fragment")
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
		models[name] = Model{
			Model:            model.Model,
			Reasoning:        model.Reasoning,
			Score:            model.Score,
			CompactThreshold: model.CompactThreshold,
		}
	}

	limits, err := parseLimits(raw.Limits)
	if err != nil {
		return Config{}, err
	}
	return Config{
		Mode:         raw.Mode,
		DefaultModel: raw.DefaultModel,
		Models:       models,
		Provider:     raw.Provider,
		Limits:       limits,
	}, nil
}

func parseLimits(raw rawLimits) (Limits, error) {
	requests := DefaultMaxModelRequests
	if raw.MaxModelRequests != nil {
		requests = *raw.MaxModelRequests
	}
	if requests < 0 {
		return Limits{}, errors.New("limits.max_model_requests must not be negative")
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
	return Limits{MaxModelRequests: requests, MaxTurnDuration: duration}, nil
}

func (c Config) Endpoint(resource string) (string, error) {
	base, err := url.Parse(c.Provider.URL)
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
