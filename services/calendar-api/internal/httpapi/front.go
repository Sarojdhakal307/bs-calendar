package httpapi

import (
	"io/fs"
	"net/http"
	"strings"

	"bscalendar/web"
)

// staticCSP allows the embedded website and dashboard to load their own scripts and styles and to call
// the API on the same origin, and nothing else.
const staticCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// front maps the public paths of the single port:
//
//	/          website (embedded web/site)
//	/api/...   the API, with the /api prefix removed (/api/v1/today -> /v1/today)
//	/admin/    admin dashboard (embedded web/admin)
//	/docs      API reference (served by the mux)
//
// Everything else (/v1/..., /openapi.yaml, /healthz, /readyz) goes to the mux unchanged, because API
// responses contain links such as /v1/calendar/data/12.
func (s *Server) front(api http.Handler) http.Handler {
	siteFS := web.Site()
	site := static(siteFS, "no-cache")
	admin := http.StripPrefix("/admin", static(web.Admin(), "no-cache"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/api" || p == "/admin":
			http.Redirect(w, r, p+"/", http.StatusMovedPermanently)
		case strings.HasPrefix(p, "/api/"):
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(p, "/api")
			r2.URL.RawPath = ""
			api.ServeHTTP(w, r2)
		case strings.HasPrefix(p, "/admin/"):
			info(r).route = "/admin/"
			admin.ServeHTTP(w, r)
		case isSiteFile(siteFS, r):
			info(r).route = "/"
			site.ServeHTTP(w, r)
		default:
			api.ServeHTTP(w, r)
		}
	})
}

// isSiteFile reports whether a GET/HEAD request is for the website's home page or one of its files.
func isSiteFile(fsys fs.FS, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.URL.Path == "/" {
		return true
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	st, err := fs.Stat(fsys, name)
	return err == nil && !st.IsDir()
}

// static serves embedded files with a CSP that lets them run (the API's default CSP blocks everything).
func static(fsys fs.FS, cache string) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", staticCSP)
		h.Set("Cache-Control", cache)
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		files.ServeHTTP(w, r)
	})
}
