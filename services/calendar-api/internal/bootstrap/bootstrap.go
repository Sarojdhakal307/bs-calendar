// Package bootstrap prepares a database: migrations, the default tenant, the seed year
// table, default categories, the first super admin and optional development API keys.
// Every step is idempotent, so it is safe to run on every deploy.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"bscalendar/fixtures"
	"bscalendar/services/calendar-api/internal/audit"
	"bscalendar/services/calendar-api/internal/auth"
	"bscalendar/services/calendar-api/internal/bscal"
	"bscalendar/services/calendar-api/internal/calendardata"
	"bscalendar/services/calendar-api/internal/config"
	"bscalendar/services/calendar-api/internal/events"
	"bscalendar/services/calendar-api/internal/store"
)

// Run migrates and seeds the database.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	pool, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.Migrate(ctx, cfg.DatabaseURL, "up", log); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return Seed(ctx, pool, cfg, log)
}

// Seed performs the idempotent data steps (used by Run and by tests).
func Seed(ctx context.Context, pool *pgxpool.Pool, cfg config.Config, log *slog.Logger) error {
	tenantID, err := EnsureTenant(ctx, pool, cfg.DefaultTenantSlug)
	if err != nil {
		return fmt.Errorf("tenant: %w", err)
	}
	years, err := bscal.LoadSeed(fixtures.YearTableSeed)
	if err != nil {
		return fmt.Errorf("seed file: %w", err)
	}
	seeded, err := calendardata.Seed(ctx, pool, years)
	if err != nil {
		return fmt.Errorf("seed year table: %w", err)
	}
	log.Info("year table", "seeded", seeded, "years", len(years))
	n, err := events.EnsureDefaultCategories(ctx, pool, tenantID)
	if err != nil {
		return fmt.Errorf("categories: %w", err)
	}
	log.Info("default categories", "created", n)

	svc := auth.NewService(pool, nil, 0)
	if cfg.BootstrapAdminEmail != "" {
		created, err := svc.EnsureSuperAdmin(ctx, tenantID, cfg.BootstrapAdminEmail, cfg.BootstrapAdminPassword)
		if err != nil {
			return fmt.Errorf("bootstrap admin: %w", err)
		}
		log.Info("bootstrap super admin", "email", cfg.BootstrapAdminEmail, "created", created)
	}
	actor := audit.Actor{TenantID: tenantID}
	for kind, key := range map[string]string{"public": cfg.BootstrapPublicKey, "server": cfg.BootstrapServerKey} {
		if key == "" {
			continue
		}
		prefix := map[string]string{"public": "pk_", "server": "sk_"}[kind]
		if !strings.HasPrefix(key, prefix) || len(key) < 20 {
			return fmt.Errorf("BOOTSTRAP_%s_KEY must start with %s and be at least 20 characters", strings.ToUpper(kind), prefix)
		}
		created, err := svc.EnsureClient(ctx, actor, "bootstrap "+kind+" key", kind, key)
		if err != nil {
			return fmt.Errorf("bootstrap %s key: %w", kind, err)
		}
		log.Info("bootstrap API key", "kind", kind, "prefix", key[:min(11, len(key))], "created", created)
	}
	return nil
}

// EnsureTenant creates the tenant if missing and returns its id.
func EnsureTenant(ctx context.Context, pool *pgxpool.Pool, slug string) (string, error) {
	if _, err := pool.Exec(ctx, `INSERT INTO tenants (slug, name) VALUES ($1, $1) ON CONFLICT (slug) DO NOTHING`, slug); err != nil {
		return "", err
	}
	return TenantID(ctx, pool, slug)
}

// TenantID looks up a tenant by slug.
func TenantID(ctx context.Context, pool *pgxpool.Pool, slug string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug = $1`, slug).Scan(&id)
	if store.IsNoRows(err) {
		return "", fmt.Errorf("tenant %q not found; run `calendar-api bootstrap` first", slug)
	}
	return id, err
}
