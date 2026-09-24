package bscal

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Status says how trustworthy a BS year's month lengths are.
type Status string

const (
	// Verified years are confirmed (see the seed provenance and docs/reliable.md §3.1).
	Verified Status = "verified"
	// Projected years are estimates that may change when the official calendar is published.
	Projected Status = "projected"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool { return s == Verified || s == Projected }

// YearInfo describes one BS year.
type YearInfo struct {
	Year      int
	MonthDays [12]int
	StartDay  int64 // epoch day of 1 Baisakh
	Status    Status
	Source    string
}

// Length returns the number of days in the year.
func (y YearInfo) Length() int {
	n := 0
	for _, d := range y.MonthDays {
		n += d
	}
	return n
}

// Issue is a single validation finding.
type Issue struct {
	Year     int    `json:"year,omitempty"`
	Severity string `json:"severity"` // "error" or "warning"
	Message  string `json:"message"`
}

// ValidationError lists every invariant a table breaks.
type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	msgs := make([]string, 0, len(e.Issues))
	for _, is := range e.Issues {
		if is.Severity == "error" {
			msgs = append(msgs, fmt.Sprintf("BS %d: %s", is.Year, is.Message))
		}
	}
	return "invalid year table: " + strings.Join(msgs, "; ")
}

// Validate checks every table invariant (docs/reliable.md §3.2). It returns warnings
// and, if any hard rule is broken, a *ValidationError.
func Validate(years []YearInfo) (warnings []Issue, err error) {
	var errs []Issue
	if len(years) == 0 {
		return nil, &ValidationError{Issues: []Issue{{Severity: "error", Message: "table is empty"}}}
	}
	for i, y := range years {
		if i > 0 && y.Year != years[i-1].Year+1 {
			errs = append(errs, Issue{Year: y.Year, Severity: "error",
				Message: fmt.Sprintf("years must be contiguous; follows %d", years[i-1].Year)})
		}
		for m, d := range y.MonthDays {
			if d < 29 || d > 32 {
				errs = append(errs, Issue{Year: y.Year, Severity: "error",
					Message: fmt.Sprintf("month %d has %d days; must be 29-32", m+1, d)})
			}
		}
		switch l := y.Length(); {
		case l == 365 || l == 366:
		case l == 364 || l == 367:
			warnings = append(warnings, Issue{Year: y.Year, Severity: "warning",
				Message: fmt.Sprintf("unusual year length %d days", l)})
		default:
			errs = append(errs, Issue{Year: y.Year, Severity: "error",
				Message: fmt.Sprintf("year length %d days is impossible (364-367 allowed)", l)})
		}
		if !y.Status.Valid() {
			errs = append(errs, Issue{Year: y.Year, Severity: "error",
				Message: fmt.Sprintf("unknown status %q", y.Status)})
		}
		start := EpochToAD(y.StartDay)
		if start.Month != 4 || start.Day < 10 || start.Day > 18 {
			errs = append(errs, Issue{Year: y.Year, Severity: "error",
				Message: fmt.Sprintf("1 Baisakh falls on AD %s; expected 10-18 April", start)})
		}
		if i > 0 {
			prev := years[i-1]
			if want := prev.StartDay + int64(prev.Length()); y.StartDay != want {
				errs = append(errs, Issue{Year: y.Year, Severity: "error",
					Message: fmt.Sprintf("starts on AD %s but BS %d ends on AD %s; change month lengths so the total stays the same, or change both years together",
						start, prev.Year, EpochToAD(want-1))})
			}
		}
	}
	if len(errs) > 0 {
		return warnings, &ValidationError{Issues: append(errs, warnings...)}
	}
	return warnings, nil
}

// Table is an immutable, validated BS year table. Safe for concurrent use.
type Table struct {
	version int64
	years   []YearInfo
	minDay  int64
	maxDay  int64
}

// NewTable validates years and builds a table. Years must be sorted ascending.
func NewTable(version int64, years []YearInfo) (*Table, error) {
	cp := append([]YearInfo(nil), years...)
	if _, err := Validate(cp); err != nil {
		return nil, err
	}
	last := cp[len(cp)-1]
	return &Table{
		version: version,
		years:   cp,
		minDay:  cp[0].StartDay,
		maxDay:  last.StartDay + int64(last.Length()) - 1,
	}, nil
}

// Version returns the data version this table was built from.
func (t *Table) Version() int64 { return t.version }

// MinYear returns the first supported BS year.
func (t *Table) MinYear() int { return t.years[0].Year }

// MaxYear returns the last supported BS year.
func (t *Table) MaxYear() int { return t.years[len(t.years)-1].Year }

// Years returns a copy of all years.
func (t *Table) Years() []YearInfo { return append([]YearInfo(nil), t.years...) }

// EpochRange returns the first and last supported epoch days (inclusive).
func (t *Table) EpochRange() (minDay, maxDay int64) { return t.minDay, t.maxDay }

// Year returns the info for a BS year.
func (t *Table) Year(y int) (YearInfo, bool) {
	i := y - t.years[0].Year
	if i < 0 || i >= len(t.years) {
		return YearInfo{}, false
	}
	return t.years[i], true
}

// StatusOf returns the status of a BS year, or "" if out of range.
func (t *Table) StatusOf(bsYear int) Status {
	y, ok := t.Year(bsYear)
	if !ok {
		return ""
	}
	return y.Status
}

// InRange reports whether an epoch day is supported.
func (t *Table) InRange(n int64) bool { return n >= t.minDay && n <= t.maxDay }

// BSToEpoch converts a BS date to an epoch day.
func (t *Table) BSToEpoch(bs Date) (int64, error) {
	y, ok := t.Year(bs.Year)
	if !ok {
		return 0, fmt.Errorf("%w: BS year %d (supported %d-%d)", ErrOutOfRange, bs.Year, t.MinYear(), t.MaxYear())
	}
	if bs.Month < 1 || bs.Month > 12 || bs.Day < 1 || bs.Day > y.MonthDays[bs.Month-1] {
		return 0, fmt.Errorf("%w: BS %s does not exist", ErrInvalidDate, bs)
	}
	n := y.StartDay
	for m := 0; m < bs.Month-1; m++ {
		n += int64(y.MonthDays[m])
	}
	return n + int64(bs.Day-1), nil
}

// EpochToBS converts an epoch day to a BS date.
func (t *Table) EpochToBS(n int64) (Date, error) {
	if !t.InRange(n) {
		return Date{}, fmt.Errorf("%w: AD %s (supported %s to %s)", ErrOutOfRange, EpochToAD(n), EpochToAD(t.minDay), EpochToAD(t.maxDay))
	}
	i := sort.Search(len(t.years), func(i int) bool { return t.years[i].StartDay > n }) - 1
	y := t.years[i]
	off := int(n - y.StartDay)
	for m, md := range y.MonthDays {
		if off < md {
			return Date{Year: y.Year, Month: m + 1, Day: off + 1}, nil
		}
		off -= md
	}
	return Date{}, fmt.Errorf("%w: internal table gap at %d", ErrOutOfRange, n) // unreachable for a valid table
}

// ToBS converts an AD date to BS.
func (t *Table) ToBS(ad Date) (Date, error) {
	n, err := ADToEpoch(ad)
	if err != nil {
		return Date{}, err
	}
	return t.EpochToBS(n)
}

// ToAD converts a BS date to AD.
func (t *Table) ToAD(bs Date) (Date, error) {
	n, err := t.BSToEpoch(bs)
	if err != nil {
		return Date{}, err
	}
	return EpochToAD(n), nil
}

// ToEpoch converts a date in either calendar to an epoch day, checking the supported range.
func (t *Table) ToEpoch(cal Calendar, d Date) (int64, error) {
	switch cal {
	case BS:
		return t.BSToEpoch(d)
	case AD:
		n, err := ADToEpoch(d)
		if err != nil {
			return 0, err
		}
		if !t.InRange(n) {
			return 0, fmt.Errorf("%w: AD %s (supported %s to %s)", ErrOutOfRange, d, EpochToAD(t.minDay), EpochToAD(t.maxDay))
		}
		return n, nil
	}
	return 0, fmt.Errorf("%w: unknown calendar %q", ErrInvalidDate, cal)
}

// FromEpoch converts an epoch day to a date in the given calendar.
func (t *Table) FromEpoch(cal Calendar, n int64) (Date, error) {
	if cal == BS {
		return t.EpochToBS(n)
	}
	if !t.InRange(n) {
		return Date{}, fmt.Errorf("%w: epoch day %d", ErrOutOfRange, n)
	}
	return EpochToAD(n), nil
}

// DaysInMonth returns the length of a month in either calendar.
func (t *Table) DaysInMonth(cal Calendar, year, month int) (int, error) {
	if month < 1 || month > 12 {
		return 0, fmt.Errorf("%w: month %d", ErrInvalidDate, month)
	}
	if cal == AD {
		return DaysInGregorianMonth(year, month), nil
	}
	y, ok := t.Year(year)
	if !ok {
		return 0, fmt.Errorf("%w: BS year %d", ErrOutOfRange, year)
	}
	return y.MonthDays[month-1], nil
}

// IsNextDay reports whether b is the BS day immediately after a.
func (t *Table) IsNextDay(a, b Date) bool {
	na, err1 := t.BSToEpoch(a)
	nb, err2 := t.BSToEpoch(b)
	return err1 == nil && err2 == nil && nb == na+1
}

// SnapshotYear is the wire format of one year.
type SnapshotYear struct {
	Y      int     `json:"y"`
	Start  string  `json:"start"`
	Days   [12]int `json:"days"`
	Status Status  `json:"status"`
}

// Snapshot is the wire format of a whole table, served at /v1/calendar/data/{version}.
type Snapshot struct {
	Version int64          `json:"version"`
	SHA256  string         `json:"sha256"`
	MinYear int            `json:"minYear"`
	MaxYear int            `json:"maxYear"`
	Years   []SnapshotYear `json:"years"`
}

// Snapshot returns the wire format. SHA256 covers the years in a canonical text form,
// so clients can verify a download (invariant I8).
func (t *Table) Snapshot() Snapshot {
	ys := make([]SnapshotYear, len(t.years))
	for i, y := range t.years {
		ys[i] = SnapshotYear{Y: y.Year, Start: EpochToAD(y.StartDay).String(), Days: y.MonthDays, Status: y.Status}
	}
	return Snapshot{Version: t.version, SHA256: YearsChecksum(ys), MinYear: t.MinYear(), MaxYear: t.MaxYear(), Years: ys}
}

// YearsChecksum returns the hex SHA-256 of the canonical text form of years: one line per
// year, "<y>:<start>:<d1>,<d2>,...,<d12>:<status>\n", in table order. The form does not
// depend on JSON key order or whitespace, so any client language can reproduce it, e.g.
//
//	years.map(y => `${y.y}:${y.start}:${y.days.join(",")}:${y.status}\n`).join("")
func YearsChecksum(ys []SnapshotYear) string {
	h := sha256.New()
	for _, y := range ys {
		days := make([]string, len(y.Days))
		for i, d := range y.Days {
			days[i] = strconv.Itoa(d)
		}
		fmt.Fprintf(h, "%d:%s:%s:%s\n", y.Y, y.Start, strings.Join(days, ","), y.Status)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TableFromSnapshot rebuilds and validates a table from its wire format.
func TableFromSnapshot(s Snapshot) (*Table, error) {
	if s.SHA256 != "" && s.SHA256 != YearsChecksum(s.Years) {
		return nil, errors.New("snapshot checksum mismatch")
	}
	years := make([]YearInfo, len(s.Years))
	for i, y := range s.Years {
		start, err := ParseDate(y.Start)
		if err != nil {
			return nil, err
		}
		n, err := ADToEpoch(start)
		if err != nil {
			return nil, err
		}
		years[i] = YearInfo{Year: y.Y, MonthDays: y.Days, StartDay: n, Status: y.Status}
	}
	return NewTable(s.Version, years)
}
