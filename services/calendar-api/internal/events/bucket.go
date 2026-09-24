package events

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/bscal"
	"bscalendar/services/calendar-api/internal/store"
)

type cachedBucket struct {
	bucket *Bucket
	json   []byte
}

// bucketCache keeps built buckets by (tenant, year, version). Entries are immutable,
// so a stale entry can never be served for a newer version.
type bucketCache struct {
	mu  sync.Mutex
	max int
	m   map[string]cachedBucket
}

func newBucketCache(max int) *bucketCache {
	return &bucketCache{max: max, m: map[string]cachedBucket{}}
}

func (c *bucketCache) get(k string) (cachedBucket, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *bucketCache) put(k string, v cachedBucket) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		c.m = map[string]cachedBucket{} // simple and safe: rebuild on demand
	}
	c.m[k] = v
}

// baseVersion is the part of every bucket version shared by all years.
func baseVersion(ctx context.Context, q store.Querier, tenantID string) (int64, error) {
	v, err := store.Versions(ctx, q, ScopeData, ScopeCategories(tenantID), ScopeRecurring(tenantID))
	if err != nil {
		return 0, err
	}
	return v[ScopeData] + v[ScopeCategories(tenantID)] + v[ScopeRecurring(tenantID)], nil
}

// BucketVersion returns the current version of one BS-year bucket.
func (s *Service) BucketVersion(ctx context.Context, tenantID string, bsYear int) (int64, error) {
	v, err := store.Versions(ctx, s.pool, ScopeData, ScopeCategories(tenantID), ScopeRecurring(tenantID), ScopeYear(tenantID, bsYear))
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, n := range v {
		sum += n
	}
	return sum, nil
}

// ManifestBuckets returns versions for years that have their own counter, plus the
// default version for every other year.
func (s *Service) ManifestBuckets(ctx context.Context, tenantID string) (map[string]int64, int64, error) {
	base, err := baseVersion(ctx, s.pool, tenantID)
	if err != nil {
		return nil, 0, err
	}
	years, err := store.VersionsWithPrefix(ctx, s.pool, ScopeYearPrefix(tenantID))
	if err != nil {
		return nil, 0, err
	}
	out := make(map[string]int64, len(years))
	for scope, v := range years {
		out[strings.TrimPrefix(scope, ScopeYearPrefix(tenantID))] = base + v
	}
	return out, base, nil
}

// BucketJSON returns the serialised bucket for the current version.
func (s *Service) BucketJSON(ctx context.Context, tenantID string, bsYear int) ([]byte, int64, error) {
	cb, err := s.bucket(ctx, tenantID, bsYear)
	if err != nil {
		return nil, 0, err
	}
	return cb.json, cb.bucket.Version, nil
}

func (s *Service) bucket(ctx context.Context, tenantID string, bsYear int) (cachedBucket, error) {
	t := s.tables.Table()
	yi, ok := t.Year(bsYear)
	if !ok {
		return cachedBucket{}, fmt.Errorf("%w: BS year %d (supported %d-%d)", bscal.ErrOutOfRange, bsYear, t.MinYear(), t.MaxYear())
	}
	// Read the version before the content: a concurrent change can only make the
	// content newer than its label, never older.
	version, err := s.BucketVersion(ctx, tenantID, bsYear)
	if err != nil {
		return cachedBucket{}, err
	}
	key := tenantID + ":" + strconv.Itoa(bsYear) + ":" + strconv.FormatInt(version, 10) + ":" + strconv.FormatInt(t.Version(), 10)
	if cb, ok := s.cache.get(key); ok {
		return cb, nil
	}
	from := yi.StartDay
	to := yi.StartDay + int64(yi.Length()) - 1
	occ, err := s.occurrences(ctx, tenantID, t, from, to)
	if err != nil {
		return cachedBucket{}, err
	}
	cats, err := s.PublicCategories(ctx, tenantID)
	if err != nil {
		return cachedBucket{}, err
	}
	b := &Bucket{
		BSYear: bsYear, Version: version, Status: yi.Status,
		Range:       DateRange{Start: bscal.EpochToAD(from).String(), End: bscal.EpochToAD(to).String()},
		GeneratedAt: s.now().UTC().Truncate(time.Second),
		Categories:  cats,
		Events:      occ,
	}
	js, err := json.Marshal(b)
	if err != nil {
		return cachedBucket{}, err
	}
	cb := cachedBucket{bucket: b, json: js}
	s.cache.put(key, cb)
	return cb, nil
}

type pubRow struct {
	row
	holiday    bool
	colorLight string
	colorDark  string
}

// occurrences expands every published event overlapping [from, to].
func (s *Service) occurrences(ctx context.Context, tenantID string, t *bscal.Table, from, to int64) ([]Occurrence, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+rowCols+`, COALESCE(e.is_holiday, c.is_holiday), c.color_light, c.color_dark
		FROM events e JOIN categories c ON c.id = e.category_id
		WHERE e.tenant_id = $1 AND e.status = 'published' AND e.deleted_at IS NULL AND e.ad_start <= $3::date
		  AND ((e.recurrence = 'none' AND e.ad_end >= $2::date)
		    OR (e.recurrence <> 'none' AND (e.recur_until IS NULL OR e.recur_until >= $2::date - 400)))
		ORDER BY e.ad_start, e.id`, tenantID, timeOf(bscal.EpochToAD(from)), timeOf(bscal.EpochToAD(to)))
	if err != nil {
		return nil, err
	}
	rs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pubRow, error) {
		var x pubRow
		err := r.Scan(append(x.dest(), &x.holiday, &x.colorLight, &x.colorDark)...)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	out := []Occurrence{}
	for i := range rs {
		r := &rs[i]
		for _, sp := range expand(t, &r.row, from, to) {
			o := Occurrence{
				ID: r.ID, EventID: r.ID, Title: r.Title, Description: r.Description, Category: r.CategoryKey,
				IsHoliday: r.holiday, Basis: r.Basis,
				Start:  DatePair{AD: bscal.EpochToAD(sp.start).String(), BS: bsString(t, sp.start)},
				End:    DatePair{AD: bscal.EpochToAD(sp.end).String(), BS: bsString(t, sp.end)},
				AllDay: r.AllDay, StartTime: r.StartTime, EndTime: r.EndTime, TZ: r.TZ,
				Recurring: r.Recurrence != "none", Color: Colors{Light: r.colorLight, Dark: r.colorDark},
				Version: r.Version, UpdatedAt: r.UpdatedAt, startEpoch: sp.start, endEpoch: sp.end,
			}
			if o.Recurring {
				o.ID = r.ID + "@" + o.Start.AD
			}
			out = append(out, o)
		}
	}
	slices.SortStableFunc(out, func(a, b Occurrence) int {
		if a.startEpoch != b.startEpoch {
			return int(a.startEpoch - b.startEpoch)
		}
		return strings.Compare(a.Title.En, b.Title.En)
	})
	return out, nil
}

// RangeQuery filters public occurrences.
type RangeQuery struct {
	From, To   int64 // epoch days, inclusive
	Categories []string
	Q          string
}

// MaxRangeDays limits GET /v1/events.
const MaxRangeDays = 366

// Range returns published occurrences overlapping [From, To], served from buckets.
func (s *Service) Range(ctx context.Context, tenantID string, rq RangeQuery) ([]Occurrence, error) {
	t := s.tables.Table()
	if rq.To < rq.From {
		return nil, apperr.Validation(apperr.FieldError{Field: "to", Message: "must not be before from"})
	}
	if rq.To-rq.From+1 > MaxRangeDays {
		return nil, apperr.Validation(apperr.FieldError{Field: "to", Message: fmt.Sprintf("range is limited to %d days", MaxRangeDays)})
	}
	y1, err := t.EpochToBS(rq.From)
	if err != nil {
		return nil, err
	}
	y2, err := t.EpochToBS(rq.To)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(rq.Q))
	seen := map[string]bool{}
	out := []Occurrence{}
	for y := y1.Year; y <= y2.Year; y++ {
		cb, err := s.bucket(ctx, tenantID, y)
		if err != nil {
			return nil, err
		}
		for _, o := range cb.bucket.Events {
			if seen[o.ID] || o.startEpoch > rq.To || o.endEpoch < rq.From {
				continue
			}
			if len(rq.Categories) > 0 && !slices.Contains(rq.Categories, o.Category) {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(o.Title.En), q) && !strings.Contains(strings.ToLower(o.Title.Ne), q) {
				continue
			}
			seen[o.ID] = true
			out = append(out, o)
		}
	}
	return out, nil
}
