// Package config loads service configuration from environment variables.
// Every variable is documented in .env.example (development) and
// deploy/compose/.env.production.example (production).
//
// Any variable can instead be read from a file by setting NAME_FILE (for example
// JWT_SIGNING_KEY_FILE=/run/secrets/jwt), which is how Docker secrets are mounted.
package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environments.
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Config is the full service configuration.
type Config struct {
	AppEnv string

	HTTPAddr             string
	MetricsAddr          string
	DatabaseURL          string
	MigrationDatabaseURL string // owner role for migrations; defaults to DatabaseURL
	DBMaxConns           int32
	DBStatementTimeout   time.Duration

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
	AdminAllowedCIDRs  []netip.Prefix
	RequestTimeout     time.Duration
	ShutdownDrain      time.Duration

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

	// Warnings are non-fatal findings (logged at startup).
	Warnings []string
}

// IsProduction reports whether APP_ENV is production.
func (c Config) IsProduction() bool { return c.AppEnv == EnvProduction }

// Known development defaults that must never reach production.
var devMarkers = []string{"dev-only", "change-me", "_dev_", "example.com"}

// Load reads configuration. When forServe is true, secrets needed by the HTTP server are required.
func Load(forServe bool) (Config, error) {
	l := &loader{}
	c := Config{
		AppEnv:                 strings.ToLower(l.env("APP_ENV", EnvDevelopment)),
		HTTPAddr:               l.env("HTTP_ADDR", ":8080"),
		MetricsAddr:            l.env("METRICS_ADDR", ":9090"),
		DatabaseURL:            l.env("DATABASE_URL", ""),
		LogFormat:              l.env("LOG_FORMAT", "json"),
		JWTIssuer:              l.env("JWT_ISSUER", "calendar-api"),
		PublicBaseURL:          strings.TrimRight(l.env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		CORSAllowedOrigins:     list(l.env("CORS_ALLOWED_ORIGINS", "*")),
		MinSupportedClient:     l.env("MIN_SUPPORTED_CLIENT", "0.0.0"),
		DefaultTenantSlug:      l.env("DEFAULT_TENANT_SLUG", "default"),
		BootstrapAdminEmail:    strings.ToLower(l.env("BOOTSTRAP_ADMIN_EMAIL", "")),
		BootstrapAdminPassword: l.env("BOOTSTRAP_ADMIN_PASSWORD", ""),
		BootstrapPublicKey:     l.env("BOOTSTRAP_PUBLIC_KEY", ""),
		BootstrapServerKey:     l.env("BOOTSTRAP_SERVER_KEY", ""),
		CDNPurgeURL:            l.env("CDN_PURGE_URL", ""),
		CDNPurgeToken:          l.env("CDN_PURGE_TOKEN", ""),
	}
	c.MigrationDatabaseURL = l.env("MIGRATION_DATABASE_URL", c.DatabaseURL)
	switch c.AppEnv {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		l.fail("APP_ENV must be development, staging or production, got %q", c.AppEnv)
	}
	var err error
	if c.LogLevel, err = level(l.env("LOG_LEVEL", "info")); err != nil {
		l.errs = append(l.errs, err)
	}
	c.AccessTTL = l.duration("JWT_ACCESS_TTL", 15*time.Minute)
	c.RefreshTTL = l.duration("JWT_REFRESH_TTL", 30*24*time.Hour)
	c.WorkerPollInterval = l.duration("WORKER_POLL_INTERVAL", time.Second)
	c.DBStatementTimeout = l.duration("DB_STATEMENT_TIMEOUT", 15*time.Second)
	c.RequestTimeout = l.duration("REQUEST_TIMEOUT", 30*time.Second)
	drain := time.Duration(0)
	if c.AppEnv != EnvDevelopment {
		drain = 5 * time.Second // let load balancers see /readyz fail before connections close
	}
	c.ShutdownDrain = l.durationOrZero("SHUTDOWN_DRAIN", drain)
	c.DBMaxConns = int32(l.integer("DB_MAX_CONNS", 10, 2, 200))
	c.RequireAPIKey = l.boolean("REQUIRE_API_KEY", true)
	c.TrustProxy = l.boolean("TRUST_PROXY", false)
	c.AllowPrivateWebhooks = l.boolean("ALLOW_PRIVATE_WEBHOOKS", false)
	c.WorkerInProcess = l.boolean("WORKER_IN_PROCESS", false)
	for _, s := range list(l.env("ADMIN_ALLOWED_CIDRS", "")) {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			if a, aerr := netip.ParseAddr(s); aerr == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			} else {
				l.fail("ADMIN_ALLOWED_CIDRS: invalid CIDR %q", s)
				continue
			}
		}
		c.AdminAllowedCIDRs = append(c.AdminAllowedCIDRs, p.Masked())
	}

	if c.DatabaseURL == "" {
		l.fail("DATABASE_URL is required")
	}
	jwtKey := l.env("JWT_SIGNING_KEY", "")
	if forServe {
		if len(jwtKey) < 32 {
			l.fail("JWT_SIGNING_KEY must be at least 32 characters")
		}
		c.JWTSigningKey = []byte(jwtKey)
	}
	whKey := l.env("WEBHOOK_SECRET_KEY", "")
	if whKey != "" {
		if len(whKey) < 32 {
			l.fail("WEBHOOK_SECRET_KEY must be at least 32 characters")
		}
		k := sha256.Sum256([]byte(whKey))
		c.WebhookEncKey = k[:]
	} else if forServe {
		l.fail("WEBHOOK_SECRET_KEY is required (used to encrypt webhook secrets at rest)")
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		l.fail("LOG_FORMAT must be json or text, got %q", c.LogFormat)
	}
	if c.AppEnv != EnvDevelopment {
		c.checkDeployed(l, jwtKey, whKey)
	}
	c.Warnings = l.warnings
	return c, errors.Join(l.errs...)
}

// checkDeployed refuses development shortcuts outside development (fail closed).
func (c *Config) checkDeployed(l *loader, jwtKey, whKey string) {
	hasDev := func(v string) bool {
		lv := strings.ToLower(v)
		for _, m := range devMarkers {
			if strings.Contains(lv, m) {
				return true
			}
		}
		return false
	}
	if hasDev(jwtKey) {
		l.fail("JWT_SIGNING_KEY looks like a development value; generate one with: openssl rand -base64 48")
	}
	if hasDev(whKey) {
		l.fail("WEBHOOK_SECRET_KEY looks like a development value; generate one with: openssl rand -base64 48")
	}
	if c.BootstrapAdminPassword != "" && hasDev(c.BootstrapAdminPassword) {
		l.fail("BOOTSTRAP_ADMIN_PASSWORD is a development default")
	}
	if hasDev(c.BootstrapPublicKey) || hasDev(c.BootstrapServerKey) {
		l.fail("BOOTSTRAP_PUBLIC_KEY / BOOTSTRAP_SERVER_KEY are development keys; issue keys through the admin API instead")
	}
	if c.AllowPrivateWebhooks {
		l.fail("ALLOW_PRIVATE_WEBHOOKS must be false outside development (SSRF protection)")
	}
	if !c.RequireAPIKey {
		l.fail("REQUIRE_API_KEY must be true outside development")
	}
	if u, err := url.Parse(c.PublicBaseURL); err != nil || u.Scheme != "https" {
		l.fail("PUBLIC_BASE_URL must be an https URL outside development, got %q", c.PublicBaseURL)
	}
	if len(c.CORSAllowedOrigins) == 1 && c.CORSAllowedOrigins[0] == "*" {
		l.warn("CORS_ALLOWED_ORIGINS is *; list your web origins explicitly")
	}
	if !c.TrustProxy {
		l.warn("TRUST_PROXY is false; behind a load balancer every client will appear to have the proxy's IP (rate limits and audit IPs)")
	}
	if strings.Contains(c.DatabaseURL, "sslmode=disable") {
		l.warn("DATABASE_URL uses sslmode=disable; use sslmode=require (or verify-full) unless the database is on a private network")
	}
	if len(c.AdminAllowedCIDRs) == 0 && c.IsProduction() {
		l.warn("ADMIN_ALLOWED_CIDRS is empty; the admin API is reachable from any network")
	}
}

// Logger builds the process logger.
func (c Config) Logger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

type loader struct {
	errs     []error
	warnings []string
}

func (l *loader) fail(format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf(format, args...))
}
func (l *loader) warn(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

// env reads NAME, or the contents of the file named by NAME_FILE.
func (l *loader) env(k, def string) string {
	if path, ok := os.LookupEnv(k + "_FILE"); ok && strings.TrimSpace(path) != "" {
		b, err := os.ReadFile(strings.TrimSpace(path))
		if err != nil {
			l.fail("%s_FILE: %v", k, err)
			return def
		}
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
		return def
	}
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func (l *loader) duration(k string, def time.Duration) time.Duration {
	d := l.durationOrZero(k, def)
	if d <= 0 {
		l.fail("%s must be positive", k)
		return def
	}
	return d
}

func (l *loader) durationOrZero(k string, def time.Duration) time.Duration {
	v := l.env(k, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		l.fail("%s: invalid duration %q", k, v)
		return def
	}
	return d
}

func (l *loader) boolean(k string, def bool) bool {
	v := l.env(k, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail("%s: invalid boolean %q", k, v)
		return def
	}
	return b
}

func (l *loader) integer(k string, def, lo, hi int) int {
	v := l.env(k, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		l.fail("%s must be an integer from %d to %d, got %q", k, lo, hi, v)
		return def
	}
	return n
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

func level(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	return l, nil
}
