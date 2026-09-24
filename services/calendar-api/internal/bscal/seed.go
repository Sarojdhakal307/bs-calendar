package bscal

import (
	"encoding/json"
	"fmt"
)

// SeedFile is the format of fixtures/year-table.seed.json.
type SeedFile struct {
	SchemaVersion int `json:"schemaVersion"`
	Anchor        struct {
		BS string `json:"bs"`
		AD string `json:"ad"`
	} `json:"anchor"`
	Provenance json.RawMessage `json:"provenance"`
	Years      []SeedYear      `json:"years"`
}

// SeedYear is one year in the seed file.
type SeedYear struct {
	Y      int    `json:"y"`
	Days   []int  `json:"days"`
	Status Status `json:"status"`
	Source string `json:"source"`
}

// LoadSeed parses a seed file and computes each year's start day from the anchor.
// The anchor must be 1 Baisakh of the first year.
func LoadSeed(b []byte) ([]YearInfo, error) {
	var f SeedFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	if f.SchemaVersion != 1 {
		return nil, fmt.Errorf("seed: unsupported schemaVersion %d", f.SchemaVersion)
	}
	if len(f.Years) == 0 {
		return nil, fmt.Errorf("seed: no years")
	}
	abs, err := ParseDate(f.Anchor.BS)
	if err != nil {
		return nil, fmt.Errorf("seed anchor: %w", err)
	}
	if abs != (Date{Year: f.Years[0].Y, Month: 1, Day: 1}) {
		return nil, fmt.Errorf("seed: anchor BS %s must be 1 Baisakh of the first year %d", abs, f.Years[0].Y)
	}
	aad, err := ParseDate(f.Anchor.AD)
	if err != nil {
		return nil, fmt.Errorf("seed anchor: %w", err)
	}
	start, err := ADToEpoch(aad)
	if err != nil {
		return nil, fmt.Errorf("seed anchor: %w", err)
	}
	years := make([]YearInfo, len(f.Years))
	for i, sy := range f.Years {
		if len(sy.Days) != 12 {
			return nil, fmt.Errorf("seed: BS %d has %d months", sy.Y, len(sy.Days))
		}
		var md [12]int
		copy(md[:], sy.Days)
		years[i] = YearInfo{Year: sy.Y, MonthDays: md, StartDay: start, Status: sy.Status, Source: sy.Source}
		start += int64(years[i].Length())
	}
	if _, err := Validate(years); err != nil {
		return nil, err
	}
	return years, nil
}
