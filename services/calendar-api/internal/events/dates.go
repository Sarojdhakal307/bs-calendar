package events

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/teambition/rrule-go"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/bscal"
)

// Dates are the materialised AD range of an event's first occurrence.
type Dates struct {
	ADStart     bscal.Date
	ADEnd       bscal.Date
	StartEpoch  int64
	EndEpoch    int64
	BSYearStart int
	BSYearEnd   int
}

// MaxSpanDays limits a single occurrence to one year.
const MaxSpanDays = 366

// Materialize converts an event's local dates (in its basis calendar) to AD and BS-year bounds.
func Materialize(t *bscal.Table, basis bscal.Calendar, start, end bscal.Date) (Dates, error) {
	s, err := t.ToEpoch(basis, start)
	if err != nil {
		return Dates{}, err
	}
	e, err := t.ToEpoch(basis, end)
	if err != nil {
		return Dates{}, err
	}
	if e < s {
		return Dates{}, fmt.Errorf("%w: end %s is before start %s", bscal.ErrInvalidDate, end, start)
	}
	bs1, err := t.EpochToBS(s)
	if err != nil {
		return Dates{}, err
	}
	bs2, err := t.EpochToBS(e)
	if err != nil {
		return Dates{}, err
	}
	return Dates{ADStart: bscal.EpochToAD(s), ADEnd: bscal.EpochToAD(e), StartEpoch: s, EndEpoch: e,
		BSYearStart: bs1.Year, BSYearEnd: bs2.Year}, nil
}

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

type normalized struct {
	categoryID  string
	categoryKey string
	title       Text
	description *Text
	basis       bscal.Calendar
	start, end  bscal.Date
	dates       Dates
	allDay      bool
	startTime   *string
	endTime     *string
	tz          string
	recurrence  string
	rrule       *string
	recurUntil  *bscal.Date
	isHoliday   *bool
}

// validate checks an EventInput against the table and returns field errors.
// Category lookup is done by the caller (it needs the database).
func validate(t *bscal.Table, in EventInput) (normalized, []apperr.FieldError) {
	var fe []apperr.FieldError
	add := func(f, m string) { fe = append(fe, apperr.FieldError{Field: f, Message: m}) }
	n := normalized{categoryKey: strings.TrimSpace(in.Category)}

	n.title = Text{En: strings.TrimSpace(in.Title.En), Ne: strings.TrimSpace(in.Title.Ne)}
	if n.title.En == "" || len([]rune(n.title.En)) > 200 {
		add("title.en", "required, at most 200 characters")
	}
	if len([]rune(n.title.Ne)) > 200 {
		add("title.ne", "at most 200 characters")
	}
	if in.Description != nil {
		d := Text{En: strings.TrimSpace(in.Description.En), Ne: strings.TrimSpace(in.Description.Ne)}
		if len([]rune(d.En)) > 5000 || len([]rune(d.Ne)) > 5000 {
			add("description", "at most 5000 characters per language")
		}
		if d.En != "" || d.Ne != "" {
			n.description = &d
		}
	}
	if n.categoryKey == "" {
		add("category", "required")
	}

	basis, err := bscal.ParseCalendar(in.Basis)
	if err != nil {
		add("basis", "must be AD or BS")
	}
	n.basis = basis
	start, err := bscal.ParseDate(in.Start)
	if err != nil {
		add("start", "must be a date in YYYY-MM-DD format")
	}
	end := start
	if in.End != "" {
		if end, err = bscal.ParseDate(in.End); err != nil {
			add("end", "must be a date in YYYY-MM-DD format")
		}
	}
	n.start, n.end = start, end
	if len(fe) == 0 {
		d, err := Materialize(t, basis, start, end)
		switch {
		case err != nil:
			add("start", dateMessage(err, basis))
		case d.EndEpoch-d.StartEpoch+1 > MaxSpanDays:
			add("end", fmt.Sprintf("an event can span at most %d days", MaxSpanDays))
		default:
			n.dates = d
		}
	}

	n.allDay = in.AllDay == nil || *in.AllDay
	if n.allDay {
		if in.StartTime != nil || in.EndTime != nil {
			add("startTime", "must be omitted for all-day events")
		}
	} else {
		if in.StartTime == nil || !hhmm.MatchString(*in.StartTime) {
			add("startTime", "required for timed events, format HH:MM")
		}
		if in.EndTime != nil && !hhmm.MatchString(*in.EndTime) {
			add("endTime", "format HH:MM")
		}
		if in.StartTime != nil && in.EndTime != nil && start == end && *in.EndTime <= *in.StartTime {
			add("endTime", "must be after startTime on a single-day event")
		}
		n.startTime, n.endTime = in.StartTime, in.EndTime
	}

	n.tz = in.TZ
	if n.tz == "" {
		n.tz = "Asia/Kathmandu"
	}
	if _, err := time.LoadLocation(n.tz); err != nil || n.tz == "Local" {
		add("tz", "must be an IANA time zone such as Asia/Kathmandu")
	}

	n.recurrence = in.Recurrence
	if n.recurrence == "" {
		n.recurrence = "none"
	}
	switch n.recurrence {
	case "none":
	case "yearly_bs":
		if basis != bscal.BS {
			add("recurrence", "yearly_bs requires basis BS")
		}
	case "yearly_ad":
		if basis != bscal.AD {
			add("recurrence", "yearly_ad requires basis AD")
		}
	case "rrule":
		if basis != bscal.AD {
			add("recurrence", "rrule requires basis AD")
		}
		if in.RRule == nil || strings.TrimSpace(*in.RRule) == "" {
			add("rrule", "required when recurrence is rrule")
		} else if err := checkRRule(*in.RRule); err != nil {
			add("rrule", err.Error())
		} else {
			s := strings.TrimSpace(*in.RRule)
			n.rrule = &s
		}
	default:
		add("recurrence", "must be one of none, yearly_bs, yearly_ad, rrule")
	}
	if n.recurrence != "rrule" && in.RRule != nil {
		add("rrule", "only allowed when recurrence is rrule")
	}
	if in.RecurUntil != nil {
		if n.recurrence == "none" {
			add("recurUntil", "only allowed for recurring events")
		} else if u, err := bscal.ParseDate(*in.RecurUntil); err != nil || !bscal.ValidGregorian(u) {
			add("recurUntil", "must be an AD date in YYYY-MM-DD format")
		} else if n.dates.StartEpoch != 0 && bscal.DaysFromCivil(u.Year, u.Month, u.Day) < n.dates.StartEpoch {
			add("recurUntil", "must not be before the first occurrence")
		} else {
			n.recurUntil = &u
		}
	}
	n.isHoliday = in.IsHoliday
	return n, fe
}

func dateMessage(err error, basis bscal.Calendar) string {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	return fmt.Sprintf("invalid %s date: %s", basis, msg)
}

// checkRRule accepts DAILY/WEEKLY/MONTHLY/YEARLY rules without DTSTART (it comes from start).
func checkRRule(s string) error {
	up := strings.ToUpper(s)
	if strings.Contains(up, "DTSTART") {
		return fmt.Errorf("must not contain DTSTART; the event start is used")
	}
	opt, err := rrule.StrToROption(s)
	if err != nil {
		return fmt.Errorf("invalid RRULE: %v", err)
	}
	switch opt.Freq {
	case rrule.DAILY, rrule.WEEKLY, rrule.MONTHLY, rrule.YEARLY:
	default:
		return fmt.Errorf("FREQ must be DAILY, WEEKLY, MONTHLY or YEARLY")
	}
	return nil
}

// ---- recurrence expansion ---------------------------------------------------

// maxOccurrences caps the expansion of one event in one query window.
const maxOccurrences = 400

type span struct{ start, end int64 }

// expand returns the occurrences of r that overlap [from, to] (epoch days, inclusive).
func expand(t *bscal.Table, r *row, from, to int64) []span {
	s0, e0 := epochOf(r.ADStart), epochOf(r.ADEnd)
	dur := e0 - s0
	var until int64 = 1 << 62
	if r.RecurUntil != nil {
		until = epochOf(*r.RecurUntil)
	}
	overlaps := func(s int64) bool { return s <= to && s+dur >= from && s <= until && t.InRange(s) }
	var out []span
	push := func(s int64) bool {
		if overlaps(s) {
			out = append(out, span{s, s + dur})
		}
		return len(out) < maxOccurrences
	}
	minDay, maxDay := t.EpochRange()
	lo, hi := max(from-dur, minDay, s0), min(to, maxDay)
	switch r.Recurrence {
	case "none":
		push(s0)
	case "yearly_bs":
		if lo > hi {
			return nil
		}
		base, err := bscal.ParseDate(r.StartLocal)
		if err != nil {
			return nil
		}
		y1, _ := t.EpochToBS(lo)
		y2, _ := t.EpochToBS(hi)
		for y := max(y1.Year, base.Year); y <= y2.Year; y++ {
			yi, ok := t.Year(y)
			if !ok {
				continue
			}
			d := min(base.Day, yi.MonthDays[base.Month-1])
			if s, err := t.BSToEpoch(bscal.Date{Year: y, Month: base.Month, Day: d}); err == nil && !push(s) {
				break
			}
		}
	case "yearly_ad":
		if lo > hi {
			return nil
		}
		base := dateOf(r.ADStart)
		for y := max(bscal.EpochToAD(lo).Year, base.Year); y <= bscal.EpochToAD(hi).Year; y++ {
			d := min(base.Day, bscal.DaysInGregorianMonth(y, base.Month))
			if !push(bscal.DaysFromCivil(y, base.Month, d)) {
				break
			}
		}
	case "rrule":
		if r.RRule == nil || lo > hi {
			return nil
		}
		opt, err := rrule.StrToROption(*r.RRule)
		if err != nil {
			return nil
		}
		opt.Dtstart = r.ADStart.UTC()
		rr, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil
		}
		after := timeOf(bscal.EpochToAD(lo))
		before := timeOf(bscal.EpochToAD(hi))
		for _, tm := range rr.Between(after, before, true) {
			if !push(epochOf(tm)) {
				break
			}
		}
	}
	return out
}
