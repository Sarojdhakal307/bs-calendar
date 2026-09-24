package bscal

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"bscalendar/fixtures"
)

func seedTable(tb testing.TB) *Table {
	tb.Helper()
	years, err := LoadSeed(fixtures.YearTableSeed)
	if err != nil {
		tb.Fatalf("load seed: %v", err)
	}
	t, err := NewTable(1, years)
	if err != nil {
		tb.Fatalf("new table: %v", err)
	}
	return t
}

type goldenFile struct {
	Cases []struct {
		AD, BS, Source, Note string
	} `json:"cases"`
	Invalid []struct {
		Calendar, Date, Reason string
	} `json:"invalid"`
	OutOfRange []struct {
		Calendar, Date string
	} `json:"outOfRange"`
}

func loadGolden(t *testing.T) goldenFile {
	t.Helper()
	var g goldenFile
	if err := json.Unmarshal(fixtures.Conversions, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Cases) == 0 {
		t.Fatal("no golden cases")
	}
	return g
}

func mustDate(t testing.TB, s string) Date {
	t.Helper()
	d, err := ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGoldenFixtures(t *testing.T) {
	tbl := seedTable(t)
	for _, c := range loadGolden(t).Cases {
		t.Run(c.AD+"_"+c.BS, func(t *testing.T) {
			bs, err := tbl.ToBS(mustDate(t, c.AD))
			if err != nil || bs.String() != c.BS {
				t.Fatalf("ToBS(%s) = %s, %v; want %s (source: %s)", c.AD, bs, err, c.BS, c.Source)
			}
			if tbl.StatusOf(bs.Year) != Verified {
				t.Fatalf("golden case in non-verified year %d", bs.Year)
			}
			ad, err := tbl.ToAD(mustDate(t, c.BS))
			if err != nil || ad.String() != c.AD {
				t.Fatalf("ToAD(%s) = %s, %v; want %s", c.BS, ad, err, c.AD)
			}
		})
	}
}

func TestInvalidAndOutOfRange(t *testing.T) {
	tbl := seedTable(t)
	g := loadGolden(t)
	for _, c := range g.Invalid {
		d, perr := ParseDate(c.Date)
		var err error
		if perr != nil {
			err = perr
		} else {
			_, err = tbl.ToEpoch(Calendar(c.Calendar), d)
		}
		if !errors.Is(err, ErrInvalidDate) {
			t.Errorf("%s %s (%s): got %v, want ErrInvalidDate", c.Calendar, c.Date, c.Reason, err)
		}
	}
	for _, c := range g.OutOfRange {
		_, err := tbl.ToEpoch(Calendar(c.Calendar), mustDate(t, c.Date))
		if !errors.Is(err, ErrOutOfRange) {
			t.Errorf("%s %s: got %v, want ErrOutOfRange", c.Calendar, c.Date, err)
		}
	}
}

// Every supported day must round-trip and BS days must be consecutive (docs/reliable.md §5.4).
func TestEveryDayRoundTripAndConsecutive(t *testing.T) {
	tbl := seedTable(t)
	lo, hi := tbl.EpochRange()
	prev, err := tbl.EpochToBS(lo)
	if err != nil {
		t.Fatal(err)
	}
	if prev != (Date{Year: tbl.MinYear(), Month: 1, Day: 1}) {
		t.Fatalf("first day is %s", prev)
	}
	for n := lo + 1; n <= hi; n++ {
		ad := EpochToAD(n)
		bs, err := tbl.ToBS(ad)
		if err != nil {
			t.Fatalf("ToBS(%s): %v", ad, err)
		}
		back, err := tbl.ToAD(bs)
		if err != nil || back != ad {
			t.Fatalf("round trip %s -> %s -> %s (%v)", ad, bs, back, err)
		}
		if !tbl.IsNextDay(prev, bs) {
			t.Fatalf("gap between %s and %s", prev, bs)
		}
		prev = bs
	}
	last, _ := tbl.Year(tbl.MaxYear())
	if prev != (Date{Year: last.Year, Month: 12, Day: last.MonthDays[11]}) {
		t.Fatalf("last day is %s", prev)
	}
}

// The integer civil algorithm must agree with the standard library.
func TestCivilMatchesStdlib(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 200_000; i++ {
		n := r.Int64N(200_000) - 100_000 // 1696..2243
		y, m, d := CivilFromDays(n)
		want := time.Unix(n*86400, 0).UTC()
		if want.Year() != y || int(want.Month()) != m || want.Day() != d {
			t.Fatalf("CivilFromDays(%d) = %d-%d-%d, stdlib %s", n, y, m, d, want.Format(time.DateOnly))
		}
		if got := DaysFromCivil(y, m, d); got != n {
			t.Fatalf("DaysFromCivil(%d-%d-%d) = %d, want %d", y, m, d, got, n)
		}
		if got, wantW := Weekday(n), int(want.Weekday()); got != wantW {
			t.Fatalf("Weekday(%d) = %d, want %d", n, got, wantW)
		}
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	tbl := seedTable(t)
	s := tbl.Snapshot()
	if s.SHA256 == "" || len(s.Years) != tbl.MaxYear()-tbl.MinYear()+1 {
		t.Fatalf("bad snapshot header %+v", s.SHA256)
	}
	b, _ := json.Marshal(s)
	var back Snapshot
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	t2, err := TableFromSnapshot(back)
	if err != nil {
		t.Fatal(err)
	}
	if t2.Snapshot().SHA256 != s.SHA256 {
		t.Fatal("checksum changed after round trip")
	}
	back.Years[3].Days[0]++ // tamper
	if _, err := TableFromSnapshot(back); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
}

func TestValidateRejectsBrokenTables(t *testing.T) {
	good := seedTable(t).Years()[:3]
	cases := map[string]func(ys []YearInfo){
		"month too short":   func(ys []YearInfo) { ys[1].MonthDays[0] = 28 },
		"month too long":    func(ys []YearInfo) { ys[1].MonthDays[0] = 33 },
		"gap in years":      func(ys []YearInfo) { ys[2].Year++ },
		"broken start":      func(ys []YearInfo) { ys[2].StartDay++ },
		"length changed":    func(ys []YearInfo) { ys[1].MonthDays[0]--; ys[1].MonthDays[1]-- },
		"bad status":        func(ys []YearInfo) { ys[0].Status = "maybe" },
		"new year in March": func(ys []YearInfo) { ys[0].StartDay -= 40 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ys := append([]YearInfo(nil), good...)
			mutate(ys)
			_, err := Validate(ys)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected ValidationError, got %v", err)
			}
		})
	}
	if _, err := Validate(good); err != nil {
		t.Fatalf("good table rejected: %v", err)
	}
}

func TestMonthGrid(t *testing.T) {
	tbl := seedTable(t)
	cells, err := tbl.MonthGrid(BS, 2083, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != GridCells || cells[0].Weekday != 0 {
		t.Fatalf("grid must have 42 cells starting on Sunday")
	}
	in := 0
	for i, c := range cells {
		if c.InMonth {
			in++
			if c.BS.Month != 1 || c.BS.Year != 2083 {
				t.Fatalf("cell %d in month but BS %s", i, c.BS)
			}
		}
		if i > 0 && c.EpochDay != cells[i-1].EpochDay+1 {
			t.Fatal("cells not consecutive")
		}
	}
	y, _ := tbl.Year(2083)
	if in != y.MonthDays[0] {
		t.Fatalf("in-month cells %d, want %d", in, y.MonthDays[0])
	}
	// Monday start
	cells, _ = tbl.MonthGrid(AD, 2026, 9, 1)
	if cells[0].Weekday != 1 {
		t.Fatal("weekStart=1 must start on Monday")
	}
}

func TestDigitsAndNames(t *testing.T) {
	if got := ToNepaliDigits("2083-06-08"); got != "२०८३-०६-०८" {
		t.Fatalf("ToNepaliDigits = %q", got)
	}
	if got := FromNepaliDigits("२०८३-०६-०८"); got != "2083-06-08" {
		t.Fatalf("FromNepaliDigits = %q", got)
	}
	if d, err := ParseDate("२०८३-०१-०१"); err != nil || d != (Date{2083, 1, 1}) {
		t.Fatalf("ParseDate Devanagari = %v %v", d, err)
	}
	if MonthName(BS, 6).En != "Ashwin" || WeekdayName(6).En != "Saturday" {
		t.Fatal("names")
	}
}

func TestNepalToday(t *testing.T) {
	// 18:14:59 UTC is 23:59:59 in Nepal; 18:15 UTC is the next day in Nepal.
	before := time.Date(2026, 9, 24, 18, 14, 59, 0, time.UTC)
	after := before.Add(time.Second)
	if EpochToAD(NepalTodayEpoch(before)) != (Date{2026, 9, 24}) {
		t.Fatal("before Nepal midnight")
	}
	if EpochToAD(NepalTodayEpoch(after)) != (Date{2026, 9, 25}) {
		t.Fatal("after Nepal midnight")
	}
	loc, err := time.LoadLocation("Asia/Kathmandu")
	if err == nil && TodayEpochIn(after, loc) != NepalTodayEpoch(after) {
		t.Fatal("fixed offset disagrees with tz database")
	}
}

func FuzzParseAndConvertBS(f *testing.F) {
	tbl := seedTable(f)
	for _, s := range []string{"2083-01-01", "2083-03-32", "2083-13-01", "", "२०८३-०१-०१", "1975-01-01", "2100-12-30"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseDate(s)
		if err != nil {
			return
		}
		n, err := tbl.BSToEpoch(d)
		if err != nil {
			if !errors.Is(err, ErrInvalidDate) && !errors.Is(err, ErrOutOfRange) {
				t.Fatalf("unexpected error type: %v", err)
			}
			return
		}
		back, err := tbl.EpochToBS(n)
		if err != nil || back != d {
			t.Fatalf("parsed %q to %s but round trip gave %s (%v)", s, d, back, err)
		}
	})
}
