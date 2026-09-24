package outbox

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"type":"events.changed"}`)
	now := time.Unix(1_790_000_000, 0)
	h := Sign("whsec_test", now.Unix(), body)
	if !strings.HasPrefix(h, "t=1790000000,v1=") {
		t.Fatalf("header format: %s", h)
	}
	if !Verify("whsec_test", h, body, now.Add(time.Minute), 5*time.Minute) {
		t.Fatal("valid signature rejected")
	}
	if Verify("whsec_other", h, body, now, 5*time.Minute) {
		t.Fatal("wrong secret accepted")
	}
	if Verify("whsec_test", h, []byte(`{"type":"x"}`), now, 5*time.Minute) {
		t.Fatal("tampered body accepted")
	}
	if Verify("whsec_test", h, body, now.Add(10*time.Minute), 5*time.Minute) {
		t.Fatal("replayed (old) signature accepted")
	}
	if Verify("whsec_test", "garbage", body, now, 5*time.Minute) {
		t.Fatal("garbage header accepted")
	}
}

func TestSecretEncryption(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	enc, err := encrypt(key, "whsec_abc")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("whsec_abc")) {
		t.Fatal("secret stored in clear text")
	}
	if got, err := decrypt(key, enc); err != nil || got != "whsec_abc" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if _, err := decrypt(bytes.Repeat([]byte{2}, 32), enc); err == nil {
		t.Fatal("wrong key must fail")
	}
	enc2, _ := encrypt(key, "whsec_abc")
	if bytes.Equal(enc, enc2) {
		t.Fatal("nonces must differ")
	}
}

func TestBackoff(t *testing.T) {
	prev := time.Duration(0)
	total := time.Duration(0)
	for a := 1; a <= 15; a++ {
		d := backoff(a)
		if d < prev || d > 6*time.Hour {
			t.Fatalf("attempt %d: %s", a, d)
		}
		prev = d
		total += d
	}
	if total < 24*time.Hour || total > 72*time.Hour {
		t.Fatalf("15 attempts should span roughly a day or two, got %s", total)
	}
}

func TestPublicIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "10.0.0.1": false, "192.168.1.5": false, "127.0.0.1": false,
		"169.254.169.254": false, "::1": false, "fc00::1": false, "2001:4860:4860::8888": true, "::ffff:10.0.0.1": false,
	} {
		if got := publicIP(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicIP(%s) = %v, want %v", addr, got, want)
		}
	}
}
