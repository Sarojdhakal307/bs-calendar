package auth

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"bscalendar/services/calendar-api/internal/apperr"
	"bscalendar/services/calendar-api/internal/audit"
	"bscalendar/services/calendar-api/internal/store"
)

// Admin is an authenticated admin user for one request.
type Admin struct {
	UserID    string
	TenantID  string
	Email     string
	Role      string
	SessionID string
}

// Actor converts the admin to an audit actor.
func (a Admin) Actor(ip, requestID string) audit.Actor {
	return audit.Actor{UserID: a.UserID, TenantID: a.TenantID, Email: a.Email, Role: a.Role, IP: ip, RequestID: requestID}
}

// Client is an API client identified by an API key.
type Client struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"-"`
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	KeyPrefix      string     `json:"keyPrefix"`
	AllowedOrigins []string   `json:"allowedOrigins"`
	RatePerMin     int        `json:"ratePerMin"`
	Revoked        bool       `json:"revoked"`
	LastUsedAt     *time.Time `json:"lastUsedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// User is an admin account.
type User struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Disabled    bool       `json:"disabled"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// TokenPair is returned by login and refresh.
type TokenPair struct {
	AccessToken      string    `json:"accessToken"`
	TokenType        string    `json:"tokenType"`
	ExpiresIn        int       `json:"expiresIn"`
	RefreshToken     string    `json:"refreshToken"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
	User             User      `json:"user"`
}

// Service implements authentication and access management.
type Service struct {
	pool       *pgxpool.Pool
	tokens     *TokenManager
	refreshTTL time.Duration
	now        func() time.Time

	keyMu    sync.Mutex
	keyCache map[string]keyCacheEntry
}

type keyCacheEntry struct {
	client   Client
	err      error
	expires  time.Time
	touchDue time.Time
}

// NewService creates the auth service.
func NewService(pool *pgxpool.Pool, tokens *TokenManager, refreshTTL time.Duration) *Service {
	return &Service{pool: pool, tokens: tokens, refreshTTL: refreshTTL, now: time.Now, keyCache: map[string]keyCacheEntry{}}
}

var errBadLogin = apperr.Unauthorized(apperr.CodeInvalidCredentials, "Email or password is incorrect.")

// Login checks a password and starts a session.
func (s *Service) Login(ctx context.Context, email, password, ip, userAgent string) (TokenPair, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var u User
	var tenantID string
	var hash *string
	var disabledAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, email, role, password_hash, disabled_at, last_login_at, created_at
		FROM admin_users WHERE email = $1`, email).
		Scan(&u.ID, &tenantID, &u.Email, &u.Role, &hash, &disabledAt, &u.LastLoginAt, &u.CreatedAt)
	if err != nil && !store.IsNoRows(err) {
		return TokenPair{}, err
	}
	h := ""
	if hash != nil {
		h = *hash
	}
	if !CheckPassword(h, password) || err != nil || disabledAt != nil {
		return TokenPair{}, errBadLogin
	}
	refresh := randomToken("rt_")
	now := s.now()
	var sid string
	err = store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO admin_sessions (user_id, refresh_hash, expires_at, ip, user_agent)
			VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,'')) RETURNING id::text`,
			u.ID, HashToken(refresh), now.Add(s.refreshTTL), ip, truncate(userAgent, 300)).Scan(&sid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE admin_users SET last_login_at = now() WHERE id = $1`, u.ID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Actor{UserID: u.ID, TenantID: tenantID, IP: ip}, "auth.login", "admin_user", u.ID, nil, nil)
	})
	if err != nil {
		return TokenPair{}, err
	}
	return s.pair(u, tenantID, sid, refresh, now)
}

func (s *Service) pair(u User, tenantID, sid, refresh string, now time.Time) (TokenPair, error) {
	access, exp, err := s.tokens.Issue(u.ID, tenantID, u.Role, sid, now)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{
		AccessToken:      access,
		TokenType:        "Bearer",
		ExpiresIn:        int(exp.Sub(now).Seconds()),
		RefreshToken:     refresh,
		RefreshExpiresAt: now.Add(s.refreshTTL).UTC(),
		User:             u,
	}, nil
}

// Refresh rotates a refresh token. Reusing an old token revokes the session (theft detection).
func (s *Service) Refresh(ctx context.Context, refreshToken, ip, userAgent string) (TokenPair, error) {
	h := HashToken(refreshToken)
	now := s.now()
	next := randomToken("rt_")
	var (
		u        User
		tenantID string
		sid      string
		reused   bool
	)
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var expires time.Time
		var revoked, disabled *time.Time
		err := tx.QueryRow(ctx, `
			SELECT s.id::text, s.expires_at, s.revoked_at, u.id::text, u.tenant_id::text, u.email, u.role,
			       u.disabled_at, u.last_login_at, u.created_at
			FROM admin_sessions s JOIN admin_users u ON u.id = s.user_id
			WHERE s.refresh_hash = $1 FOR UPDATE OF s`, h).
			Scan(&sid, &expires, &revoked, &u.ID, &tenantID, &u.Email, &u.Role, &disabled, &u.LastLoginAt, &u.CreatedAt)
		if store.IsNoRows(err) {
			// Not current: was it a previous (already rotated) token? Then someone replayed it.
			tag, err := tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at = now()
				WHERE prev_refresh_hash = $1 AND revoked_at IS NULL`, h)
			if err != nil {
				return err
			}
			reused = tag.RowsAffected() > 0
			return nil
		}
		if err != nil {
			return err
		}
		if revoked != nil || disabled != nil || now.After(expires) {
			sid = ""
			return nil
		}
		_, err = tx.Exec(ctx, `
			UPDATE admin_sessions SET prev_refresh_hash = refresh_hash, refresh_hash = $2,
			       expires_at = $3, last_used_at = now(), ip = COALESCE(NULLIF($4,''), ip),
			       user_agent = COALESCE(NULLIF($5,''), user_agent)
			WHERE id = $1`, sid, HashToken(next), now.Add(s.refreshTTL), ip, truncate(userAgent, 300))
		return err
	})
	if err != nil {
		return TokenPair{}, err
	}
	if reused {
		return TokenPair{}, apperr.Unauthorized(apperr.CodeTokenReused,
			"This refresh token was already used. The session has been revoked; sign in again.")
	}
	if sid == "" || u.ID == "" {
		return TokenPair{}, apperr.Unauthorized(apperr.CodeUnauthorized, "Refresh token is invalid or expired.")
	}
	return s.pair(u, tenantID, sid, next, now)
}

// Logout revokes a session.
func (s *Service) Logout(ctx context.Context, a Admin, ip, requestID string) error {
	return store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, a.SessionID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, a.Actor(ip, requestID), "auth.logout", "admin_user", a.UserID, nil, nil)
	})
}

// Authenticate verifies a bearer token and loads the live session, so revoked sessions,
// disabled users and role changes take effect immediately.
func (s *Service) Authenticate(ctx context.Context, bearer string) (Admin, error) {
	c, err := s.tokens.Parse(bearer)
	if err != nil {
		return Admin{}, apperr.Unauthorized(apperr.CodeUnauthorized, "Access token is invalid or expired.")
	}
	var a Admin
	var revoked, disabled *time.Time
	var expires time.Time
	err = s.pool.QueryRow(ctx, `
		SELECT u.id::text, u.tenant_id::text, u.email, u.role, s.id::text, s.revoked_at, s.expires_at, u.disabled_at
		FROM admin_sessions s JOIN admin_users u ON u.id = s.user_id
		WHERE s.id = $1 AND u.id = $2`, c.SessionID, c.Subject).
		Scan(&a.UserID, &a.TenantID, &a.Email, &a.Role, &a.SessionID, &revoked, &expires, &disabled)
	if store.IsNoRows(err) {
		return Admin{}, apperr.Unauthorized(apperr.CodeUnauthorized, "Session not found.")
	}
	if err != nil {
		return Admin{}, err
	}
	if revoked != nil || disabled != nil || s.now().After(expires) {
		return Admin{}, apperr.Unauthorized(apperr.CodeUnauthorized, "Session is no longer valid.")
	}
	return a, nil
}

// LookupKey resolves an API key. Results are cached for a minute (errors for 10 seconds).
func (s *Service) LookupKey(ctx context.Context, key string) (Client, error) {
	now := s.now()
	s.keyMu.Lock()
	e, ok := s.keyCache[key]
	s.keyMu.Unlock()
	if ok && now.Before(e.expires) {
		if e.err == nil && now.After(e.touchDue) {
			s.touchKey(e.client.ID)
			s.keyMu.Lock()
			e.touchDue = now.Add(5 * time.Minute)
			s.keyCache[key] = e
			s.keyMu.Unlock()
		}
		return e.client, e.err
	}
	var c Client
	var revoked *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, name, kind, key_prefix, allowed_origins, rate_per_min, revoked_at, last_used_at, created_at
		FROM api_clients WHERE key_hash = $1`, HashToken(key)).
		Scan(&c.ID, &c.TenantID, &c.Name, &c.Kind, &c.KeyPrefix, &c.AllowedOrigins, &c.RatePerMin, &revoked, &c.LastUsedAt, &c.CreatedAt)
	entry := keyCacheEntry{client: c, expires: now.Add(time.Minute), touchDue: now.Add(5 * time.Minute)}
	switch {
	case store.IsNoRows(err) || (err == nil && revoked != nil):
		entry = keyCacheEntry{err: apperr.Unauthorized(apperr.CodeInvalidAPIKey, "API key is invalid or revoked."), expires: now.Add(10 * time.Second)}
	case err != nil:
		return Client{}, err // do not cache transient failures
	default:
		s.touchKey(c.ID)
	}
	s.keyMu.Lock()
	if len(s.keyCache) > 10_000 {
		s.keyCache = map[string]keyCacheEntry{}
	}
	s.keyCache[key] = entry
	s.keyMu.Unlock()
	return entry.client, entry.err
}

func (s *Service) touchKey(id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.pool.Exec(ctx, `UPDATE api_clients SET last_used_at = now() WHERE id = $1`, id)
	}()
}

func (s *Service) forgetKeys() {
	s.keyMu.Lock()
	s.keyCache = map[string]keyCacheEntry{}
	s.keyMu.Unlock()
}

// ---- users -------------------------------------------------------------------

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// UserInput creates a user.
type UserInput struct {
	Email    string `json:"email"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

// UserPatch updates a user. Nil fields are unchanged.
type UserPatch struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
	Password *string `json:"password"`
}

// ListUsers returns the tenant's admin users.
func (s *Service) ListUsers(ctx context.Context, tenantID string) ([]User, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, email, role, disabled_at IS NOT NULL, last_login_at, created_at
		FROM admin_users WHERE tenant_id = $1 ORDER BY email`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		var u User
		err := r.Scan(&u.ID, &u.Email, &u.Role, &u.Disabled, &u.LastLoginAt, &u.CreatedAt)
		return u, err
	})
}

// CreateUser adds an admin user.
func (s *Service) CreateUser(ctx context.Context, actor audit.Actor, in UserInput) (User, error) {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	var fe []apperr.FieldError
	if !emailRe.MatchString(in.Email) {
		fe = append(fe, apperr.FieldError{Field: "email", Message: "must be a valid email address"})
	}
	if !ValidRole(in.Role) {
		fe = append(fe, apperr.FieldError{Field: "role", Message: "must be one of " + strings.Join(Roles, ", ")})
	}
	if err := ValidatePassword(in.Password); err != nil {
		fe = append(fe, apperr.FieldError{Field: "password", Message: err.Error()})
	}
	if len(fe) > 0 {
		return User{}, apperr.Validation(fe...)
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return User{}, err
	}
	var u User
	err = store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO admin_users (tenant_id, email, role, password_hash) VALUES ($1, $2, $3, $4)
			RETURNING id::text, email, role, false, last_login_at, created_at`,
			actor.TenantID, in.Email, in.Role, hash).Scan(&u.ID, &u.Email, &u.Role, &u.Disabled, &u.LastLoginAt, &u.CreatedAt)
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, actor, "user.create", "admin_user", u.ID, nil, u)
	})
	if err != nil {
		if ae := apperr.From(err); ae.Code == apperr.CodeConflict {
			return User{}, apperr.Conflict(apperr.CodeConflict, "A user with this email already exists.")
		}
	}
	return u, err
}

// UpdateUser changes role, disabled state or password. Users cannot lock themselves out,
// and the last active super admin cannot be demoted or disabled.
func (s *Service) UpdateUser(ctx context.Context, actor audit.Actor, id string, p UserPatch) (User, error) {
	if p.Role != nil && !ValidRole(*p.Role) {
		return User{}, apperr.Validation(apperr.FieldError{Field: "role", Message: "must be one of " + strings.Join(Roles, ", ")})
	}
	if p.Password != nil {
		if err := ValidatePassword(*p.Password); err != nil {
			return User{}, apperr.Validation(apperr.FieldError{Field: "password", Message: err.Error()})
		}
	}
	if id == actor.UserID && ((p.Disabled != nil && *p.Disabled) || (p.Role != nil && *p.Role != actor.Role)) {
		return User{}, apperr.Forbidden(apperr.CodeForbidden, "You cannot disable yourself or change your own role.")
	}
	var before, after User
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id::text, email, role, disabled_at IS NOT NULL, last_login_at, created_at
			FROM admin_users WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, actor.TenantID).
			Scan(&before.ID, &before.Email, &before.Role, &before.Disabled, &before.LastLoginAt, &before.CreatedAt)
		if store.IsNoRows(err) {
			return apperr.NotFound("user")
		}
		if err != nil {
			return err
		}
		demoting := before.Role == RoleSuperAdmin && ((p.Role != nil && *p.Role != RoleSuperAdmin) || (p.Disabled != nil && *p.Disabled))
		if demoting {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM admin_users
				WHERE tenant_id = $1 AND role = 'super_admin' AND disabled_at IS NULL AND id <> $2`, actor.TenantID, id).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return apperr.Conflict(apperr.CodeConflict, "At least one active super admin must remain.")
			}
		}
		var hash *string
		if p.Password != nil {
			h, err := HashPassword(*p.Password)
			if err != nil {
				return err
			}
			hash = &h
		}
		err = tx.QueryRow(ctx, `
			UPDATE admin_users SET
			  role = COALESCE($2, role),
			  disabled_at = CASE WHEN $3::boolean IS NULL THEN disabled_at WHEN $3 THEN COALESCE(disabled_at, now()) ELSE NULL END,
			  password_hash = COALESCE($4, password_hash),
			  updated_at = now()
			WHERE id = $1
			RETURNING id::text, email, role, disabled_at IS NOT NULL, last_login_at, created_at`,
			id, p.Role, p.Disabled, hash).Scan(&after.ID, &after.Email, &after.Role, &after.Disabled, &after.LastLoginAt, &after.CreatedAt)
		if err != nil {
			return err
		}
		if (p.Disabled != nil && *p.Disabled) || p.Password != nil {
			if _, err := tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, id); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, actor, "user.update", "admin_user", id, before, after)
	})
	return after, err
}

// ---- API clients -----------------------------------------------------------

// ClientInput creates an API client.
type ClientInput struct {
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	AllowedOrigins []string `json:"allowedOrigins"`
	RatePerMin     *int     `json:"ratePerMin"`
}

// ListClients returns API clients (never their keys).
func (s *Service) ListClients(ctx context.Context, tenantID string) ([]Client, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, tenant_id::text, name, kind, key_prefix, allowed_origins, rate_per_min,
		       revoked_at IS NOT NULL, last_used_at, created_at
		FROM api_clients WHERE tenant_id = $1 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Client, error) {
		var c Client
		err := r.Scan(&c.ID, &c.TenantID, &c.Name, &c.Kind, &c.KeyPrefix, &c.AllowedOrigins, &c.RatePerMin, &c.Revoked, &c.LastUsedAt, &c.CreatedAt)
		return c, err
	})
}

// CreateClient issues a new key. The full key is returned once and never stored.
func (s *Service) CreateClient(ctx context.Context, actor audit.Actor, in ClientInput) (Client, string, error) {
	var fe []apperr.FieldError
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		fe = append(fe, apperr.FieldError{Field: "name", Message: "required, at most 100 characters"})
	}
	if in.Kind != "public" && in.Kind != "server" {
		fe = append(fe, apperr.FieldError{Field: "kind", Message: "must be public or server"})
	}
	for i, o := range in.AllowedOrigins {
		if u, err := url.Parse(o); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" {
			fe = append(fe, apperr.FieldError{Field: "allowedOrigins[" + itoa(i) + "]", Message: "must be an origin like https://example.com"})
		}
	}
	rate := 600
	if in.Kind == "server" {
		rate = 6000
	}
	if in.RatePerMin != nil {
		if *in.RatePerMin < 1 || *in.RatePerMin > 1_000_000 {
			fe = append(fe, apperr.FieldError{Field: "ratePerMin", Message: "must be 1-1000000"})
		}
		rate = *in.RatePerMin
	}
	if len(fe) > 0 {
		return Client{}, "", apperr.Validation(fe...)
	}
	if in.AllowedOrigins == nil {
		in.AllowedOrigins = []string{}
	}
	prefix := "pk_"
	if in.Kind == "server" {
		prefix = "sk_"
	}
	key := randomToken(prefix)
	c, err := s.insertClient(ctx, actor, in.Name, in.Kind, key, in.AllowedOrigins, rate)
	return c, key, err
}

// EnsureClient inserts a client with a known key if it does not exist (used by bootstrap).
func (s *Service) EnsureClient(ctx context.Context, actor audit.Actor, name, kind, key string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM api_clients WHERE key_hash = $1)`, HashToken(key)).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	rate := 600
	if kind == "server" {
		rate = 6000
	}
	_, err := s.insertClient(ctx, actor, name, kind, key, []string{}, rate)
	return err == nil, err
}

func (s *Service) insertClient(ctx context.Context, actor audit.Actor, name, kind, key string, origins []string, rate int) (Client, error) {
	var c Client
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO api_clients (tenant_id, name, kind, key_prefix, key_hash, allowed_origins, rate_per_min, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8,'')::uuid)
			RETURNING id::text, tenant_id::text, name, kind, key_prefix, allowed_origins, rate_per_min, false, last_used_at, created_at`,
			actor.TenantID, name, kind, key[:min(len(key), 11)], HashToken(key), origins, rate, actor.UserID).
			Scan(&c.ID, &c.TenantID, &c.Name, &c.Kind, &c.KeyPrefix, &c.AllowedOrigins, &c.RatePerMin, &c.Revoked, &c.LastUsedAt, &c.CreatedAt)
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, actor, "api_client.create", "api_client", c.ID, nil, c)
	})
	return c, err
}

// RevokeClient revokes a key immediately (the local cache is cleared; other replicas within a minute).
func (s *Service) RevokeClient(ctx context.Context, actor audit.Actor, id string) error {
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE api_clients SET revoked_at = now()
			WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL`, id, actor.TenantID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.NotFound("active API client")
		}
		return audit.Write(ctx, tx, actor, "api_client.revoke", "api_client", id, nil, nil)
	})
	if err == nil {
		s.forgetKeys()
	}
	return err
}

// ---- bootstrap helpers -----------------------------------------------------

// EnsureSuperAdmin creates the first super admin if no user with that email exists.
func (s *Service) EnsureSuperAdmin(ctx context.Context, tenantID, email, password string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM admin_users WHERE email = $1)`, email).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if !emailRe.MatchString(email) {
		return false, errors.New("BOOTSTRAP_ADMIN_EMAIL is not a valid email")
	}
	if err := ValidatePassword(password); err != nil {
		return false, errors.New("BOOTSTRAP_ADMIN_PASSWORD " + err.Error())
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO admin_users (tenant_id, email, role, password_hash) VALUES ($1, $2, 'super_admin', $3)`,
		tenantID, email, hash)
	return err == nil, err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}
