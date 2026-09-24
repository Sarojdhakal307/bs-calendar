// Package outbox implements the transactional outbox: changes enqueue webhook deliveries
// and CDN purges in the same database transaction, and a worker delivers them with retries.
// A failed delivery never blocks or rolls back the admin action that caused it.
package outbox

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/audit"
	"bscalendar/services/calendar-api/internal/store"
)

// Topics a webhook can subscribe to.
var Topics = []string{"events", "categories", "config", "data"}

// Event is the JSON body sent to webhooks.
type Event struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`  // e.g. "events.changed"
	Topic    string         `json:"topic"` // one of Topics
	TenantID string         `json:"tenantId,omitempty"`
	At       time.Time      `json:"at"`
	Data     map[string]any `json:"data,omitempty"`
}

// Enqueue adds one webhook delivery per active subscriber of the topic.
// tenantID "" targets every tenant (used for global year-table changes).
func Enqueue(ctx context.Context, q store.Querier, tenantID, topic, typ string, data map[string]any) error {
	ev := Event{ID: newID(), Type: typ, Topic: topic, TenantID: tenantID, At: time.Now().UTC(), Data: data}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO outbox (kind, payload)
		SELECT 'webhook', jsonb_build_object('webhookId', w.id::text, 'event', $3::jsonb)
		FROM webhooks w
		WHERE w.active AND $2 = ANY (w.topics) AND ($1 = '' OR w.tenant_id = NULLIF($1,'')::uuid)`,
		tenantID, topic, body)
	return err
}

// EnqueuePurge schedules a CDN purge of the given absolute URLs (no-op when no CDN is configured).
func EnqueuePurge(ctx context.Context, q store.Querier, enabled bool, urls ...string) error {
	if !enabled || len(urls) == 0 {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"files": urls})
	_, err := q.Exec(ctx, `INSERT INTO outbox (kind, payload) VALUES ('cdn.purge', $1)`, b)
	return err
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "evt_" + hex.EncodeToString(b)
}

// Sign returns the X-Calendar-Signature header value: t=<unix>,v1=<hex HMAC-SHA256 of "t.body">.
func Sign(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.", ts)
	m.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(m.Sum(nil)))
}

// Verify checks a signature header (used by tests and documented for receivers).
func Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) bool {
	var ts int64
	var sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			sig = v
		}
	}
	if ts == 0 || sig == "" || now.Sub(time.Unix(ts, 0)).Abs() > tolerance {
		return false
	}
	want := Sign(secret, ts, body)
	_, wantSig, _ := strings.Cut(want, ",v1=")
	return hmac.Equal([]byte(sig), []byte(wantSig))
}

// ---- secrets at rest -------------------------------------------------------

func encrypt(key []byte, plain string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plain), nil), nil
}

func decrypt(key, data []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	p, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	return string(p), err
}

// ---- webhook management ----------------------------------------------------

// Webhook is a registered receiver.
type Webhook struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Topics    []string  `json:"topics"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"createdAt"`
}

// Delivery is one outbox row for a webhook.
type Delivery struct {
	ID        int64      `json:"id"`
	EventType string     `json:"eventType"`
	Attempts  int        `json:"attempts"`
	State     string     `json:"state"` // pending, delivered, dead
	LastError *string    `json:"lastError"`
	NextRunAt time.Time  `json:"nextRunAt"`
	DoneAt    *time.Time `json:"doneAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

// Manager manages webhooks.
type Manager struct {
	pool         *pgxpool.Pool
	key          []byte
	allowPrivate bool
}

// NewManager creates a webhook manager.
func NewManager(pool *pgxpool.Pool, encKey []byte, allowPrivate bool) *Manager {
	return &Manager{pool: pool, key: encKey, allowPrivate: allowPrivate}
}

// WebhookInput registers a webhook.
type WebhookInput struct {
	URL    string   `json:"url"`
	Topics []string `json:"topics"`
}

// Create registers a webhook and returns its signing secret (shown once).
func (m *Manager) Create(ctx context.Context, actor audit.Actor, in WebhookInput) (Webhook, string, error) {
	var fe []apperr.FieldError
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		fe = append(fe, apperr.FieldError{Field: "url", Message: "must be an absolute http(s) URL"})
	} else if !m.allowPrivate {
		if u.Scheme != "https" {
			fe = append(fe, apperr.FieldError{Field: "url", Message: "must use https"})
		}
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicIP(ip) {
			fe = append(fe, apperr.FieldError{Field: "url", Message: "must not point to a private or local address"})
		}
	}
	if len(in.Topics) == 0 {
		fe = append(fe, apperr.FieldError{Field: "topics", Message: "at least one topic is required"})
	}
	for i, t := range in.Topics {
		if !slices.Contains(Topics, t) {
			fe = append(fe, apperr.FieldError{Field: fmt.Sprintf("topics[%d]", i), Message: "must be one of " + strings.Join(Topics, ", ")})
		}
	}
	if len(fe) > 0 {
		return Webhook{}, "", apperr.Validation(fe...)
	}
	secretRaw := make([]byte, 32)
	_, _ = rand.Read(secretRaw)
	secret := "whsec_" + base64.RawURLEncoding.EncodeToString(secretRaw)
	enc, err := encrypt(m.key, secret)
	if err != nil {
		return Webhook{}, "", err
	}
	topics := slices.Compact(slices.Sorted(slices.Values(in.Topics)))
	var w Webhook
	err = store.InTx(ctx, m.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO webhooks (tenant_id, url, secret_enc, topics, created_by) VALUES ($1, $2, $3, $4, NULLIF($5,'')::uuid)
			RETURNING id::text, url, topics, active, created_at`,
			actor.TenantID, in.URL, enc, topics, actor.UserID).Scan(&w.ID, &w.URL, &w.Topics, &w.Active, &w.CreatedAt); err != nil {
			return err
		}
		return audit.Write(ctx, tx, actor, "webhook.create", "webhook", w.ID, nil, w)
	})
	return w, secret, err
}

// List returns the tenant's webhooks.
func (m *Manager) List(ctx context.Context, tenantID string) ([]Webhook, error) {
	rows, err := m.pool.Query(ctx, `SELECT id::text, url, topics, active, created_at FROM webhooks
		WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Webhook, error) {
		var w Webhook
		err := r.Scan(&w.ID, &w.URL, &w.Topics, &w.Active, &w.CreatedAt)
		return w, err
	})
}

// Delete deactivates a webhook (history is kept).
func (m *Manager) Delete(ctx context.Context, actor audit.Actor, id string) error {
	return store.InTx(ctx, m.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE webhooks SET active = false WHERE id = $1 AND tenant_id = $2 AND active`, id, actor.TenantID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.NotFound("active webhook")
		}
		return audit.Write(ctx, tx, actor, "webhook.delete", "webhook", id, nil, nil)
	})
}

// Deliveries lists recent deliveries for a webhook.
func (m *Manager) Deliveries(ctx context.Context, tenantID, id string, limit int) ([]Delivery, error) {
	var ok bool
	if err := m.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM webhooks WHERE id = $1 AND tenant_id = $2)`, id, tenantID).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.NotFound("webhook")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := m.pool.Query(ctx, `
		SELECT id, payload->'event'->>'type', attempts,
		       CASE WHEN done_at IS NOT NULL THEN 'delivered' WHEN dead_at IS NOT NULL THEN 'dead' ELSE 'pending' END,
		       last_error, next_run_at, done_at, created_at
		FROM outbox WHERE kind = 'webhook' AND payload->>'webhookId' = $1
		ORDER BY id DESC LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Delivery, error) {
		var d Delivery
		err := r.Scan(&d.ID, &d.EventType, &d.Attempts, &d.State, &d.LastError, &d.NextRunAt, &d.DoneAt, &d.CreatedAt)
		return d, err
	})
}

// Stats summarises the outbox for health checks.
type Stats struct {
	Pending             int64   `json:"pending"`
	Dead                int64   `json:"dead"`
	OldestPendingAgeSec float64 `json:"oldestPendingAgeSeconds"`
}

// GetStats returns outbox statistics.
func GetStats(ctx context.Context, q store.Querier) (Stats, error) {
	var s Stats
	err := q.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE done_at IS NULL AND dead_at IS NULL),
		       count(*) FILTER (WHERE dead_at IS NOT NULL),
		       COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE done_at IS NULL AND dead_at IS NULL)), 0)::float8
		FROM outbox`).Scan(&s.Pending, &s.Dead, &s.OldestPendingAgeSec)
	return s, err
}

// ---- worker ----------------------------------------------------------------

// Worker delivers outbox rows.
type Worker struct {
	pool      *pgxpool.Pool
	key       []byte
	client    *http.Client
	purgeURL  string
	purgeTok  string
	interval  time.Duration
	log       *slog.Logger
	OnResult  func(kind, result string) // metrics hook
	OnBacklog func(Stats)
	maxTries  int
}

// WorkerConfig configures a Worker.
type WorkerConfig struct {
	EncKey       []byte
	PurgeURL     string
	PurgeToken   string
	Interval     time.Duration
	AllowPrivate bool
}

// NewWorker creates a worker. Outbound connections to private addresses are refused
// unless AllowPrivate is set (development only), which blocks SSRF via webhook URLs.
func NewWorker(pool *pgxpool.Pool, cfg WorkerConfig, log *slog.Logger) *Worker {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !cfg.AllowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !publicIP(ip) {
				return fmt.Errorf("refusing to connect to non-public address %s", host)
			}
			return nil
		}
	}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second, MaxIdleConns: 20}
	return &Worker{
		pool: pool, key: cfg.EncKey, purgeURL: cfg.PurgeURL, purgeTok: cfg.PurgeToken,
		interval: cfg.Interval, log: log, maxTries: 15,
		client: &http.Client{Timeout: 10 * time.Second, Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

// Run processes the outbox until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("outbox worker started", "interval", w.interval.String())
	t := time.NewTicker(w.interval)
	defer t.Stop()
	statsEvery := time.NewTicker(15 * time.Second)
	defer statsEvery.Stop()
	for {
		for {
			n, err := w.RunOnce(ctx, 20)
			if err != nil && ctx.Err() == nil {
				w.log.Error("outbox batch failed", "err", err)
			}
			if n == 0 || err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			w.log.Info("outbox worker stopped")
			return
		case <-statsEvery.C:
			if w.OnBacklog != nil {
				if s, err := GetStats(ctx, w.pool); err == nil {
					w.OnBacklog(s)
				}
			}
		case <-t.C:
		}
	}
}

type job struct {
	id       int64
	kind     string
	payload  []byte
	attempts int
}

// RunOnce claims up to limit due rows with a lease, delivers them, and records results.
func (w *Worker) RunOnce(ctx context.Context, limit int) (int, error) {
	rows, err := w.pool.Query(ctx, `
		UPDATE outbox SET attempts = attempts + 1, next_run_at = now() + interval '5 minutes'
		WHERE id IN (
			SELECT id FROM outbox
			WHERE done_at IS NULL AND dead_at IS NULL AND next_run_at <= now()
			ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING id, kind, payload, attempts`, limit)
	if err != nil {
		return 0, err
	}
	jobs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (job, error) {
		var j job
		err := r.Scan(&j.id, &j.kind, &j.payload, &j.attempts)
		return j, err
	})
	if err != nil {
		return 0, err
	}
	for _, j := range jobs {
		derr := w.deliver(ctx, j)
		result := "delivered"
		switch {
		case derr == nil:
			_, err = w.pool.Exec(ctx, `UPDATE outbox SET done_at = now(), last_error = NULL WHERE id = $1`, j.id)
		case j.attempts >= w.maxTries:
			result = "dead"
			w.log.Error("outbox delivery dead-lettered", "id", j.id, "kind", j.kind, "attempts", j.attempts, "err", derr)
			_, err = w.pool.Exec(ctx, `UPDATE outbox SET dead_at = now(), last_error = $2 WHERE id = $1`, j.id, derr.Error())
		default:
			result = "retry"
			w.log.Warn("outbox delivery failed, will retry", "id", j.id, "kind", j.kind, "attempts", j.attempts, "err", derr)
			_, err = w.pool.Exec(ctx, `UPDATE outbox SET next_run_at = now() + ($2::int * interval '1 second'), last_error = $3 WHERE id = $1`,
				j.id, int(backoff(j.attempts).Seconds()), derr.Error())
		}
		if w.OnResult != nil {
			w.OnResult(j.kind, result)
		}
		if err != nil {
			return len(jobs), err
		}
	}
	return len(jobs), nil
}

// backoff grows 30s, 1m, 2m ... capped at 6h, so 15 attempts span a little over 24 hours.
func backoff(attempt int) time.Duration {
	d := 30 * time.Second << min(attempt-1, 10)
	return min(d, 6*time.Hour)
}

func (w *Worker) deliver(ctx context.Context, j job) error {
	switch j.kind {
	case "webhook":
		var p struct {
			WebhookID string          `json:"webhookId"`
			Event     json.RawMessage `json:"event"`
		}
		if err := json.Unmarshal(j.payload, &p); err != nil {
			return err
		}
		var target string
		var enc []byte
		var active bool
		err := w.pool.QueryRow(ctx, `SELECT url, secret_enc, active FROM webhooks WHERE id = $1`, p.WebhookID).Scan(&target, &enc, &active)
		if store.IsNoRows(err) || (err == nil && !active) {
			return nil // webhook removed: drop silently
		}
		if err != nil {
			return err
		}
		secret, err := decrypt(w.key, enc)
		if err != nil {
			return fmt.Errorf("decrypt webhook secret: %w", err)
		}
		var ev Event
		_ = json.Unmarshal(p.Event, &ev)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(p.Event))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "bs-calendar-webhooks/1")
		req.Header.Set("X-Calendar-Event", ev.Type)
		req.Header.Set("X-Calendar-Delivery", strconv.FormatInt(j.id, 10))
		req.Header.Set("X-Calendar-Signature", Sign(secret, time.Now().Unix(), p.Event))
		return w.send(req)
	case "cdn.purge":
		if w.purgeURL == "" {
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.purgeURL, bytes.NewReader(j.payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if w.purgeTok != "" {
			req.Header.Set("Authorization", "Bearer "+w.purgeTok)
		}
		return w.send(req)
	}
	return fmt.Errorf("unknown outbox kind %q", j.kind)
}

func (w *Worker) send(req *http.Request) error {
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
