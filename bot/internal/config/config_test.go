package config

import (
	"strings"
	"testing"
)

func productionConfig() Config {
	return Config{
		AppEnv:        "production",
		PublicBaseURL: "https://guard.example.test",
		JWTSecret:     strings.Repeat("j", 32),
		WebhookSecret: strings.Repeat("w", 32),
		EncryptionKey: strings.Repeat("e", 32),
	}
}

func TestConfigValidateProductionSecrets(t *testing.T) {
	cfg := productionConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "http", mutate: func(c *Config) { c.PublicBaseURL = "http://guard.example.test" }, want: "HTTPS"},
		{name: "short jwt", mutate: func(c *Config) { c.JWTSecret = "short" }, want: "JWT_SECRET"},
		{name: "missing encryption", mutate: func(c *Config) { c.EncryptionKey = "" }, want: "ENCRYPTION_KEY"},
		{name: "placeholder", mutate: func(c *Config) { c.WebhookSecret = "change-me-change-me-change-me-change-me" }, want: "placeholder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := cfg
			tt.mutate(&candidate)
			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestConfigValidateDevelopmentAllowsHTTPAndShortSecrets(t *testing.T) {
	cfg := Config{AppEnv: "development", PublicBaseURL: "http://localhost:8080"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
