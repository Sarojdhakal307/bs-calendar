// Package httpapi exposes the calendar service over HTTP. Every route here is described
// in api/openapi.yaml, and the flow test validates real responses against that file.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/auth"
	"bscalendar/services/calendar-api/internal/calendardata"
	"bscalendar/services/calendar-api/internal/config"
	"bscalendar/services/calendar-api/internal/events"
	"bscalendar/services/calendar-api/internal/outbox"
	"bscalendar/services/calendar-api/internal/uiconfig"
)

// Deps are the server's collaborators.
type Deps struct {
	Config          config.Config
	Log             *slog.Logger
	Pool            *pgxpool.Pool
	Auth            *auth.Service
	Data            *calendardata.Service
	Events          *events.Service
	UI              *uiconfig.Service
	Webhooks        *outbox.Manager
	Metrics         *Metrics
	DefaultTenantID string
	BuildVersion    string
}

// Server is the HTTP API.
type Server struct {
	cfg       config.Config
	log       *slog.Logger
	pool      *pgxpool.Pool
	auth      *auth.Service
	data      *calendardata.Service
	events    *events.Service
	ui        *uiconfig.Service
	webhooks  *outbox.Manager
	metrics   *Metrics
	tenantID  string
	version   string
	now       func() time.Time
	clientLim *limiterSet
	ipLim     *limiterSet
	loginLim  *limiterSet
	adminLim  *limiterSet
	mux       *http.ServeMux
}

// New builds the server and registers routes.
func New(d Deps) *Server {
	s := &Server{
		cfg: d.Config, log: d.Log, pool: d.Pool, auth: d.Auth, data: d.Data, events: d.Events, ui: d.UI,
		webhooks: d.Webhooks, metrics: d.Metrics, tenantID: d.DefaultTenantID, version: d.BuildVersion,
		now: time.Now, clientLim: newLimiterSet(), ipLim: newLimiterSet(), loginLim: newLimiterSet(), adminLim: newLimiterSet(),
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

// Handler returns the root handler with middleware applied.
func (s *Server) Handler() http.Handler { return s.base(s.cors(s.mux)) }

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// wrap records the matched route and converts errors to problem documents.
func (s *Server) wrap(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info(r).route = r.Pattern
		if err := h(w, r); err != nil {
			writeProblem(w, r, err)
		}
	}
}

// public authenticates an API key (X-Api-Key; the api_key query parameter is accepted
// only on the ICS feed, because calendar apps cannot send headers).
func (s *Server) public(h handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		ri := info(r)
		key := r.Header.Get("X-Api-Key")
		if key == "" && strings.HasSuffix(r.URL.Path, ".ics") {
			key = r.URL.Query().Get("api_key")
		}
		w.Header().Add("Vary", "X-Api-Key")
		if key == "" {
			if s.cfg.RequireAPIKey {
				return apperr.Unauthorized(apperr.CodeInvalidAPIKey, "Send your API key in the X-Api-Key header.")
			}
			ri.tenantID = s.tenantID
			if err := s.limit(w, s.ipLim, ri.ip, 600); err != nil {
				return err
			}
			return h(w, r)
		}
		c, err := s.auth.LookupKey(r.Context(), key)
		if err != nil {
			return err
		}
		if origin := r.Header.Get("Origin"); origin != "" && len(c.AllowedOrigins) > 0 && !contains(c.AllowedOrigins, origin) {
			return apperr.Forbidden(apperr.CodeOriginNotAllowed, "This API key is not allowed from origin "+origin+".")
		}
		ri.client, ri.tenantID = &c, c.TenantID
		if err := s.limit(w, s.clientLim, c.ID, c.RatePerMin); err != nil {
			return err
		}
		return h(w, r)
	}
}

// admin authenticates a bearer token and checks a permission.
func (s *Server) admin(perm auth.Permission, h handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || tok == "" {
			return apperr.Unauthorized(apperr.CodeUnauthorized, "Send an access token: Authorization: Bearer <token>.")
		}
		a, err := s.auth.Authenticate(r.Context(), strings.TrimSpace(tok))
		if err != nil {
			return err
		}
		ri := info(r)
		ri.admin, ri.tenantID = &a, a.TenantID
		w.Header().Set("Cache-Control", cachePrivate)
		if err := s.limit(w, s.adminLim, a.UserID, 1200); err != nil {
			return err
		}
		if !auth.Can(a.Role, perm) {
			return apperr.Forbidden(apperr.CodeForbidden, "Your role ("+a.Role+") does not have the "+string(perm)+" permission.")
		}
		return h(w, r)
	}
}

func (s *Server) actor(r *http.Request) auditActor {
	ri := info(r)
	return ri.admin.Actor(ri.ip, ri.id)
}

func (s *Server) routes() {
	m := s.mux
	// Operations and documentation (no auth).
	m.HandleFunc("GET /healthz", s.wrap(s.healthz))
	m.HandleFunc("GET /readyz", s.wrap(s.readyz))
	m.HandleFunc("GET /openapi.yaml", s.wrap(s.openapi))
	m.HandleFunc("GET /docs", s.wrap(s.docsRedoc))
	m.HandleFunc("GET /docs/try", s.wrap(s.docsSwagger))
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/docs", http.StatusFound) })

	// Public read API (API key).
	m.HandleFunc("GET /v1/manifest", s.wrap(s.public(s.getManifest)))
	m.HandleFunc("GET /v1/calendar/data/latest", s.wrap(s.public(s.getDataLatest)))
	m.HandleFunc("GET /v1/calendar/data/{version}", s.wrap(s.public(s.getData)))
	m.HandleFunc("GET /v1/ui-config/{app}/{version}", s.wrap(s.public(s.getUIConfig)))
	m.HandleFunc("GET /v1/events/buckets/{bsYear}/{version}", s.wrap(s.public(s.getBucket)))
	m.HandleFunc("GET /v1/events", s.wrap(s.public(s.getEvents)))
	m.HandleFunc("GET /v1/events.ics", s.wrap(s.public(s.getICS)))
	m.HandleFunc("GET /v1/categories", s.wrap(s.public(s.getCategories)))
	m.HandleFunc("GET /v1/convert", s.wrap(s.public(s.getConvert)))
	m.HandleFunc("GET /v1/today", s.wrap(s.public(s.getToday)))
	m.HandleFunc("GET /v1/months/{basis}/{year}/{month}", s.wrap(s.public(s.getMonth)))
	m.HandleFunc("POST /v1/telemetry", s.wrap(s.public(s.postTelemetry)))

	// Admin authentication.
	m.HandleFunc("POST /v1/admin/auth/login", s.wrap(s.login))
	m.HandleFunc("POST /v1/admin/auth/refresh", s.wrap(s.refresh))
	m.HandleFunc("POST /v1/admin/auth/logout", s.wrap(s.admin(auth.PermRead, s.logout)))
	m.HandleFunc("GET /v1/admin/me", s.wrap(s.admin(auth.PermRead, s.me)))

	// Categories.
	m.HandleFunc("GET /v1/admin/categories", s.wrap(s.admin(auth.PermRead, s.listCategories)))
	m.HandleFunc("POST /v1/admin/categories", s.wrap(s.admin(auth.PermCategoriesWrite, s.createCategory)))
	m.HandleFunc("PATCH /v1/admin/categories/{id}", s.wrap(s.admin(auth.PermCategoriesWrite, s.patchCategory)))
	m.HandleFunc("DELETE /v1/admin/categories/{id}", s.wrap(s.admin(auth.PermCategoriesWrite, s.deleteCategory)))

	// Events.
	m.HandleFunc("GET /v1/admin/events", s.wrap(s.admin(auth.PermRead, s.listEvents)))
	m.HandleFunc("POST /v1/admin/events", s.wrap(s.admin(auth.PermEventsWrite, s.createEvent)))
	m.HandleFunc("POST /v1/admin/events/import", s.wrap(s.admin(auth.PermEventsWrite, s.importEvents)))
	m.HandleFunc("POST /v1/admin/events/copy-year", s.wrap(s.admin(auth.PermEventsWrite, s.copyYear)))
	m.HandleFunc("GET /v1/admin/events/{id}", s.wrap(s.admin(auth.PermRead, s.getEvent)))
	m.HandleFunc("PATCH /v1/admin/events/{id}", s.wrap(s.admin(auth.PermEventsWrite, s.patchEvent)))
	m.HandleFunc("DELETE /v1/admin/events/{id}", s.wrap(s.admin(auth.PermEventsWrite, s.deleteEvent)))
	m.HandleFunc("POST /v1/admin/events/{id}/publish", s.wrap(s.admin(auth.PermEventsWrite, s.eventAction("publish"))))
	m.HandleFunc("POST /v1/admin/events/{id}/archive", s.wrap(s.admin(auth.PermEventsWrite, s.eventAction("archive"))))
	m.HandleFunc("POST /v1/admin/events/{id}/restore", s.wrap(s.admin(auth.PermEventsWrite, s.eventAction("restore"))))

	// Year table.
	m.HandleFunc("GET /v1/admin/years", s.wrap(s.admin(auth.PermRead, s.listYears)))
	m.HandleFunc("GET /v1/admin/years/drafts", s.wrap(s.admin(auth.PermRead, s.listDrafts)))
	m.HandleFunc("POST /v1/admin/years/drafts", s.wrap(s.admin(auth.PermYearsPropose, s.createDraft)))
	m.HandleFunc("GET /v1/admin/years/drafts/{id}", s.wrap(s.admin(auth.PermRead, s.getDraft)))
	m.HandleFunc("POST /v1/admin/years/drafts/{id}/approve", s.wrap(s.admin(auth.PermYearsApprove, s.approveDraft)))
	m.HandleFunc("POST /v1/admin/years/drafts/{id}/reject", s.wrap(s.admin(auth.PermYearsPropose, s.rejectDraft)))

	// UI configuration.
	m.HandleFunc("POST /v1/admin/ui-configs/validate", s.wrap(s.admin(auth.PermRead, s.validateUIConfig)))
	m.HandleFunc("GET /v1/admin/ui-configs/{app}", s.wrap(s.admin(auth.PermRead, s.listUIConfigs)))
	m.HandleFunc("POST /v1/admin/ui-configs/{app}", s.wrap(s.admin(auth.PermConfigDraft, s.createUIConfig)))
	m.HandleFunc("POST /v1/admin/ui-configs/{app}/rollout", s.wrap(s.admin(auth.PermConfigPublish, s.rolloutUIConfig)))
	m.HandleFunc("POST /v1/admin/ui-configs/{app}/rollback", s.wrap(s.admin(auth.PermConfigPublish, s.rollbackUIConfig)))
	m.HandleFunc("GET /v1/admin/ui-configs/{app}/{version}", s.wrap(s.admin(auth.PermRead, s.getUIConfigVersion)))
	m.HandleFunc("PATCH /v1/admin/ui-configs/{app}/{version}", s.wrap(s.admin(auth.PermConfigDraft, s.updateUIConfig)))
	m.HandleFunc("POST /v1/admin/ui-configs/{app}/{version}/submit", s.wrap(s.admin(auth.PermConfigDraft, s.submitUIConfig)))
	m.HandleFunc("POST /v1/admin/ui-configs/{app}/{version}/approve", s.wrap(s.admin(auth.PermConfigPublish, s.approveUIConfig)))
	m.HandleFunc("POST /v1/admin/ui-configs/{app}/{version}/reject", s.wrap(s.admin(auth.PermConfigPublish, s.rejectUIConfig)))

	// Platform.
	m.HandleFunc("GET /v1/admin/api-clients", s.wrap(s.admin(auth.PermPlatformManage, s.listClients)))
	m.HandleFunc("POST /v1/admin/api-clients", s.wrap(s.admin(auth.PermPlatformManage, s.createClient)))
	m.HandleFunc("DELETE /v1/admin/api-clients/{id}", s.wrap(s.admin(auth.PermPlatformManage, s.revokeClient)))
	m.HandleFunc("GET /v1/admin/webhooks", s.wrap(s.admin(auth.PermPlatformManage, s.listWebhooks)))
	m.HandleFunc("POST /v1/admin/webhooks", s.wrap(s.admin(auth.PermPlatformManage, s.createWebhook)))
	m.HandleFunc("DELETE /v1/admin/webhooks/{id}", s.wrap(s.admin(auth.PermPlatformManage, s.deleteWebhook)))
	m.HandleFunc("GET /v1/admin/webhooks/{id}/deliveries", s.wrap(s.admin(auth.PermPlatformManage, s.listDeliveries)))
	m.HandleFunc("GET /v1/admin/users", s.wrap(s.admin(auth.PermPlatformManage, s.listUsers)))
	m.HandleFunc("POST /v1/admin/users", s.wrap(s.admin(auth.PermPlatformManage, s.createUser)))
	m.HandleFunc("PATCH /v1/admin/users/{id}", s.wrap(s.admin(auth.PermPlatformManage, s.patchUser)))
	m.HandleFunc("GET /v1/admin/audit", s.wrap(s.admin(auth.PermRead, s.listAudit)))
	m.HandleFunc("GET /v1/admin/health/data", s.wrap(s.admin(auth.PermRead, s.dataHealth)))

	// Anything else is a JSON 404.
	m.HandleFunc("/", s.wrap(func(http.ResponseWriter, *http.Request) error {
		return apperr.NotFound("route")
	}))
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// healthz reports liveness.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, r, http.StatusOK, map[string]any{"status": "ok", "version": s.version}, cacheNoStore, "")
	return nil
}

// readyz reports whether the database and year table are available.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]string{"database": "ok", "calendarData": "ok"}
	status := http.StatusOK
	if err := s.pool.Ping(ctx); err != nil {
		checks["database"], status = "unavailable", http.StatusServiceUnavailable
	}
	if !s.data.Loaded() {
		checks["calendarData"], status = "not loaded", http.StatusServiceUnavailable
	}
	st := "ready"
	if status != http.StatusOK {
		st = "not ready"
	}
	writeJSON(w, r, status, map[string]any{"status": st, "checks": checks}, cacheNoStore, "")
	return nil
}
