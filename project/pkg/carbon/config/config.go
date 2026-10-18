// Package config loads Carbon Data Service process configuration from a YAML
// file and environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

const (
	// DefaultPort is the stand HTTP port (ARCH §5, REST + /metrics on one listener).
	DefaultPort = 8080

	// DefaultCacheTTL is the CDS cache refresh interval used until T4.x
	// wires a dedicated refreshInterval field (canon 30s).
	DefaultCacheTTL = 30 * time.Second

	envPort                 = "CARBON_PORT"
	envCacheTTL             = "CARBON_CACHE_TTL"
	envProviderType         = "CARBON_PROVIDER_TYPE"
	envLiveAPIKey           = "CARBON_LIVE_API_KEY"
	envReplayDatasetPath    = "CARBON_REPLAY_DATASET_PATH"
	envHTTPTimeout          = "CARBON_HTTP_TIMEOUT"
	envReplayTimeMultiplier = "CARBON_REPLAY_TIME_MULTIPLIER"
	envReplayLoop           = "CARBON_REPLAY_LOOP"
	envReplayLookup         = "CARBON_REPLAY_LOOKUP"
)

// LookupEnv resolves an environment variable. Tests inject a fake.
type LookupEnv func(key string) string

// Config is the Carbon Data Service process configuration (not plugin args).
type Config struct {
	Port                 int
	CacheTTL             time.Duration
	ProviderType         v1alpha1.ProviderMode
	LiveAPIKey           string
	ReplayDatasetPath    string
	HTTPTimeout          time.Duration
	ReplayTimeMultiplier float64
	ReplayLoop           string
	ReplayLookup         string
}

// Load reads defaults, then YAML at path, then environment overrides.
// path is required and must exist. getenv may be nil (uses os.Getenv).
func Load(path string, getenv LookupEnv) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Config{
		Port:         DefaultPort,
		CacheTTL:     DefaultCacheTTL,
		ProviderType: v1alpha1.ProviderModeMock,
	}
	if strings.TrimSpace(path) == "" {
		return Config{}, fmt.Errorf("config path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	if err := applyYAML(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if err := applyEnv(&cfg, getenv); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks domain of port, TTL and provider-specific required fields.
func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d is not in 1..65535", c.Port)
	}
	if c.CacheTTL <= 0 {
		return fmt.Errorf("cacheTTL must be a positive duration, got %s", c.CacheTTL)
	}
	switch c.ProviderType {
	case v1alpha1.ProviderModeMock:
	case v1alpha1.ProviderModeLive:
		if strings.TrimSpace(c.LiveAPIKey) == "" {
			return fmt.Errorf("providerType %q requires %s (or liveApiKey)", c.ProviderType, envLiveAPIKey)
		}
	case v1alpha1.ProviderModeReplay:
		if strings.TrimSpace(c.ReplayDatasetPath) == "" {
			return fmt.Errorf("providerType %q requires replayDatasetPath", c.ProviderType)
		}
	default:
		return fmt.Errorf("providerType %q is unknown (want %s, %s or %s)",
			c.ProviderType, v1alpha1.ProviderModeMock, v1alpha1.ProviderModeReplay, v1alpha1.ProviderModeLive)
	}
	if c.ReplayTimeMultiplier < 0 {
		return fmt.Errorf("replayTimeMultiplier must be >= 0")
	}
	switch c.ReplayLoop {
	case "", "linear", "cycle":
	default:
		return fmt.Errorf("replayLoop %q must be linear or cycle", c.ReplayLoop)
	}
	switch c.ReplayLookup {
	case "", "step", "linear":
	default:
		return fmt.Errorf("replayLookup %q must be step or linear", c.ReplayLookup)
	}
	return nil
}

// ListenAddr returns the TCP bind address for the HTTP listener.
func (c Config) ListenAddr() string {
	return ":" + strconv.Itoa(c.Port)
}

// LogValue implements slog.LogValuer and never emits the live API key.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("port", c.Port),
		slog.String("cacheTTL", c.CacheTTL.String()),
		slog.String("providerType", string(c.ProviderType)),
		slog.Bool("liveApiKeySet", strings.TrimSpace(c.LiveAPIKey) != ""),
		slog.String("replayDatasetPath", c.ReplayDatasetPath),
	)
}

func applyYAML(data []byte, cfg *Config) error {
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("line %d: expected 'key: value'", i+1)
		}
		key = strings.TrimSpace(key)
		value = unquoteYAML(stripInlineComment(strings.TrimSpace(value)))
		if err := setField(cfg, key, value); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	return nil
}

func applyEnv(cfg *Config, getenv LookupEnv) error {
	if v := strings.TrimSpace(getenv(envPort)); v != "" {
		if err := setField(cfg, "port", v); err != nil {
			return fmt.Errorf("%s: %w", envPort, err)
		}
	}
	if v := strings.TrimSpace(getenv(envCacheTTL)); v != "" {
		if err := setField(cfg, "cacheTTL", v); err != nil {
			return fmt.Errorf("%s: %w", envCacheTTL, err)
		}
	}
	if v := strings.TrimSpace(getenv(envProviderType)); v != "" {
		if err := setField(cfg, "providerType", v); err != nil {
			return fmt.Errorf("%s: %w", envProviderType, err)
		}
	}
	if v := getenv(envLiveAPIKey); v != "" {
		cfg.LiveAPIKey = v
	}
	if v := strings.TrimSpace(getenv(envReplayDatasetPath)); v != "" {
		cfg.ReplayDatasetPath = v
	}
	if v := strings.TrimSpace(getenv(envHTTPTimeout)); v != "" {
		if err := setField(cfg, "httpTimeout", v); err != nil {
			return fmt.Errorf("%s: %w", envHTTPTimeout, err)
		}
	}
	if v := strings.TrimSpace(getenv(envReplayTimeMultiplier)); v != "" {
		if err := setField(cfg, "replayTimeMultiplier", v); err != nil {
			return fmt.Errorf("%s: %w", envReplayTimeMultiplier, err)
		}
	}
	if v := strings.TrimSpace(getenv(envReplayLoop)); v != "" {
		if err := setField(cfg, "replayLoop", v); err != nil {
			return fmt.Errorf("%s: %w", envReplayLoop, err)
		}
	}
	if v := strings.TrimSpace(getenv(envReplayLookup)); v != "" {
		if err := setField(cfg, "replayLookup", v); err != nil {
			return fmt.Errorf("%s: %w", envReplayLookup, err)
		}
	}
	return nil
}

func setField(cfg *Config, key, value string) error {
	switch key {
	case "port":
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("port %q is not an integer", value)
		}
		cfg.Port = port
	case "cacheTTL":
		seconds, err := v1alpha1.ParseDurationSeconds(value)
		if err != nil {
			return fmt.Errorf("cacheTTL: %w", err)
		}
		if seconds == 0 {
			return fmt.Errorf("cacheTTL must be > 0s")
		}
		cfg.CacheTTL = time.Duration(seconds) * time.Second
	case "providerType":
		cfg.ProviderType = v1alpha1.ProviderMode(value)
	case "liveApiKey":
		cfg.LiveAPIKey = value
	case "replayDatasetPath":
		cfg.ReplayDatasetPath = value
	case "httpTimeout":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("httpTimeout %q: %w", value, err)
		}
		if d <= 0 {
			return fmt.Errorf("httpTimeout must be > 0")
		}
		cfg.HTTPTimeout = d
	case "replayTimeMultiplier":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("replayTimeMultiplier %q: %w", value, err)
		}
		cfg.ReplayTimeMultiplier = f
	case "replayLoop":
		cfg.ReplayLoop = value
	case "replayLookup":
		cfg.ReplayLookup = value
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	return nil
}

func stripInlineComment(value string) string {
	if value == "" {
		return value
	}
	if value[0] == '"' || value[0] == '\'' {
		return value
	}
	if i := strings.Index(value, " #"); i >= 0 {
		return strings.TrimSpace(value[:i])
	}
	return value
}

func unquoteYAML(value string) string {
	if len(value) >= 2 {
		if value[0] == '"' && value[len(value)-1] == '"' {
			return strings.ReplaceAll(value[1:len(value)-1], `\"`, `"`)
		}
		if value[0] == '\'' && value[len(value)-1] == '\'' {
			return value[1 : len(value)-1]
		}
	}
	return value
}
