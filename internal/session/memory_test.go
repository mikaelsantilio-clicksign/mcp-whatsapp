package session

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStore_Session(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	phone := "+5511999"

	if _, err := s.GetSession(ctx, phone); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	want := &Session{
		PhoneNumber:  phone,
		AccessToken:  "a",
		RefreshToken: "r",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := s.PutSession(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "a" || got.RefreshToken != "r" {
		t.Fatalf("session mismatch: %+v", got)
	}

	if err := s.DeleteSession(ctx, phone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, phone); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestMemoryStore_PendingByStateAndLink(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	p := &Pending{
		State:        "state-abc",
		LinkToken:    "tok-xyz",
		AuthorizeURL: "https://example/auth?x=1",
		PhoneNumber:  "+5511",
		CodeVerifier: "v",
		ExpiresAt:    time.Now().Add(time.Minute),
	}
	if err := s.PutPending(ctx, p); err != nil {
		t.Fatal(err)
	}
	gotByState, err := s.GetPendingByState(ctx, "state-abc")
	if err != nil {
		t.Fatal(err)
	}
	if gotByState.LinkToken != "tok-xyz" {
		t.Fatalf("link token: %s", gotByState.LinkToken)
	}
	gotByLink, err := s.GetPendingByLinkToken(ctx, "tok-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if gotByLink.State != "state-abc" {
		t.Fatalf("state: %s", gotByLink.State)
	}

	if err := s.DeletePending(ctx, "state-abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPendingByState(ctx, "state-abc"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := s.GetPendingByLinkToken(ctx, "tok-xyz"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for link, got %v", err)
	}
}

func TestMemoryStore_PendingExpires(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	p := &Pending{
		State:       "s",
		LinkToken:   "l",
		PhoneNumber: "+1",
		ExpiresAt:   time.Now().Add(-time.Second),
	}
	_ = s.PutPending(ctx, p)
	if _, err := s.GetPendingByState(ctx, "s"); err != ErrExpired {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
	if _, err := s.GetPendingByState(ctx, "s"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after expired cleanup, got %v", err)
	}
}
