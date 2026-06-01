package clicksign

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/session"
)

func TestIsMultiAccountErr(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"empty", "", false},
		{"unrelated 400", `{"errors":[{"detail":"name is required"}]}`, false},
		{"portuguese hint", "Multiplas contas Clicksign estao disponiveis", true},
		{"x-account-key hint", "X-Account-Key header is required", true},
		{"english hint", "user has multiple accounts, please send account", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMultiAccountErr([]byte(tc.body)); got != tc.want {
				t.Fatalf("isMultiAccountErr(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestMultiAccountError_IsSentinel(t *testing.T) {
	err := &MultiAccountError{}
	if !errors.Is(err, ErrMultiAccount) {
		t.Fatal("expected MultiAccountError to satisfy errors.Is(ErrMultiAccount)")
	}
}

// fakeStore is a minimal session.Store impl for client unit tests.
type fakeStore struct {
	sess  *session.Session
	creg  *session.ClientRegistration
	saved *session.Session
}

func (f *fakeStore) GetSession(_ context.Context, _ string) (*session.Session, error) {
	if f.sess == nil {
		return nil, session.ErrNotFound
	}
	cp := *f.sess
	return &cp, nil
}
func (f *fakeStore) PutSession(_ context.Context, s *session.Session) error {
	cp := *s
	f.saved = &cp
	f.sess = &cp
	return nil
}
func (f *fakeStore) DeleteSession(_ context.Context, _ string) error { return nil }
func (f *fakeStore) PutPending(_ context.Context, _ *session.Pending) error {
	return nil
}
func (f *fakeStore) GetPendingByState(_ context.Context, _ string) (*session.Pending, error) {
	return nil, session.ErrNotFound
}
func (f *fakeStore) GetPendingByLinkToken(_ context.Context, _ string) (*session.Pending, error) {
	return nil, session.ErrNotFound
}
func (f *fakeStore) DeletePending(_ context.Context, _ string) error { return nil }
func (f *fakeStore) GetClientRegistration(_ context.Context) (*session.ClientRegistration, error) {
	if f.creg == nil {
		return nil, session.ErrNotFound
	}
	cp := *f.creg
	return &cp, nil
}
func (f *fakeStore) PutClientRegistration(_ context.Context, r *session.ClientRegistration) error {
	cp := *r
	f.creg = &cp
	return nil
}

func TestClient_ListTemplates_AppliesXAccountKey(t *testing.T) {
	var captured *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Clone(r.Context())
		_, _ = w.Write([]byte(`{"data":[{"id":"t1","type":"templates","attributes":{"name":"Modelo"}}]}`))
	}))
	defer srv.Close()

	store := &fakeStore{
		sess: &session.Session{
			PhoneNumber:      "5511999",
			AccessToken:      "abc",
			RefreshToken:     "ref",
			PreferredAccount: "acct-1",
			ExpiresAt:        time.Now().Add(time.Hour),
		},
	}
	c := NewClient(Config{BaseURL: srv.URL}, nil, store, nil)

	got, err := c.ListTemplates(context.Background(), "5511999")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(got) != 1 || got[0].Attributes.Name != "Modelo" {
		t.Fatalf("unexpected templates: %#v", got)
	}
	if captured == nil {
		t.Fatal("server never received request")
	}
	if h := captured.Header.Get("Authorization"); h != "Bearer abc" {
		t.Fatalf("Authorization=%q want %q", h, "Bearer abc")
	}
	if h := captured.Header.Get("X-Account-Key"); h != "acct-1" {
		t.Fatalf("X-Account-Key=%q want %q", h, "acct-1")
	}
	if captured.URL.Path != "/templates" {
		t.Fatalf("path=%q want /templates", captured.URL.Path)
	}
}

func TestClient_ListTemplates_MultiAccountError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"Multiplas contas Clicksign estao disponiveis"}]}`))
	}))
	defer srv.Close()

	store := &fakeStore{
		sess: &session.Session{
			PhoneNumber:  "5511999",
			AccessToken:  "abc",
			RefreshToken: "ref",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	}
	c := NewClient(Config{BaseURL: srv.URL}, nil, store, nil)

	_, err := c.ListTemplates(context.Background(), "5511999")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrMultiAccount) {
		t.Fatalf("expected ErrMultiAccount, got %v", err)
	}
	var typed *MultiAccountError
	if !errors.As(err, &typed) {
		t.Fatal("expected *MultiAccountError via errors.As")
	}
}

func TestClient_NoSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached when session is absent")
	}))
	defer srv.Close()

	store := &fakeStore{}
	c := NewClient(Config{BaseURL: srv.URL}, nil, store, nil)
	_, err := c.ListTemplates(context.Background(), "5511999")
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected session-expired error, got %v", err)
	}
}
