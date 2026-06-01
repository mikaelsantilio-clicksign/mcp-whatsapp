package oauth

import (
	"strings"
	"testing"
	"time"
)

func TestPKCE_RoundTrip(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Verifier) < 32 {
		t.Fatalf("verifier too short: %d", len(p.Verifier))
	}
	if len(p.Challenge) < 32 {
		t.Fatalf("challenge too short: %d", len(p.Challenge))
	}
	if strings.ContainsAny(p.Challenge, "=") {
		t.Fatalf("challenge must be base64 raw url (no padding): %s", p.Challenge)
	}
}

func TestStateSigner_NewVerify(t *testing.T) {
	s := NewStateSigner("0123456789abcdef0123456789abcdef")
	state, payload, err := s.New("+5511999", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Phone != "+5511999" {
		t.Fatalf("phone: %s", payload.Phone)
	}
	got, err := s.Verify(state)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Phone != payload.Phone || got.Nonce != payload.Nonce {
		t.Fatalf("payload mismatch: %+v vs %+v", got, payload)
	}
}

func TestStateSigner_InvalidMAC(t *testing.T) {
	s := NewStateSigner("secret-secret-secret-secret-secret")
	state, _, err := s.New("+5511", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// flip a character at the end (mac region) — should reject
	tampered := state[:len(state)-2] + "AA"
	if _, err := s.Verify(tampered); err != ErrInvalidState {
		t.Fatalf("expected ErrInvalidState, got %v", err)
	}
}

func TestStateSigner_Expired(t *testing.T) {
	s := NewStateSigner("secret-secret-secret-secret-secret")
	state, _, err := s.New("+5511", -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(state); err != ErrStateExpired {
		t.Fatalf("expected ErrStateExpired, got %v", err)
	}
}

func TestNewLinkToken(t *testing.T) {
	t1, err := NewLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	t2, _ := NewLinkToken()
	if t1 == t2 {
		t.Fatalf("link tokens should be unique: %s == %s", t1, t2)
	}
	if strings.Contains(t1, "=") {
		t.Fatalf("link token should not contain padding: %s", t1)
	}
	if len(t1) < 10 {
		t.Fatalf("link token too short: %s", t1)
	}
}
