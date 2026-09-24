package bscal

import (
	"fmt"
	"time"
)

// DaysFromCivil returns the number of days since 1970-01-01 for a proleptic Gregorian date.
// It is Howard Hinnant's days_from_civil algorithm, valid for years >= 1.
func DaysFromCivil(y, m, d int) int64 {
	if m <= 2 {
		y--
	}
	era := y / 400
	yoe := y - era*400                     // [0, 399]
	doy := (153*((m+9)%12)+2)/5 + d - 1    // [0, 365]
	doe := yoe*365 + yoe/4 - yoe/100 + doy // [0, 146096]
	return int64(era*146097+doe) - 719468
}

// CivilFromDays is the inverse of DaysFromCivil (Hinnant's civil_from_days), valid for z >= -719468.
func CivilFromDays(z int64) (year, month, day int) {
	z += 719468
	era := z / 146097
	doe := z - era*146097                                  // [0, 146096]
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365 // [0, 399]
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100) // [0, 365]
	mp := (5*doy + 2) / 153                  // [0, 11]
	d := doy - (153*mp+2)/5 + 1              // [1, 31]
	m := mp + 3
	if m > 12 {
		m -= 12
	}
	if m <= 2 {
		y++
	}
	return int(y), int(m), int(d)
}

// IsLeapYear reports whether an AD year is a leap year.
func IsLeapYear(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

// DaysInGregorianMonth returns the number of days in an AD month.
func DaysInGregorianMonth(y, m int) int {
	switch m {
	case 2:
		if IsLeapYear(y) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	default:
		return 31
	}
}

// ValidGregorian reports whether d is a real AD date (year 1..9999).
func ValidGregorian(d Date) bool {
	return d.Year >= 1 && d.Year <= 9999 && d.Month >= 1 && d.Month <= 12 &&
		d.Day >= 1 && d.Day <= DaysInGregorianMonth(d.Year, d.Month)
}

// ADToEpoch converts a valid AD date to an epoch day.
func ADToEpoch(d Date) (int64, error) {
	if !ValidGregorian(d) {
		return 0, fmt.Errorf("%w: %s is not a valid AD date", ErrInvalidDate, d)
	}
	return DaysFromCivil(d.Year, d.Month, d.Day), nil
}

// EpochToAD converts an epoch day to an AD date.
func EpochToAD(n int64) Date {
	y, m, d := CivilFromDays(n)
	return Date{Year: y, Month: m, Day: d}
}

// Weekday returns 0 (Sunday) .. 6 (Saturday) for an epoch day. 1970-01-01 was a Thursday.
func Weekday(n int64) int {
	w := (n + 4) % 7
	if w < 0 {
		w += 7
	}
	return int(w)
}

// nepalOffset is Nepal Standard Time (UTC+05:45). Nepal has no daylight saving time.
const nepalOffset = 345 * time.Minute

// NepalTodayEpoch returns today's epoch day in Nepal for the given instant.
// It uses the fixed offset, so it works without a time zone database.
func NepalTodayEpoch(now time.Time) int64 {
	t := now.UTC().Add(nepalOffset)
	return DaysFromCivil(t.Year(), int(t.Month()), t.Day())
}

// TodayEpochIn returns today's epoch day in the given location.
func TodayEpochIn(now time.Time, loc *time.Location) int64 {
	t := now.In(loc)
	return DaysFromCivil(t.Year(), int(t.Month()), t.Day())
}
