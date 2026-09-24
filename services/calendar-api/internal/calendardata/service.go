// Package calendardata owns the BS year table: the live in-memory copy used for every
// conversion, immutable published snapshots, hot reload across replicas (LISTEN/NOTIFY),
// and the four-eyes draft → approve workflow for changes.
package calendardata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/bscal"
	"bscalendar/services/calendar-api/internal/events"
	"bscalendar/services/calendar-api/internal/outbox"
	"bscalendar/services/calendar-api/internal/store"
)

const notifyChannel = "calendar_data"

type state struct {
	table    *bscal.Table
	snapshot []byte
	sha      string
}

// Service holds the current table.
type Service struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	cur      atomic.Pointer[state]
	purge    bool
	baseURL  string
	OnReload func(version int64)
}

// NewService creates the service. Call Load before serving.
func NewService(pool *pgxpool.Pool, log *slog.Logger, baseURL string, purge bool) *Service {
	return &Service{pool: pool, log: log, baseURL: baseURL, purge: purge}
}

// Table returns the live table. It never returns nil after a successful Load.
func (s *Service) Table() *bscal.Table {
	if st := s.cur.Load(); st != nil {
		return st.table
	}
	return nil
}

// Current returns the serialised snapshot, its version and checksum.
func (s *Service) Current() (snapshot []byte, version int64, sha string) {
	st := s.cur.Load()
	return st.snapshot, st.table.Version(), st.sha
}

// Loaded reports whether a table is available.
func (s *Service) Loaded() bool { return s.cur.Load() != nil }

// Load reads the newest snapshot from the database.
func (s *Service) Load(ctx context.Context) error {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT snapshot FROM calendar_data_versions ORDER BY version DESC LIMIT 1`).Scan(&raw)
	if store.IsNoRows(err) {
		return errors.New("no calendar data in the database; run `calendar-api bootstrap` first")
	}
	if err != nil {
		return err
	}
	return s.install(raw)
}

func (s *Service) install(raw []byte) error {
	var snap bscal.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}
	t, err := bscal.TableFromSnapshot(snap)
	if err != nil {
		return fmt.Errorf("snapshot %d failed validation: %w", snap.Version, err)
	}
	if old := s.cur.Load(); old != nil && old.table.Version() >= t.Version() {
		return nil
	}
	s.cur.Store(&state{table: t, snapshot: raw, sha: snap.SHA256})
	s.log.Info("calendar data loaded", "version", t.Version(), "minYear", t.MinYear(), "maxYear", t.MaxYear(), "sha256", snap.SHA256)
	if s.OnReload != nil {
		s.OnReload(t.Version())
	}
	return nil
}

// Watch keeps the table fresh: it LISTENs for publish notifications and also polls
// every minute as a backstop. It returns when ctx is cancelled.
func (s *Service) Watch(ctx context.Context) {
	poll := time.NewTicker(time.Minute)
	defer poll.Stop()
	for ctx.Err() == nil {
		if err := s.listen(ctx, poll.C); err != nil && ctx.Err() == nil {
			s.log.Warn("calendar data listener stopped, reconnecting", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (s *Service) listen(ctx context.Context, poll <-chan time.Time) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return err
	}
	for {
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		n, err := conn.Conn().WaitForNotification(wctx)
		cancel()
		select {
		case <-poll:
			if err := s.Load(ctx); err != nil {
				s.log.Error("calendar data poll failed", "err", err)
			}
		default:
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, context.DeadlineExceeded) {
				continue // idle timeout; loop to check the poll ticker
			}
			return err
		}
		s.log.Info("calendar data change notified", "version", n.Payload)
		if err := s.Load(ctx); err != nil {
			s.log.Error("calendar data reload failed", "err", err)
		}
	}
}

// SnapshotByVersion returns an immutable snapshot (current or historical).
func (s *Service) SnapshotByVersion(ctx context.Context, version int64) ([]byte, error) {
	if st := s.cur.Load(); st != nil && st.table.Version() == version {
		return st.snapshot, nil
	}
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT snapshot FROM calendar_data_versions WHERE version = $1`, version).Scan(&raw)
	if store.IsNoRows(err) {
		return nil, (&apperr.Error{Status: 404, Code: apperr.CodeVersionNotFound, Title: "Version not found",
			Detail: fmt.Sprintf("Calendar data version %d does not exist.", version)})
	}
	return raw, err
}

// ---- seeding ---------------------------------------------------------------

// Seed writes the first snapshot from seed years if the database has none. Idempotent.
func Seed(ctx context.Context, pool *pgxpool.Pool, years []bscal.YearInfo) (bool, error) {
	seeded := false
	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('calendar_data'))`); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM calendar_data_versions)`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		version, err := store.BumpVersion(ctx, tx, events.ScopeData)
		if err != nil {
			return err
		}
		if err := writeYears(ctx, tx, years, ""); err != nil {
			return err
		}
		if err := writeSnapshot(ctx, tx, version, years, ""); err != nil {
			return err
		}
		seeded = true
		return nil
	})
	return seeded, err
}

func writeYears(ctx context.Context, tx pgx.Tx, years []bscal.YearInfo, actorID string) error {
	for _, y := range years {
		days := make([]int32, 12)
		for i, d := range y.MonthDays {
			days[i] = int32(d)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO calendar_years (bs_year, month_days, ad_start, status, source, updated_by)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6,'')::uuid)
			ON CONFLICT (bs_year) DO UPDATE SET month_days = EXCLUDED.month_days, ad_start = EXCLUDED.ad_start,
			  status = EXCLUDED.status, source = EXCLUDED.source, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			y.Year, days, dayTime(y.StartDay), string(y.Status), y.Source, actorID); err != nil {
			return fmt.Errorf("write BS %d: %w", y.Year, err)
		}
	}
	return nil
}

func writeSnapshot(ctx context.Context, tx pgx.Tx, version int64, years []bscal.YearInfo, draftID string) error {
	t, err := bscal.NewTable(version, years)
	if err != nil {
		return err
	}
	snap := t.Snapshot()
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO calendar_data_versions (version, snapshot, sha256, draft_id) VALUES ($1, $2, $3, NULLIF($4,'')::uuid)`,
		version, raw, snap.SHA256, draftID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, notifyChannel, strconv.FormatInt(version, 10))
	return err
}

func dayTime(n int64) time.Time {
	d := bscal.EpochToAD(n)
	return time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
}

func loadYears(ctx context.Context, q store.Querier, lock bool) ([]bscal.YearInfo, error) {
	sql := `SELECT bs_year, month_days, ad_start, status, source FROM calendar_years ORDER BY bs_year`
	if lock {
		sql += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (bscal.YearInfo, error) {
		var y bscal.YearInfo
		var days []int32
		var start time.Time
		var status string
		if err := r.Scan(&y.Year, &days, &start, &status, &y.Source); err != nil {
			return y, err
		}
		for i := 0; i < 12 && i < len(days); i++ {
			y.MonthDays[i] = int(days[i])
		}
		y.StartDay = bscal.DaysFromCivil(start.Year(), int(start.Month()), start.Day())
		y.Status = bscal.Status(status)
		return y, nil
	})
}

// YearView is one year as shown to admins.
type YearView struct {
	BSYear  int          `json:"bsYear"`
	ADStart string       `json:"adStart"`
	ADEnd   string       `json:"adEnd"`
	Days    [12]int      `json:"days"`
	Length  int          `json:"length"`
	Status  bscal.Status `json:"status"`
	Source  string       `json:"source"`
}

func view(y bscal.YearInfo) YearView {
	return YearView{BSYear: y.Year, ADStart: bscal.EpochToAD(y.StartDay).String(),
		ADEnd: bscal.EpochToAD(y.StartDay + int64(y.Length()) - 1).String(), Days: y.MonthDays,
		Length: y.Length(), Status: y.Status, Source: y.Source}
}

// Years lists the current table with sources.
func (s *Service) Years(ctx context.Context) ([]YearView, error) {
	ys, err := loadYears(ctx, s.pool, false)
	if err != nil {
		return nil, err
	}
	out := make([]YearView, len(ys))
	for i, y := range ys {
		out[i] = view(y)
	}
	return out, nil
}

// notifyAll enqueues data-change webhooks for every tenant and a manifest purge.
func (s *Service) notifyAll(ctx context.Context, tx pgx.Tx, version int64) error {
	if err := outbox.Enqueue(ctx, tx, "", "data", "data.published", map[string]any{"dataVersion": version}); err != nil {
		return err
	}
	return outbox.EnqueuePurge(ctx, tx, s.purge, s.baseURL+"/v1/manifest")
}
