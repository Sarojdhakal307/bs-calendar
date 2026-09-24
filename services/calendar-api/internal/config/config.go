// Package config loads service configuration from environment variables.
// Every variable is documented in .env.example at the repository root.
package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the full service configuration.
type Config struct {
	HTTPAddr    string
	MetricsAddr string
	DatabaseURL string

	LogLevel  slog.Level
	LogFormat string // "json" or "text"

	JWTSigningKey []byte
	JWTIssuer     string
	AccessTTL     time.Duration
	RefreshTTL    time.Duration

	PublicBaseURL      string
	CORSAllowedOrigins []string
	RequireAPIKey      bool
	TrustProxy         bool
	MinSupportedClient string

	DefaultTenantSlug      string
	BootstrapAdminEmail    string
	BootstrapAdminPassword string
	BootstrapPublicKey     string
	BootstrapServerKey     string

	WebhookEncKey        []byte // 32 bytes, derived from WEBHOOK_SECRET_KEY
	AllowPrivateWebhooks bool
	CDNPurgeURL          string
	CDNPurgeToken        string

	WorkerInProcess    bool
	WorkerPollInterval time.Duration
}

// Load reads configuration. When forServe is true, secrets needed by the HTTP server are required.
func Load(forServe bool) (Config, error) {
	var errs []error
	c := Config{
		HTTPAddr:               env("HTTP_ADDR", ":8080"),
		MetricsAddr:            env("METRICS_ADDR", ":9090"),
		DatabaseURL:            env("DATABASE_URL", ""),
		LogFormat:              env("LOG_FORMAT", "json"),
		JWTIssuer:              env("JWT_ISSUER", "calendar-api"),
		PublicBaseURL:          strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		CORSAllowedOrigins:     list(env("CORS_ALLOWED_ORIGINS", "*")),
		MinSupportedClient:     env("MIN_SUPPORTED_CLIENT", "0.0.0"),
		DefaultTenantSlug:      env("DEFAULT_TENANT_SLUG", "default"),
		BootstrapAdminEmail:    strings.ToLower(env("BOOTSTRAP_ADMIN_EMAIL", "")),
		BootstrapAdminPassword: env("BOOTSTRAP_ADMIN_PASSWORD", ""),
		BootstrapPublicKey:     env("BOOTSTRAP_PUBLIC_KEY", ""),
		BootstrapServerKey:     env("BOOTSTRAP_SERVER_KEY", ""),
		CDNPurgeURL:            env("CDN_PURGE_URL", ""),
		CDNPurgeToken:          env("CDN_PURGE_TOKEN", ""),
	}
	var err error
	if c.LogLevel, err = level(env("LOG_LEVEL", "info")); err != nil {
		errs = append(errs, err)
	}
	c.AccessTTL = duration("JWT_ACCESS_TTL", 15*time.Minute, &errs)
	c.RefreshTTL = duration("JWT_REFRESH_TTL", 30*24*time.Hour, &errs)
	c.WorkerPollInterval = duration("WORKER_POLL_INTERVAL", time.Second, &errs)
	c.RequireAPIKey = boolean("REQUIRE_API_KEY", true, &errs)
	c.TrustProxy = boolean("TRUST_PROXY", false, &errs)
	c.AllowPrivateWebhooks = boolean("ALLOW_PRIVATE_WEBHOOKS", false, &errs)
	c.WorkerInProcess = boolean("WORKER_IN_PROCESS", false, &errs)

	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if forServe {
		key := env("JWT_SIGNING_KEY", "")
		if len(key) < 32 {
			errs = append(errs, errors.New("JWT_SIGNING_KEY must be at least 32 characters"))
		}
		c.JWTSigningKey = []byte(key)
	}
	if s := env("WEBHOOK_SECRET_KEY", ""); s != "" {
		if len(s) < 32 {
			errs = append(errs, errors.New("WEBHOOK_SECRET_KEY must be at least 32 characters"))
		}
		k := sha256.Sum256([]byte(s))
		c.WebhookEncKey = k[:]
	} else if forServe {
		errs = append(errs, errors.New("WEBHOOK_SECRET_KEY is required (used to encrypt webhook secrets at rest)"))
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text, got %q", c.LogFormat))
	}
	return c, errors.Join(errs...)
}

// Logger builds the process logger.
func (c Config) Logger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func duration(k string, def time.Duration, errs *[]error) time.Duration {
	v := env(k, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		*errs = append(*errs, fmt.Errorf("%s: invalid duration %q", k, v))
		return def
	}
	return d
}

func boolean(k string, def bool, errs *[]error) bool {
	v := env(k, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: invalid boolean %q", k, v))
		return def
	}
	return b
}

func level(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	return l, nil
}
