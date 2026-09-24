package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestTokenRoundTripAndRejections(t *testing.T) {
	key := []byte(strings.Repeat("k", 48))
	m := NewTokenManager(key, "calendar-api", 15*time.Minute)
	now := time.Now()
	tok, exp, err := m.Issue("user-1", "tenant-1", RoleEditor, "session-1", now)
	if err != nil || exp.Sub(now) != 15*time.Minute {
		t.Fatalf("issue: %v %v", err, exp)
	}
	c, err := m.Parse(tok)
	if err != nil || c.Subject != "user-1" || c.SessionID != "session-1" || c.Role != RoleEditor {
		t.Fatalf("parse: %+v %v", c, err)
	}
	other := NewTokenManager([]byte(strings.Repeat("x", 48)), "calendar-api", time.Minute)
	if _, err := other.Parse(tok); err == nil {
		t.Fatal("token signed with another key accepted")
	}
	wrongIssuer := NewTokenManager(key, "someone-else", time.Minute)
	if _, err := wrongIssuer.Parse(tok); err == nil {
		t.Fatal("token from another issuer accepted")
	}
	old, _, _ := m.Issue("user-1", "tenant-1", RoleEditor, "session-1", now.Add(-time.Hour))
	if _, err := m.Parse(old); err == nil {
		t.Fatal("expired token accepted")
	}
	none := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{SessionID: "s", RegisteredClaims: jwt.RegisteredClaims{
		Subject: "u", Issuer: "calendar-api", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}})
	unsigned, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := m.Parse(unsigned); err == nil {
		t.Fatal(`"alg": "none" token accepted`)
	}
}

func TestPasswords(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse battery") || CheckPassword(h, "wrong") || CheckPassword("", "anything") {
		t.Fatal("password check")
	}
	for pw, ok := range map[string]bool{"short": false, "long enough pass": true, " padded password ": false, strings.Repeat("a", 73): false} {
		if (ValidatePassword(pw) == nil) != ok {
			t.Errorf("ValidatePassword(%q)", pw)
		}
	}
}

func TestRoleMatrix(t *testing.T) {
	cases := []struct {
		role string
		perm Permission
		want bool
	}{
		{RoleViewer, PermRead, true}, {RoleViewer, PermEventsWrite, false},
		{RoleEditor, PermEventsWrite, true}, {RoleEditor, PermYearsApprove, false},
		{RoleDesigner, PermConfigPublish, true}, {RoleDesigner, PermEventsWrite, false},
		{RoleCalendarAdmin, PermYearsApprove, true}, {RoleCalendarAdmin, PermConfigDraft, false},
		{RoleSuperAdmin, PermPlatformManage, true}, {"hacker", PermRead, false},
	}
	for _, c := range cases {
		if Can(c.role, c.perm) != c.want {
			t.Errorf("Can(%s, %s) != %v", c.role, c.perm, c.want)
		}
	}
}

func TestHashTokenIsStable(t *testing.T) {
	if string(HashToken("a")) != string(HashToken("a")) || string(HashToken("a")) == string(HashToken("b")) {
		t.Fatal("hash")
	}
	k := randomToken("pk_")
	if !strings.HasPrefix(k, "pk_") || len(k) < 40 {
		t.Fatalf("key %q", k)
	}
}
