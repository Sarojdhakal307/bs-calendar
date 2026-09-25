package httpapi_test

// End-to-end flow test with contract enforcement.
//
// It runs the real server (same wiring as production, via internal/app) against a real
// Postgres, walks through every documented flow, and validates EVERY request and response
// against api/openapi.yaml. It also fails if any operation in the spec is never exercised,
// so the documentation and the implementation cannot drift apart.
//
// Run: docker compose --profile test run --rm test
// (needs TEST_DATABASE_URL; the test DROPS and recreates the public schema of that database).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/jackc/pgx/v5/pgxpool"

	"bscalendar/api"
	"bscalendar/fixtures"
	"bscalendar/services/calendar-api/internal/app"
	"bscalendar/services/calendar-api/internal/bootstrap"
	"bscalendar/services/calendar-api/internal/config"
	"bscalendar/services/calendar-api/internal/outbox"
	"bscalendar/services/calendar-api/internal/store"
)

const (
	pubKey    = "pk_test_public_key_000000001"
	srvKey    = "sk_test_server_key_000000001"
	adminPass = "admin-password-123"
	userPass  = "user-password-1234"
)

func init() {
	text := func(r io.Reader, _ http.Header, _ *openapi3.SchemaRef, _ openapi3filter.EncodingFn) (any, error) {
		b, err := io.ReadAll(r)
		return string(b), err
	}
	openapi3filter.RegisterBodyDecoder("application/merge-patch+json", openapi3filter.JSONBodyDecoder)
	openapi3filter.RegisterBodyDecoder("application/problem+json", openapi3filter.JSONBodyDecoder)
	openapi3filter.RegisterBodyDecoder("text/calendar", text)
	openapi3filter.RegisterBodyDecoder("text/csv", text)
	openapi3filter.RegisterBodyDecoder("application/yaml", text)
}

type harness struct {
	ts      *httptest.Server
	router  routers.Router
	doc     *openapi3.T
	pool    *pgxpool.Pool
	cfg     config.Config
	log     *slog.Logger
	a       *app.App
	mu      sync.Mutex
	covered map[string]bool
}

type req struct {
	method  string
	path    string
	body    any // nil, []byte, string or a JSON-encodable value
	ctype   string
	headers map[string]string
	invalid bool // the request deliberately breaks the contract (skip request validation)
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) obj(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, r.body)
	}
	return m
}

// get walks a dotted path through decoded JSON ("a.b.0.c").
func get(v any, path string) any {
	for _, p := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[p]
		case []any:
			var i int
			if _, err := fmt.Sscanf(p, "%d", &i); err != nil || i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

func setup(t *testing.T) *harness {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set; run: docker compose --profile test run --rm test")
	}
	// Safety: this test drops the public schema, so refuse anything that is not clearly a test database.
	if pc, err := pgxpool.ParseConfig(dbURL); err != nil || !strings.Contains(pc.ConnConfig.Database, "test") {
		t.Fatalf("refusing to run: TEST_DATABASE_URL must point at a database whose name contains \"test\" (the test drops its public schema)")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	reset, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reset.Exec(ctx, `DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	reset.Close()
	if err := store.Migrate(ctx, dbURL, "up", log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := store.Open(ctx, store.Options{URL: dbURL}, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cfg := config.Config{
		DatabaseURL: dbURL, JWTSigningKey: []byte(strings.Repeat("k", 48)), JWTIssuer: "calendar-api-test",
		AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour, PublicBaseURL: "http://calendar.test",
		CORSAllowedOrigins: []string{"*"}, RequireAPIKey: true, MinSupportedClient: "1.0.0",
		DefaultTenantSlug: "default", BootstrapAdminEmail: "admin@example.com", BootstrapAdminPassword: adminPass,
		BootstrapPublicKey: pubKey, BootstrapServerKey: srvKey, WebhookEncKey: bytes.Repeat([]byte{7}, 32),
		AllowPrivateWebhooks: true, WorkerPollInterval: time.Second,
	}
	if err := bootstrap.Seed(ctx, pool, cfg, log); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a, err := app.New(ctx, cfg, pool, log, "test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(a.Server.Handler())
	t.Cleanup(ts.Close)

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(api.OpenAPI)
	if err != nil {
		t.Fatalf("load openapi.yaml: %v", err)
	}
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("openapi.yaml is not a valid OpenAPI document: %v", err)
	}
	doc.Servers = openapi3.Servers{{URL: ts.URL}}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{ts: ts, router: router, doc: doc, pool: pool, cfg: cfg, log: log, a: a, covered: map[string]bool{}}
}

func (h *harness) build(rq req) (*http.Request, []byte) {
	var body []byte
	switch b := rq.body.(type) {
	case nil:
	case []byte:
		body = b
	case string:
		body = []byte(b)
	default:
		body, _ = json.Marshal(b)
	}
	httpReq, _ := http.NewRequest(rq.method, h.ts.URL+rq.path, bytes.NewReader(body))
	if body != nil {
		ct := rq.ctype
		if ct == "" {
			ct = "application/json"
		}
		httpReq.Header.Set("Content-Type", ct)
	}
	for k, v := range rq.headers {
		httpReq.Header.Set(k, v)
	}
	return httpReq, body
}

// do sends a request and validates it (unless invalid) and its response against the spec.
func (h *harness) do(t *testing.T, rq req) resp {
	t.Helper()
	ctx := context.Background()
	httpReq, body := h.build(rq)
	valReq, _ := h.build(rq)
	route, params, err := h.router.FindRoute(valReq)
	if err != nil {
		t.Fatalf("%s %s is not described in openapi.yaml: %v", rq.method, rq.path, err)
	}
	in := &openapi3filter.RequestValidationInput{Request: valReq, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, MultiError: true}}
	if !rq.invalid {
		if err := openapi3filter.ValidateRequest(ctx, in); err != nil {
			t.Errorf("REQUEST does not match the contract (%s %s):\n%v\nbody: %s", rq.method, rq.path, err, body)
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(res.Body)
	h.mu.Lock()
	h.covered[route.Operation.OperationID] = true
	h.mu.Unlock()

	opts := &openapi3filter.Options{IncludeResponseStatus: true, MultiError: true}
	if res.StatusCode == http.StatusNotModified || res.StatusCode == http.StatusNoContent || res.StatusCode == http.StatusFound {
		opts.ExcludeResponseBody = true
	}
	out := &openapi3filter.ResponseValidationInput{RequestValidationInput: in, Status: res.StatusCode, Header: res.Header, Options: opts}
	out.SetBodyBytes(rb)
	if err := openapi3filter.ValidateResponse(ctx, out); err != nil {
		t.Errorf("RESPONSE does not match the contract (%s %s -> %d):\n%v\nbody: %.1500s", rq.method, rq.path, res.StatusCode, err, rb)
	}
	return resp{status: res.StatusCode, header: res.Header, body: rb}
}

func expect(t *testing.T, r resp, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d; body: %.2000s", r.status, status, r.body)
	}
}

func expectProblem(t *testing.T, r resp, status int, code string) {
	t.Helper()
	expect(t, r, status)
	if ct := r.header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type %q, want application/problem+json", ct)
	}
	if got := get(r.obj(t), "code"); got != code {
		t.Fatalf("problem code %v, want %s; body: %s", got, code, r.body)
	}
}

func key(k string) map[string]string { return map[string]string{"X-Api-Key": k} }

func bearer(tok string, extra ...string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + tok}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

func (h *harness) login(t *testing.T, email, password string) (access, refresh string) {
	t.Helper()
	r := h.do(t, req{method: "POST", path: "/v1/admin/auth/login", body: map[string]string{"email": email, "password": password}})
	expect(t, r, 200)
	m := r.obj(t)
	return get(m, "accessToken").(string), get(m, "refreshToken").(string)
}

func TestAPIFlow(t *testing.T) {
	h := setup(t)
	var (
		admin, approver, designer1, designer2, viewer string
		holidayID, newYearID, rruleID, importedID     string
		bucketVersion                                 float64
	)

	t.Run("operations and docs", func(t *testing.T) {
		expect(t, h.do(t, req{method: "GET", path: "/healthz"}), 200)
		r := h.do(t, req{method: "GET", path: "/readyz"})
		expect(t, r, 200)
		r = h.do(t, req{method: "GET", path: "/openapi.yaml"})
		expect(t, r, 200)
		if !bytes.Equal(r.body, api.OpenAPI) {
			t.Fatal("served spec differs from api/openapi.yaml")
		}
		for _, p := range []string{"/docs", "/docs/try"} {
			res, err := http.Get(h.ts.URL + p)
			if err != nil || res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Security-Policy"), "cdn.jsdelivr.net") {
				t.Fatalf("%s: %v %v", p, err, res)
			}
			res.Body.Close()
		}
		res, _ := http.Get(h.ts.URL + "/v1/does-not-exist")
		if res.StatusCode != 404 || res.Header.Get("Content-Type") != "application/problem+json" {
			t.Fatalf("unknown route: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
		}
		res.Body.Close()
	})

	t.Run("client sync: manifest and immutable resources", func(t *testing.T) {
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/manifest"}), 401, "INVALID_API_KEY")
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/manifest", headers: key("pk_wrong_key_1234567890")}), 401, "INVALID_API_KEY")

		r := h.do(t, req{method: "GET", path: "/v1/manifest?app=mobile&clientVersion=1.2.0", headers: key(pubKey)})
		expect(t, r, 200)
		m := r.obj(t)
		if get(m, "dataVersion") != 1.0 || get(m, "supportedRange.minBsYear") != 1975.0 || get(m, "supportedRange.maxBsYear") != 2100.0 {
			t.Fatalf("unexpected manifest: %s", r.body)
		}
		if get(m, "updateRequired") != false || get(m, "config.version") != 0.0 {
			t.Fatalf("unexpected manifest flags: %s", r.body)
		}
		// Conditional GET: unchanged manifest revalidates with 304 even though serverTime moves.
		etag := r.header.Get("ETag")
		r2 := h.do(t, req{method: "GET", path: "/v1/manifest?app=mobile&clientVersion=1.2.0", headers: map[string]string{"X-Api-Key": pubKey, "If-None-Match": etag}})
		expect(t, r2, 304)
		r = h.do(t, req{method: "GET", path: "/v1/manifest?app=mobile&clientVersion=0.9.0", headers: key(pubKey)})
		if get(r.obj(t), "updateRequired") != true {
			t.Fatal("clients below MIN_SUPPORTED_CLIENT must get updateRequired=true")
		}
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/manifest?app=BAD!", headers: key(pubKey), invalid: true}), 422, "VALIDATION_FAILED")

		r = h.do(t, req{method: "GET", path: "/v1/calendar/data/latest", headers: key(pubKey)})
		expect(t, r, 302)
		if r.header.Get("Location") != "/v1/calendar/data/1" {
			t.Fatalf("latest redirects to %q", r.header.Get("Location"))
		}
		r = h.do(t, req{method: "GET", path: "/v1/calendar/data/1", headers: key(pubKey)})
		expect(t, r, 200)
		if !strings.Contains(r.header.Get("Cache-Control"), "immutable") {
			t.Fatal("data must be immutable")
		}
		if get(r.obj(t), "sha256") != get(m, "dataSha256") {
			t.Fatal("manifest checksum does not match the table")
		}
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/calendar/data/99", headers: key(pubKey)}), 404, "VERSION_NOT_FOUND")

		r = h.do(t, req{method: "GET", path: "/v1/ui-config/mobile/0", headers: key(pubKey)})
		expect(t, r, 200)
		if !bytes.Equal(r.body, fixtures.UIConfigDefault) {
			t.Fatal("version 0 must be the built-in default config")
		}
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/ui-config/mobile/5", headers: key(pubKey)}), 404, "VERSION_NOT_FOUND")

		r = h.do(t, req{method: "GET", path: "/v1/events/buckets/2083/1", headers: key(pubKey)})
		expect(t, r, 200)
		if get(r.obj(t), "status") != "verified" {
			t.Fatal("2083 should be verified")
		}
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/events/buckets/2083/9", headers: key(pubKey)}), 404, "VERSION_NOT_FOUND")
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/events/buckets/2200/1", headers: key(pubKey)}), 422, "OUT_OF_RANGE")
	})

	t.Run("dates: convert, today, month grid", func(t *testing.T) {
		r := h.do(t, req{method: "GET", path: "/v1/convert?ad=2026-09-24", headers: key(pubKey)})
		expect(t, r, 200)
		m := r.obj(t)
		if get(m, "bs.date") != "2083-06-08" || get(m, "weekday.name.en") != "Thursday" || get(m, "bs.dateNe") != "२०८३-०६-०८" {
			t.Fatalf("convert: %s", r.body)
		}
		r = h.do(t, req{method: "GET", path: "/v1/convert?bs=" + url.QueryEscape("२०८३-०१-०१"), headers: key(pubKey)})
		expect(t, r, 200)
		if get(r.obj(t), "ad.date") != "2026-04-14" {
			t.Fatalf("1 Baisakh 2083: %s", r.body)
		}
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/convert?bs=2083-03-33", headers: key(pubKey)}), 422, "INVALID_DATE")
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/convert?ad=1900-01-01", headers: key(pubKey)}), 422, "OUT_OF_RANGE")
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/convert", headers: key(pubKey)}), 422, "VALIDATION_FAILED")
		r = h.do(t, req{method: "GET", path: "/v1/today?tz=Asia/Kathmandu", headers: key(pubKey)})
		expect(t, r, 200)
		if get(r.obj(t), "timeZone") != "Asia/Kathmandu" {
			t.Fatal("today must echo the time zone")
		}
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/today?tz=Mars/Base", headers: key(pubKey)}), 422, "VALIDATION_FAILED")
		r = h.do(t, req{method: "GET", path: "/v1/months/AD/2026/9?weekStart=1", headers: key(pubKey)})
		expect(t, r, 200)
		if get(r.obj(t), "cells.0.weekday") != 1.0 {
			t.Fatal("weekStart=1 grid must start on Monday")
		}
	})

	t.Run("admin auth and users", func(t *testing.T) {
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/auth/login",
			body: map[string]string{"email": "admin@example.com", "password": "wrong-password-000"}}), 401, "INVALID_CREDENTIALS")
		admin, _ = h.login(t, "admin@example.com", adminPass)
		r := h.do(t, req{method: "GET", path: "/v1/admin/me", headers: bearer(admin)})
		expect(t, r, 200)
		if get(r.obj(t), "role") != "super_admin" {
			t.Fatal("bootstrap admin must be super_admin")
		}
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/admin/me"}), 401, "UNAUTHORIZED")

		for email, role := range map[string]string{"approver@example.com": "calendar_admin", "designer1@example.com": "designer",
			"designer2@example.com": "designer", "viewer@example.com": "viewer"} {
			r := h.do(t, req{method: "POST", path: "/v1/admin/users", headers: bearer(admin), body: map[string]string{"email": email, "role": role, "password": userPass}})
			expect(t, r, 201)
		}
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/users", headers: bearer(admin),
			body: map[string]string{"email": "viewer@example.com", "role": "viewer", "password": userPass}}), 409, "CONFLICT")
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/users", headers: bearer(admin),
			body: map[string]string{"email": "x@example.com", "role": "viewer", "password": "short"}, invalid: true}), 422, "VALIDATION_FAILED")
		r = h.do(t, req{method: "GET", path: "/v1/admin/users", headers: bearer(admin)})
		expect(t, r, 200)
		if n := len(get(r.obj(t), "items").([]any)); n != 5 {
			t.Fatalf("users: %d", n)
		}
		approver, _ = h.login(t, "approver@example.com", userPass)
		designer1, _ = h.login(t, "designer1@example.com", userPass)
		designer2, _ = h.login(t, "designer2@example.com", userPass)
		viewer, _ = h.login(t, "viewer@example.com", userPass)
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/admin/users", headers: bearer(viewer)}), 403, "FORBIDDEN")

		// Refresh rotation and reuse detection on a separate session.
		_, rt1 := h.login(t, "admin@example.com", adminPass)
		r = h.do(t, req{method: "POST", path: "/v1/admin/auth/refresh", body: map[string]string{"refreshToken": rt1}})
		expect(t, r, 200)
		access2 := get(r.obj(t), "accessToken").(string)
		expect(t, h.do(t, req{method: "GET", path: "/v1/admin/me", headers: bearer(access2)}), 200)
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/auth/refresh", body: map[string]string{"refreshToken": rt1}}), 401, "TOKEN_REUSED")
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/admin/me", headers: bearer(access2)}), 401, "UNAUTHORIZED")

		// Logout revokes the session immediately.
		tmp, _ := h.login(t, "admin@example.com", adminPass)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/auth/logout", headers: bearer(tmp)}), 204)
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/admin/me", headers: bearer(tmp)}), 401, "UNAUTHORIZED")
	})

	t.Run("categories", func(t *testing.T) {
		r := h.do(t, req{method: "GET", path: "/v1/admin/categories", headers: bearer(admin)})
		expect(t, r, 200)
		if n := len(get(r.obj(t), "items").([]any)); n != 4 {
			t.Fatalf("default categories: %d", n)
		}
		r = h.do(t, req{method: "POST", path: "/v1/admin/categories", headers: bearer(admin), body: map[string]any{
			"key": "school", "name": map[string]string{"en": "School", "ne": "विद्यालय"}, "colorLight": "#6D28D9", "colorDark": "#C4B5FD", "sortOrder": 50}})
		expect(t, r, 201)
		schoolID := get(r.obj(t), "id").(string)
		r = h.do(t, req{method: "PATCH", path: "/v1/admin/categories/" + schoolID, headers: bearer(admin), ctype: "application/merge-patch+json",
			body: map[string]any{"colorLight": "#5B21B6"}})
		expect(t, r, 200)
		if get(r.obj(t), "colorLight") != "#5B21B6" || get(r.obj(t), "name.ne") != "विद्यालय" {
			t.Fatalf("merge patch must change only the sent field: %s", r.body)
		}
		expectProblem(t, h.do(t, req{method: "PATCH", path: "/v1/admin/categories/" + schoolID, headers: bearer(admin),
			ctype: "application/merge-patch+json", body: map[string]any{"key": "renamed"}, invalid: true}), 422, "VALIDATION_FAILED")
		r = h.do(t, req{method: "POST", path: "/v1/admin/categories", headers: bearer(admin), body: map[string]any{
			"key": "temp_cat", "name": map[string]string{"en": "Temp"}, "colorLight": "#000000", "colorDark": "#FFFFFF"}})
		expect(t, r, 201)
		expect(t, h.do(t, req{method: "DELETE", path: "/v1/admin/categories/" + get(r.obj(t), "id").(string), headers: bearer(admin)}), 204)
		r = h.do(t, req{method: "GET", path: "/v1/categories", headers: key(pubKey)})
		expect(t, r, 200)
		if n := len(get(r.obj(t), "categories").([]any)); n != 5 {
			t.Fatalf("public categories: %d", n)
		}
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/categories", headers: bearer(viewer), body: map[string]any{
			"key": "nope", "name": map[string]string{"en": "No"}, "colorLight": "#000000", "colorDark": "#FFFFFF"}}), 403, "FORBIDDEN")
	})

	t.Run("events: create, optimistic locking, publish, buckets", func(t *testing.T) {
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/events", headers: bearer(admin), body: map[string]any{
			"category": "public_holiday", "title": map[string]string{"en": "Bad"}, "basis": "BS", "start": "2083-03-33"}}), 422, "VALIDATION_FAILED")
		r := h.do(t, req{method: "POST", path: "/v1/admin/events", headers: bearer(admin), body: map[string]any{
			"category": "public_holiday", "title": map[string]string{"en": "Office foundation day", "ne": "स्थापना दिवस"},
			"basis": "BS", "start": "2083-06-20"}})
		expect(t, r, 201)
		m := r.obj(t)
		holidayID = get(m, "id").(string)
		if get(m, "status") != "draft" || get(m, "ad.start") != "2026-10-06" || r.header.Get("ETag") != `"v1"` {
			t.Fatalf("created event: %s (etag %s)", r.body, r.header.Get("ETag"))
		}
		expect(t, h.do(t, req{method: "GET", path: "/v1/admin/events/" + holidayID, headers: bearer(approver)}), 200)

		patch := map[string]any{"description": map[string]string{"en": "Offices closed"}}
		expectProblem(t, h.do(t, req{method: "PATCH", path: "/v1/admin/events/" + holidayID, headers: bearer(admin),
			ctype: "application/merge-patch+json", body: patch, invalid: true}), 428, "PRECONDITION_REQUIRED")
		r = h.do(t, req{method: "PATCH", path: "/v1/admin/events/" + holidayID, headers: bearer(admin, "If-Match", `"v1"`),
			ctype: "application/merge-patch+json", body: patch})
		expect(t, r, 200)
		if get(r.obj(t), "version") != 2.0 || get(r.obj(t), "description.en") != "Offices closed" {
			t.Fatalf("patched: %s", r.body)
		}
		r = h.do(t, req{method: "PATCH", path: "/v1/admin/events/" + holidayID, headers: bearer(admin, "If-Match", `"v1"`),
			ctype: "application/merge-patch+json", body: patch})
		expectProblem(t, r, 412, "VERSION_CONFLICT")
		if get(r.obj(t), "currentVersion") != 2.0 {
			t.Fatal("412 must report currentVersion")
		}
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/events", headers: bearer(viewer), body: map[string]any{
			"category": "event", "title": map[string]string{"en": "x"}, "basis": "AD", "start": "2026-10-01"}}), 403, "FORBIDDEN")

		before := h.do(t, req{method: "GET", path: "/v1/manifest", headers: key(pubKey)}).obj(t)
		r = h.do(t, req{method: "POST", path: "/v1/admin/events/" + holidayID + "/publish", headers: bearer(admin, "If-Match", `"v2"`)})
		expect(t, r, 200)
		if get(r.obj(t), "status") != "published" {
			t.Fatal("not published")
		}
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/events/" + holidayID + "/publish", headers: bearer(admin)}), 200) // idempotent

		r = h.do(t, req{method: "POST", path: "/v1/admin/events", headers: bearer(admin), body: map[string]any{
			"category": "observance", "title": map[string]string{"en": "Nepali New Year", "ne": "नयाँ वर्ष"},
			"basis": "BS", "start": "2083-01-01", "recurrence": "yearly_bs"}})
		expect(t, r, 201)
		newYearID = get(r.obj(t), "id").(string)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/events/" + newYearID + "/publish", headers: bearer(admin)}), 200)
		r = h.do(t, req{method: "POST", path: "/v1/admin/events", headers: bearer(admin), body: map[string]any{
			"category": "event", "title": map[string]string{"en": "Team review"}, "basis": "AD", "start": "2026-10-09",
			"allDay": false, "startTime": "10:00", "endTime": "11:00", "recurrence": "rrule", "rrule": "FREQ=MONTHLY;BYDAY=2FR",
			"recurUntil": "2027-03-31"}})
		expect(t, r, 201)
		rruleID = get(r.obj(t), "id").(string)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/events/" + rruleID + "/publish", headers: bearer(admin)}), 200)

		after := h.do(t, req{method: "GET", path: "/v1/manifest", headers: key(pubKey)}).obj(t)
		if get(after, "defaultBucketVersion").(float64) <= get(before, "defaultBucketVersion").(float64) {
			t.Fatal("publishing a recurring event must bump every bucket")
		}
		bucketVersion = get(after, "eventBuckets.2083").(float64)
		r = h.do(t, req{method: "GET", path: "/v1/events/buckets/2083/1", headers: key(pubKey)})
		expect(t, r, 302)
		loc := r.header.Get("Location")
		if loc != fmt.Sprintf("/v1/events/buckets/2083/%d", int(bucketVersion)) {
			t.Fatalf("stale bucket redirects to %s", loc)
		}
		r = h.do(t, req{method: "GET", path: loc, headers: key(pubKey)})
		expect(t, r, 200)
		if !strings.Contains(r.header.Get("Cache-Control"), "immutable") {
			t.Fatal("current bucket must be immutable")
		}
		evs := get(r.obj(t), "events").([]any)
		var titles []string
		for _, e := range evs {
			titles = append(titles, get(e, "title.en").(string))
		}
		if !slices.Contains(titles, "Office foundation day") || !slices.Contains(titles, "Nepali New Year") || !slices.Contains(titles, "Team review") {
			t.Fatalf("bucket events: %v", titles)
		}
		// Next year's bucket contains the recurring new-year event on 1 Baisakh 2084.
		m2084 := h.do(t, req{method: "GET", path: "/v1/manifest", headers: key(pubKey)}).obj(t)
		v2084 := get(m2084, "defaultBucketVersion").(float64)
		r = h.do(t, req{method: "GET", path: fmt.Sprintf("/v1/events/buckets/2084/%d", int(v2084)), headers: key(pubKey)})
		expect(t, r, 200)
		if !strings.Contains(string(r.body), `"id":"`+newYearID+`@2027-04-14"`) {
			t.Fatalf("yearly_bs occurrence missing from 2084: %.600s", r.body)
		}

		r = h.do(t, req{method: "GET", path: "/v1/events?from=2083-06-01&to=2083-06-31&basis=BS", headers: key(pubKey)})
		expect(t, r, 200)
		if get(r.obj(t), "count") != 2.0 {
			t.Fatalf("Ashwin 2083 has the holiday and one monthly review: %s", r.body)
		}
		r = h.do(t, req{method: "GET", path: "/v1/events?from=2026-10-01&to=2027-03-31&category=event", headers: key(pubKey)})
		expect(t, r, 200)
		if get(r.obj(t), "count") != 6.0 {
			t.Fatalf("second Fridays Oct-Mar should be 6 occurrences: %s", r.body)
		}
		r = h.do(t, req{method: "GET", path: "/v1/months/BS/2083/6?include=events", headers: key(pubKey)})
		expect(t, r, 200)
		found := false
		for _, c := range get(r.obj(t), "cells").([]any) {
			if get(c, "bs") == "2083-06-20" {
				found = get(c, "isHoliday") == true && len(get(c, "eventIds").([]any)) == 1
			}
		}
		if !found {
			t.Fatal("month grid must mark 20 Ashwin 2083 as a holiday with one event")
		}
		r = h.do(t, req{method: "GET", path: "/v1/events.ics?api_key=" + pubKey + "&lang=ne", invalid: true})
		expect(t, r, 200)
		ics := string(r.body)
		if !strings.Contains(ics, "BEGIN:VCALENDAR") || !strings.Contains(ics, "SUMMARY:स्थापना दिवस") || !strings.Contains(ics, "DTSTART;VALUE=DATE:20261006") {
			t.Fatalf("ics: %.800s", ics)
		}
		r = h.do(t, req{method: "GET", path: "/v1/admin/events?status=published&limit=2", headers: bearer(viewer)})
		expect(t, r, 200)
		next := get(r.obj(t), "nextCursor")
		if len(get(r.obj(t), "items").([]any)) != 2 || next == nil {
			t.Fatalf("pagination: %s", r.body)
		}
		r = h.do(t, req{method: "GET", path: "/v1/admin/events?status=published&limit=2&cursor=" + next.(string), headers: bearer(viewer)})
		expect(t, r, 200)
		if len(get(r.obj(t), "items").([]any)) != 1 {
			t.Fatalf("second page: %s", r.body)
		}
	})

	t.Run("events: lifecycle, import, copy-year", func(t *testing.T) {
		csv := "category,title_en,basis,start,end\n" +
			"event,Imported one,BS,2083-07-01,\n" +
			"event,Imported two,AD,2026-12-25,2026-12-26\n" +
			"observance,Bad date,BS,2083-03-33,\n"
		r := h.do(t, req{method: "POST", path: "/v1/admin/events/import", headers: bearer(admin), body: csv, ctype: "text/csv"})
		expect(t, r, 200)
		if get(r.obj(t), "invalid") != 1.0 || get(r.obj(t), "created") != 0.0 {
			t.Fatalf("dry run: %s", r.body)
		}
		r = h.do(t, req{method: "POST", path: "/v1/admin/events/import?dryRun=false", headers: bearer(admin), body: csv, ctype: "text/csv"})
		expectProblem(t, r, 422, "VALIDATION_FAILED")
		good := strings.Join(strings.Split(csv, "\n")[:3], "\n") + "\n"
		r = h.do(t, req{method: "POST", path: "/v1/admin/events/import?dryRun=false", headers: bearer(admin), body: good, ctype: "text/csv"})
		expect(t, r, 201)
		if get(r.obj(t), "created") != 2.0 {
			t.Fatalf("import: %s", r.body)
		}
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/events/import", headers: bearer(admin), body: `{}`, invalid: true}), 415, "UNSUPPORTED_MEDIA_TYPE")

		r = h.do(t, req{method: "GET", path: "/v1/admin/events?q=imported%20one", headers: bearer(admin)})
		importedID = get(r.obj(t), "items.0.id").(string)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/events/" + importedID + "/publish", headers: bearer(admin)}), 200)
		r = h.do(t, req{method: "POST", path: "/v1/admin/events/" + importedID + "/archive", headers: bearer(admin)})
		expect(t, r, 200)
		ver := int(get(r.obj(t), "version").(float64))
		expectProblem(t, h.do(t, req{method: "DELETE", path: "/v1/admin/events/" + importedID, headers: bearer(admin), invalid: true}), 428, "PRECONDITION_REQUIRED")
		r = h.do(t, req{method: "DELETE", path: "/v1/admin/events/" + importedID, headers: bearer(admin, "If-Match", fmt.Sprintf(`"v%d"`, ver))})
		expect(t, r, 200)
		if get(r.obj(t), "deletedAt") == nil {
			t.Fatal("delete must set deletedAt")
		}
		r = h.do(t, req{method: "POST", path: "/v1/admin/events/" + importedID + "/restore", headers: bearer(admin)})
		expect(t, r, 200)
		if get(r.obj(t), "status") != "draft" || get(r.obj(t), "deletedAt") != nil {
			t.Fatalf("restore: %s", r.body)
		}
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/events/" + importedID + "/restore", headers: bearer(admin)}), 409, "INVALID_STATE")
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/admin/events/00000000-0000-0000-0000-000000000000", headers: bearer(admin)}), 404, "NOT_FOUND")

		body := map[string]any{"fromBsYear": 2083, "toBsYear": 2084, "categories": []string{"public_holiday"}}
		r = h.do(t, req{method: "POST", path: "/v1/admin/events/copy-year", headers: bearer(admin), body: body})
		expect(t, r, 201)
		if len(get(r.obj(t), "created").([]any)) != 1 {
			t.Fatalf("copy-year: %s", r.body)
		}
		r = h.do(t, req{method: "POST", path: "/v1/admin/events/copy-year", headers: bearer(admin), body: body})
		expect(t, r, 201)
		if len(get(r.obj(t), "created").([]any)) != 0 || len(get(r.obj(t), "skipped").([]any)) != 1 {
			t.Fatalf("copy-year must be idempotent: %s", r.body)
		}
	})

	t.Run("year table: four-eyes drafts, impact, publish", func(t *testing.T) {
		r := h.do(t, req{method: "GET", path: "/v1/admin/years", headers: bearer(viewer)})
		expect(t, r, 200)
		items := get(r.obj(t), "items").([]any)
		var y2083, y2084 []any
		for _, it := range items {
			switch get(it, "bsYear") {
			case 2083.0:
				y2083 = get(it, "days").([]any)
			case 2084.0:
				y2084 = get(it, "days").([]any)
			}
		}
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/years/drafts", headers: bearer(admin), invalid: true, body: map[string]any{
			"changes": []any{map[string]any{"bsYear": 2084, "days": []int{33, 31, 32, 31, 31, 30, 30, 30, 29, 30, 30, 30}, "status": "verified", "source": "x"}}}}),
			422, "VALIDATION_FAILED")

		// A draft that moves Ashwin by one day: impact must list the BS event on 20 Ashwin 2083.
		shifted := slices.Clone(y2083)
		shifted[4], shifted[6] = shifted[4].(float64)-1, shifted[6].(float64)+1
		r = h.do(t, req{method: "POST", path: "/v1/admin/years/drafts", headers: bearer(approver), body: map[string]any{
			"changes": []any{map[string]any{"bsYear": 2083, "days": shifted, "status": "verified", "source": "impact test"}}}})
		expect(t, r, 201)
		if !strings.Contains(string(r.body), holidayID) {
			t.Fatalf("impact must list the moved event: %s", r.body)
		}
		shiftDraft := get(r.obj(t), "id").(string)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/years/drafts/" + shiftDraft + "/reject", headers: bearer(approver), body: map[string]string{"reason": "test only"}}), 200)
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/years/drafts/" + shiftDraft + "/approve", headers: bearer(admin)}), 409, "INVALID_STATE")

		// Mark 2084 verified (same month lengths) with a second approver.
		r = h.do(t, req{method: "POST", path: "/v1/admin/years/drafts", headers: bearer(admin), body: map[string]any{
			"changes": []any{map[string]any{"bsYear": 2084, "days": y2084, "status": "verified", "source": "Official calendar 2084 (test)"}},
			"note":    "Verify 2084"}})
		expect(t, r, 201)
		draft := get(r.obj(t), "id").(string)
		expect(t, h.do(t, req{method: "GET", path: "/v1/admin/years/drafts/" + draft, headers: bearer(viewer)}), 200)
		r = h.do(t, req{method: "GET", path: "/v1/admin/years/drafts?state=pending", headers: bearer(viewer)})
		expect(t, r, 200)
		// Four-eyes: a calendar admin cannot approve their own draft (a super admin can).
		r = h.do(t, req{method: "POST", path: "/v1/admin/years/drafts", headers: bearer(approver), body: map[string]any{
			"changes": []any{map[string]any{"bsYear": 2084, "days": y2084, "status": "verified", "source": "Own draft (test)"}}}})
		expect(t, r, 201)
		ownDraft := get(r.obj(t), "id").(string)
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/years/drafts/" + ownDraft + "/approve", headers: bearer(approver)}), 403, "FOUR_EYES_REQUIRED")
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/years/drafts/" + ownDraft + "/reject", headers: bearer(approver), body: map[string]string{"reason": "test only"}}), 200)
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/years/drafts/" + draft + "/approve", headers: bearer(designer1)}), 403, "FORBIDDEN")
		r = h.do(t, req{method: "POST", path: "/v1/admin/years/drafts/" + draft + "/approve", headers: bearer(approver)})
		expect(t, r, 200)
		if get(r.obj(t), "state") != "approved" {
			t.Fatal("not approved")
		}
		m := h.do(t, req{method: "GET", path: "/v1/manifest", headers: key(pubKey)}).obj(t)
		if get(m, "dataVersion") != 2.0 || get(m, "firstProjectedBsYear") != 2085.0 {
			t.Fatalf("after approval: %v", m)
		}
		expect(t, h.do(t, req{method: "GET", path: "/v1/calendar/data/2", headers: key(pubKey)}), 200)
		expect(t, h.do(t, req{method: "GET", path: "/v1/calendar/data/1", headers: key(pubKey)}), 200) // old versions stay immutable
		if h.a.Data.Table().Version() != 2 {
			t.Fatal("in-memory table not reloaded")
		}
	})

	t.Run("ui config: validate, review, staged rollout, rollback", func(t *testing.T) {
		var cfg map[string]any
		_ = json.Unmarshal(fixtures.UIConfigDefault, &cfg)
		bad := cloneJSON(cfg)
		get(bad, "theme.light").(map[string]any)["text"] = "#FFFFFF" // white on white
		r := h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/validate", headers: bearer(viewer), body: bad})
		expect(t, r, 200)
		if get(r.obj(t), "valid") != false || !strings.Contains(string(r.body), "/theme/light/text") {
			t.Fatalf("contrast gate: %s", r.body)
		}
		unknown := cloneJSON(cfg)
		unknown["surprise"] = true
		if get(h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/validate", headers: bearer(viewer), body: unknown}).obj(t), "valid") != false {
			t.Fatal("unknown fields must be rejected")
		}

		v1 := cloneJSON(cfg)
		get(v1, "theme.light").(map[string]any)["primary"] = "#1E40AF"
		r = h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile", headers: bearer(designer1), body: map[string]any{"config": v1, "note": "Darker primary"}})
		expect(t, r, 201)
		if get(r.obj(t), "version") != 1.0 || get(r.obj(t), "validation.valid") != true {
			t.Fatalf("draft: %s", r.body)
		}
		r = h.do(t, req{method: "PATCH", path: "/v1/admin/ui-configs/mobile/1", headers: bearer(designer1), body: map[string]any{"config": v1, "minClientVersion": "1.0.0"}})
		expect(t, r, 200)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile/1/submit", headers: bearer(designer1)}), 200)
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile/1/approve", headers: bearer(designer1)}), 403, "FOUR_EYES_REQUIRED")
		r = h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile/1/approve", headers: bearer(designer2), body: map[string]int{"rolloutPercent": 20}})
		expect(t, r, 200)
		m := h.do(t, req{method: "GET", path: "/v1/manifest?app=mobile&clientVersion=1.2.0", headers: key(pubKey)}).obj(t)
		if get(m, "config.version") != 0.0 || get(m, "config.candidate.version") != 1.0 || get(m, "config.candidate.percent") != 20.0 {
			t.Fatalf("staged rollout manifest: %v", get(m, "config"))
		}
		r = h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile/rollout", headers: bearer(designer2), body: map[string]int{"percent": 100}})
		expect(t, r, 200)
		if get(r.obj(t), "stableVersion") != 1.0 || get(r.obj(t), "candidateVersion") != nil {
			t.Fatalf("promote: %s", r.body)
		}
		r = h.do(t, req{method: "GET", path: "/v1/ui-config/mobile/1", headers: key(pubKey)})
		expect(t, r, 200)

		// v2: rejected in review.
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile", headers: bearer(designer1), body: map[string]any{"config": cfg}}), 201)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile/2/submit", headers: bearer(designer1)}), 200)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile/2/reject", headers: bearer(designer2), body: map[string]string{"reason": "Not needed"}}), 200)
		// Invalid drafts cannot be submitted.
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile", headers: bearer(designer1), body: map[string]any{"config": bad}}), 201)
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile/3/submit", headers: bearer(designer1)}), 422, "VALIDATION_FAILED")
		// v4 published to everyone, then an emergency rollback to v1 creates v5.
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile", headers: bearer(designer1), body: map[string]any{"config": cfg}}), 201)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile/4/submit", headers: bearer(designer1)}), 200)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile/4/approve", headers: bearer(designer2)}), 200)
		r = h.do(t, req{method: "POST", path: "/v1/admin/ui-configs/mobile/rollback", headers: bearer(designer1), body: map[string]any{"toVersion": 1, "reason": "v4 broke dark mode"}})
		expect(t, r, 201)
		if get(r.obj(t), "version") != 5.0 || get(r.obj(t), "origin") != "rollback" {
			t.Fatalf("rollback: %s", r.body)
		}
		r = h.do(t, req{method: "GET", path: "/v1/admin/ui-configs/mobile", headers: bearer(viewer)})
		expect(t, r, 200)
		if get(r.obj(t), "channel.stableVersion") != 5.0 {
			t.Fatalf("channel: %s", r.body)
		}
		r = h.do(t, req{method: "GET", path: "/v1/admin/ui-configs/mobile/4", headers: bearer(viewer)})
		expect(t, r, 200)
		if get(r.obj(t), "status") != "rolled_back" {
			t.Fatalf("the version rolled back from must be marked: %s", r.body)
		}
		if get(h.do(t, req{method: "GET", path: "/v1/manifest?app=mobile", headers: key(pubKey)}).obj(t), "config.version") != 5.0 {
			t.Fatal("manifest must point at the rollback version")
		}
		// An old app below v1's minClientVersion (1.0.0) keeps the default.
		if get(h.do(t, req{method: "GET", path: "/v1/manifest?app=mobile&clientVersion=0.5.0", headers: key(pubKey)}).obj(t), "config.version") != 0.0 {
			t.Fatal("incompatible clients must not receive configs that need a newer app")
		}
	})

	t.Run("platform: API keys, origins, webhooks, audit, health", func(t *testing.T) {
		r := h.do(t, req{method: "POST", path: "/v1/admin/api-clients", headers: bearer(admin), body: map[string]any{
			"name": "Website", "kind": "public", "allowedOrigins": []string{"https://www.example.com"}}})
		expect(t, r, 201)
		siteKey := get(r.obj(t), "key").(string)
		clientID := get(r.obj(t), "id").(string)
		r = h.do(t, req{method: "GET", path: "/v1/manifest", headers: map[string]string{"X-Api-Key": siteKey, "Origin": "https://www.example.com"}})
		expect(t, r, 200)
		if r.header.Get("Access-Control-Allow-Origin") != "https://www.example.com" {
			t.Fatal("CORS header missing")
		}
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/manifest", headers: map[string]string{"X-Api-Key": siteKey, "Origin": "https://evil.example"}}), 403, "ORIGIN_NOT_ALLOWED")
		r = h.do(t, req{method: "GET", path: "/v1/admin/api-clients", headers: bearer(admin)})
		expect(t, r, 200)
		if strings.Contains(string(r.body), siteKey) {
			t.Fatal("keys must never be listed")
		}
		expect(t, h.do(t, req{method: "DELETE", path: "/v1/admin/api-clients/" + clientID, headers: bearer(admin)}), 204)
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/manifest", headers: key(siteKey)}), 401, "INVALID_API_KEY")

		// Webhook: register, trigger, deliver with the worker, verify the signature.
		got := make(chan *http.Request, 10)
		bodies := make(chan []byte, 10)
		sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got <- r
			bodies <- b
		}))
		defer sink.Close()
		r = h.do(t, req{method: "POST", path: "/v1/admin/webhooks", headers: bearer(admin), body: map[string]any{"url": sink.URL + "/hook", "topics": []string{"events"}}})
		expect(t, r, 201)
		secret := get(r.obj(t), "secret").(string)
		hookID := get(r.obj(t), "id").(string)
		expect(t, h.do(t, req{method: "GET", path: "/v1/admin/webhooks", headers: bearer(admin)}), 200)
		expect(t, h.do(t, req{method: "POST", path: "/v1/admin/events/" + importedID + "/publish", headers: bearer(admin)}), 200)
		w := app.NewWorker(h.pool, h.cfg, h.log, h.a.Metrics)
		if n, err := w.RunOnce(context.Background(), 10); err != nil || n != 1 {
			t.Fatalf("worker delivered %d (%v)", n, err)
		}
		select {
		case rq := <-got:
			b := <-bodies
			if !outbox.Verify(secret, rq.Header.Get("X-Calendar-Signature"), b, time.Now(), 5*time.Minute) {
				t.Fatal("webhook signature does not verify")
			}
			if rq.Header.Get("X-Calendar-Event") != "events.changed" {
				t.Fatalf("event type %q", rq.Header.Get("X-Calendar-Event"))
			}
		case <-time.After(5 * time.Second):
			t.Fatal("webhook not delivered")
		}
		r = h.do(t, req{method: "GET", path: "/v1/admin/webhooks/" + hookID + "/deliveries", headers: bearer(admin)})
		expect(t, r, 200)
		if get(r.obj(t), "items.0.state") != "delivered" {
			t.Fatalf("deliveries: %s", r.body)
		}
		expect(t, h.do(t, req{method: "DELETE", path: "/v1/admin/webhooks/" + hookID, headers: bearer(admin)}), 204)

		r = h.do(t, req{method: "GET", path: "/v1/admin/audit?entity=event&limit=5", headers: bearer(viewer)})
		expect(t, r, 200)
		if len(get(r.obj(t), "items").([]any)) != 5 || get(r.obj(t), "nextCursor") == nil {
			t.Fatalf("audit: %.500s", r.body)
		}
		r = h.do(t, req{method: "GET", path: "/v1/admin/health/data", headers: bearer(viewer)})
		expect(t, r, 200)
		if get(r.obj(t), "currentBsYear") == nil || get(r.obj(t), "outbox.pending") == nil {
			t.Fatalf("health: %s", r.body)
		}

		r = h.do(t, req{method: "POST", path: "/v1/telemetry", headers: key(pubKey), body: map[string]any{
			"events": []any{map[string]any{"type": "config_applied", "app": "mobile", "clientVersion": "1.2.0", "configVersion": 5}}}})
		expect(t, r, 202)
		expectProblem(t, h.do(t, req{method: "POST", path: "/v1/telemetry", headers: key(pubKey), invalid: true,
			body: map[string]any{"events": []any{map[string]any{"type": "made_up"}}}}), 422, "VALIDATION_FAILED")

		// Disabling a user revokes their sessions immediately.
		var viewerID string
		for _, u := range get(h.do(t, req{method: "GET", path: "/v1/admin/users", headers: bearer(admin)}).obj(t), "items").([]any) {
			if get(u, "email") == "viewer@example.com" {
				viewerID = get(u, "id").(string)
			}
		}
		r = h.do(t, req{method: "PATCH", path: "/v1/admin/users/" + viewerID, headers: bearer(admin), body: map[string]any{"disabled": true}})
		expect(t, r, 200)
		expectProblem(t, h.do(t, req{method: "GET", path: "/v1/admin/me", headers: bearer(viewer)}), 401, "UNAUTHORIZED")
	})

	t.Run("every documented operation was exercised", func(t *testing.T) {
		var missing []string
		for path, item := range h.doc.Paths.Map() {
			for method, op := range item.Operations() {
				if !h.covered[op.OperationID] {
					missing = append(missing, fmt.Sprintf("%s %s (%s)", method, path, op.OperationID))
				}
			}
		}
		slices.Sort(missing)
		if len(missing) > 0 {
			t.Fatalf("%d operations in openapi.yaml are not covered by this flow test:\n  %s", len(missing), strings.Join(missing, "\n  "))
		}
	})
}

func cloneJSON(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
