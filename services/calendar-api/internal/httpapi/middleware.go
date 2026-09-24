package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/auth"
)

// reqInfo carries per-request identity. It is mutable so outer middleware (logging)
// can read what inner handlers resolved.
type reqInfo struct {
	id       string
	ip       string
	route    string
	tenantID string
	client   *auth.Client
	admin    *auth.Admin
}

type ctxKey struct{}

func info(r *http.Request) *reqInfo {
	if ri, ok := r.Context().Value(ctxKey{}).(*reqInfo); ok {
		return ri
	}
	return &reqInfo{}
}

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}

// base installs request info, request IDs, panic recovery, access logs, metrics and security headers.
func (s *Server) base(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ri := &reqInfo{ip: s.clientIP(r)}
		if id := r.Header.Get("X-Request-Id"); requestIDRe.MatchString(id) {
			ri.id = id
		} else {
			ri.id = newRequestID()
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, ri))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		h := w.Header()
		h.Set("X-Request-Id", ri.id)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic", "panic", fmt.Sprint(p), "stack", string(debug.Stack()), "requestId", ri.id)
				if !sw.wrote {
					writeProblem(sw, r, apperr.Internal(fmt.Errorf("panic: %v", p)))
				}
			}
			route := ri.route
			if route == "" {
				route = "unmatched"
			}
			dur := time.Since(start)
			s.metrics.observe(route, r.Method, sw.status, dur)
			attrs := []any{"method", r.Method, "path", r.URL.Path, "route", route, "status", sw.status,
				"bytes", sw.bytes, "durationMs", float64(dur.Microseconds()) / 1000, "requestId", ri.id, "ip", ri.ip}
			if ri.client != nil {
				attrs = append(attrs, "apiClient", ri.client.Name)
			}
			if ri.admin != nil {
				attrs = append(attrs, "adminUser", ri.admin.UserID)
			}
			level := slog.LevelInfo
			if sw.status >= 500 {
				level = slog.LevelError
			} else if route == "GET /healthz" || route == "GET /readyz" {
				level = slog.LevelDebug
			}
			s.log.Log(r.Context(), level, "http request", attrs...)
		}()
		next.ServeHTTP(sw, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// cors answers preflights and adds CORS headers for allowed origins.
func (s *Server) cors(next http.Handler) http.Handler {
	allowAll := slices.Contains(s.cfg.CORSAllowedOrigins, "*")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		h := w.Header()
		h.Add("Vary", "Origin")
		if origin != "" && (allowAll || slices.Contains(s.cfg.CORSAllowedOrigins, origin)) {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Expose-Headers", "ETag, Location, Retry-After, X-Request-Id, RateLimit-Limit, RateLimit-Remaining")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Api-Key, If-Match, If-None-Match, X-Request-Id")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the caller's IP. X-Forwarded-For is trusted only when TRUST_PROXY is set.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- rate limiting ---------------------------------------------------------

type limiterSet struct {
	mu sync.Mutex
	m  map[string]*limiterEntry
}

type limiterEntry struct {
	l    *rate.Limiter
	seen time.Time
}

func newLimiterSet() *limiterSet { return &limiterSet{m: map[string]*limiterEntry{}} }

// allow applies a token bucket of perMin requests per minute (burst perMin/6, at least 10).
func (ls *limiterSet) allow(key string, perMin int) (ok bool, remaining int) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	now := time.Now()
	e, found := ls.m[key]
	if !found {
		if len(ls.m) > 50_000 {
			for k, v := range ls.m {
				if now.Sub(v.seen) > 10*time.Minute {
					delete(ls.m, k)
				}
			}
		}
		e = &limiterEntry{l: rate.NewLimiter(rate.Limit(float64(perMin)/60), max(perMin/6, 10))}
		ls.m[key] = e
	}
	e.seen = now
	ok = e.l.AllowN(now, 1)
	return ok, int(e.l.TokensAt(now))
}

func (s *Server) limit(w http.ResponseWriter, set *limiterSet, key string, perMin int) error {
	ok, remaining := set.allow(key, perMin)
	w.Header().Set("RateLimit-Limit", strconv.Itoa(perMin))
	w.Header().Set("RateLimit-Remaining", strconv.Itoa(max(remaining, 0)))
	if !ok {
		s.metrics.rateLimited.Inc()
		w.Header().Set("Retry-After", "10")
		return &apperr.Error{Status: http.StatusTooManyRequests, Code: apperr.CodeRateLimited, Title: "Too many requests",
			Detail: "Rate limit exceeded. Retry after the number of seconds in Retry-After."}
	}
	return nil
}
