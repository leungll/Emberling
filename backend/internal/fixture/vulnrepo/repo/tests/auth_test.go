// Package tests is the acceptance suite of the token service. It lives outside the
// packages it tests on purpose: a fix may change internal/auth, never these tests.
package tests

import (
	"errors"
	"strings"
	"testing"
	"time"

	"example.com/tokenservice/internal/auth"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

const testAudience = "tokenservice"

func newIssuer(t *testing.T, audience string, ttl time.Duration) *auth.Issuer {
	t.Helper()
	issuer, err := auth.NewIssuer(testSecret, audience, ttl)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return issuer
}

func TestIssueAndVerify_RoundTrip_ReturnsSubjectAndExpiry(t *testing.T) {
	issuer := newIssuer(t, testAudience, time.Hour)
	now := time.Now().Truncate(time.Second)
	token, err := issuer.Issue("alice", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if strings.Count(token, ".") != 2 {
		t.Fatalf("token %q is not a three-part JWT", token)
	}
	identity, err := issuer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if identity.Subject != "alice" {
		t.Errorf("subject = %q, want alice", identity.Subject)
	}
	if want := now.Add(time.Hour); !identity.ExpiresAt.Equal(want) {
		t.Errorf("expiresAt = %v, want %v", identity.ExpiresAt, want)
	}
}

func TestVerify_ExpiredToken_ReturnsErrExpired(t *testing.T) {
	issuer := newIssuer(t, testAudience, time.Minute)
	token, err := issuer.Issue("alice", time.Now().Add(-2*time.Minute))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := issuer.Verify(token); !errors.Is(err, auth.ErrExpired) {
		t.Fatalf("Verify error = %v, want ErrExpired", err)
	}
}

func TestVerify_TokenForAnotherAudience_ReturnsErrAudience(t *testing.T) {
	other := newIssuer(t, "billing", time.Hour)
	token, err := other.Issue("alice", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	issuer := newIssuer(t, testAudience, time.Hour)
	if _, err := issuer.Verify(token); !errors.Is(err, auth.ErrAudience) {
		t.Fatalf("Verify error = %v, want ErrAudience", err)
	}
}

func TestVerify_TamperedToken_ReturnsErrInvalid(t *testing.T) {
	issuer := newIssuer(t, testAudience, time.Hour)
	token, err := issuer.Issue("alice", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	parts := strings.Split(token, ".")
	parts[2] = strings.Repeat("A", len(parts[2]))
	if _, err := issuer.Verify(strings.Join(parts, ".")); !errors.Is(err, auth.ErrInvalid) {
		t.Fatalf("Verify error = %v, want ErrInvalid", err)
	}
}

func TestVerify_UnsignedToken_ReturnsErrInvalid(t *testing.T) {
	issuer := newIssuer(t, testAudience, time.Hour)
	// A token whose header declares "none" as its algorithm must never verify.
	unsigned := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJhbGljZSIsImF1ZCI6InRva2Vuc2VydmljZSJ9."
	if _, err := issuer.Verify(unsigned); !errors.Is(err, auth.ErrInvalid) {
		t.Fatalf("Verify error = %v, want ErrInvalid", err)
	}
}

func TestNewIssuer_ShortSecret_IsRejected(t *testing.T) {
	if _, err := auth.NewIssuer([]byte("short"), testAudience, time.Hour); err == nil {
		t.Fatal("NewIssuer accepted a short secret")
	}
}
