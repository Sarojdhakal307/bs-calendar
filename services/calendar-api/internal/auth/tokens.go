package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Claims are the JWT access token claims. The token is short-lived; every admin request
// also checks the session in the database, so revocation and role changes apply at once.
type Claims struct {
	SessionID string `json:"sid"`
	TenantID  string `json:"tid"`
	Role      string `json:"role"`
	jwt.RegisteredClaims
}

// TokenManager signs and verifies access tokens (HS256).
type TokenManager struct {
	key    []byte
	issuer string
	ttl    time.Duration
}

// NewTokenManager creates a token manager.
func NewTokenManager(key []byte, issuer string, ttl time.Duration) *TokenManager {
	return &TokenManager{key: key, issuer: issuer, ttl: ttl}
}

// TTL returns the access token lifetime.
func (m *TokenManager) TTL() time.Duration { return m.ttl }

// Issue creates a signed access token.
func (m *TokenManager) Issue(userID, tenantID, role, sessionID string, now time.Time) (string, time.Time, error) {
	exp := now.Add(m.ttl)
	c := Claims{
		SessionID: sessionID,
		TenantID:  tenantID,
		Role:      role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(m.key)
	return s, exp, err
}

// Parse verifies a token and returns its claims.
func (m *TokenManager) Parse(tok string) (*Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(tok, &c, func(*jwt.Token) (any, error) { return m.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(m.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return nil, err
	}
	if c.Subject == "" || c.SessionID == "" {
		return nil, errors.New("token missing subject or session")
	}
	return &c, nil
}

// randomToken returns prefix + 32 random bytes in URL-safe base64.
func randomToken(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// HashToken hashes a high-entropy token (refresh tokens, API keys). bcrypt is not needed
// because these tokens are random, not human-chosen.
func HashToken(tok string) []byte {
	s := sha256.Sum256([]byte(tok))
	return s[:]
}

const bcryptCost = 12

// HashPassword hashes a human password with bcrypt.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(b), err
}

// dummyHash lets failed logins for unknown users take the same time as for known users.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equaliser-not-a-password"), bcryptCost)

// CheckPassword compares a password with a bcrypt hash in constant time.
func CheckPassword(hash, pw string) bool {
	if hash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// ValidatePassword enforces the password policy.
func ValidatePassword(pw string) error {
	switch {
	case len(pw) < 12:
		return errors.New("must be at least 12 characters")
	case len(pw) > 72:
		return errors.New("must be at most 72 bytes") // bcrypt limit
	case strings.TrimSpace(pw) != pw:
		return errors.New("must not start or end with spaces")
	}
	return nil
}
