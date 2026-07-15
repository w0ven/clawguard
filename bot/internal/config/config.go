package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	AppEnv                   string  `env:"APP_ENV" envDefault:"development"`
	HTTPPort                 int     `env:"HTTP_PORT" envDefault:"8080"`
	BotToken                 string  `env:"BOT_TOKEN,required"`
	BotUsername              string  `env:"BOT_USERNAME,required"`
	TelegramLoginBotUsername string  `env:"TELEGRAM_LOGIN_BOT_USERNAME,required"`
	WebhookSecret            string  `env:"WEBHOOK_SECRET,required"`
	PublicBaseURL            string  `env:"PUBLIC_BASE_URL,required"`
	WebBaseURL               string  `env:"WEB_BASE_URL"`
	DailyReportEnabled       bool    `env:"DAILY_REPORT_ENABLED" envDefault:"true"`
	LogRawUpdates            bool    `env:"CLAWGUARD_LOG_RAW_UPDATES" envDefault:"false"`
	SuperAdminIDs            []int64 `env:"SUPER_ADMIN_IDS" envSeparator:","`
	AdminTelegramIDs         []int64 `env:"ADMIN_TELEGRAM_IDS" envSeparator:","`
	PostgresHost             string  `env:"POSTGRES_HOST,required"`
	PostgresPort             int     `env:"POSTGRES_PORT" envDefault:"5432"`
	PostgresDB               string  `env:"POSTGRES_DB,required"`
	PostgresUser             string  `env:"POSTGRES_USER,required"`
	PostgresPassword         string  `env:"POSTGRES_PASSWORD,required"`
	RedisHost                string  `env:"REDIS_HOST,required"`
	RedisPort                int     `env:"REDIS_PORT" envDefault:"6379"`
	RedisPassword            string  `env:"REDIS_PASSWORD"`
	JWTSecret                string  `env:"JWT_SECRET,required"`
	TurnstileSiteKey         string  `env:"TURNSTILE_SITE_KEY"`
	TurnstileSecret          string  `env:"TURNSTILE_SECRET_KEY"`
	EncryptionKey            string  `env:"ENCRYPTION_KEY"`
	LLMProviders             string  `env:"LLM_PROVIDERS"`
	Retention                RetentionConfig
}

type RetentionConfig struct {
	ViolationsDays       int `env:"RETENTION_VIOLATIONS_DAYS" envDefault:"90"`
	AIDecisionsDays      int `env:"RETENTION_AI_DECISIONS_DAYS" envDefault:"90"`
	ConfigAuditDays      int `env:"RETENTION_CONFIG_AUDIT_DAYS" envDefault:"365"`
	ProfileCheckLogsDays int `env:"RETENTION_PROFILE_CHECK_LOGS_DAYS" envDefault:"90"`
	ZombieDays           int `env:"RETENTION_ZOMBIE_DAYS" envDefault:"30"`
	BannedDays           int `env:"RETENTION_BANNED_DAYS" envDefault:"90"`
}

type LLMProvider struct {
	Name      string   `json:"name"`
	BaseURL   string   `json:"base_url"`
	APIKeyEnv string   `json:"api_key_env"`
	Models    []string `json:"models"`
}

func Load() (Config, error) {
	loadDotenv()

	cfg := Config{}
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse env: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	publicURL, err := url.Parse(strings.TrimSpace(c.PublicBaseURL))
	if err != nil || publicURL.Host == "" || (publicURL.Scheme != "http" && publicURL.Scheme != "https") {
		return errors.New("PUBLIC_BASE_URL must be an absolute HTTP(S) URL")
	}

	if !strings.EqualFold(strings.TrimSpace(c.AppEnv), "production") {
		return nil
	}
	if publicURL.Scheme != "https" {
		return errors.New("PUBLIC_BASE_URL must use HTTPS in production")
	}
	for _, secret := range []struct {
		name  string
		value string
	}{
		{name: "JWT_SECRET", value: c.JWTSecret},
		{name: "WEBHOOK_SECRET", value: c.WebhookSecret},
		{name: "ENCRYPTION_KEY", value: c.EncryptionKey},
	} {
		value := strings.TrimSpace(secret.value)
		if len(value) < 32 {
			return fmt.Errorf("%s must contain at least 32 characters in production", secret.name)
		}
		if isPlaceholderSecret(value) {
			return fmt.Errorf("%s must not use a placeholder value in production", secret.name)
		}
	}
	return nil
}

func isPlaceholderSecret(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, marker := range []string{"change-me", "changeme", "replace-me", "your-secret", "example-secret"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func (c Config) DatabaseURL() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s",
		c.PostgresUser,
		c.PostgresPassword,
		c.PostgresHost,
		c.PostgresPort,
		c.PostgresDB,
	)
}

func (c Config) RedisAddr() string {
	return fmt.Sprintf("%s:%d", c.RedisHost, c.RedisPort)
}

func (c Config) ListenAddr() string {
	return fmt.Sprintf(":%d", c.HTTPPort)
}

func (c Config) WebhookURL() string {
	return strings.TrimRight(c.PublicBaseURL, "/") + "/webhook/" + c.WebhookSecret
}

func (c Config) AdminWebBaseURL() string {
	if strings.TrimSpace(c.WebBaseURL) != "" {
		return strings.TrimRight(c.WebBaseURL, "/")
	}
	return strings.TrimRight(c.PublicBaseURL, "/")
}

func (c Config) ParseLLMProviders() ([]LLMProvider, error) {
	raw := strings.TrimSpace(c.LLMProviders)
	if raw == "" {
		return nil, nil
	}

	var providers []LLMProvider
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		return nil, fmt.Errorf("parse LLM_PROVIDERS: %w", err)
	}
	for index := range providers {
		providers[index].Name = strings.TrimSpace(providers[index].Name)
		providers[index].BaseURL = strings.TrimSpace(providers[index].BaseURL)
		providers[index].APIKeyEnv = strings.TrimSpace(providers[index].APIKeyEnv)
	}
	return providers, nil
}

func loadDotenv() {
	candidates := []string{
		".env",
		filepath.Join("..", ".env"),
		filepath.Join("..", "..", ".env"),
	}

	for _, path := range candidates {
		if err := loadDotenvFile(path); err == nil {
			return
		}
	}
}

func loadDotenvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			return errors.New("invalid .env line")
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if _, exists := os.LookupEnv(key); exists {
			continue
		}

		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}

	return scanner.Err()
}
