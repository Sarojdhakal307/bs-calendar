package httpapi

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/audit"
	"bscalendar/services/calendar-api/internal/auth"
	"bscalendar/services/calendar-api/internal/bscal"
	"bscalendar/services/calendar-api/internal/calendardata"
	"bscalendar/services/calendar-api/internal/events"
	"bscalendar/services/calendar-api/internal/outbox"
	"bscalendar/services/calendar-api/internal/uiconfig"
)

type auditActor = audit.Actor

func list[T any](items []T) map[string]any {
	if items == nil {
		items = []T{}
	}
	return map[string]any{"items": items}
}

// ---- auth ------------------------------------------------------------------------

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	if err := s.limit(w, s.loginLim, info(r).ip, 10); err != nil {
		return err
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	pair, err := s.auth.Login(r.Context(), body.Email, body.Password, info(r).ip, r.UserAgent())
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, pair, cacheNoStore, "")
	return nil
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) error {
	if err := s.limit(w, s.loginLim, "refresh:"+info(r).ip, 60); err != nil {
		return err
	}
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	pair, err := s.auth.Refresh(r.Context(), body.RefreshToken, info(r).ip, r.UserAgent())
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, pair, cacheNoStore, "")
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	ri := info(r)
	if err := s.auth.Logout(r.Context(), *ri.admin, ri.ip, ri.id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	a := info(r).admin
	perms := []auth.Permission{}
	for _, p := range []auth.Permission{auth.PermRead, auth.PermEventsWrite, auth.PermCategoriesWrite, auth.PermYearsPropose,
		auth.PermYearsApprove, auth.PermConfigDraft, auth.PermConfigPublish, auth.PermPlatformManage} {
		if auth.Can(a.Role, p) {
			perms = append(perms, p)
		}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"id": a.UserID, "email": a.Email, "role": a.Role, "permissions": perms}, cachePrivate, "")
	return nil
}

// ---- categories --------------------------------------------------------------------

func (s *Server) listCategories(w http.ResponseWriter, r *http.Request) error {
	cs, err := s.events.ListCategories(r.Context(), info(r).tenantID)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, list(cs), cachePrivate, "")
	return nil
}

func (s *Server) createCategory(w http.ResponseWriter, r *http.Request) error {
	var in events.CategoryInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	c, err := s.events.CreateCategory(r.Context(), s.actor(r), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/admin/categories/"+c.ID)
	writeJSON(w, r, http.StatusCreated, c, cacheNoStore, "")
	return nil
}

func (s *Server) patchCategory(w http.ResponseWriter, r *http.Request) error {
	patch, err := mergePatchBody(w, r)
	if err != nil {
		return err
	}
	if !events.ValidUUID(r.PathValue("id")) {
		return apperr.NotFound("category")
	}
	c, err := s.events.PatchCategory(r.Context(), s.actor(r), r.PathValue("id"), patch)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, c, cacheNoStore, "")
	return nil
}

func (s *Server) deleteCategory(w http.ResponseWriter, r *http.Request) error {
	if !events.ValidUUID(r.PathValue("id")) {
		return apperr.NotFound("category")
	}
	if err := s.events.DeleteCategory(r.Context(), s.actor(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func mergePatchBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if err := requireContentType(r, "application/merge-patch+json", "application/json"); err != nil {
		return nil, err
	}
	b, err := readBody(w, r, maxBody)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, apperr.BadRequest("Request body is empty.")
	}
	return b, nil
}

// ---- events ------------------------------------------------------------------------

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		return err
	}
	f := events.ListFilter{Status: q.Get("status"), Category: q.Get("category"), Q: q.Get("q"), Cursor: q.Get("cursor"), Limit: limit}
	for name, dst := range map[string]**bscal.Date{"from": &f.From, "to": &f.To} {
		if v := q.Get(name); v != "" {
			d, err := bscal.ParseDate(v)
			if err != nil || !bscal.ValidGregorian(d) {
				return apperr.Validation(apperr.FieldError{Field: name, Message: "must be an AD date in YYYY-MM-DD format"})
			}
			*dst = &d
		}
	}
	p, err := s.events.List(r.Context(), info(r).tenantID, f)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, p, cachePrivate, "")
	return nil
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request) error {
	var in events.EventInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	e, err := s.events.Create(r.Context(), s.actor(r), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/admin/events/"+e.ID)
	writeJSON(w, r, http.StatusCreated, e, cacheNoStore, events.ETag(e.Version))
	return nil
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) error {
	e, err := s.events.Get(r.Context(), info(r).tenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, e, cachePrivate, events.ETag(e.Version))
	return nil
}

func (s *Server) patchEvent(w http.ResponseWriter, r *http.Request) error {
	v, err := requireIfMatch(r)
	if err != nil {
		return err
	}
	patch, err := mergePatchBody(w, r)
	if err != nil {
		return err
	}
	e, err := s.events.Patch(r.Context(), s.actor(r), r.PathValue("id"), v, patch)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, e, cacheNoStore, events.ETag(e.Version))
	return nil
}

func (s *Server) deleteEvent(w http.ResponseWriter, r *http.Request) error {
	v, err := requireIfMatch(r)
	if err != nil {
		return err
	}
	e, err := s.events.Transition(r.Context(), s.actor(r), r.PathValue("id"), "delete", &v)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, e, cacheNoStore, events.ETag(e.Version))
	return nil
}

func (s *Server) eventAction(action string) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		v, err := ifMatch(r)
		if err != nil {
			return err
		}
		e, err := s.events.Transition(r.Context(), s.actor(r), r.PathValue("id"), action, v)
		if err != nil {
			return err
		}
		writeJSON(w, r, http.StatusOK, e, cacheNoStore, events.ETag(e.Version))
		return nil
	}
}

func (s *Server) importEvents(w http.ResponseWriter, r *http.Request) error {
	if err := requireContentType(r, "text/csv"); err != nil {
		return err
	}
	b, err := readBody(w, r, maxImportBody)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	dry := q.Get("dryRun") != "false"
	skip := q.Get("skipDuplicates") == "true"
	rep, err := s.events.Import(r.Context(), s.actor(r), bytes.NewReader(b), dry, skip)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if !dry {
		status = http.StatusCreated
	}
	writeJSON(w, r, status, rep, cacheNoStore, "")
	return nil
}

func (s *Server) copyYear(w http.ResponseWriter, r *http.Request) error {
	var in events.CopyYearInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	rep, err := s.events.CopyYear(r.Context(), s.actor(r), in)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusCreated, rep, cacheNoStore, "")
	return nil
}

// ---- year table ----------------------------------------------------------------------

func (s *Server) listYears(w http.ResponseWriter, r *http.Request) error {
	ys, err := s.data.Years(r.Context())
	if err != nil {
		return err
	}
	_, v, sha := s.data.Current()
	writeJSON(w, r, http.StatusOK, map[string]any{"dataVersion": v, "sha256": sha, "items": ys}, cachePrivate, "")
	return nil
}

func (s *Server) listDrafts(w http.ResponseWriter, r *http.Request) error {
	ds, err := s.data.ListDrafts(r.Context(), r.URL.Query().Get("state"))
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, list(ds), cachePrivate, "")
	return nil
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) error {
	var in calendardata.DraftInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	d, err := s.data.ProposeDraft(r.Context(), s.actor(r), in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/admin/years/drafts/"+d.ID)
	writeJSON(w, r, http.StatusCreated, d, cacheNoStore, "")
	return nil
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) error {
	d, err := s.data.GetDraft(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, d, cachePrivate, "")
	return nil
}

func (s *Server) approveDraft(w http.ResponseWriter, r *http.Request) error {
	d, err := s.data.Approve(r.Context(), s.actor(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	s.metrics.SetDataVersion(s.data.Table().Version())
	writeJSON(w, r, http.StatusOK, d, cacheNoStore, "")
	return nil
}

type reasonBody struct {
	Reason string `json:"reason"`
}

func (s *Server) rejectDraft(w http.ResponseWriter, r *http.Request) error {
	var b reasonBody
	if err := decodeJSON(w, r, &b); err != nil {
		return err
	}
	d, err := s.data.Reject(r.Context(), s.actor(r), r.PathValue("id"), b.Reason)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, d, cacheNoStore, "")
	return nil
}

// ---- UI config ---------------------------------------------------------------------------

func appParam(r *http.Request) (string, error) {
	app := r.PathValue("app")
	if !uiconfig.ValidApp(app) {
		return "", apperr.Validation(apperr.FieldError{Field: "app", Message: "2-31 characters: lower-case letters, digits, dashes"})
	}
	return app, nil
}

func appVersion(r *http.Request) (string, int, error) {
	app, err := appParam(r)
	if err != nil {
		return "", 0, err
	}
	v, err := pathInt(r, "version")
	if err != nil || v < 1 {
		return "", 0, apperr.NotFound("config version")
	}
	return app, v, nil
}

func (s *Server) validateUIConfig(w http.ResponseWriter, r *http.Request) error {
	if err := requireContentType(r, "application/json"); err != nil {
		return err
	}
	b, err := readBody(w, r, maxBody)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, s.ui.Validate(b), cacheNoStore, "")
	return nil
}

func (s *Server) listUIConfigs(w http.ResponseWriter, r *http.Request) error {
	app, err := appParam(r)
	if err != nil {
		return err
	}
	ch, vs, err := s.ui.List(r.Context(), info(r).tenantID, app)
	if err != nil {
		return err
	}
	if vs == nil {
		vs = []uiconfig.Version{}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"channel": ch, "items": vs}, cachePrivate, "")
	return nil
}

func (s *Server) createUIConfig(w http.ResponseWriter, r *http.Request) error {
	app, err := appParam(r)
	if err != nil {
		return err
	}
	var in uiconfig.Input
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	v, err := s.ui.CreateDraft(r.Context(), s.actor(r), app, in)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/admin/ui-configs/"+app+"/"+strconv.Itoa(v.Version))
	writeJSON(w, r, http.StatusCreated, withReport(s, v), cacheNoStore, "")
	return nil
}

type versionWithReport struct {
	uiconfig.Version
	Validation uiconfig.Report `json:"validation"`
}

func withReport(s *Server, v uiconfig.Version) versionWithReport {
	return versionWithReport{Version: v, Validation: s.ui.Validate(v.Config)}
}

func (s *Server) getUIConfigVersion(w http.ResponseWriter, r *http.Request) error {
	app, ver, err := appVersion(r)
	if err != nil {
		return err
	}
	v, err := s.ui.Get(r.Context(), info(r).tenantID, app, ver)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, withReport(s, v), cachePrivate, "")
	return nil
}

func (s *Server) updateUIConfig(w http.ResponseWriter, r *http.Request) error {
	app, ver, err := appVersion(r)
	if err != nil {
		return err
	}
	var in uiconfig.Input
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	v, err := s.ui.UpdateDraft(r.Context(), s.actor(r), app, ver, in)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, withReport(s, v), cacheNoStore, "")
	return nil
}

func (s *Server) submitUIConfig(w http.ResponseWriter, r *http.Request) error {
	app, ver, err := appVersion(r)
	if err != nil {
		return err
	}
	v, err := s.ui.Submit(r.Context(), s.actor(r), app, ver)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, withReport(s, v), cacheNoStore, "")
	return nil
}

func (s *Server) approveUIConfig(w http.ResponseWriter, r *http.Request) error {
	app, ver, err := appVersion(r)
	if err != nil {
		return err
	}
	var b struct {
		RolloutPercent *int `json:"rolloutPercent"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &b); err != nil {
			return err
		}
	}
	pct := 100
	if b.RolloutPercent != nil {
		pct = *b.RolloutPercent
	}
	v, err := s.ui.Approve(r.Context(), s.actor(r), app, ver, pct)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, withReport(s, v), cacheNoStore, "")
	return nil
}

func (s *Server) rejectUIConfig(w http.ResponseWriter, r *http.Request) error {
	app, ver, err := appVersion(r)
	if err != nil {
		return err
	}
	var b reasonBody
	if err := decodeJSON(w, r, &b); err != nil {
		return err
	}
	v, err := s.ui.Reject(r.Context(), s.actor(r), app, ver, b.Reason)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, withReport(s, v), cacheNoStore, "")
	return nil
}

func (s *Server) rolloutUIConfig(w http.ResponseWriter, r *http.Request) error {
	app, err := appParam(r)
	if err != nil {
		return err
	}
	var b struct {
		Percent *int `json:"percent"`
	}
	if err := decodeJSON(w, r, &b); err != nil {
		return err
	}
	if b.Percent == nil {
		return apperr.Validation(apperr.FieldError{Field: "percent", Message: "required, 0-100"})
	}
	ch, err := s.ui.SetRollout(r.Context(), s.actor(r), app, *b.Percent)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, ch, cacheNoStore, "")
	return nil
}

func (s *Server) rollbackUIConfig(w http.ResponseWriter, r *http.Request) error {
	app, err := appParam(r)
	if err != nil {
		return err
	}
	var b struct {
		ToVersion int    `json:"toVersion"`
		Reason    string `json:"reason"`
	}
	if err := decodeJSON(w, r, &b); err != nil {
		return err
	}
	v, err := s.ui.Rollback(r.Context(), s.actor(r), app, b.ToVersion, b.Reason)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusCreated, withReport(s, v), cacheNoStore, "")
	return nil
}

// ---- platform ----------------------------------------------------------------------------

func (s *Server) listClients(w http.ResponseWriter, r *http.Request) error {
	cs, err := s.auth.ListClients(r.Context(), info(r).tenantID)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, list(cs), cachePrivate, "")
	return nil
}

func (s *Server) createClient(w http.ResponseWriter, r *http.Request) error {
	var in auth.ClientInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	c, key, err := s.auth.CreateClient(r.Context(), s.actor(r), in)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusCreated, struct {
		auth.Client
		Key string `json:"key"`
	}{c, key}, cacheNoStore, "")
	return nil
}

func (s *Server) revokeClient(w http.ResponseWriter, r *http.Request) error {
	if !events.ValidUUID(r.PathValue("id")) {
		return apperr.NotFound("API client")
	}
	if err := s.auth.RevokeClient(r.Context(), s.actor(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) error {
	ws, err := s.webhooks.List(r.Context(), info(r).tenantID)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, list(ws), cachePrivate, "")
	return nil
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) error {
	var in outbox.WebhookInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	wh, secret, err := s.webhooks.Create(r.Context(), s.actor(r), in)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusCreated, struct {
		outbox.Webhook
		Secret string `json:"secret"`
	}{wh, secret}, cacheNoStore, "")
	return nil
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) error {
	if !events.ValidUUID(r.PathValue("id")) {
		return apperr.NotFound("webhook")
	}
	if err := s.webhooks.Delete(r.Context(), s.actor(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) error {
	if !events.ValidUUID(r.PathValue("id")) {
		return apperr.NotFound("webhook")
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		return err
	}
	ds, err := s.webhooks.Deliveries(r.Context(), info(r).tenantID, r.PathValue("id"), limit)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, list(ds), cachePrivate, "")
	return nil
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) error {
	us, err := s.auth.ListUsers(r.Context(), info(r).tenantID)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, list(us), cachePrivate, "")
	return nil
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) error {
	var in auth.UserInput
	if err := decodeJSON(w, r, &in); err != nil {
		return err
	}
	u, err := s.auth.CreateUser(r.Context(), s.actor(r), in)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusCreated, u, cacheNoStore, "")
	return nil
}

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request) error {
	if !events.ValidUUID(r.PathValue("id")) {
		return apperr.NotFound("user")
	}
	var p auth.UserPatch
	if err := decodeJSON(w, r, &p); err != nil {
		return err
	}
	u, err := s.auth.UpdateUser(r.Context(), s.actor(r), r.PathValue("id"), p)
	if err != nil {
		return err
	}
	writeJSON(w, r, http.StatusOK, u, cacheNoStore, "")
	return nil
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		return err
	}
	if a := q.Get("actorId"); a != "" && !events.ValidUUID(a) {
		return apperr.Validation(apperr.FieldError{Field: "actorId", Message: "must be a user id"})
	}
	items, next, err := audit.List(r.Context(), s.pool, info(r).tenantID, audit.Filter{
		Entity: q.Get("entity"), EntityID: q.Get("entityId"), ActorID: q.Get("actorId"), Action: q.Get("action"),
		Cursor: q.Get("cursor"), Limit: limit,
	})
	if err != nil {
		if err.Error() == "invalid cursor" {
			return apperr.BadRequest("Invalid cursor.")
		}
		return err
	}
	var nc *string
	if next != "" {
		nc = &next
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"items": items, "nextCursor": nc}, cachePrivate, "")
	return nil
}

// dataHealth powers the admin dashboard and the yearly-operations alerts (docs/reliable.md §9.2).
func (s *Server) dataHealth(w http.ResponseWriter, r *http.Request) error {
	t := s.data.Table()
	today := bscal.NepalTodayEpoch(s.now())
	cur := s.currentBSYear(t)
	res := map[string]any{"dataVersion": t.Version(), "supportedRange": s.rangeOf(t), "currentBsYear": cur}
	if fp := firstProjected(t); fp != nil {
		y, _ := t.Year(*fp)
		res["firstProjectedBsYear"] = *fp
		res["daysUntilProjected"] = y.StartDay - today
	} else {
		res["firstProjectedBsYear"] = nil
		res["daysUntilProjected"] = nil
	}
	next := map[string]any{"bsYear": cur + 1}
	if y, ok := t.Year(cur + 1); ok {
		var holidays int
		if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM events e JOIN categories c ON c.id = e.category_id
			WHERE e.tenant_id = $1 AND e.status = 'published' AND e.deleted_at IS NULL AND COALESCE(e.is_holiday, c.is_holiday)
			  AND e.bs_year_start <= $2 AND e.bs_year_end >= $2`, info(r).tenantID, cur+1).Scan(&holidays); err != nil {
			return err
		}
		next["startsOn"] = bscal.EpochToAD(y.StartDay).String()
		next["startsInDays"] = y.StartDay - today
		next["status"] = y.Status
		next["publishedHolidays"] = holidays
	}
	res["nextYear"] = next
	st, err := outbox.GetStats(r.Context(), s.pool)
	if err != nil {
		return err
	}
	res["outbox"] = st
	writeJSON(w, r, http.StatusOK, res, cachePrivate, "")
	return nil
}

var _ = errors.New
