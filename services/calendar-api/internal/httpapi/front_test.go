package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bscalendar/services/calendar-api/internal/config"
)

func TestFrontRoutesOnePort(t *testing.T) {
	s := &Server{cfg: config.Config{AppEnv: config.EnvDevelopment}, metrics: NewMetrics(), log: discardLogger()}
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/today", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("api:" + r.URL.Path)) })
	api.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("docs")) })
	h := s.base(s.front(api))

	cases := []struct {
		path, wantBody, wantCT string
		wantCode             int
	}{
		{"/", "<title>BS Calendar API</title>", "text/html", 200},
		{"/site.js", "", "javascript", 200},
		{"/admin/", "<title>Calendar Admin</title>", "text/html", 200},
		{"/admin/app.js", "", "javascript", 200},
		{"/admin", "", "", 301},
		{"/api", "", "", 301},
		{"/api/v1/today", "api:/v1/today", "", 200},
		{"/v1/today", "api:/v1/today", "", 200},
		{"/docs", "docs", "", 200},
		{"/nope", "", "", 404},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.wantCode {
			t.Errorf("%s: status %d, want %d", c.path, rec.Code, c.wantCode)
			continue
		}
		if c.wantBody != "" && !strings.Contains(rec.Body.String(), c.wantBody) {
			t.Errorf("%s: body %.80q does not contain %q", c.path, rec.Body.String(), c.wantBody)
		}
		if c.wantCT != "" && !strings.Contains(rec.Header().Get("Content-Type"), c.wantCT) {
			t.Errorf("%s: content type %q", c.path, rec.Header().Get("Content-Type"))
		}
		if strings.HasPrefix(c.path, "/admin/") || c.path == "/" {
			if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
				t.Errorf("%s: static pages need their own CSP", c.path)
			}
		}
	}
	// POST to a site path must reach the API, not the file server.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if strings.Contains(rec.Body.String(), "<title>") {
		t.Error("POST / must not serve the website")
	}
}
