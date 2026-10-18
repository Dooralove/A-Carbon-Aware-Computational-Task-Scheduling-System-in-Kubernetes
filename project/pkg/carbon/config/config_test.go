package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

func TestLoadYAMLAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte("port: 8080\ncacheTTL: 30s\nproviderType: mock\nliveApiKey: \"\"\nreplayDatasetPath: \"\"\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path, func(key string) string {
		if key == "CARBON_PORT" {
			return "9090"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9090 {
		t.Fatalf("port: got %d want 9090", cfg.Port)
	}
	if cfg.CacheTTL != 30*time.Second {
		t.Fatalf("cacheTTL: got %s", cfg.CacheTTL)
	}
	if cfg.ProviderType != v1alpha1.ProviderModeMock {
		t.Fatalf("providerType: got %s", cfg.ProviderType)
	}
}

func TestLoadRejectsBareCacheTTL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8080\ncacheTTL: 30\nproviderType: mock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, func(string) string { return "" }); err == nil {
		t.Fatal("bare cacheTTL integer must be rejected")
	}
}

func TestValidateLiveRequiresKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8080\ncacheTTL: 30s\nproviderType: live\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, func(string) string { return "" }); err == nil {
		t.Fatal("live without API key must fail")
	}
}

func TestLogValueRedactsAPIKey(t *testing.T) {
	cfg := Config{
		Port:         8080,
		CacheTTL:     DefaultCacheTTL,
		ProviderType: v1alpha1.ProviderModeLive,
		LiveAPIKey:   "super-secret",
	}
	var b strings.Builder
	logger := slog.New(slog.NewTextHandler(&b, nil))
	logger.Info("cfg", "config", cfg)
	out := b.String()
	if strings.Contains(out, "super-secret") {
		t.Fatalf("api key leaked: %s", out)
	}
	if !strings.Contains(out, "liveApiKeySet=true") {
		t.Fatalf("expected liveApiKeySet: %s", out)
	}
}
