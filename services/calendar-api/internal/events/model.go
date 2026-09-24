// Package events manages categories and events, expands recurrences, and builds the
// immutable BS-year buckets that clients cache (docs/architecture.md §7.5).
package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/bscal"
)

// TableSource provides the current year table.
type TableSource interface {
	Table() *bscal.Table
}

// Text is a bilingual string. English is required; Nepali is optional.
type Text struct {
	En string `json:"en"`
	Ne string `json:"ne,omitempty"`
}

// DateRange is an inclusive date range in one calendar.
type DateRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Colors are a category's colours per theme.
type Colors struct {
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

// Event is the admin view of an event.
type Event struct {
	ID          string     `json:"id"`
	Category    string     `json:"category"`
	Title       Text       `json:"title"`
	Description *Text      `json:"description"`
	Basis       string     `json:"basis"`
	Start       string     `json:"start"`
	End         string     `json:"end"`
	AD          DateRange  `json:"ad"`
	BS          DateRange  `json:"bs"`
	AllDay      bool       `json:"allDay"`
	StartTime   *string    `json:"startTime"`
	EndTime     *string    `json:"endTime"`
	TZ          string     `json:"tz"`
	Recurrence  string     `json:"recurrence"`
	RRule       *string    `json:"rrule"`
	RecurUntil  *string    `json:"recurUntil"`
	IsHoliday   *bool      `json:"isHoliday"`
	Status      string     `json:"status"`
	Version     int        `json:"version"`
	CreatedBy   string     `json:"createdBy"`
	UpdatedBy   string     `json:"updatedBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	DeletedAt   *time.Time `json:"deletedAt"`
}

// EventInput creates an event, or is the merged result of a PATCH.
type EventInput struct {
	Category    string  `json:"category"`
	Title       Text    `json:"title"`
	Description *Text   `json:"description,omitempty"`
	Basis       string  `json:"basis"`
	Start       string  `json:"start"`
	End         string  `json:"end,omitempty"`
	AllDay      *bool   `json:"allDay,omitempty"`
	StartTime   *string `json:"startTime,omitempty"`
	EndTime     *string `json:"endTime,omitempty"`
	TZ          string  `json:"tz,omitempty"`
	Recurrence  string  `json:"recurrence,omitempty"`
	RRule       *string `json:"rrule,omitempty"`
	RecurUntil  *string `json:"recurUntil,omitempty"`
	IsHoliday   *bool   `json:"isHoliday,omitempty"`
}

// DatePair is one day in both calendars. BS is null only outside the supported range.
type DatePair struct {
	AD string  `json:"ad"`
	BS *string `json:"bs"`
}

// Occurrence is a public, expanded event instance.
type Occurrence struct {
	ID          string    `json:"id"`
	EventID     string    `json:"eventId"`
	Title       Text      `json:"title"`
	Description *Text     `json:"description"`
	Category    string    `json:"category"`
	IsHoliday   bool      `json:"isHoliday"`
	Basis       string    `json:"basis"`
	Start       DatePair  `json:"start"`
	End         DatePair  `json:"end"`
	AllDay      bool      `json:"allDay"`
	StartTime   *string   `json:"startTime"`
	EndTime     *string   `json:"endTime"`
	TZ          string    `json:"tz"`
	Recurring   bool      `json:"recurring"`
	Color       Colors    `json:"color"`
	Version     int       `json:"version"`
	UpdatedAt   time.Time `json:"updatedAt"`
	startEpoch  int64
	endEpoch    int64
}

// Bucket is every published event occurrence touching one BS year.
type Bucket struct {
	BSYear      int              `json:"bsYear"`
	Version     int64            `json:"version"`
	Status      bscal.Status     `json:"status"`
	Range       DateRange        `json:"range"`
	GeneratedAt time.Time        `json:"generatedAt"`
	Categories  []PublicCategory `json:"categories"`
	Events      []Occurrence     `json:"events"`
}

// row mirrors the events table joined with its category.
type row struct {
	ID          string
	TenantID    string
	CategoryID  string
	CategoryKey string
	Title       Text
	Description *Text
	Basis       string
	StartLocal  string
	EndLocal    string
	ADStart     time.Time
	ADEnd       time.Time
	BSYearStart int
	BSYearEnd   int
	AllDay      bool
	StartTime   *string
	EndTime     *string
	TZ          string
	Recurrence  string
	RRule       *string
	RecurUntil  *time.Time
	IsHoliday   *bool
	Status      string
	Version     int
	CreatedBy   string
	UpdatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

const rowCols = `e.id::text, e.tenant_id::text, e.category_id::text, c.key, e.title, e.description, e.date_basis,
	e.start_local, e.end_local, e.ad_start, e.ad_end, e.bs_year_start, e.bs_year_end, e.all_day, e.start_time,
	e.end_time, e.tz, e.recurrence, e.rrule, e.recur_until, e.is_holiday, e.status, e.version,
	e.created_by::text, e.updated_by::text, e.created_at, e.updated_at, e.deleted_at`

func (r *row) dest() []any {
	return []any{&r.ID, &r.TenantID, &r.CategoryID, &r.CategoryKey, &r.Title, &r.Description, &r.Basis,
		&r.StartLocal, &r.EndLocal, &r.ADStart, &r.ADEnd, &r.BSYearStart, &r.BSYearEnd, &r.AllDay, &r.StartTime,
		&r.EndTime, &r.TZ, &r.Recurrence, &r.RRule, &r.RecurUntil, &r.IsHoliday, &r.Status, &r.Version,
		&r.CreatedBy, &r.UpdatedBy, &r.CreatedAt, &r.UpdatedAt, &r.DeletedAt}
}

func dateOf(t time.Time) bscal.Date {
	t = t.UTC()
	return bscal.Date{Year: t.Year(), Month: int(t.Month()), Day: t.Day()}
}

func epochOf(t time.Time) int64 {
	t = t.UTC()
	return bscal.DaysFromCivil(t.Year(), int(t.Month()), t.Day())
}

func timeOf(d bscal.Date) time.Time {
	return time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
}

func bsString(t *bscal.Table, n int64) *string {
	d, err := t.EpochToBS(n)
	if err != nil {
		return nil
	}
	s := d.String()
	return &s
}

func (r *row) toEvent(t *bscal.Table) Event {
	e := Event{
		ID: r.ID, Category: r.CategoryKey, Title: r.Title, Description: r.Description, Basis: r.Basis,
		Start: r.StartLocal, End: r.EndLocal,
		AD:     DateRange{Start: dateOf(r.ADStart).String(), End: dateOf(r.ADEnd).String()},
		AllDay: r.AllDay, StartTime: r.StartTime, EndTime: r.EndTime, TZ: r.TZ,
		Recurrence: r.Recurrence, RRule: r.RRule, IsHoliday: r.IsHoliday, Status: r.Status, Version: r.Version,
		CreatedBy: r.CreatedBy, UpdatedBy: r.UpdatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, DeletedAt: r.DeletedAt,
	}
	if bs := bsString(t, epochOf(r.ADStart)); bs != nil {
		e.BS.Start = *bs
	}
	if bs := bsString(t, epochOf(r.ADEnd)); bs != nil {
		e.BS.End = *bs
	}
	if r.RecurUntil != nil {
		s := dateOf(*r.RecurUntil).String()
		e.RecurUntil = &s
	}
	return e
}

func (r *row) toInput() EventInput {
	allDay := r.AllDay
	in := EventInput{
		Category: r.CategoryKey, Title: r.Title, Description: r.Description, Basis: r.Basis,
		Start: r.StartLocal, End: r.EndLocal, AllDay: &allDay, StartTime: r.StartTime, EndTime: r.EndTime,
		TZ: r.TZ, Recurrence: r.Recurrence, RRule: r.RRule, IsHoliday: r.IsHoliday,
	}
	if r.RecurUntil != nil {
		s := dateOf(*r.RecurUntil).String()
		in.RecurUntil = &s
	}
	return in
}

// ETag returns the strong ETag for a resource version.
func ETag(version int) string { return `"v` + strconv.Itoa(version) + `"` }

// ---- JSON Merge Patch (RFC 7396) -------------------------------------------

// ApplyMergePatch applies an RFC 7396 merge patch to current and decodes the result
// strictly into out, so unknown fields are rejected.
func ApplyMergePatch(current any, patch []byte, out any) error {
	base, err := json.Marshal(current)
	if err != nil {
		return err
	}
	var target map[string]any
	if err := json.Unmarshal(base, &target); err != nil {
		return err
	}
	var p any
	if err := json.Unmarshal(patch, &p); err != nil {
		return apperr.BadRequest("Request body is not valid JSON: " + err.Error())
	}
	pm, ok := p.(map[string]any)
	if !ok {
		return apperr.BadRequest("A merge patch must be a JSON object.")
	}
	merged, _ := json.Marshal(mergePatch(target, pm))
	dec := json.NewDecoder(bytes.NewReader(merged))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return DecodeError(err)
	}
	return nil
}

func mergePatch(target, patch map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for k, v := range patch {
		switch pv := v.(type) {
		case nil:
			delete(target, k)
		case map[string]any:
			tm, _ := target[k].(map[string]any)
			target[k] = mergePatch(tm, pv)
		default:
			target[k] = v
		}
	}
	return target
}

// DecodeError turns a JSON decoding error into a helpful 400/422.
func DecodeError(err error) error {
	var ute *json.UnmarshalTypeError
	var se *json.SyntaxError
	switch {
	case errors.As(err, &ute):
		return apperr.Validation(apperr.FieldError{Field: ute.Field, Message: "must be of type " + ute.Type.String()})
	case errors.As(err, &se):
		return apperr.BadRequest(fmt.Sprintf("Malformed JSON at offset %d.", se.Offset))
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		f := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return apperr.Validation(apperr.FieldError{Field: f, Message: "unknown field"})
	}
	return apperr.BadRequest("Invalid JSON body: " + err.Error())
}
