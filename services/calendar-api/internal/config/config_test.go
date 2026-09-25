package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"APP_ENV", "DATABASE_URL", "JWT_SIGNING_KEY", "WEBHOOK_SECRET_KEY", "PUBLIC_BASE_URL",
		"REQUIRE_API_KEY", "ALLOW_PRIVATE_WEBHOOKS", "BOOTSTRAP_ADMIN_PASSWORD", "BOOTSTRAP_PUBLIC_KEY",
		"BOOTSTRAP_SERVER_KEY", "CORS_ALLOWED_ORIGINS", "TRUST_PROXY", "ADMIN_ALLOWED_CIDRS", "JWT_SIGNING_KEY_FILE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

var goodProduction = map[string]string{
	"APP_ENV":              "production",
	"DATABASE_URL":         "postgres://app:secret@db:5432/calendar?sslmode=require",
	"JWT_SIGNING_KEY":      "Qm9vdHN0cmFwLXJhbmRvbS1rZXktdGhhdC1pcy1sb25nLWVub3VnaA",
	"WEBHOOK_SECRET_KEY":   "YW5vdGhlci1yYW5kb20ta2V5LXRoYXQtaXMtbG9uZy1lbm91Z2gh",
	"PUBLIC_BASE_URL":      "https://calendar-api.example.org",
	"CORS_ALLOWED_ORIGINS": "https://www.example.org",
	"TRUST_PROXY":          "true",
	"ADMIN_ALLOWED_CIDRS":  "10.0.0.0/8, 192.0.2.7",
}

func TestProductionAcceptsHardenedConfig(t *testing.T) {
	setEnv(t, goodProduction)
	c, err := Load(true)
	if err != nil {
		t.Fatalf("hardened production config rejected: %v", err)
	}
	if !c.IsProduction() || len(c.AdminAllowedCIDRs) != 2 || c.ShutdownDrain == 0 || len(c.Warnings) != 0 {
		t.Fatalf("unexpected: %+v warnings=%v", c.AdminAllowedCIDRs, c.Warnings)
	}
	if c.AdminAllowedCIDRs[1].String() != "192.0.2.7/32" {
		t.Fatalf("single IPs become /32: %s", c.AdminAllowedCIDRs[1])
	}
}

func TestProductionRefusesDevelopmentShortcuts(t *testing.T) {
	cases := map[string]map[string]string{
		"dev JWT key":        {"JWT_SIGNING_KEY": "dev-only-jwt-signing-key-change-me-0123456789abcdef"},
		"dev webhook key":    {"WEBHOOK_SECRET_KEY": "dev-only-webhook-encryption-key-change-me-0123456789"},
		"dev admin password": {"BOOTSTRAP_ADMIN_PASSWORD": "change-me-please-now"},
		"dev API key":        {"BOOTSTRAP_PUBLIC_KEY": "pk_dev_local_public_key_0001"},
		"private webhooks":   {"ALLOW_PRIVATE_WEBHOOKS": "true"},
		"no API key":         {"REQUIRE_API_KEY": "false"},
		"http base URL":      {"PUBLIC_BASE_URL": "http://calendar-api.example.org"},
		"bad CIDR":           {"ADMIN_ALLOWED_CIDRS": "10.0.0.0/33"},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range goodProduction {
				env[k] = v
			}
			for k, v := range override {
				env[k] = v
			}
			setEnv(t, env)
			if _, err := Load(true); err == nil {
				t.Fatal("expected production to refuse this configuration")
			}
		})
	}
}

func TestDevelopmentAllowsDefaultsButStagingDoesNot(t *testing.T) {
	dev := map[string]string{"DATABASE_URL": "postgres://x", "JWT_SIGNING_KEY": "dev-only-jwt-signing-key-change-me-0123456789abcdef",
		"WEBHOOK_SECRET_KEY": "dev-only-webhook-encryption-key-change-me-0123456789", "ALLOW_PRIVATE_WEBHOOKS": "true"}
	setEnv(t, dev)
	if _, err := Load(true); err != nil {
		t.Fatalf("development must accept dev defaults: %v", err)
	}
	dev["APP_ENV"] = "staging"
	setEnv(t, dev)
	if _, err := Load(true); err == nil {
		t.Fatal("staging must refuse dev defaults")
	}
}

func TestSecretsFromFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwt")
	secret := strings.Repeat("s", 40)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for k, v := range goodProduction {
		env[k] = v
	}
	delete(env, "JWT_SIGNING_KEY")
	env["JWT_SIGNING_KEY_FILE"] = path
	setEnv(t, env)
	c, err := Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.JWTSigningKey) != secret {
		t.Fatalf("secret file not read (or not trimmed): %q", c.JWTSigningKey)
	}
	env["JWT_SIGNING_KEY_FILE"] = filepath.Join(dir, "missing")
	setEnv(t, env)
	if _, err := Load(true); err == nil {
		t.Fatal("a missing secret file must be an error")
	}
}

func TestWarnings(t *testing.T) {
	env := map[string]string{}
	for k, v := range goodProduction {
		env[k] = v
	}
	env["CORS_ALLOWED_ORIGINS"] = "*"
	delete(env, "TRUST_PROXY")
	delete(env, "ADMIN_ALLOWED_CIDRS")
	setEnv(t, env)
	c, err := Load(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 3 {
		t.Fatalf("want 3 warnings (CORS, proxy, admin CIDRs), got %v", c.Warnings)
	}
}
