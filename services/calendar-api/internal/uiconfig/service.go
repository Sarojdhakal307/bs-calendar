// Package uiconfig manages admin-controlled UI configuration: JSON Schema validation,
// a WCAG contrast gate, a four-eyes draft → review → publish lifecycle, staged rollout
// and rollback. Published versions are immutable (docs/architecture.md §7.6, §11).
package uiconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"bscalendar/fixtures"
	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/audit"
	"bscalendar/services/calendar-api/internal/outbox"
	"bscalendar/services/calendar-api/internal/store"
)

// Finding is one validation message.
type Finding struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Report is the result of validating a config.
type Report struct {
	Valid    bool      `json:"valid"`
	Errors   []Finding `json:"errors"`
	Warnings []Finding `json:"warnings"`
}

// Version is one stored config version.
type Version struct {
	App              string          `json:"app"`
	Version          int             `json:"version"`
	Status           string          `json:"status"`
	Origin           string          `json:"origin"`
	SchemaVersion    int             `json:"schemaVersion"`
	MinClientVersion string          `json:"minClientVersion"`
	Note             *string         `json:"note"`
	RejectReason     *string         `json:"rejectReason"`
	Config           json.RawMessage `json:"config"`
	CreatedBy        string          `json:"createdBy"`
	ApprovedBy       *string         `json:"approvedBy"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
	PublishedAt      *time.Time      `json:"publishedAt"`
}

// Channel says which versions an app receives.
type Channel struct {
	App              string    `json:"app"`
	StableVersion    int       `json:"stableVersion"`
	CandidateVersion *int      `json:"candidateVersion"`
	CandidatePercent int       `json:"candidatePercent"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// ManifestConfig is the config section of the manifest.
type ManifestConfig struct {
	App              string     `json:"app"`
	Version          int        `json:"version"`
	MinClientVersion string     `json:"minClientVersion"`
	Candidate        *Candidate `json:"candidate"`
}

// Candidate is a staged-rollout version.
type Candidate struct {
	Version int `json:"version"`
	Percent int `json:"percent"`
}

// Input creates or updates a draft.
type Input struct {
	Config           json.RawMessage `json:"config"`
	MinClientVersion string          `json:"minClientVersion"`
	Note             string          `json:"note"`
}

// Service implements UI config management.
type Service struct {
	pool    *pgxpool.Pool
	schema  *jsonschema.Schema
	purge   bool
	baseURL string
}

// NewService compiles the embedded JSON Schema.
func NewService(pool *pgxpool.Pool, baseURL string, purge bool) (*Service, error) {
	sch, err := compileSchema()
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, schema: sch, purge: purge, baseURL: baseURL}, nil
}

func compileSchema() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(fixtures.UIConfigSchema))
	if err != nil {
		return nil, fmt.Errorf("ui config schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("ui-config.schema.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("ui-config.schema.json")
}

var (
	appRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// ValidApp reports whether an app key is well-formed.
func ValidApp(app string) bool { return appRe.MatchString(app) }

// Validate checks a config against the schema and the contrast rules.
func (s *Service) Validate(raw []byte) Report {
	rep := Report{Errors: []Finding{}, Warnings: []Finding{}}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		rep.Errors = append(rep.Errors, Finding{Path: "", Message: "not valid JSON: " + err.Error()})
		return rep
	}
	if err := s.schema.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			collect(ve, &rep.Errors)
		} else {
			rep.Errors = append(rep.Errors, Finding{Message: err.Error()})
		}
	}
	if len(rep.Errors) == 0 {
		var cfg struct {
			Theme map[string]map[string]string `json:"theme"`
		}
		_ = json.Unmarshal(raw, &cfg)
		for _, scheme := range []string{"light", "dark"} {
			checkContrast(scheme, cfg.Theme[scheme], &rep)
		}
	}
	rep.Valid = len(rep.Errors) == 0
	return rep
}

var printer = message.NewPrinter(language.English)

func collect(ve *jsonschema.ValidationError, out *[]Finding) {
	if len(ve.Causes) == 0 {
		msg := ve.Error()
		if ve.ErrorKind != nil {
			msg = ve.ErrorKind.LocalizedString(printer)
		}
		*out = append(*out, Finding{Path: "/" + strings.Join(ve.InstanceLocation, "/"), Message: msg})
		return
	}
	for _, c := range ve.Causes {
		collect(c, out)
	}
}

// contrastRules pairs a foreground token with a background token and the WCAG AA minimum.
var contrastRules = []struct {
	fg, bg string
	min    float64
	why    string
}{
	{"text", "bg", 4.5, "body text"},
	{"text", "surface", 4.5, "text on surfaces"},
	{"muted", "bg", 4.5, "secondary text"},
	{"onPrimary", "primary", 4.5, "selected day"},
	{"holiday", "bg", 4.5, "holiday numbers"},
	{"weekend", "bg", 4.5, "weekend numbers"},
	{"today", "bg", 3.0, "today indicator (non-text)"},
}

func checkContrast(scheme string, p map[string]string, rep *Report) {
	for _, r := range contrastRules {
		fg, ok1 := luminance(p[r.fg])
		bg, ok2 := luminance(p[r.bg])
		if !ok1 || !ok2 {
			continue
		}
		ratio := (math.Max(fg, bg) + 0.05) / (math.Min(fg, bg) + 0.05)
		if ratio < r.min {
			rep.Errors = append(rep.Errors, Finding{
				Path:    fmt.Sprintf("/theme/%s/%s", scheme, r.fg),
				Message: fmt.Sprintf("contrast %.2f:1 against %s is below %.1f:1 required for %s (WCAG AA)", ratio, r.bg, r.min, r.why),
			})
		}
	}
}

// luminance returns the WCAG relative luminance of #RRGGBB.
func luminance(hex string) (float64, bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, false
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return 0, false
	}
	ch := func(c uint64) float64 {
		s := float64(c) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(v>>16&0xff) + 0.7152*ch(v>>8&0xff) + 0.0722*ch(v&0xff), true
}

// ---- semver ----------------------------------------------------------------

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

// SemverLess reports a < b for x.y.z versions. Invalid versions sort first.
func SemverLess(a, b string) bool {
	x, ok1 := parseSemver(a)
	y, ok2 := parseSemver(b)
	if !ok1 || !ok2 {
		return !ok1 && ok2
	}
	for i := range 3 {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

// ---- storage ---------------------------------------------------------------

const versionCols = `app_key, version, status, origin, schema_version, min_client_version, note, reject_reason, config,
	created_by::text, approved_by::text, created_at, updated_at, published_at`

func scanVersion(r pgx.Row) (Version, error) {
	var v Version
	err := r.Scan(&v.App, &v.Version, &v.Status, &v.Origin, &v.SchemaVersion, &v.MinClientVersion, &v.Note, &v.RejectReason,
		&v.Config, &v.CreatedBy, &v.ApprovedBy, &v.CreatedAt, &v.UpdatedAt, &v.PublishedAt)
	return v, err
}

func (s *Service) checkInput(in *Input) error {
	var fe []apperr.FieldError
	in.Config = bytes.TrimSpace(in.Config)
	var obj map[string]any
	if len(in.Config) == 0 || json.Unmarshal(in.Config, &obj) != nil {
		fe = append(fe, apperr.FieldError{Field: "config", Message: "must be a JSON object"})
	}
	if in.MinClientVersion == "" {
		in.MinClientVersion = "0.0.0"
	}
	if !semverRe.MatchString(in.MinClientVersion) {
		fe = append(fe, apperr.FieldError{Field: "minClientVersion", Message: "must look like 1.2.3"})
	}
	if len(in.Note) > 1000 {
		fe = append(fe, apperr.FieldError{Field: "note", Message: "at most 1000 characters"})
	}
	if len(fe) > 0 {
		return apperr.Validation(fe...)
	}
	return nil
}

func schemaVersionOf(raw []byte) int {
	var v struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.SchemaVersion
}

// CreateDraft stores a new draft version. Drafts may be incomplete; submit requires validity.
func (s *Service) CreateDraft(ctx context.Context, actor audit.Actor, app string, in Input) (Version, error) {
	if !ValidApp(app) {
		return Version{}, apperr.Validation(apperr.FieldError{Field: "app", Message: "2-31 characters: lower-case letters, digits, dashes"})
	}
	if err := s.checkInput(&in); err != nil {
		return Version{}, err
	}
	var v Version
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('ui_config:' || $1 || ':' || $2))`, actor.TenantID, app); err != nil {
			return err
		}
		var err error
		v, err = scanVersion(tx.QueryRow(ctx, `
			INSERT INTO ui_configs (tenant_id, app_key, version, status, schema_version, config, min_client_version, note, created_by)
			VALUES ($1, $2, (SELECT COALESCE(max(version), 0) + 1 FROM ui_configs WHERE tenant_id = $1 AND app_key = $2),
			        'draft', $3, $4, $5, NULLIF($6,''), $7)
			RETURNING `+versionCols, actor.TenantID, app, schemaVersionOf(in.Config), in.Config, in.MinClientVersion, in.Note, actor.UserID))
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, actor, "ui_config.create", "ui_config", fmt.Sprintf("%s/%d", app, v.Version), nil, v)
	})
	return v, err
}

// UpdateDraft replaces a draft's content.
func (s *Service) UpdateDraft(ctx context.Context, actor audit.Actor, app string, version int, in Input) (Version, error) {
	if err := s.checkInput(&in); err != nil {
		return Version{}, err
	}
	return s.transition(ctx, actor, app, version, "ui_config.update", func(tx pgx.Tx, cur Version) error {
		if cur.Status != "draft" {
			return apperr.InvalidState("Only drafts can be edited; create a new draft instead.")
		}
		_, err := tx.Exec(ctx, `UPDATE ui_configs SET config = $4, schema_version = $5, min_client_version = $6, note = NULLIF($7,''), updated_at = now()
			WHERE tenant_id = $1 AND app_key = $2 AND version = $3`,
			actor.TenantID, app, version, in.Config, schemaVersionOf(in.Config), in.MinClientVersion, in.Note)
		return err
	})
}

// Submit moves a valid draft to review.
func (s *Service) Submit(ctx context.Context, actor audit.Actor, app string, version int) (Version, error) {
	return s.transition(ctx, actor, app, version, "ui_config.submit", func(tx pgx.Tx, cur Version) error {
		if cur.Status != "draft" {
			return apperr.InvalidState("Only drafts can be submitted.")
		}
		if rep := s.Validate(cur.Config); !rep.Valid {
			return apperr.Validation().With("report", rep)
		}
		_, err := tx.Exec(ctx, `UPDATE ui_configs SET status = 'in_review', updated_at = now() WHERE tenant_id = $1 AND app_key = $2 AND version = $3`,
			actor.TenantID, app, version)
		return err
	})
}

// Approve publishes a version under review, to percent of installs (100 = everyone).
func (s *Service) Approve(ctx context.Context, actor audit.Actor, app string, version, percent int) (Version, error) {
	if percent < 1 || percent > 100 {
		return Version{}, apperr.Validation(apperr.FieldError{Field: "rolloutPercent", Message: "must be 1-100"})
	}
	return s.transition(ctx, actor, app, version, "ui_config.publish", func(tx pgx.Tx, cur Version) error {
		if cur.Status != "in_review" {
			return apperr.InvalidState("Only versions in review can be approved.")
		}
		if cur.CreatedBy == actor.UserID {
			return apperr.Forbidden(apperr.CodeFourEyes, "A different designer must approve this version.")
		}
		if rep := s.Validate(cur.Config); !rep.Valid {
			return apperr.Validation().With("report", rep)
		}
		if _, err := tx.Exec(ctx, `UPDATE ui_configs SET status = 'published', approved_by = $4, published_at = now(), updated_at = now()
			WHERE tenant_id = $1 AND app_key = $2 AND version = $3`, actor.TenantID, app, version, actor.UserID); err != nil {
			return err
		}
		return s.route(ctx, tx, actor.TenantID, app, version, percent)
	})
}

// route points the channel at version: stable when percent is 100, otherwise candidate.
func (s *Service) route(ctx context.Context, tx pgx.Tx, tenantID, app string, version, percent int) error {
	ch, err := getChannel(ctx, tx, tenantID, app, true)
	if err != nil {
		return err
	}
	supersede := func(v *int) error {
		if v == nil || *v == version {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE ui_configs SET status = 'superseded', updated_at = now()
			WHERE tenant_id = $1 AND app_key = $2 AND version = $3 AND status = 'published'`, tenantID, app, *v)
		return err
	}
	if percent == 100 {
		stable := ch.StableVersion
		if err := supersede(&stable); err != nil {
			return err
		}
		if err := supersede(ch.CandidateVersion); err != nil {
			return err
		}
		ch.StableVersion, ch.CandidateVersion, ch.CandidatePercent = version, nil, 0
	} else {
		if err := supersede(ch.CandidateVersion); err != nil {
			return err
		}
		ch.CandidateVersion, ch.CandidatePercent = &version, percent
	}
	return saveChannel(ctx, tx, tenantID, ch)
}

// Reject closes a draft or a version in review.
func (s *Service) Reject(ctx context.Context, actor audit.Actor, app string, version int, reason string) (Version, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 {
		return Version{}, apperr.Validation(apperr.FieldError{Field: "reason", Message: "required, at most 1000 characters"})
	}
	return s.transition(ctx, actor, app, version, "ui_config.reject", func(tx pgx.Tx, cur Version) error {
		if cur.Status != "in_review" && cur.Status != "draft" {
			return apperr.InvalidState("Only drafts or versions in review can be rejected.")
		}
		_, err := tx.Exec(ctx, `UPDATE ui_configs SET status = 'rejected', reject_reason = $4, updated_at = now()
			WHERE tenant_id = $1 AND app_key = $2 AND version = $3`, actor.TenantID, app, version, reason)
		return err
	})
}

// SetRollout changes the candidate's percentage. 100 promotes it to stable; 0 pauses it.
func (s *Service) SetRollout(ctx context.Context, actor audit.Actor, app string, percent int) (Channel, error) {
	if percent < 0 || percent > 100 {
		return Channel{}, apperr.Validation(apperr.FieldError{Field: "percent", Message: "must be 0-100"})
	}
	var ch Channel
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getChannel(ctx, tx, actor.TenantID, app, true)
		if err != nil {
			return err
		}
		if cur.CandidateVersion == nil {
			return apperr.InvalidState("There is no candidate version being rolled out.")
		}
		if percent == 100 {
			err = s.route(ctx, tx, actor.TenantID, app, *cur.CandidateVersion, 100)
		} else {
			cur.CandidatePercent = percent
			err = saveChannel(ctx, tx, actor.TenantID, cur)
		}
		if err != nil {
			return err
		}
		if ch, err = getChannel(ctx, tx, actor.TenantID, app, false); err != nil {
			return err
		}
		return s.after(ctx, tx, actor, "ui_config.rollout", app, cur, ch)
	})
	return ch, err
}

// Rollback republishes an earlier version's content as a new version for everyone.
// It is an emergency action, so it skips review but is fully audited.
func (s *Service) Rollback(ctx context.Context, actor audit.Actor, app string, toVersion int, reason string) (Version, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 {
		return Version{}, apperr.Validation(apperr.FieldError{Field: "reason", Message: "required, at most 1000 characters"})
	}
	var out Version
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('ui_config:' || $1 || ':' || $2))`, actor.TenantID, app); err != nil {
			return err
		}
		src, err := scanVersion(tx.QueryRow(ctx, `SELECT `+versionCols+` FROM ui_configs WHERE tenant_id = $1 AND app_key = $2 AND version = $3`,
			actor.TenantID, app, toVersion))
		if store.IsNoRows(err) {
			return apperr.NotFound("config version")
		}
		if err != nil {
			return err
		}
		if src.Status != "published" && src.Status != "superseded" {
			return apperr.InvalidState("Only versions that were published (and not rolled back) can be rolled back to.")
		}
		prev, err := getChannel(ctx, tx, actor.TenantID, app, true)
		if err != nil {
			return err
		}
		out, err = scanVersion(tx.QueryRow(ctx, `
			INSERT INTO ui_configs (tenant_id, app_key, version, status, origin, schema_version, config, min_client_version, note,
			                        created_by, approved_by, published_at)
			VALUES ($1, $2, (SELECT max(version) + 1 FROM ui_configs WHERE tenant_id = $1 AND app_key = $2), 'published', 'rollback',
			        $3, $4, $5, $6, $7, $7, now())
			RETURNING `+versionCols, actor.TenantID, app, src.SchemaVersion, src.Config, src.MinClientVersion,
			fmt.Sprintf("Rollback to v%d: %s", toVersion, reason), actor.UserID))
		if err != nil {
			return err
		}
		if err := s.route(ctx, tx, actor.TenantID, app, out.Version, 100); err != nil {
			return err
		}
		// The versions being rolled back from are marked so they are never offered again,
		// not even to older apps looking for a compatible fallback.
		bad := []int{}
		if prev.StableVersion > 0 && prev.StableVersion != toVersion {
			bad = append(bad, prev.StableVersion)
		}
		if prev.CandidateVersion != nil && *prev.CandidateVersion != toVersion {
			bad = append(bad, *prev.CandidateVersion)
		}
		if _, err := tx.Exec(ctx, `UPDATE ui_configs SET status = 'rolled_back', updated_at = now()
			WHERE tenant_id = $1 AND app_key = $2 AND version = ANY($3) AND status IN ('published','superseded')`,
			actor.TenantID, app, bad); err != nil {
			return err
		}
		return s.after(ctx, tx, actor, "ui_config.rollback", app, src, out)
	})
	return out, err
}

func (s *Service) transition(ctx context.Context, actor audit.Actor, app string, version int, action string, fn func(pgx.Tx, Version) error) (Version, error) {
	var out Version
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanVersion(tx.QueryRow(ctx, `SELECT `+versionCols+` FROM ui_configs WHERE tenant_id = $1 AND app_key = $2 AND version = $3 FOR UPDATE`,
			actor.TenantID, app, version))
		if store.IsNoRows(err) {
			return apperr.NotFound("config version")
		}
		if err != nil {
			return err
		}
		if err := fn(tx, cur); err != nil {
			return err
		}
		if out, err = scanVersion(tx.QueryRow(ctx, `SELECT `+versionCols+` FROM ui_configs WHERE tenant_id = $1 AND app_key = $2 AND version = $3`,
			actor.TenantID, app, version)); err != nil {
			return err
		}
		if action == "ui_config.update" || action == "ui_config.submit" || action == "ui_config.reject" {
			return audit.Write(ctx, tx, actor, action, "ui_config", fmt.Sprintf("%s/%d", app, version), cur, out)
		}
		return s.after(ctx, tx, actor, action, app, cur, out)
	})
	return out, err
}

// after audits a change that clients can see and notifies subscribers.
func (s *Service) after(ctx context.Context, tx pgx.Tx, actor audit.Actor, action, app string, before, after any) error {
	if err := audit.Write(ctx, tx, actor, action, "ui_config", app, before, after); err != nil {
		return err
	}
	if err := outbox.Enqueue(ctx, tx, actor.TenantID, "config", "config.changed", map[string]any{"app": app}); err != nil {
		return err
	}
	return outbox.EnqueuePurge(ctx, tx, s.purge, s.baseURL+"/v1/manifest")
}

func getChannel(ctx context.Context, q store.Querier, tenantID, app string, lock bool) (Channel, error) {
	sql := `SELECT app_key, COALESCE(stable_version, 0), candidate_version, candidate_percent, updated_at
		FROM ui_config_channels WHERE tenant_id = $1 AND app_key = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	var ch Channel
	err := q.QueryRow(ctx, sql, tenantID, app).Scan(&ch.App, &ch.StableVersion, &ch.CandidateVersion, &ch.CandidatePercent, &ch.UpdatedAt)
	if store.IsNoRows(err) {
		return Channel{App: app}, nil
	}
	return ch, err
}

func saveChannel(ctx context.Context, tx pgx.Tx, tenantID string, ch Channel) error {
	var stable *int
	if ch.StableVersion > 0 {
		stable = &ch.StableVersion
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO ui_config_channels (tenant_id, app_key, stable_version, candidate_version, candidate_percent)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, app_key) DO UPDATE SET stable_version = EXCLUDED.stable_version,
		  candidate_version = EXCLUDED.candidate_version, candidate_percent = EXCLUDED.candidate_percent, updated_at = now()`,
		tenantID, ch.App, stable, ch.CandidateVersion, ch.CandidatePercent)
	return err
}

// List returns the channel and every version for an app, newest first.
func (s *Service) List(ctx context.Context, tenantID, app string) (Channel, []Version, error) {
	ch, err := getChannel(ctx, s.pool, tenantID, app, false)
	if err != nil {
		return Channel{}, nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+versionCols+` FROM ui_configs WHERE tenant_id = $1 AND app_key = $2 ORDER BY version DESC LIMIT 200`, tenantID, app)
	if err != nil {
		return Channel{}, nil, err
	}
	vs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Version, error) { return scanVersion(r) })
	return ch, vs, err
}

// Get returns one version.
func (s *Service) Get(ctx context.Context, tenantID, app string, version int) (Version, error) {
	v, err := scanVersion(s.pool.QueryRow(ctx, `SELECT `+versionCols+` FROM ui_configs WHERE tenant_id = $1 AND app_key = $2 AND version = $3`, tenantID, app, version))
	if store.IsNoRows(err) {
		return Version{}, apperr.NotFound("config version")
	}
	return v, err
}

// PublicConfig returns an immutable published config. Version 0 is the built-in default.
func (s *Service) PublicConfig(ctx context.Context, tenantID, app string, version int) ([]byte, error) {
	if version == 0 {
		return fixtures.UIConfigDefault, nil
	}
	var raw []byte
	// Published URLs are immutable, so rolled-back versions stay readable for clients that cached the link.
	err := s.pool.QueryRow(ctx, `SELECT config FROM ui_configs WHERE tenant_id = $1 AND app_key = $2 AND version = $3
		AND status IN ('published','superseded','rolled_back')`, tenantID, app, version).Scan(&raw)
	if store.IsNoRows(err) {
		return nil, &apperr.Error{Status: 404, Code: apperr.CodeVersionNotFound, Title: "Version not found",
			Detail: fmt.Sprintf("No published config version %d for app %s.", version, app)}
	}
	return raw, err
}

// Manifest returns which config version a client should use. If the stable version needs a
// newer client, the newest compatible published version is offered instead.
func (s *Service) Manifest(ctx context.Context, tenantID, app, clientVersion string) (ManifestConfig, error) {
	out := ManifestConfig{App: app, MinClientVersion: "0.0.0"}
	ch, err := getChannel(ctx, s.pool, tenantID, app, false)
	if err != nil || ch.StableVersion == 0 && ch.CandidateVersion == nil {
		return out, err
	}
	rows, err := s.pool.Query(ctx, `SELECT version, min_client_version FROM ui_configs
		WHERE tenant_id = $1 AND app_key = $2 AND status IN ('published','superseded') ORDER BY version DESC`, tenantID, app)
	if err != nil {
		return out, err
	}
	type vm struct {
		v   int
		min string
	}
	vs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (vm, error) {
		var x vm
		err := r.Scan(&x.v, &x.min)
		return x, err
	})
	if err != nil {
		return out, err
	}
	compatible := func(min string) bool { return clientVersion == "" || !SemverLess(clientVersion, min) }
	minOf := map[int]string{}
	for _, x := range vs {
		minOf[x.v] = x.min
	}
	if ch.StableVersion > 0 {
		if compatible(minOf[ch.StableVersion]) {
			out.Version, out.MinClientVersion = ch.StableVersion, minOf[ch.StableVersion]
		} else {
			for _, x := range vs {
				if x.v < ch.StableVersion && compatible(x.min) {
					out.Version, out.MinClientVersion = x.v, x.min
					break
				}
			}
		}
	}
	if ch.CandidateVersion != nil && ch.CandidatePercent > 0 && compatible(minOf[*ch.CandidateVersion]) {
		out.Candidate = &Candidate{Version: *ch.CandidateVersion, Percent: ch.CandidatePercent}
	}
	return out, nil
}
