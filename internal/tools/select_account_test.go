package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// memStore is a tiny in-memory Store sufficient for select_account tests.
// We only need GetSession/PutSession; the other methods are stubs.
type memStore struct{ sess *session.Session }

func (m *memStore) GetSession(_ context.Context, _ string) (*session.Session, error) {
	if m.sess == nil {
		return nil, session.ErrNotFound
	}
	out := *m.sess
	return &out, nil
}
func (m *memStore) PutSession(_ context.Context, s *session.Session) error {
	cp := *s
	m.sess = &cp
	return nil
}
func (m *memStore) DeleteSession(_ context.Context, _ string) error { m.sess = nil; return nil }
func (m *memStore) PutPending(_ context.Context, _ *session.Pending) error { return nil }
func (m *memStore) GetPendingByState(_ context.Context, _ string) (*session.Pending, error) {
	return nil, session.ErrNotFound
}
func (m *memStore) GetPendingByLinkToken(_ context.Context, _ string) (*session.Pending, error) {
	return nil, session.ErrNotFound
}
func (m *memStore) DeletePending(_ context.Context, _ string) error { return nil }
func (m *memStore) GetClientRegistration(_ context.Context) (*session.ClientRegistration, error) {
	return nil, session.ErrNotFound
}
func (m *memStore) PutClientRegistration(_ context.Context, _ *session.ClientRegistration) error {
	return nil
}

func TestSelectAccount_ClearsPendingOnSuccess(t *testing.T) {
	store := &memStore{sess: &session.Session{
		PhoneNumber: "+55",
		PendingAccounts: []session.PendingAccount{
			{Key: "acc-a", Name: "Acme"},
			{Key: "acc-b", Name: "Beta"},
		},
	}}
	tool := selectAccountTool(CatalogDeps{Store: store})

	out, err := tool.Run(context.Background(), "+55", map[string]any{"account_key": "acc-b"})
	if err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if !strings.Contains(out, `"account_key":"acc-b"`) {
		t.Errorf("unexpected output: %s", out)
	}
	if store.sess.AccountKey != "acc-b" {
		t.Errorf("AccountKey=%q (want acc-b)", store.sess.AccountKey)
	}
	if len(store.sess.PendingAccounts) != 0 {
		t.Errorf("PendingAccounts should be cleared, got %+v", store.sess.PendingAccounts)
	}
}

func TestSelectAccount_RejectsKeyOutsidePending(t *testing.T) {
	store := &memStore{sess: &session.Session{
		PhoneNumber: "+55",
		PendingAccounts: []session.PendingAccount{
			{Key: "acc-a", Name: "Acme"},
		},
	}}
	tool := selectAccountTool(CatalogDeps{Store: store})

	if _, err := tool.Run(context.Background(), "+55", map[string]any{"account_key": "hallucinated"}); err == nil {
		t.Fatalf("expected error for key outside pending list")
	}
	if store.sess.AccountKey != "" {
		t.Errorf("AccountKey should remain empty after rejection, got %q", store.sess.AccountKey)
	}
	if len(store.sess.PendingAccounts) != 1 {
		t.Errorf("pending list should remain intact, got %+v", store.sess.PendingAccounts)
	}
}

func TestSelectAccount_NoPendingAcceptsAnyKey(t *testing.T) {
	store := &memStore{sess: &session.Session{PhoneNumber: "+55"}}
	tool := selectAccountTool(CatalogDeps{Store: store})

	if _, err := tool.Run(context.Background(), "+55", map[string]any{"account_key": "fresh-key"}); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if store.sess.AccountKey != "fresh-key" {
		t.Errorf("AccountKey=%q (want fresh-key)", store.sess.AccountKey)
	}
}

func TestSelectAccount_MissingArgErrors(t *testing.T) {
	store := &memStore{sess: &session.Session{PhoneNumber: "+55"}}
	tool := selectAccountTool(CatalogDeps{Store: store})

	if _, err := tool.Run(context.Background(), "+55", map[string]any{}); err == nil {
		t.Fatal("expected error for missing account_key")
	}
}
