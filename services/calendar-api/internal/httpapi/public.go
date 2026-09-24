package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/bscal"
	"bscalendar/services/calendar-api/internal/events"
	"bscalendar/services/calendar-api/internal/uiconfig"
)

// ---- manifest ----------------------------------------------------------------

type supportedRange struct {
	MinBSYear int    `json:"minBsYear"`
	MaxBSYear int    `json:"maxBsYear"`
	MinAD     string `json:"minAd"`
	MaxAD     string `json:"maxAd"`
}

type manifestLinks struct {
	Data           string `json:"data"`
	Config         string `json:"config"`
	BucketTemplate string `json:"bucketTemplate"`
}

type manifest struct {
	DataVersion          int64                   `json:"dataVersion"`
	DataSHA256           string                  `json:"dataSha256"`
	SupportedRange       supportedRange          `json:"supportedRange"`
	CurrentBSYear        int                     `json:"currentBsYear"`
	FirstProjectedBSYear *int                    `json:"firstProjectedBsYear"`
	Config               uiconfig.ManifestConfig `json:"config"`
	EventBuckets         map[string]int64        `json:"eventBuckets"`
	DefaultBucketVersion int64                   `json:"defaultBucketVersion"`
	MinSupportedClient   string                  `json:"minSupportedClient"`
	UpdateRequired       bool                    `json:"updateRequired"`
	Links                manifestLinks           `json:"links"`
	ServerTime           *time.Time              `json:"serverTime,omitempty"`
}

func (s *Server) rangeOf(t *bscal.Table) supportedRange {
	lo, hi := t.EpochRange()
	return supportedRange{MinBSYear: t.MinYear(), MaxBSYear: t.MaxYear(), MinAD: bscal.EpochToAD(lo).String(), MaxAD: bscal.EpochToAD(hi).String()}
}

func firstProjected(t *bscal.Table) *int {
	for _, y := range t.Years() {
		if y.Status == bscal.Projected {
			v := y.Year
			return &v
		}
	}
	return nil
}

func (s *Server) currentBSYear(t *bscal.Table) int {
	if d, err := t.EpochToBS(bscal.NepalTodayEpoch(s.now())); err == nil {
		return d.Year
	}
	return t.MaxYear()
}

// getManifest is the single frequently-changing resource. Everything it points to is immutable.
func (s *Server) getManifest(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	app := q.Get("app")
	if app == "" {
		app = "web"
	}
	cv := q.Get("clientVersion")
	var fe []apperr.FieldError
	if !uiconfig.ValidApp(app) {
		fe = append(fe, apperr.FieldError{Field: "app", Message: "2-31 characters: lower-case letters, digits, dashes"})
	}
	if _, ok := parseSemver(cv); cv != "" && !ok {
		fe = append(fe, apperr.FieldError{Field: "clientVersion", Message: "must look like 1.2.3"})
	}
	if len(fe) > 0 {
		return apperr.Validation(fe...)
	}
	tenant := info(r).tenantID
	t := s.data.Table()
	_, dataVersion, sha := s.data.Current()
	cfg, err := s.ui.Manifest(r.Context(), tenant, app, cv)
	if err != nil {
		return err
	}
	buckets, def, err := s.events.ManifestBuckets(r.Context(), tenant)
	if err != nil {
		return err
	}
	m := manifest{
		DataVersion: dataVersion, DataSHA256: sha, SupportedRange: s.rangeOf(t), CurrentBSYear: s.currentBSYear(t),
		FirstProjectedBSYear: firstProjected(t), Config: cfg, EventBuckets: buckets, DefaultBucketVersion: def,
		MinSupportedClient: s.cfg.MinSupportedClient,
		UpdateRequired:     cv != "" && uiconfig.SemverLess(cv, s.cfg.MinSupportedClient),
		Links: manifestLinks{
			Data:           fmt.Sprintf("/v1/calendar/data/%d", dataVersion),
			Config:         fmt.Sprintf("/v1/ui-config/%s/%d", app, cfg.Version),
			BucketTemplate: "/v1/events/buckets/{bsYear}/{version}",
		},
	}
	// The ETag ignores serverTime so unchanged manifests revalidate with 304.
	body, _ := json.Marshal(m)
	etag := weakETag(body)
	now := s.now().UTC().Truncate(time.Second)
	m.ServerTime = &now
	writeJSON(w, r, http.StatusOK, m, cacheManifest, etag)
	return nil
}

func parseSemver(s string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ---- immutable resources ----------------------------------------------------------------

func (s *Server) getDataLatest(w http.ResponseWriter, r *http.Request) error {
	_, v, _ := s.data.Current()
	w.Header().Set("Cache-Control", cacheNoStore)
	http.Redirect(w, r, fmt.Sprintf("/v1/calendar/data/%d", v), http.StatusFound)
	return nil
}

func (s *Server) getData(w http.ResponseWriter, r *http.Request) error {
	v, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || v < 1 {
		return apperr.BadRequest("version must be a positive integer")
	}
	raw, err := s.data.SnapshotByVersion(r.Context(), v)
	if err != nil {
		return err
	}
	writeBytes(w, r, http.StatusOK, "application/json; charset=utf-8", raw, cacheImmutable, fmt.Sprintf(`"data-%d"`, v))
	return nil
}

func (s *Server) getUIConfig(w http.ResponseWriter, r *http.Request) error {
	app := r.PathValue("app")
	if !uiconfig.ValidApp(app) {
		return apperr.NotFound("app")
	}
	v, err := pathInt(r, "version")
	if err != nil || v < 0 {
		return apperr.BadRequest("version must be a non-negative integer")
	}
	raw, err := s.ui.PublicConfig(r.Context(), info(r).tenantID, app, v)
	if err != nil {
		return err
	}
	cache := cacheImmutable
	if v == 0 {
		cache = cacheHour // the built-in default changes with server releases
	}
	writeBytes(w, r, http.StatusOK, "application/json; charset=utf-8", raw, cache, fmt.Sprintf(`"config-%s-%d"`, app, v))
	return nil
}

// getBucket serves an immutable BS-year bucket. An older version redirects to the current one.
func (s *Server) getBucket(w http.ResponseWriter, r *http.Request) error {
	year, err := pathInt(r, "bsYear")
	if err != nil {
		return err
	}
	want, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || want < 0 {
		return apperr.BadRequest("version must be a non-negative integer")
	}
	tenant := info(r).tenantID
	t := s.data.Table()
	if _, ok := t.Year(year); !ok {
		return fmt.Errorf("%w: BS year %d (supported %d-%d)", bscal.ErrOutOfRange, year, t.MinYear(), t.MaxYear())
	}
	cur, err := s.events.BucketVersion(r.Context(), tenant, year)
	if err != nil {
		return err
	}
	switch {
	case want < cur:
		w.Header().Set("Cache-Control", cacheNoStore)
		http.Redirect(w, r, fmt.Sprintf("/v1/events/buckets/%d/%d", year, cur), http.StatusFound)
		return nil
	case want > cur:
		return &apperr.Error{Status: http.StatusNotFound, Code: apperr.CodeVersionNotFound, Title: "Version not found",
			Detail: fmt.Sprintf("Bucket %d has version %d; version %d does not exist yet.", year, cur, want)}
	}
	body, v, err := s.events.BucketJSON(r.Context(), tenant, year)
	if err != nil {
		return err
	}
	cache := cacheImmutable
	if v != want { // changed between the two reads: serve it, but do not cache it forever
		cache = cacheNoStore
	}
	writeBytes(w, r, http.StatusOK, "application/json; charset=utf-8", body, cache, fmt.Sprintf(`"bucket-%d-%d"`, year, v))
	return nil
}

// ---- queries ------------------------------------------------------------------

func (s *Server) parseDateParam(t *bscal.Table, r *http.Request, name string, basis bscal.Calendar) (int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, apperr.Validation(apperr.FieldError{Field: name, Message: "required (YYYY-MM-DD)"})
	}
	d, err := bscal.ParseDate(v)
	if err != nil {
		return 0, err
	}
	return t.ToEpoch(basis, d)
}

func basisParam(r *http.Request) (bscal.Calendar, error) {
	b := r.URL.Query().Get("basis")
	if b == "" {
		return bscal.AD, nil
	}
	c, err := bscal.ParseCalendar(b)
	if err != nil {
		return "", apperr.Validation(apperr.FieldError{Field: "basis", Message: "must be AD or BS"})
	}
	return c, nil
}

type eventsResult struct {
	Basis  string              `json:"basis"`
	From   string              `json:"from"`
	To     string              `json:"to"`
	Count  int                 `json:"count"`
	Events []events.Occurrence `json:"events"`
}

func (s *Server) getEvents(w http.ResponseWriter, r *http.Request) error {
	basis, err := basisParam(r)
	if err != nil {
		return err
	}
	t := s.data.Table()
	from, err := s.parseDateParam(t, r, "from", basis)
	if err != nil {
		return err
	}
	to, err := s.parseDateParam(t, r, "to", basis)
	if err != nil {
		return err
	}
	occ, err := s.events.Range(r.Context(), info(r).tenantID, events.RangeQuery{
		From: from, To: to, Categories: splitList(r.URL.Query().Get("category")), Q: r.URL.Query().Get("q"),
	})
	if err != nil {
		return err
	}
	res := eventsResult{Basis: string(basis), From: r.URL.Query().Get("from"), To: r.URL.Query().Get("to"), Count: len(occ), Events: occ}
	writeJSON(w, r, http.StatusOK, res, cacheShort, "")
	return nil
}

func (s *Server) getICS(w http.ResponseWriter, r *http.Request) error {
	t := s.data.Table()
	cur := s.currentBSYear(t)
	from, err := queryInt(r, "fromYear", max(cur-1, t.MinYear()), t.MinYear(), t.MaxYear())
	if err != nil {
		return err
	}
	to, err := queryInt(r, "toYear", min(cur+1, t.MaxYear()), t.MinYear(), t.MaxYear())
	if err != nil {
		return err
	}
	if to < from || to-from > 4 {
		return apperr.Validation(apperr.FieldError{Field: "toYear", Message: "must be within 4 years after fromYear"})
	}
	lang := r.URL.Query().Get("lang")
	if lang != "" && lang != "en" && lang != "ne" {
		return apperr.Validation(apperr.FieldError{Field: "lang", Message: "must be en or ne"})
	}
	body, err := s.events.ICS(r.Context(), info(r).tenantID, from, to, splitList(r.URL.Query().Get("category")), lang)
	if err != nil {
		return err
	}
	writeBytes(w, r, http.StatusOK, "text/calendar; charset=utf-8", body, cacheHour, "")
	return nil
}

func (s *Server) getCategories(w http.ResponseWriter, r *http.Request) error {
	cs, err := s.events.PublicCategories(r.Context(), info(r).tenantID)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"categories": cs}, cacheShort, "")
	return nil
}

// ---- conversion ---------------------------------------------------------------

type adInfo struct {
	Date      string          `json:"date"`
	Year      int             `json:"year"`
	Month     int             `json:"month"`
	Day       int             `json:"day"`
	MonthName bscal.Localized `json:"monthName"`
}

type bsInfo struct {
	Date        string          `json:"date"`
	DateNe      string          `json:"dateNe"`
	Year        int             `json:"year"`
	Month       int             `json:"month"`
	Day         int             `json:"day"`
	MonthName   bscal.Localized `json:"monthName"`
	DaysInMonth int             `json:"daysInMonth"`
	YearStatus  bscal.Status    `json:"yearStatus"`
}

type weekdayInfo struct {
	Index int             `json:"index"`
	Name  bscal.Localized `json:"name"`
}

type conversion struct {
	AD          adInfo      `json:"ad"`
	BS          bsInfo      `json:"bs"`
	Weekday     weekdayInfo `json:"weekday"`
	EpochDay    int64       `json:"epochDay"`
	DataVersion int64       `json:"dataVersion"`
	TimeZone    string      `json:"timeZone,omitempty"`
}

func convertEpoch(t *bscal.Table, n int64) (conversion, error) {
	bs, err := t.EpochToBS(n)
	if err != nil {
		return conversion{}, err
	}
	ad := bscal.EpochToAD(n)
	dim, _ := t.DaysInMonth(bscal.BS, bs.Year, bs.Month)
	w := bscal.Weekday(n)
	return conversion{
		AD: adInfo{Date: ad.String(), Year: ad.Year, Month: ad.Month, Day: ad.Day, MonthName: bscal.MonthName(bscal.AD, ad.Month)},
		BS: bsInfo{Date: bs.String(), DateNe: bscal.ToNepaliDigits(bs.String()), Year: bs.Year, Month: bs.Month, Day: bs.Day,
			MonthName: bscal.MonthName(bscal.BS, bs.Month), DaysInMonth: dim, YearStatus: t.StatusOf(bs.Year)},
		Weekday:     weekdayInfo{Index: w, Name: bscal.WeekdayName(w)},
		EpochDay:    n,
		DataVersion: t.Version(),
	}, nil
}

func (s *Server) getConvert(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	ad, bs := q.Get("ad"), q.Get("bs")
	if (ad == "") == (bs == "") {
		return apperr.Validation(apperr.FieldError{Field: "ad", Message: "send exactly one of ad or bs (YYYY-MM-DD)"})
	}
	t := s.data.Table()
	cal, raw := bscal.AD, ad
	if bs != "" {
		cal, raw = bscal.BS, bs
	}
	d, err := bscal.ParseDate(raw)
	if err != nil {
		return err
	}
	n, err := t.ToEpoch(cal, d)
	if err != nil {
		return err
	}
	c, err := convertEpoch(t, n)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, c, cacheHour, fmt.Sprintf(`W/"conv-%d-%d"`, t.Version(), n))
	return nil
}

func (s *Server) getToday(w http.ResponseWriter, r *http.Request) error {
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		tz = "Asia/Kathmandu"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "Local" {
		return apperr.Validation(apperr.FieldError{Field: "tz", Message: "must be an IANA time zone such as Asia/Kathmandu"})
	}
	t := s.data.Table()
	c, err := convertEpoch(t, bscal.TodayEpochIn(s.now(), loc))
	if err != nil {
		return err
	}
	c.TimeZone = tz
	writeJSON(w, r, http.StatusOK, c, "public, max-age=30, s-maxage=30", "")
	return nil
}

// ---- month grid -----------------------------------------------------------------

type monthCell struct {
	AD        string   `json:"ad"`
	BS        *string  `json:"bs"`
	Day       int      `json:"day"`
	Weekday   int      `json:"weekday"`
	InMonth   bool     `json:"inMonth"`
	IsToday   bool     `json:"isToday"`
	IsWeekend bool     `json:"isWeekend"`
	IsHoliday bool     `json:"isHoliday"`
	EventIDs  []string `json:"eventIds"`
}

type monthGrid struct {
	Basis       string              `json:"basis"`
	Year        int                 `json:"year"`
	Month       int                 `json:"month"`
	MonthName   bscal.Localized     `json:"monthName"`
	DaysInMonth int                 `json:"daysInMonth"`
	WeekStart   int                 `json:"weekStart"`
	YearStatus  *bscal.Status       `json:"yearStatus"`
	DataVersion int64               `json:"dataVersion"`
	Cells       []monthCell         `json:"cells"`
	Events      []events.Occurrence `json:"events"`
}

func (s *Server) getMonth(w http.ResponseWriter, r *http.Request) error {
	basis, err := bscal.ParseCalendar(r.PathValue("basis"))
	if err != nil {
		return apperr.BadRequest("basis must be AD or BS")
	}
	year, err := pathInt(r, "year")
	if err != nil {
		return err
	}
	month, err := pathInt(r, "month")
	if err != nil {
		return err
	}
	weekStart, err := queryInt(r, "weekStart", 0, 0, 6)
	if err != nil {
		return err
	}
	weekend := []int{6}
	if v := r.URL.Query().Get("weekendDays"); v != "" {
		weekend = nil
		for _, p := range splitList(v) {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n > 6 {
				return apperr.Validation(apperr.FieldError{Field: "weekendDays", Message: "comma-separated weekdays 0-6"})
			}
			weekend = append(weekend, n)
		}
	}
	t := s.data.Table()
	cells, err := t.MonthGrid(basis, year, month, weekStart)
	if err != nil {
		return err
	}
	dim, _ := t.DaysInMonth(basis, year, month)
	g := monthGrid{Basis: string(basis), Year: year, Month: month, MonthName: bscal.MonthName(basis, month), DaysInMonth: dim,
		WeekStart: weekStart, DataVersion: t.Version(), Cells: make([]monthCell, len(cells)), Events: []events.Occurrence{}}
	if basis == bscal.BS {
		st := t.StatusOf(year)
		g.YearStatus = &st
	}
	byDay := map[int64][]events.Occurrence{}
	if r.URL.Query().Get("include") == "events" {
		lo, hi := t.EpochRange()
		from, to := max(cells[0].EpochDay, lo), min(cells[len(cells)-1].EpochDay, hi)
		occ, err := s.events.Range(r.Context(), info(r).tenantID, events.RangeQuery{From: from, To: to})
		if err != nil {
			return err
		}
		g.Events = occ
		for _, o := range occ {
			s0, _ := t.ToEpoch(bscal.AD, mustDate(o.Start.AD))
			e0, _ := t.ToEpoch(bscal.AD, mustDate(o.End.AD))
			for n := max(s0, from); n <= min(e0, to); n++ {
				byDay[n] = append(byDay[n], o)
			}
		}
	}
	today := bscal.NepalTodayEpoch(s.now())
	for i, c := range cells {
		mc := monthCell{AD: c.AD.String(), Weekday: c.Weekday, InMonth: c.InMonth, IsToday: c.EpochDay == today,
			IsWeekend: contains(intsToStrings(weekend), strconv.Itoa(c.Weekday)), EventIDs: []string{}}
		if basis == bscal.BS {
			mc.Day = c.BS.Day
		} else {
			mc.Day = c.AD.Day
		}
		if c.BSOk {
			bs := c.BS.String()
			mc.BS = &bs
		}
		for _, o := range byDay[c.EpochDay] {
			mc.EventIDs = append(mc.EventIDs, o.ID)
			mc.IsHoliday = mc.IsHoliday || o.IsHoliday
		}
		g.Cells[i] = mc
	}
	writeJSON(w, r, http.StatusOK, g, "public, max-age=300, s-maxage=300", "")
	return nil
}

func mustDate(s string) bscal.Date {
	d, _ := bscal.ParseDate(s)
	return d
}

func intsToStrings(xs []int) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = strconv.Itoa(x)
	}
	return out
}

// ---- telemetry ---------------------------------------------------------------------

var telemetryTypes = []string{"table_rejected", "config_applied", "config_field_fallback", "sync_failed", "out_of_range", "update_required"}

type telemetryBatch struct {
	Events []struct {
		Type          string `json:"type"`
		App           string `json:"app"`
		ClientVersion string `json:"clientVersion"`
		DataVersion   int64  `json:"dataVersion"`
		ConfigVersion int    `json:"configVersion"`
		Detail        string `json:"detail"`
	} `json:"events"`
}

// postTelemetry accepts anonymous client health signals (docs/reliable.md §10).
func (s *Server) postTelemetry(w http.ResponseWriter, r *http.Request) error {
	var b telemetryBatch
	if err := decodeJSON(w, r, &b); err != nil {
		return err
	}
	if len(b.Events) == 0 || len(b.Events) > 50 {
		return apperr.Validation(apperr.FieldError{Field: "events", Message: "send 1-50 events"})
	}
	for i, e := range b.Events {
		if !contains(telemetryTypes, e.Type) {
			return apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("events[%d].type", i), Message: "must be one of " + strings.Join(telemetryTypes, ", ")})
		}
		if len(e.Detail) > 500 {
			return apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("events[%d].detail", i), Message: "at most 500 characters"})
		}
	}
	for _, e := range b.Events {
		s.metrics.telemetry.WithLabelValues(e.Type).Inc()
		if e.Type != "config_applied" {
			s.log.Info("client telemetry", "type", e.Type, "app", e.App, "clientVersion", e.ClientVersion,
				"dataVersion", e.DataVersion, "configVersion", e.ConfigVersion, "detail", e.Detail)
		}
	}
	writeJSON(w, r, http.StatusAccepted, map[string]int{"accepted": len(b.Events)}, cacheNoStore, "")
	return nil
}
