package events

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/audit"
	"bscalendar/services/calendar-api/internal/bscal"
	"bscalendar/services/calendar-api/internal/store"
)

// ImportColumns are the accepted CSV columns. category, title_en, basis and start are required.
var ImportColumns = []string{"category", "title_en", "title_ne", "basis", "start", "end", "all_day", "start_time",
	"end_time", "recurrence", "rrule", "recur_until", "is_holiday", "description_en", "description_ne"}

// byteOrderMark is stripped from the first header (spreadsheet apps often add it).
const byteOrderMark = string(rune(0xFEFF))

// MaxImportRows limits one import.
const MaxImportRows = 5000

// ImportRow is the result for one CSV row (row 1 is the header, so data starts at 2).
type ImportRow struct {
	Row       int                 `json:"row"`
	Title     string              `json:"title"`
	Start     string              `json:"start"`
	Errors    []apperr.FieldError `json:"errors"`
	Duplicate bool                `json:"duplicate"`
}

// ImportReport summarises an import.
type ImportReport struct {
	DryRun     bool        `json:"dryRun"`
	Total      int         `json:"total"`
	Valid      int         `json:"valid"`
	Invalid    int         `json:"invalid"`
	Duplicates int         `json:"duplicates"`
	Created    int         `json:"created"`
	Rows       []ImportRow `json:"rows"`
}

// Import reads CSV rows, validates all of them, and (unless dryRun) creates them as drafts
// in one transaction. Nothing is created if any row is invalid.
func (s *Service) Import(ctx context.Context, actor audit.Actor, r io.Reader, dryRun, skipDuplicates bool) (ImportReport, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return ImportReport{}, apperr.BadRequest("CSV must start with a header row: " + err.Error())
	}
	idx := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, byteOrderMark)))
		if !contains(ImportColumns, h) {
			return ImportReport{}, apperr.Validation(apperr.FieldError{Field: "header", Message: fmt.Sprintf("unknown column %q; allowed: %s", h, strings.Join(ImportColumns, ", "))})
		}
		idx[h] = i
	}
	for _, req := range []string{"category", "title_en", "basis", "start"} {
		if _, ok := idx[req]; !ok {
			return ImportReport{}, apperr.Validation(apperr.FieldError{Field: "header", Message: "missing required column " + req})
		}
	}
	rep := ImportReport{DryRun: dryRun, Rows: []ImportRow{}}
	var inputs []EventInput
	seenInFile := map[string]bool{}
	err = store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		for line := 2; ; line++ {
			rec, err := cr.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return apperr.BadRequest(fmt.Sprintf("CSV parse error near row %d: %v", line, err))
			}
			if rep.Total++; rep.Total > MaxImportRows {
				return apperr.Validation(apperr.FieldError{Field: "file", Message: fmt.Sprintf("at most %d rows per import", MaxImportRows)})
			}
			get := func(col string) string {
				if i, ok := idx[col]; ok && i < len(rec) {
					return strings.TrimSpace(rec[i])
				}
				return ""
			}
			in := EventInput{Category: get("category"), Title: Text{En: get("title_en"), Ne: get("title_ne")},
				Basis: strings.ToUpper(get("basis")), Start: get("start"), End: get("end"), TZ: "Asia/Kathmandu",
				Recurrence: get("recurrence")}
			var rowErr []apperr.FieldError
			if v := get("all_day"); v != "" {
				b, err := strconv.ParseBool(v)
				if err != nil {
					rowErr = append(rowErr, apperr.FieldError{Field: "all_day", Message: "must be true or false"})
				}
				in.AllDay = &b
			}
			if v := get("is_holiday"); v != "" {
				b, err := strconv.ParseBool(v)
				if err != nil {
					rowErr = append(rowErr, apperr.FieldError{Field: "is_holiday", Message: "must be true or false"})
				}
				in.IsHoliday = &b
			}
			for col, dst := range map[string]**string{"start_time": &in.StartTime, "end_time": &in.EndTime, "rrule": &in.RRule, "recur_until": &in.RecurUntil} {
				if v := get(col); v != "" {
					*dst = &v
				}
			}
			if en, ne := get("description_en"), get("description_ne"); en != "" || ne != "" {
				in.Description = &Text{En: en, Ne: ne}
			}
			res := ImportRow{Row: line, Title: in.Title.En, Start: in.Start, Errors: rowErr}
			if _, err := s.resolve(ctx, tx, actor.TenantID, in); err != nil {
				var ae *apperr.Error
				if !errors.As(err, &ae) || ae.Code != apperr.CodeValidation {
					return err
				}
				res.Errors = append(res.Errors, ae.Fields...)
			}
			if len(res.Errors) == 0 {
				key := strings.ToLower(in.Category + "|" + in.Basis + "|" + in.Start + "|" + in.Title.En)
				dup := seenInFile[key]
				if !dup {
					if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events e JOIN categories c ON c.id = e.category_id
						WHERE e.tenant_id = $1 AND e.deleted_at IS NULL AND c.key = $2 AND e.date_basis = $3
						  AND e.start_local = $4 AND lower(e.title->>'en') = lower($5))`,
						actor.TenantID, in.Category, in.Basis, in.Start, in.Title.En).Scan(&dup); err != nil {
						return err
					}
				}
				seenInFile[key] = true
				res.Duplicate = dup
				if dup {
					rep.Duplicates++
				}
				rep.Valid++
				if !(dup && skipDuplicates) {
					inputs = append(inputs, in)
				}
			} else {
				rep.Invalid++
			}
			rep.Rows = append(rep.Rows, res)
		}
		if rep.Invalid > 0 || dryRun {
			return nil
		}
		for _, in := range inputs {
			if _, err := s.createInTx(ctx, tx, actor, in); err != nil {
				return err
			}
			rep.Created++
		}
		return audit.Write(ctx, tx, actor, "event.import", "event", "bulk", nil, map[string]any{"created": rep.Created, "total": rep.Total})
	})
	if err != nil {
		return ImportReport{}, err
	}
	if rep.Invalid > 0 && !dryRun {
		return rep, apperr.Validation().With("report", rep)
	}
	return rep, nil
}

// CopyYearInput copies one-off BS events from one year to another as drafts.
type CopyYearInput struct {
	FromBSYear int      `json:"fromBsYear"`
	ToBSYear   int      `json:"toBsYear"`
	Categories []string `json:"categories"`
}

// CopyYearReport lists what was created.
type CopyYearReport struct {
	Created []string `json:"created"`
	Skipped []Shift  `json:"skipped"`
}

// CopyYear creates draft copies of published one-off BS events in FromBSYear on the same
// month and day of ToBSYear (clamped to the month length). Lunar festivals move every
// year, so an editor must review each draft before publishing. Running it twice does not
// create duplicates.
func (s *Service) CopyYear(ctx context.Context, actor audit.Actor, in CopyYearInput) (CopyYearReport, error) {
	t := s.tables.Table()
	var fe []apperr.FieldError
	if _, ok := t.Year(in.FromBSYear); !ok {
		fe = append(fe, apperr.FieldError{Field: "fromBsYear", Message: "outside the supported range"})
	}
	if _, ok := t.Year(in.ToBSYear); !ok {
		fe = append(fe, apperr.FieldError{Field: "toBsYear", Message: "outside the supported range"})
	}
	if in.FromBSYear == in.ToBSYear {
		fe = append(fe, apperr.FieldError{Field: "toBsYear", Message: "must differ from fromBsYear"})
	}
	if len(fe) > 0 {
		return CopyYearReport{}, apperr.Validation(fe...)
	}
	rep := CopyYearReport{Created: []string{}, Skipped: []Shift{}}
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+rowCols+` FROM events e JOIN categories c ON c.id = e.category_id
			WHERE e.tenant_id = $1 AND e.deleted_at IS NULL AND e.status = 'published' AND e.recurrence = 'none'
			  AND e.date_basis = 'BS' AND e.start_local LIKE $2 AND (cardinality($3::text[]) = 0 OR c.key = ANY($3))
			ORDER BY e.start_local`, actor.TenantID, fmt.Sprintf("%04d-%%", in.FromBSYear), nonNil(in.Categories))
		if err != nil {
			return err
		}
		src, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			err := r.Scan(x.dest()...)
			return x, err
		})
		if err != nil {
			return err
		}
		for _, r := range src {
			start, _ := bscal.ParseDate(r.StartLocal)
			end, _ := bscal.ParseDate(r.EndLocal)
			dur, _ := t.BSToEpoch(end)
			s0, _ := t.BSToEpoch(start)
			ty, _ := t.Year(in.ToBSYear)
			ns := bscal.Date{Year: in.ToBSYear, Month: start.Month, Day: min(start.Day, ty.MonthDays[start.Month-1])}
			nsEpoch, err := t.BSToEpoch(ns)
			if err != nil {
				rep.Skipped = append(rep.Skipped, Shift{EventID: r.ID, Title: r.Title.En, Basis: "BS", Start: r.StartLocal, Problem: err.Error()})
				continue
			}
			ne, err := t.EpochToBS(nsEpoch + (dur - s0))
			if err != nil {
				rep.Skipped = append(rep.Skipped, Shift{EventID: r.ID, Title: r.Title.En, Basis: "BS", Start: r.StartLocal, Problem: err.Error()})
				continue
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE tenant_id = $1 AND deleted_at IS NULL
				AND category_id = $2 AND start_local = $3 AND lower(title->>'en') = lower($4))`,
				actor.TenantID, r.CategoryID, ns.String(), r.Title.En).Scan(&exists); err != nil {
				return err
			}
			if exists {
				rep.Skipped = append(rep.Skipped, Shift{EventID: r.ID, Title: r.Title.En, Basis: "BS", Start: ns.String(), Problem: "already exists in target year"})
				continue
			}
			in := r.toInput()
			in.Start, in.End = ns.String(), ne.String()
			ev, err := s.createInTx(ctx, tx, actor, in)
			if err != nil {
				return err
			}
			rep.Created = append(rep.Created, ev.ID)
		}
		return audit.Write(ctx, tx, actor, "event.copy_year", "event", "bulk", nil, map[string]any{"from": in.FromBSYear, "to": in.ToBSYear, "created": len(rep.Created)})
	})
	return rep, err
}

func (s *Service) createInTx(ctx context.Context, tx pgx.Tx, actor audit.Actor, in EventInput) (Event, error) {
	n, err := s.resolve(ctx, tx, actor.TenantID, in)
	if err != nil {
		return Event{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO events (tenant_id, category_id, title, description, date_basis, start_local, end_local,
		  ad_start, ad_end, bs_year_start, bs_year_end, all_day, start_time, end_time, tz, recurrence, rrule,
		  recur_until, is_holiday, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$20) RETURNING id::text`,
		actor.TenantID, n.categoryID, n.title, n.description, string(n.basis), n.start.String(), n.end.String(),
		timeOf(n.dates.ADStart), timeOf(n.dates.ADEnd), n.dates.BSYearStart, n.dates.BSYearEnd, n.allDay,
		n.startTime, n.endTime, n.tz, n.recurrence, n.rrule, untilArg(n.recurUntil), n.isHoliday, actor.UserID).Scan(&id)
	if err != nil {
		return Event{}, err
	}
	r, err := getRow(ctx, tx, actor.TenantID, id, false)
	if err != nil {
		return Event{}, err
	}
	return r.toEvent(s.tables.Table()), nil
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}
