package events

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/audit"
	"bscalendar/services/calendar-api/internal/bscal"
	"bscalendar/services/calendar-api/internal/outbox"
	"bscalendar/services/calendar-api/internal/store"
)

// Version scopes (see resource_versions). A bucket's version is the sum of
// data + categories + recurring + its own year counter; each only ever grows,
// so the sum changes whenever any input changes.
const ScopeData = "data"

// ScopeCategories is bumped on any category change.
func ScopeCategories(tenantID string) string { return "categories:" + tenantID }

// ScopeRecurring is bumped when a published recurring event changes.
func ScopeRecurring(tenantID string) string { return "events:" + tenantID + ":recurring" }

// ScopeYearPrefix prefixes per-year counters.
func ScopeYearPrefix(tenantID string) string { return "events:" + tenantID + ":y:" }

// ScopeYear is bumped when a published one-off event touching that BS year changes.
func ScopeYear(tenantID string, bsYear int) string {
	return ScopeYearPrefix(tenantID) + strconv.Itoa(bsYear)
}

// Service manages categories and events.
type Service struct {
	pool    *pgxpool.Pool
	tables  TableSource
	purge   bool
	baseURL string
	now     func() time.Time
	cache   *bucketCache
}

// NewService creates the events service. purge enables CDN purge outbox rows.
func NewService(pool *pgxpool.Pool, tables TableSource, baseURL string, purge bool) *Service {
	return &Service{pool: pool, tables: tables, baseURL: baseURL, purge: purge, now: time.Now, cache: newBucketCache(512)}
}

// ListFilter narrows the admin event list.
type ListFilter struct {
	Status   string // draft, published, archived, deleted, or "" for all non-deleted
	Category string
	Q        string
	From, To *bscal.Date // AD, inclusive overlap
	Cursor   string
	Limit    int
}

// Page is a page of events.
type Page struct {
	Items      []Event `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

// List returns events for the admin panel, ordered by AD start date.
func (s *Service) List(ctx context.Context, tenantID string, f ListFilter) (Page, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	args := []any{tenantID}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	where := []string{"e.tenant_id = $1"}
	switch f.Status {
	case "":
		where = append(where, "e.deleted_at IS NULL")
	case "deleted":
		where = append(where, "e.deleted_at IS NOT NULL")
	case "draft", "published", "archived":
		where = append(where, "e.deleted_at IS NULL", "e.status = "+arg(f.Status))
	default:
		return Page{}, apperr.Validation(apperr.FieldError{Field: "status", Message: "must be draft, published, archived or deleted"})
	}
	if f.Category != "" {
		where = append(where, "c.key = "+arg(f.Category))
	}
	if f.Q != "" {
		p := "%" + store.EscapeLike(f.Q) + "%"
		ph := arg(p)
		where = append(where, "(e.title->>'en' ILIKE "+ph+" OR e.title->>'ne' ILIKE "+ph+")")
	}
	if f.From != nil {
		where = append(where, "(e.ad_end >= "+arg(timeOf(*f.From))+" OR e.recurrence <> 'none')")
	}
	if f.To != nil {
		where = append(where, "e.ad_start <= "+arg(timeOf(*f.To)))
	}
	if f.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		d, id, ok := strings.Cut(string(raw), "|")
		cd, derr := bscal.ParseDate(d)
		if err != nil || !ok || derr != nil {
			return Page{}, apperr.BadRequest("Invalid cursor.")
		}
		where = append(where, "(e.ad_start, e.id) > ("+arg(timeOf(cd))+"::date, "+arg(id)+"::uuid)")
	}
	q := `SELECT ` + rowCols + ` FROM events e JOIN categories c ON c.id = e.category_id WHERE ` +
		strings.Join(where, " AND ") + ` ORDER BY e.ad_start, e.id LIMIT ` + arg(f.Limit+1)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, err
	}
	rs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(x.dest()...)
		return x, err
	})
	if err != nil {
		return Page{}, err
	}
	t := s.tables.Table()
	p := Page{Items: make([]Event, 0, len(rs))}
	for i := range rs {
		if i == f.Limit {
			last := rs[i-1]
			c := base64.RawURLEncoding.EncodeToString([]byte(dateOf(last.ADStart).String() + "|" + last.ID))
			p.NextCursor = &c
			break
		}
		p.Items = append(p.Items, rs[i].toEvent(t))
	}
	return p, nil
}

func getRow(ctx context.Context, q store.Querier, tenantID, id string, lock bool) (row, error) {
	sql := `SELECT ` + rowCols + ` FROM events e JOIN categories c ON c.id = e.category_id WHERE e.id = $1 AND e.tenant_id = $2`
	if lock {
		sql += ` FOR UPDATE OF e`
	}
	var r row
	err := q.QueryRow(ctx, sql, id, tenantID).Scan(r.dest()...)
	if store.IsNoRows(err) {
		return row{}, apperr.NotFound("event")
	}
	return r, err
}

// Get returns one event (including soft-deleted ones).
func (s *Service) Get(ctx context.Context, tenantID, id string) (Event, error) {
	if !validUUID(id) {
		return Event{}, apperr.NotFound("event")
	}
	r, err := getRow(ctx, s.pool, tenantID, id, false)
	if err != nil {
		return Event{}, err
	}
	return r.toEvent(s.tables.Table()), nil
}

func (s *Service) resolve(ctx context.Context, q store.Querier, tenantID string, in EventInput) (normalized, error) {
	n, fe := validate(s.tables.Table(), in)
	if n.categoryKey != "" {
		err := q.QueryRow(ctx, `SELECT id::text FROM categories WHERE tenant_id = $1 AND key = $2`, tenantID, n.categoryKey).Scan(&n.categoryID)
		if store.IsNoRows(err) {
			fe = append(fe, apperr.FieldError{Field: "category", Message: "unknown category " + n.categoryKey})
		} else if err != nil {
			return n, err
		}
	}
	if len(fe) > 0 {
		return n, apperr.Validation(fe...)
	}
	return n, nil
}

func untilArg(d *bscal.Date) any {
	if d == nil {
		return nil
	}
	return timeOf(*d)
}

// Create adds an event as a draft.
func (s *Service) Create(ctx context.Context, actor audit.Actor, in EventInput) (Event, error) {
	var out Event
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if out, err = s.createInTx(ctx, tx, actor, in); err != nil {
			return err
		}
		return audit.Write(ctx, tx, actor, "event.create", "event", out.ID, nil, out)
	})
	return out, err
}

// Patch applies a JSON merge patch (RFC 7396). ifMatch must equal the current version.
func (s *Service) Patch(ctx context.Context, actor audit.Actor, id string, ifMatch int, patch []byte) (Event, error) {
	return s.mutate(ctx, actor, id, &ifMatch, "event.update", func(tx pgx.Tx, r *row) error {
		if r.DeletedAt != nil {
			return apperr.InvalidState("Deleted events cannot be edited. Restore the event first.")
		}
		var in EventInput
		if err := ApplyMergePatch(r.toInput(), patch, &in); err != nil {
			return err
		}
		n, err := s.resolve(ctx, tx, actor.TenantID, in)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE events SET category_id=$2, title=$3, description=$4, date_basis=$5, start_local=$6, end_local=$7,
			  ad_start=$8, ad_end=$9, bs_year_start=$10, bs_year_end=$11, all_day=$12, start_time=$13, end_time=$14,
			  tz=$15, recurrence=$16, rrule=$17, recur_until=$18, is_holiday=$19,
			  version = version + 1, updated_by = $20, updated_at = now()
			WHERE id = $1`,
			id, n.categoryID, n.title, n.description, string(n.basis), n.start.String(), n.end.String(),
			timeOf(n.dates.ADStart), timeOf(n.dates.ADEnd), n.dates.BSYearStart, n.dates.BSYearEnd, n.allDay,
			n.startTime, n.endTime, n.tz, n.recurrence, n.rrule, untilArg(n.recurUntil), n.isHoliday, actor.UserID)
		return err
	})
}

// Transition changes lifecycle state: publish, archive, delete or restore.
// ifMatch is optional for publish/archive/restore and required (by the HTTP layer) for delete.
func (s *Service) Transition(ctx context.Context, actor audit.Actor, id, action string, ifMatch *int) (Event, error) {
	return s.mutate(ctx, actor, id, ifMatch, "event."+action, func(tx pgx.Tx, r *row) error {
		var sql string
		switch action {
		case "publish":
			if r.DeletedAt != nil {
				return apperr.InvalidState("Restore the event before publishing it.")
			}
			if r.Status == "published" {
				return errNoChange
			}
			sql = `status = 'published'`
		case "archive":
			if r.DeletedAt != nil {
				return apperr.InvalidState("The event is deleted.")
			}
			if r.Status == "archived" {
				return errNoChange
			}
			sql = `status = 'archived'`
		case "delete":
			if r.DeletedAt != nil {
				return errNoChange
			}
			sql = `deleted_at = now()`
		case "restore":
			if r.DeletedAt == nil {
				return apperr.InvalidState("Only deleted events can be restored.")
			}
			// Dates may have become invalid if the year table changed while the event was deleted.
			if _, fe := validate(s.tables.Table(), r.toInput()); len(fe) > 0 {
				return apperr.Validation(fe...)
			}
			sql = `deleted_at = NULL, status = 'draft'`
		default:
			return apperr.BadRequest("Unknown action " + action)
		}
		_, err := tx.Exec(ctx, `UPDATE events SET `+sql+`, version = version + 1, updated_by = $2, updated_at = now() WHERE id = $1`, id, actor.UserID)
		return err
	})
}

var errNoChange = fmt.Errorf("no change")

// mutate loads and locks an event, checks If-Match, applies fn, then bumps bucket
// versions for the before and after states, audits, and enqueues notifications.
func (s *Service) mutate(ctx context.Context, actor audit.Actor, id string, ifMatch *int, action string, fn func(pgx.Tx, *row) error) (Event, error) {
	if !validUUID(id) {
		return Event{}, apperr.NotFound("event")
	}
	var out Event
	t := s.tables.Table()
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, err := getRow(ctx, tx, actor.TenantID, id, true)
		if err != nil {
			return err
		}
		if ifMatch != nil && *ifMatch != before.Version {
			return apperr.VersionConflict(before.Version)
		}
		if err := fn(tx, &before); err == errNoChange {
			out = before.toEvent(t)
			return nil
		} else if err != nil {
			return err
		}
		after, err := getRow(ctx, tx, actor.TenantID, id, false)
		if err != nil {
			return err
		}
		out = after.toEvent(t)
		if err := s.bumpBuckets(ctx, tx, actor.TenantID, before, after); err != nil {
			return err
		}
		return audit.Write(ctx, tx, actor, action, "event", id, before.toEvent(t), out)
	})
	return out, err
}

func visible(r row) bool { return r.Status == "published" && r.DeletedAt == nil }

// bumpBuckets invalidates the buckets an event affects, before and after a change.
func (s *Service) bumpBuckets(ctx context.Context, tx pgx.Tx, tenantID string, states ...row) error {
	years := map[int]bool{}
	recurring := false
	for _, r := range states {
		if !visible(r) {
			continue
		}
		if r.Recurrence != "none" {
			recurring = true
			continue
		}
		for y := r.BSYearStart; y <= r.BSYearEnd; y++ {
			years[y] = true
		}
	}
	if !recurring && len(years) == 0 {
		return nil // drafts and archived events are invisible to clients
	}
	if recurring {
		if _, err := store.BumpVersion(ctx, tx, ScopeRecurring(tenantID)); err != nil {
			return err
		}
	}
	ys := make([]int, 0, len(years))
	for y := range years {
		ys = append(ys, y)
	}
	slices.Sort(ys)
	for _, y := range ys {
		if _, err := store.BumpVersion(ctx, tx, ScopeYear(tenantID, y)); err != nil {
			return err
		}
	}
	if err := outbox.Enqueue(ctx, tx, tenantID, "events", "events.changed", map[string]any{"bsYears": ys, "recurring": recurring}); err != nil {
		return err
	}
	return outbox.EnqueuePurge(ctx, tx, s.purge, s.baseURL+"/v1/manifest")
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// ValidUUID is exported for handlers.
func ValidUUID(s string) bool { return validUUID(s) }

// Rematerialize recomputes the AD dates of every event after a year-table change.
// It returns events whose dates no longer exist, so the caller can refuse the change.
func Rematerialize(ctx context.Context, tx pgx.Tx, t *bscal.Table) (moved []Shift, invalid []Shift, err error) {
	rows, err := tx.Query(ctx, `SELECT `+rowCols+` FROM events e JOIN categories c ON c.id = e.category_id
		WHERE e.deleted_at IS NULL ORDER BY e.id`)
	if err != nil {
		return nil, nil, err
	}
	rs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(x.dest()...)
		return x, err
	})
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rs {
		sh := Shift{EventID: r.ID, Title: r.Title.En, Basis: r.Basis, Start: r.StartLocal, OldADStart: dateOf(r.ADStart).String()}
		start, err1 := bscal.ParseDate(r.StartLocal)
		end, err2 := bscal.ParseDate(r.EndLocal)
		if err1 != nil || err2 != nil {
			sh.Problem = "unparseable stored dates"
			invalid = append(invalid, sh)
			continue
		}
		d, err := Materialize(t, bscal.Calendar(r.Basis), start, end)
		if err != nil {
			sh.Problem = err.Error()
			invalid = append(invalid, sh)
			continue
		}
		sh.NewADStart = d.ADStart.String()
		if d.ADStart == dateOf(r.ADStart) && d.ADEnd == dateOf(r.ADEnd) && d.BSYearStart == r.BSYearStart && d.BSYearEnd == r.BSYearEnd {
			continue
		}
		if sh.NewADStart != sh.OldADStart {
			moved = append(moved, sh)
		}
		if _, err := tx.Exec(ctx, `UPDATE events SET ad_start=$2, ad_end=$3, bs_year_start=$4, bs_year_end=$5 WHERE id=$1`,
			r.ID, timeOf(d.ADStart), timeOf(d.ADEnd), d.BSYearStart, d.BSYearEnd); err != nil {
			return nil, nil, err
		}
	}
	return moved, invalid, nil
}

// PreviewShifts reports which events would move or break under a new table, without writing.
func PreviewShifts(ctx context.Context, q store.Querier, t *bscal.Table) (moved []Shift, invalid []Shift, err error) {
	rows, err := q.Query(ctx, `SELECT e.id::text, e.title->>'en', e.date_basis, e.start_local, e.end_local, e.ad_start
		FROM events e WHERE e.deleted_at IS NULL AND e.date_basis = 'BS' ORDER BY e.ad_start`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sh Shift
		var endLocal string
		var adStart time.Time
		if err := rows.Scan(&sh.EventID, &sh.Title, &sh.Basis, &sh.Start, &endLocal, &adStart); err != nil {
			return nil, nil, err
		}
		sh.OldADStart = dateOf(adStart).String()
		start, err1 := bscal.ParseDate(sh.Start)
		end, err2 := bscal.ParseDate(endLocal)
		if err1 != nil || err2 != nil {
			sh.Problem = "unparseable stored dates"
			invalid = append(invalid, sh)
			continue
		}
		d, err := Materialize(t, bscal.BS, start, end)
		if err != nil {
			sh.Problem = err.Error()
			invalid = append(invalid, sh)
			continue
		}
		if sh.NewADStart = d.ADStart.String(); sh.NewADStart != sh.OldADStart {
			moved = append(moved, sh)
		}
	}
	return moved, invalid, rows.Err()
}

// Shift describes an event affected by a year-table change.
type Shift struct {
	EventID    string `json:"eventId"`
	Title      string `json:"title"`
	Basis      string `json:"basis"`
	Start      string `json:"start"`
	OldADStart string `json:"oldAdStart"`
	NewADStart string `json:"newAdStart,omitempty"`
	Problem    string `json:"problem,omitempty"`
}
