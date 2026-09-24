// Command calendar-api is the BS/AD calendar service. One binary, several subcommands:
//
//	calendar-api serve              HTTP API (plus /metrics on METRICS_ADDR)
//	calendar-api worker             outbox worker (webhooks, CDN purges)
//	calendar-api bootstrap          migrate + seed (idempotent; run on every deploy)
//	calendar-api migrate up|down|status
//	calendar-api convert AD|BS YYYY-MM-DD   offline conversion with the embedded seed table
//	calendar-api dump               every supported day as CSV (for the cross-language parity job)
//	calendar-api healthcheck        exits 0 if GET /healthz succeeds (for container health checks)
//	calendar-api version
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // IANA zones even in minimal containers

	"bscalendar/fixtures"
	"bscalendar/services/calendar-api/internal/app"
	"bscalendar/services/calendar-api/internal/bootstrap"
	"bscalendar/services/calendar-api/internal/bscal"
	"bscalendar/services/calendar-api/internal/config"
	"bscalendar/services/calendar-api/internal/httpapi"
	"bscalendar/services/calendar-api/internal/store"
)

var version = "dev" // set with -ldflags "-X main.version=..."

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch cmd {
	case "serve":
		err = serve(ctx)
	case "worker":
		err = worker(ctx)
	case "bootstrap":
		err = runBootstrap(ctx)
	case "migrate":
		err = migrate(ctx)
	case "convert":
		err = convert()
	case "dump":
		err = dump()
	case "healthcheck":
		err = healthcheck()
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q (serve, worker, bootstrap, migrate, convert, dump, healthcheck, version)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func setup(forServe bool) (config.Config, *slog.Logger, error) {
	cfg, err := config.Load(forServe)
	if err != nil {
		return cfg, nil, fmt.Errorf("configuration:\n%w", err)
	}
	log := cfg.Logger().With("service", "calendar-api", "build", version)
	slog.SetDefault(log)
	return cfg, log, nil
}

func serve(ctx context.Context) error {
	cfg, log, err := setup(true)
	if err != nil {
		return err
	}
	openCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	pool, err := store.Open(openCtx, cfg.DatabaseURL, log)
	cancel()
	if err != nil {
		return err
	}
	defer pool.Close()
	a, err := app.New(ctx, cfg, pool, log, version)
	if err != nil {
		return err
	}
	go a.Data.Watch(ctx)
	srv, metrics := a.Server, a.Metrics
	if cfg.WorkerInProcess {
		go app.NewWorker(pool, cfg, log, metrics).Run(ctx)
	}

	api := &http.Server{Addr: cfg.HTTPAddr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mon := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	errc := make(chan error, 2)
	go func() { errc <- listen(api, "api", log) }()
	go func() { errc <- listen(mon, "metrics", log) }()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil {
			return err
		}
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = mon.Shutdown(sctx)
	return api.Shutdown(sctx)
}

func listen(s *http.Server, name string, log *slog.Logger) error {
	log.Info("listening", "server", name, "addr", s.Addr)
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server: %w", name, err)
	}
	return nil
}

func worker(ctx context.Context) error {
	cfg, log, err := setup(false)
	if err != nil {
		return err
	}
	if len(cfg.WebhookEncKey) == 0 {
		return errors.New("WEBHOOK_SECRET_KEY is required for the worker")
	}
	openCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	pool, err := store.Open(openCtx, cfg.DatabaseURL, log)
	cancel()
	if err != nil {
		return err
	}
	defer pool.Close()
	metrics := httpapi.NewMetrics()
	w := app.NewWorker(pool, cfg, log, metrics)
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mon := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = listen(mon, "metrics", log) }()
	w.Run(ctx)
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return mon.Shutdown(sctx)
}

func runBootstrap(ctx context.Context) error {
	cfg, log, err := setup(false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := bootstrap.Run(ctx, cfg, log); err != nil {
		return err
	}
	log.Info("bootstrap complete")
	return nil
}

func migrate(ctx context.Context) error {
	cfg, log, err := setup(false)
	if err != nil {
		return err
	}
	dir := "up"
	if len(os.Args) > 2 {
		dir = os.Args[2]
	}
	return store.Migrate(ctx, cfg.DatabaseURL, dir, log)
}

func seedTable() (*bscal.Table, error) {
	years, err := bscal.LoadSeed(fixtures.YearTableSeed)
	if err != nil {
		return nil, err
	}
	return bscal.NewTable(1, years)
}

func convert() error {
	if len(os.Args) != 4 {
		return errors.New("usage: calendar-api convert AD|BS YYYY-MM-DD")
	}
	cal, err := bscal.ParseCalendar(os.Args[2])
	if err != nil {
		return err
	}
	d, err := bscal.ParseDate(os.Args[3])
	if err != nil {
		return err
	}
	t, err := seedTable()
	if err != nil {
		return err
	}
	n, err := t.ToEpoch(cal, d)
	if err != nil {
		return err
	}
	bs, _ := t.EpochToBS(n)
	ad := bscal.EpochToAD(n)
	w := bscal.WeekdayName(bscal.Weekday(n))
	fmt.Printf("AD %s  =  BS %s (%s %d, %d)  %s  [%s]\n", ad, bs, bscal.MonthName(bscal.BS, bs.Month).En, bs.Day, bs.Year, w.En, t.StatusOf(bs.Year))
	return nil
}

func dump() error {
	t, err := seedTable()
	if err != nil {
		return err
	}
	lo, hi := t.EpochRange()
	out := os.Stdout
	fmt.Fprintln(out, "epochDay,ad,bs,status")
	for n := lo; n <= hi; n++ {
		bs, err := t.EpochToBS(n)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%d,%s,%s,%s\n", n, bscal.EpochToAD(n), bs, t.StatusOf(bs.Year))
	}
	return nil
}

func healthcheck() error {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}
