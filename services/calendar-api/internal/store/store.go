// Package store holds database plumbing shared by all services: the connection
// pool, transactions, migrations and the resource version counters behind the manifest.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Querier is satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Options configure the connection pool.
type Options struct {
	URL              string
	MaxConns         int32         // 0 keeps the URL/default value (at least 10)
	StatementTimeout time.Duration // 0 disables; protects the pool from runaway queries
}

// Open connects to Postgres, retrying until ctx is done so containers can start in any order.
func Open(ctx context.Context, o Options, log *slog.Logger) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(o.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	if o.MaxConns > 0 {
		cfg.MaxConns = o.MaxConns
	} else if cfg.MaxConns < 10 {
		cfg.MaxConns = 10
	}
	if o.StatementTimeout > 0 {
		ms := strconv.FormatInt(o.StatementTimeout.Milliseconds(), 10)
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = ms
		cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt(2*o.StatementTimeout.Milliseconds(), 10)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "calendar-api"
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.MaxConnLifetime = time.Hour
	cfg.HealthCheckPeriod = 30 * time.Second

	backoff := 500 * time.Millisecond
	for attempt := 1; ; attempt++ {
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = pool.Ping(pingCtx)
			cancel()
			if err == nil {
				return pool, nil
			}
			pool.Close()
		}
		log.Warn("database not ready, retrying", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connect to database: %w", err)
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

// InTx runs fn in a transaction, committing on nil and rolling back on error or panic.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, fn)
}

// Migrate applies (or reports) migrations. direction is "up", "down" (one step) or "status".
// A Postgres advisory lock makes concurrent runs safe.
func Migrate(ctx context.Context, url string, direction string, log *slog.Logger) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer db.Close()
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub, goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	switch direction {
	case "up":
		res, err := p.Up(ctx)
		for _, r := range res {
			log.Info("migration applied", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration.String())
		}
		return err
	case "down":
		r, err := p.Down(ctx)
		if r != nil {
			log.Info("migration rolled back", "version", r.Source.Version)
		}
		return err
	case "status":
		st, err := p.Status(ctx)
		for _, s := range st {
			log.Info("migration", "version", s.Source.Version, "file", s.Source.Path, "state", string(s.State))
		}
		return err
	}
	return fmt.Errorf("unknown migrate direction %q (want up, down or status)", direction)
}

// BumpVersion increments a resource version counter and returns the new value.
func BumpVersion(ctx context.Context, q Querier, scope string) (int64, error) {
	var v int64
	err := q.QueryRow(ctx, `
		INSERT INTO resource_versions (scope, version) VALUES ($1, 1)
		ON CONFLICT (scope) DO UPDATE SET version = resource_versions.version + 1, updated_at = now()
		RETURNING version`, scope).Scan(&v)
	return v, err
}

// Versions returns the counters for the given scopes (missing scopes are 0).
func Versions(ctx context.Context, q Querier, scopes ...string) (map[string]int64, error) {
	out := make(map[string]int64, len(scopes))
	for _, s := range scopes {
		out[s] = 0
	}
	rows, err := q.Query(ctx, `SELECT scope, version FROM resource_versions WHERE scope = ANY($1)`, scopes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var v int64
		if err := rows.Scan(&s, &v); err != nil {
			return nil, err
		}
		out[s] = v
	}
	return out, rows.Err()
}

// VersionsWithPrefix returns every counter whose scope starts with prefix.
func VersionsWithPrefix(ctx context.Context, q Querier, prefix string) (map[string]int64, error) {
	rows, err := q.Query(ctx, `SELECT scope, version FROM resource_versions WHERE starts_with(scope, $1)`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var s string
		var v int64
		if err := rows.Scan(&s, &v); err != nil {
			return nil, err
		}
		out[s] = v
	}
	return out, rows.Err()
}

// IsNoRows reports whether err is pgx.ErrNoRows.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// EscapeLike escapes % and _ for use in LIKE/ILIKE patterns.
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// LatestMigration returns the highest migration version embedded in this binary.
func LatestMigration() (int64, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			continue
		}
		v, err := strconv.ParseInt(num, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %s: bad version prefix", e.Name())
		}
		latest = max(latest, v)
	}
	return latest, nil
}

// CheckSchema fails when the database is behind this binary. It is read-only (it never
// creates goose's table), so it works with the least-privileged application role.
func CheckSchema(ctx context.Context, q Querier) error {
	want, err := LatestMigration()
	if err != nil {
		return err
	}
	var have int64
	err = q.QueryRow(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&have)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table
		return fmt.Errorf("database has no schema; run `calendar-api bootstrap` first")
	}
	if err != nil {
		return err
	}
	switch {
	case have < want:
		return fmt.Errorf("database schema is at migration %d but this build needs %d; run `calendar-api bootstrap` (or `migrate up`) before starting the new version", have, want)
	case have > want:
		// A newer build migrated already (expand/contract keeps old builds working): allowed.
		return nil
	}
	return nil
}
