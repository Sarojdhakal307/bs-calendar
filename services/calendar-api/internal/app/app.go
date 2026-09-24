// Package app wires the services together. The production server and the flow tests both
// use it, so tests exercise exactly the same object graph that runs in production.
package app

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"bscalendar/services/calendar-api/internal/auth"
	"bscalendar/services/calendar-api/internal/bootstrap"
	"bscalendar/services/calendar-api/internal/calendardata"
	"bscalendar/services/calendar-api/internal/config"
	"bscalendar/services/calendar-api/internal/events"
	"bscalendar/services/calendar-api/internal/httpapi"
	"bscalendar/services/calendar-api/internal/outbox"
	"bscalendar/services/calendar-api/internal/uiconfig"
)

// App is the assembled service.
type App struct {
	Server  *httpapi.Server
	Data    *calendardata.Service
	Metrics *httpapi.Metrics
}

// New loads the year table and builds the HTTP server.
func New(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger, version string) (*App, error) {
	tenantID, err := bootstrap.TenantID(ctx, pool, cfg.DefaultTenantSlug)
	if err != nil {
		return nil, err
	}
	purge := cfg.CDNPurgeURL != ""
	metrics := httpapi.NewMetrics()
	data := calendardata.NewService(pool, log, cfg.PublicBaseURL, purge)
	data.OnReload = metrics.SetDataVersion
	if err := data.Load(ctx); err != nil {
		return nil, err
	}
	ui, err := uiconfig.NewService(pool, cfg.PublicBaseURL, purge)
	if err != nil {
		return nil, err
	}
	srv := httpapi.New(httpapi.Deps{
		Config: cfg, Log: log, Pool: pool,
		Auth:     auth.NewService(pool, auth.NewTokenManager(cfg.JWTSigningKey, cfg.JWTIssuer, cfg.AccessTTL), cfg.RefreshTTL),
		Data:     data,
		Events:   events.NewService(pool, data, cfg.PublicBaseURL, purge),
		UI:       ui,
		Webhooks: outbox.NewManager(pool, cfg.WebhookEncKey, cfg.AllowPrivateWebhooks),
		Metrics:  metrics, DefaultTenantID: tenantID, BuildVersion: version,
	})
	return &App{Server: srv, Data: data, Metrics: metrics}, nil
}

// NewWorker builds the outbox worker with metrics hooks.
func NewWorker(pool *pgxpool.Pool, cfg config.Config, log *slog.Logger, m *httpapi.Metrics) *outbox.Worker {
	w := outbox.NewWorker(pool, outbox.WorkerConfig{EncKey: cfg.WebhookEncKey, PurgeURL: cfg.CDNPurgeURL, PurgeToken: cfg.CDNPurgeToken,
		Interval: cfg.WorkerPollInterval, AllowPrivate: cfg.AllowPrivateWebhooks}, log.With("component", "worker"))
	w.OnResult, w.OnBacklog = m.OutboxResult, m.OutboxBacklog
	return w
}
