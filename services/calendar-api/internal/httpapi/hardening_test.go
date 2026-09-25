package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"bscalendar/services/calendar-api/internal/config"
)

func reqFrom(ip string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/admin/me", nil)
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, &reqInfo{ip: ip}))
}

func TestAdminNetworkAllowList(t *testing.T) {
	s := &Server{cfg: config.Config{AdminAllowedCIDRs: []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32"),
	}}}
	for ip, allowed := range map[string]bool{
		"10.1.2.3": true, "::ffff:10.1.2.3": true, "2001:db8::1": true,
		"192.0.2.1": false, "": false, "not-an-ip": false,
	} {
		if got := s.adminNetworkAllowed(reqFrom(ip)) == nil; got != allowed {
			t.Errorf("%q: allowed=%v, want %v", ip, got, allowed)
		}
	}
	open := &Server{cfg: config.Config{}}
	if open.adminNetworkAllowed(reqFrom("192.0.2.1")) != nil {
		t.Fatal("an empty allow-list must allow every network")
	}
}

func TestBaseMiddlewareHardening(t *testing.T) {
	s := &Server{cfg: config.Config{AppEnv: config.EnvProduction, RequestTimeout: 50 * time.Millisecond},
		metrics: NewMetrics(), log: discardLogger()}
	var deadline time.Time
	h := s.base(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, _ = r.Context().Deadline()
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("production responses must carry HSTS")
	}
	if rec.Header().Get("X-Request-Id") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("standard security headers missing")
	}
	if deadline.IsZero() || time.Until(deadline) > time.Second {
		t.Fatal("requests must get a deadline from REQUEST_TIMEOUT")
	}
	dev := &Server{cfg: config.Config{AppEnv: config.EnvDevelopment}, metrics: NewMetrics(), log: discardLogger()}
	rec = httptest.NewRecorder()
	dev.base(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS must not be sent in development (plain http)")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestPublicKeyLimitIsPerClientIP(t *testing.T) {
	s := &Server{metrics: NewMetrics()}
	set := newLimiterSet()
	// Two devices sharing one public key must not share one budget.
	for i := 0; i < 10; i++ { // burst for 60/min is max(60/6, 10) = 10
		if err := s.limit(httptest.NewRecorder(), set, "client|198.51.100.1", 60); err != nil {
			t.Fatalf("device 1 request %d limited too early", i)
		}
	}
	if err := s.limit(httptest.NewRecorder(), set, "client|198.51.100.1", 60); err == nil {
		t.Fatal("device 1 should now be limited")
	}
	if err := s.limit(httptest.NewRecorder(), set, "client|198.51.100.2", 60); err != nil {
		t.Fatal("device 2 must have its own budget")
	}
}
