package calendardata

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/audit"
	"bscalendar/services/calendar-api/internal/bscal"
	"bscalendar/services/calendar-api/internal/events"
	"bscalendar/services/calendar-api/internal/store"
)

// YearChange edits or adds one year in a draft.
type YearChange struct {
	BSYear  int          `json:"bsYear"`
	Days    []int        `json:"days"`
	Status  bscal.Status `json:"status"`
	Source  string       `json:"source"`
	ADStart *string      `json:"adStart,omitempty"` // only to move a year's start, or to prepend a year
}

// DraftInput proposes changes.
type DraftInput struct {
	Changes []YearChange `json:"changes"`
	Note    string       `json:"note"`
}

// YearDiff shows one changed year.
type YearDiff struct {
	BSYear int       `json:"bsYear"`
	Before *YearView `json:"before"`
	After  YearView  `json:"after"`
}

// Impact is what approving a draft would change.
type Impact struct {
	Years           []YearDiff     `json:"years"`
	MovedEvents     []events.Shift `json:"movedEvents"`
	InvalidEvents   []events.Shift `json:"invalidEvents"`
	EventsTruncated bool           `json:"eventsTruncated"`
}

// Draft is a proposed change to the year table.
type Draft struct {
	ID          string        `json:"id"`
	State       string        `json:"state"`
	BaseVersion int64         `json:"baseVersion"`
	Changes     []YearChange  `json:"changes"`
	Note        string        `json:"note"`
	Warnings    []bscal.Issue `json:"warnings"`
	Impact      Impact        `json:"impact"`
	Reason      *string       `json:"reason"`
	CreatedBy   string        `json:"createdBy"`
	DecidedBy   *string       `json:"decidedBy"`
	CreatedAt   time.Time     `json:"createdAt"`
	DecidedAt   *time.Time    `json:"decidedAt"`
}

type storedChanges struct {
	Changes []YearChange `json:"changes"`
	Note    string       `json:"note"`
}

// apply returns a new table (as years) with changes applied, validated.
func apply(base []bscal.YearInfo, changes []YearChange) ([]bscal.YearInfo, []bscal.Issue, error) {
	if len(changes) == 0 {
		return nil, nil, apperr.Validation(apperr.FieldError{Field: "changes", Message: "at least one change is required"})
	}
	if len(changes) > 50 {
		return nil, nil, apperr.Validation(apperr.FieldError{Field: "changes", Message: "at most 50 years per draft"})
	}
	years := append([]bscal.YearInfo(nil), base...)
	var fe []apperr.FieldError
	seen := map[int]bool{}
	for i, c := range changes {
		f := func(name string) string { return fmt.Sprintf("changes[%d].%s", i, name) }
		if seen[c.BSYear] {
			fe = append(fe, apperr.FieldError{Field: f("bsYear"), Message: "duplicate year in draft"})
			continue
		}
		seen[c.BSYear] = true
		if len(c.Days) != 12 {
			fe = append(fe, apperr.FieldError{Field: f("days"), Message: "must list 12 month lengths"})
			continue
		}
		if !c.Status.Valid() {
			fe = append(fe, apperr.FieldError{Field: f("status"), Message: "must be verified or projected"})
			continue
		}
		c.Source = strings.TrimSpace(c.Source)
		if c.Source == "" || len(c.Source) > 500 {
			fe = append(fe, apperr.FieldError{Field: f("source"), Message: "required (where the numbers come from), at most 500 characters"})
			continue
		}
		var md [12]int
		copy(md[:], c.Days)
		var start *int64
		if c.ADStart != nil {
			d, err := bscal.ParseDate(*c.ADStart)
			n, err2 := bscal.ADToEpoch(d)
			if err != nil || err2 != nil {
				fe = append(fe, apperr.FieldError{Field: f("adStart"), Message: "must be an AD date in YYYY-MM-DD format"})
				continue
			}
			start = &n
		}
		first, last := years[0].Year, years[len(years)-1].Year
		switch {
		case c.BSYear >= first && c.BSYear <= last:
			y := &years[c.BSYear-first]
			y.MonthDays, y.Status, y.Source = md, c.Status, c.Source
			if start != nil {
				y.StartDay = *start
			}
		case c.BSYear == last+1:
			prev := years[len(years)-1]
			n := prev.StartDay + int64(prev.Length())
			if start != nil {
				n = *start
			}
			years = append(years, bscal.YearInfo{Year: c.BSYear, MonthDays: md, StartDay: n, Status: c.Status, Source: c.Source})
		case c.BSYear == first-1:
			if start == nil {
				fe = append(fe, apperr.FieldError{Field: f("adStart"), Message: "required when adding a year before the first year"})
				continue
			}
			years = append([]bscal.YearInfo{{Year: c.BSYear, MonthDays: md, StartDay: *start, Status: c.Status, Source: c.Source}}, years...)
		default:
			fe = append(fe, apperr.FieldError{Field: f("bsYear"), Message: fmt.Sprintf("must be within %d-%d, or extend the range by one year", first, last)})
		}
	}
	if len(fe) > 0 {
		return nil, nil, apperr.Validation(fe...)
	}
	warnings, err := bscal.Validate(years)
	if err != nil {
		return nil, nil, err
	}
	return years, warnings, nil
}

func impactOf(base, next []bscal.YearInfo, changes []YearChange) []YearDiff {
	byYear := map[int]bscal.YearInfo{}
	for _, y := range base {
		byYear[y.Year] = y
	}
	var out []YearDiff
	for _, c := range changes {
		for _, y := range next {
			if y.Year == c.BSYear {
				d := YearDiff{BSYear: y.Year, After: view(y)}
				if b, ok := byYear[y.Year]; ok {
					v := view(b)
					d.Before = &v
				}
				out = append(out, d)
			}
		}
	}
	return out
}

const maxImpactEvents = 500

// ProposeDraft validates changes, computes impact and stores a pending draft.
func (s *Service) ProposeDraft(ctx context.Context, actor audit.Actor, in DraftInput) (Draft, error) {
	base, err := loadYears(ctx, s.pool, false)
	if err != nil {
		return Draft{}, err
	}
	next, warnings, err := apply(base, in.Changes)
	if err != nil {
		return Draft{}, err
	}
	t, err := bscal.NewTable(0, next)
	if err != nil {
		return Draft{}, err
	}
	imp := Impact{Years: impactOf(base, next, in.Changes)}
	imp.MovedEvents, imp.InvalidEvents, err = events.PreviewShifts(ctx, s.pool, t)
	if err != nil {
		return Draft{}, err
	}
	if len(imp.MovedEvents) > maxImpactEvents {
		imp.MovedEvents, imp.EventsTruncated = imp.MovedEvents[:maxImpactEvents], true
	}
	_, baseVersion, _ := s.Current()
	sc, _ := json.Marshal(storedChanges{Changes: in.Changes, Note: strings.TrimSpace(in.Note)})
	if warnings == nil {
		warnings = []bscal.Issue{}
	}
	var id string
	err = store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO calendar_year_drafts (base_version, changes, warnings, impact, state, created_by)
			VALUES ($1, $2, $3, $4, 'pending', $5) RETURNING id::text`,
			baseVersion, sc, warnings, imp, actor.UserID).Scan(&id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Actor{UserID: actor.UserID, IP: actor.IP, RequestID: actor.RequestID},
			"years.propose", "year_draft", id, nil, in)
	})
	if err != nil {
		return Draft{}, err
	}
	return s.GetDraft(ctx, id)
}

const draftCols = `id::text, state, base_version, changes, warnings, impact, reason, created_by::text, decided_by::text, created_at, decided_at`

func scanDraft(r pgx.Row) (Draft, error) {
	var d Draft
	var sc storedChanges
	err := r.Scan(&d.ID, &d.State, &d.BaseVersion, &sc, &d.Warnings, &d.Impact, &d.Reason, &d.CreatedBy, &d.DecidedBy, &d.CreatedAt, &d.DecidedAt)
	d.Changes, d.Note = sc.Changes, sc.Note
	if d.Impact.MovedEvents == nil {
		d.Impact.MovedEvents = []events.Shift{}
	}
	if d.Impact.InvalidEvents == nil {
		d.Impact.InvalidEvents = []events.Shift{}
	}
	if d.Impact.Years == nil {
		d.Impact.Years = []YearDiff{}
	}
	return d, err
}

// GetDraft returns one draft.
func (s *Service) GetDraft(ctx context.Context, id string) (Draft, error) {
	if !events.ValidUUID(id) {
		return Draft{}, apperr.NotFound("draft")
	}
	d, err := scanDraft(s.pool.QueryRow(ctx, `SELECT `+draftCols+` FROM calendar_year_drafts WHERE id = $1`, id))
	if store.IsNoRows(err) {
		return Draft{}, apperr.NotFound("draft")
	}
	return d, err
}

// ListDrafts returns drafts, newest first, optionally filtered by state.
func (s *Service) ListDrafts(ctx context.Context, state string) ([]Draft, error) {
	if state != "" && state != "pending" && state != "approved" && state != "rejected" {
		return nil, apperr.Validation(apperr.FieldError{Field: "state", Message: "must be pending, approved or rejected"})
	}
	rows, err := s.pool.Query(ctx, `SELECT `+draftCols+` FROM calendar_year_drafts
		WHERE ($1 = '' OR state = $1) ORDER BY created_at DESC LIMIT 100`, state)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Draft, error) { return scanDraft(r) })
}

// Approve applies a pending draft. The approver must not be the author (four-eyes rule) unless they are a super admin.
// Approval re-validates against the current table, refuses if any event date would stop
// existing, publishes a new data version, re-materialises events and notifies replicas.
func (s *Service) Approve(ctx context.Context, actor audit.Actor, id string) (Draft, error) {
	if !events.ValidUUID(id) {
		return Draft{}, apperr.NotFound("draft")
	}
	var version int64
	var raw []byte
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('calendar_data'))`); err != nil {
			return err
		}
		d, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftCols+` FROM calendar_year_drafts WHERE id = $1 FOR UPDATE`, id))
		if store.IsNoRows(err) {
			return apperr.NotFound("draft")
		}
		if err != nil {
			return err
		}
		if d.State != "pending" {
			return apperr.InvalidState("The draft is already " + d.State + ".")
		}
		if d.CreatedBy == actor.UserID && !actor.CanSelfApprove() {
			return apperr.Forbidden(apperr.CodeFourEyes, "A different calendar admin (or a super admin) must approve this change.")
		}
		base, err := loadYears(ctx, tx, true)
		if err != nil {
			return err
		}
		next, _, err := apply(base, d.Changes)
		if err != nil {
			return err
		}
		if version, err = store.BumpVersion(ctx, tx, events.ScopeData); err != nil {
			return err
		}
		t, err := bscal.NewTable(version, next)
		if err != nil {
			return err
		}
		_, invalid, err := events.Rematerialize(ctx, tx, t)
		if err != nil {
			return err
		}
		if len(invalid) > 0 {
			return apperr.Conflict(apperr.CodeConflict, "Some events would fall on dates that no longer exist. Fix or delete them first.").
				With("invalidEvents", invalid)
		}
		// Write every year that differs from the current table (new, edited or moved).
		baseBy := map[int]bscal.YearInfo{}
		for _, y := range base {
			baseBy[y.Year] = y
		}
		var toWrite []bscal.YearInfo
		for _, y := range next {
			if b, ok := baseBy[y.Year]; !ok || b != y {
				toWrite = append(toWrite, y)
			}
		}
		if err := writeYears(ctx, tx, toWrite, actor.UserID); err != nil {
			return err
		}
		if err := writeSnapshot(ctx, tx, version, next, id); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT snapshot FROM calendar_data_versions WHERE version = $1`, version).Scan(&raw); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE calendar_year_drafts SET state = 'approved', decided_by = $2, decided_at = now() WHERE id = $1`,
			id, actor.UserID); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, audit.Actor{UserID: actor.UserID, IP: actor.IP, RequestID: actor.RequestID},
			"years.approve", "year_draft", id, nil, map[string]any{"dataVersion": version, "changes": d.Changes}); err != nil {
			return err
		}
		return s.notifyAll(ctx, tx, version)
	})
	if err != nil {
		return Draft{}, err
	}
	if err := s.install(raw); err != nil {
		s.log.Error("install approved table", "err", err)
	}
	return s.GetDraft(ctx, id)
}

// Reject closes a pending draft. Authors may withdraw their own drafts.
func (s *Service) Reject(ctx context.Context, actor audit.Actor, id, reason string) (Draft, error) {
	if !events.ValidUUID(id) {
		return Draft{}, apperr.NotFound("draft")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 {
		return Draft{}, apperr.Validation(apperr.FieldError{Field: "reason", Message: "required, at most 1000 characters"})
	}
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE calendar_year_drafts SET state = 'rejected', reason = $2, decided_by = $3, decided_at = now()
			WHERE id = $1 AND state = 'pending'`, id, reason, actor.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := s.GetDraft(ctx, id); err != nil {
				return err
			}
			return apperr.InvalidState("Only pending drafts can be rejected.")
		}
		return audit.Write(ctx, tx, audit.Actor{UserID: actor.UserID, IP: actor.IP, RequestID: actor.RequestID},
			"years.reject", "year_draft", id, nil, map[string]any{"reason": reason})
	})
	if err != nil {
		return Draft{}, err
	}
	return s.GetDraft(ctx, id)
}
