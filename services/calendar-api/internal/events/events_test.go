package events

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"bscalendar/fixtures"
	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/bscal"
)

func table(t *testing.T) *bscal.Table {
	t.Helper()
	ys, err := bscal.LoadSeed(fixtures.YearTableSeed)
	if err != nil {
		t.Fatal(err)
	}
	tb, err := bscal.NewTable(1, ys)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func mustEpoch(t *testing.T, tb *bscal.Table, cal bscal.Calendar, s string) int64 {
	t.Helper()
	d, err := bscal.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	n, err := tb.ToEpoch(cal, d)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func eventRow(t *testing.T, tb *bscal.Table, basis bscal.Calendar, start, end, recurrence string, rrule *string) *row {
	t.Helper()
	s, _ := bscal.ParseDate(start)
	e, _ := bscal.ParseDate(end)
	d, err := Materialize(tb, basis, s, e)
	if err != nil {
		t.Fatal(err)
	}
	return &row{Basis: string(basis), StartLocal: start, EndLocal: end, ADStart: timeOf(d.ADStart), ADEnd: timeOf(d.ADEnd),
		Recurrence: recurrence, RRule: rrule}
}

func adDates(spans []span) []string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = bscal.EpochToAD(s.start).String()
	}
	return out
}

func TestExpandYearlyBSClampsToMonthLength(t *testing.T) {
	tb := table(t)
	y2084, _ := tb.Year(2084)
	y2085, _ := tb.Year(2085)
	if y2084.MonthDays[2] != 32 || y2085.MonthDays[2] != 31 {
		t.Skip("fixture assumption changed: need Asar with 32 days in 2084 and 31 in 2085")
	}
	r := eventRow(t, tb, bscal.BS, "2084-03-32", "2084-03-32", "yearly_bs", nil)
	from := mustEpoch(t, tb, bscal.BS, "2085-01-01")
	to := mustEpoch(t, tb, bscal.BS, "2085-12-30")
	got := expand(tb, r, from, to)
	if len(got) != 1 {
		t.Fatalf("got %d occurrences", len(got))
	}
	bs, _ := tb.EpochToBS(got[0].start)
	if bs.String() != "2085-03-31" {
		t.Fatalf("day 32 must clamp to the last day of Asar 2085, got %s", bs)
	}
}

func TestExpandYearlyADLeapDay(t *testing.T) {
	tb := table(t)
	r := eventRow(t, tb, bscal.AD, "2024-02-29", "2024-02-29", "yearly_ad", nil)
	got := adDates(expand(tb, r, mustEpoch(t, tb, bscal.AD, "2025-01-01"), mustEpoch(t, tb, bscal.AD, "2028-12-31")))
	want := []string{"2025-02-28", "2026-02-28", "2027-02-28", "2028-02-29"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandRespectsStartAndUntil(t *testing.T) {
	tb := table(t)
	r := eventRow(t, tb, bscal.AD, "2026-01-01", "2026-01-01", "yearly_ad", nil)
	until := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	r.RecurUntil = &until
	got := adDates(expand(tb, r, mustEpoch(t, tb, bscal.AD, "2020-01-01"), mustEpoch(t, tb, bscal.AD, "2030-12-31")))
	if strings.Join(got, ",") != "2026-01-01,2027-01-01" {
		t.Fatalf("occurrences must start at the first date and stop at recurUntil: %v", got)
	}
}

func TestExpandRRuleAndMultiDayOverlap(t *testing.T) {
	tb := table(t)
	rule := "FREQ=WEEKLY;BYDAY=FR"
	r := eventRow(t, tb, bscal.AD, "2026-10-02", "2026-10-03", "rrule", &rule) // two-day weekend event
	got := expand(tb, r, mustEpoch(t, tb, bscal.AD, "2026-10-03"), mustEpoch(t, tb, bscal.AD, "2026-10-31"))
	dates := adDates(got)
	// The 2 Oct occurrence ends on 3 Oct, so it overlaps the window.
	if strings.Join(dates, ",") != "2026-10-02,2026-10-09,2026-10-16,2026-10-23,2026-10-30" {
		t.Fatalf("got %v", dates)
	}
	if got[0].end-got[0].start != 1 {
		t.Fatal("duration must be preserved")
	}
}

func TestValidateInput(t *testing.T) {
	tb := table(t)
	ok := EventInput{Category: "festival", Title: Text{En: "Test"}, Basis: "BS", Start: "2083-06-20"}
	if _, fe := validate(tb, ok); len(fe) != 0 {
		t.Fatalf("valid input rejected: %v", fe)
	}
	f := false
	nine := "09:00"
	eight := "08:00"
	daily := "FREQ=DAILY"
	secondly := "FREQ=SECONDLY"
	cases := map[string]struct {
		in    EventInput
		field string
	}{
		"missing title":         {EventInput{Category: "x", Basis: "BS", Start: "2083-01-01"}, "title.en"},
		"bad basis":             {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "XX", Start: "2083-01-01"}, "basis"},
		"impossible BS day":     {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "BS", Start: "2083-03-33"}, "start"},
		"out of range":          {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "AD", Start: "1900-01-01"}, "start"},
		"end before start":      {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "AD", Start: "2026-10-02", End: "2026-10-01"}, "start"},
		"too long":              {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "AD", Start: "2026-01-01", End: "2027-06-01"}, "end"},
		"timed without start":   {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "AD", Start: "2026-01-01", AllDay: &f}, "startTime"},
		"end before start time": {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "AD", Start: "2026-01-01", AllDay: &f, StartTime: &nine, EndTime: &eight}, "endTime"},
		"yearly_bs on AD":       {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "AD", Start: "2026-01-01", Recurrence: "yearly_bs"}, "recurrence"},
		"rrule on BS":           {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "BS", Start: "2083-01-01", Recurrence: "rrule", RRule: &daily}, "recurrence"},
		"rrule too frequent":    {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "AD", Start: "2026-01-01", Recurrence: "rrule", RRule: &secondly}, "rrule"},
		"rrule without rule":    {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "AD", Start: "2026-01-01", Recurrence: "rrule"}, "rrule"},
		"bad tz":                {EventInput{Category: "x", Title: Text{En: "a"}, Basis: "AD", Start: "2026-01-01", TZ: "Mars/Base"}, "tz"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, fe := validate(tb, c.in)
			for _, e := range fe {
				if e.Field == c.field {
					return
				}
			}
			t.Fatalf("want an error on %q, got %v", c.field, fe)
		})
	}
}

func TestMergePatchSemantics(t *testing.T) {
	desc := &Text{En: "old"}
	rr := "FREQ=DAILY"
	cur := EventInput{Category: "a", Title: Text{En: "T", Ne: "ट"}, Description: desc, Basis: "AD", Start: "2026-01-01", RRule: &rr}
	var out EventInput
	if err := ApplyMergePatch(cur, []byte(`{"title":{"en":"New"},"description":null,"rrule":null}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Title.En != "New" || out.Title.Ne != "ट" {
		t.Fatalf("nested objects must merge: %+v", out.Title)
	}
	if out.Description != nil || out.RRule != nil {
		t.Fatal("null must clear a field")
	}
	if out.Start != "2026-01-01" || out.Category != "a" {
		t.Fatal("unmentioned fields must stay")
	}
	err := ApplyMergePatch(cur, []byte(`{"surprise":1}`), &out)
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.CodeValidation || ae.Fields[0].Field != "surprise" {
		t.Fatalf("unknown fields must be rejected, got %v", err)
	}
	if err := ApplyMergePatch(cur, []byte(`[1,2]`), &out); err == nil {
		t.Fatal("a non-object patch must be rejected")
	}
}

func TestICSFolding(t *testing.T) {
	long := strings.Repeat("दिवस ", 30)
	folded := fold("SUMMARY:" + long)
	for _, line := range strings.Split(strings.TrimSuffix(folded, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line of %d octets", len(line))
		}
	}
	if strings.ReplaceAll(strings.TrimSuffix(folded, "\r\n"), "\r\n ", "") != "SUMMARY:"+long {
		t.Fatal("unfolding must restore the original line")
	}
	if esc("a,b;c\nd") != `a\,b\;c\nd` {
		t.Fatalf("escape: %s", esc("a,b;c\nd"))
	}
}

func TestOccurrenceJSONHidesInternals(t *testing.T) {
	b, _ := json.Marshal(Occurrence{startEpoch: 5, endEpoch: 6})
	if strings.Contains(string(b), "Epoch") {
		t.Fatal("internal fields leaked into JSON")
	}
}
