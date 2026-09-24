package events

import (
	"context"
	"fmt"
	"strings"

	"bscalendar/services/calendar-api/internal/bscal"
)

// ICS renders published occurrences for BS years [fromYear, toYear] as an iCalendar feed
// (RFC 5545) that Google Calendar, Apple Calendar and Outlook can subscribe to.
func (s *Service) ICS(ctx context.Context, tenantID string, fromYear, toYear int, categories []string, lang string) ([]byte, error) {
	var b strings.Builder
	line := func(format string, args ...any) { b.WriteString(fold(fmt.Sprintf(format, args...))) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//bs-calendar//calendar-api//EN")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	line("X-WR-CALNAME:%s", esc("BS/AD calendar"))
	stamp := s.now().UTC().Format("20060102T150405Z")
	seen := map[string]bool{}
	for y := fromYear; y <= toYear; y++ {
		cb, err := s.bucket(ctx, tenantID, y)
		if err != nil {
			return nil, err
		}
		for _, o := range cb.bucket.Events {
			if seen[o.ID] || (len(categories) > 0 && !contains(categories, o.Category)) {
				continue
			}
			seen[o.ID] = true
			title := o.Title.En
			if lang == "ne" && o.Title.Ne != "" {
				title = o.Title.Ne
			}
			line("BEGIN:VEVENT")
			line("UID:%s@bs-calendar", strings.ReplaceAll(o.ID, "@", "-"))
			line("DTSTAMP:%s", stamp)
			line("LAST-MODIFIED:%s", o.UpdatedAt.UTC().Format("20060102T150405Z"))
			if o.AllDay || o.StartTime == nil {
				line("DTSTART;VALUE=DATE:%s", compact(o.Start.AD))
				// DTEND is exclusive for all-day events.
				end := bscal.EpochToAD(o.endEpoch + 1)
				line("DTEND;VALUE=DATE:%s", compact(end.String()))
			} else {
				line("DTSTART;TZID=%s:%sT%s00", o.TZ, compact(o.Start.AD), strings.ReplaceAll(*o.StartTime, ":", ""))
				if o.EndTime != nil {
					line("DTEND;TZID=%s:%sT%s00", o.TZ, compact(o.End.AD), strings.ReplaceAll(*o.EndTime, ":", ""))
				}
			}
			line("SUMMARY:%s", esc(title))
			desc := ""
			if o.Start.BS != nil {
				desc = "BS " + *o.Start.BS
				if o.End.BS != nil && *o.End.BS != *o.Start.BS {
					desc += " – " + *o.End.BS
				}
			}
			if o.Description != nil {
				d := o.Description.En
				if lang == "ne" && o.Description.Ne != "" {
					d = o.Description.Ne
				}
				desc = strings.TrimSpace(desc + "\n\n" + d)
			}
			if desc != "" {
				line("DESCRIPTION:%s", esc(desc))
			}
			line("CATEGORIES:%s", esc(o.Category))
			if o.IsHoliday {
				line("TRANSP:TRANSPARENT")
			}
			line("END:VEVENT")
		}
	}
	line("END:VCALENDAR")
	return []byte(b.String()), nil
}

func compact(isoDate string) string { return strings.ReplaceAll(isoDate, "-", "") }

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func esc(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`)
	return r.Replace(s)
}

// fold splits content lines longer than 75 octets (RFC 5545 §3.1) without breaking UTF-8.
func fold(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		size := len(string(r))
		if n+size > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += size
	}
	b.WriteString("\r\n")
	return b.String()
}
