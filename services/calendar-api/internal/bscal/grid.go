package bscal

import "fmt"

// GridCells is the fixed number of cells in a month grid (6 weeks), so layouts never jump.
const GridCells = 42

// Cell is one day in a month grid.
type Cell struct {
	EpochDay int64
	AD       Date
	BS       Date
	BSOk     bool // false when the day is outside the supported BS range
	Weekday  int  // 0 = Sunday
	InMonth  bool // belongs to the month being shown
}

// MonthGrid returns 42 cells for a month in the given calendar, starting on weekStart (0..6).
func (t *Table) MonthGrid(cal Calendar, year, month, weekStart int) ([]Cell, error) {
	if weekStart < 0 || weekStart > 6 {
		return nil, fmt.Errorf("%w: weekStart %d", ErrInvalidDate, weekStart)
	}
	first, err := t.ToEpoch(cal, Date{Year: year, Month: month, Day: 1})
	if err != nil {
		return nil, err
	}
	days, err := t.DaysInMonth(cal, year, month)
	if err != nil {
		return nil, err
	}
	lead := (Weekday(first) - weekStart + 7) % 7
	start := first - int64(lead)
	cells := make([]Cell, GridCells)
	for i := range cells {
		n := start + int64(i)
		c := Cell{EpochDay: n, AD: EpochToAD(n), Weekday: Weekday(n), InMonth: n >= first && n < first+int64(days)}
		if bs, err := t.EpochToBS(n); err == nil {
			c.BS, c.BSOk = bs, true
		}
		cells[i] = c
	}
	return cells, nil
}
