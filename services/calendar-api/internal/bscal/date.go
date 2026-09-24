// Package bscal converts dates between the Gregorian (AD) and Bikram Sambat (BS) calendars.
//
// BS month lengths cannot be computed by formula, so conversion is driven by a
// year table (see Table). Every date is converted through an "epoch day": the
// number of days since 1970-01-01. The package never uses time.Time for calendar
// dates, so time zones cannot shift a date by one day.
package bscal

import (
	"errors"
	"fmt"
	"strings"
)

// Calendar identifies a calendar system.
type Calendar string

const (
	// AD is the Gregorian calendar.
	AD Calendar = "AD"
	// BS is the Bikram Sambat calendar.
	BS Calendar = "BS"
)

var (
	// ErrInvalidDate means the date does not exist (for example BS month 13 or AD 2023-02-29).
	ErrInvalidDate = errors.New("invalid date")
	// ErrOutOfRange means the date is valid in principle but outside the supported table.
	ErrOutOfRange = errors.New("date outside supported range")
)

// ParseCalendar parses "AD" or "BS" (case-insensitive).
func ParseCalendar(s string) (Calendar, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "AD":
		return AD, nil
	case "BS":
		return BS, nil
	default:
		return "", fmt.Errorf("%w: unknown calendar %q (want AD or BS)", ErrInvalidDate, s)
	}
}

// Date is a civil date without time or zone. Month is 1..12.
type Date struct {
	Year  int
	Month int
	Day   int
}

// String formats the date as YYYY-MM-DD.
func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// IsZero reports whether d is the zero value.
func (d Date) IsZero() bool { return d == Date{} }

// Compare returns -1, 0 or +1.
func (d Date) Compare(o Date) int {
	switch {
	case d.Year != o.Year:
		return sign(d.Year - o.Year)
	case d.Month != o.Month:
		return sign(d.Month - o.Month)
	default:
		return sign(d.Day - o.Day)
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// ParseDate parses a strict YYYY-MM-DD string. ASCII and Devanagari digits are accepted.
// It checks the shape only (month 1..12, day 1..32); calendar validity is checked by
// ValidGregorian or Table.
func ParseDate(s string) (Date, error) {
	s = FromNepaliDigits(strings.TrimSpace(s))
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return Date{}, fmt.Errorf("%w: %q is not in YYYY-MM-DD format", ErrInvalidDate, s)
	}
	y, ok1 := atoi(s[0:4])
	m, ok2 := atoi(s[5:7])
	d, ok3 := atoi(s[8:10])
	if !ok1 || !ok2 || !ok3 {
		return Date{}, fmt.Errorf("%w: %q is not in YYYY-MM-DD format", ErrInvalidDate, s)
	}
	if m < 1 || m > 12 || d < 1 || d > 32 {
		return Date{}, fmt.Errorf("%w: %q has month or day out of bounds", ErrInvalidDate, s)
	}
	return Date{Year: y, Month: m, Day: d}, nil
}

func atoi(s string) (int, bool) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}
