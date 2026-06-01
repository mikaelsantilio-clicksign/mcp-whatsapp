package flow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// newTestClient builds a clicksign.Client wired against an httptest server
// using a one-shot fakeStore implementation.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*clicksign.Client, *fakeFlowStore) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	store := &fakeFlowStore{
		sess: &session.Session{
			PhoneNumber:  "5511999",
			AccessToken:  "tok",
			RefreshToken: "ref",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	}
	return clicksign.NewClient(clicksign.Config{BaseURL: srv.URL}, nil, store, nil), store
}

// fakeFlowStore is a very small session.Store stub used by these flow
// tests. We declare it here (not under clicksign/) to avoid coupling.
type fakeFlowStore struct {
	sess *session.Session
}

func (f *fakeFlowStore) GetSession(_ context.Context, _ string) (*session.Session, error) {
	if f.sess == nil {
		return nil, session.ErrNotFound
	}
	cp := *f.sess
	return &cp, nil
}
func (f *fakeFlowStore) PutSession(_ context.Context, s *session.Session) error {
	cp := *s
	f.sess = &cp
	return nil
}
func (f *fakeFlowStore) DeleteSession(_ context.Context, _ string) error { return nil }
func (f *fakeFlowStore) PutPending(_ context.Context, _ *session.Pending) error {
	return nil
}
func (f *fakeFlowStore) GetPendingByState(_ context.Context, _ string) (*session.Pending, error) {
	return nil, session.ErrNotFound
}
func (f *fakeFlowStore) GetPendingByLinkToken(_ context.Context, _ string) (*session.Pending, error) {
	return nil, session.ErrNotFound
}
func (f *fakeFlowStore) DeletePending(_ context.Context, _ string) error { return nil }
func (f *fakeFlowStore) GetClientRegistration(_ context.Context) (*session.ClientRegistration, error) {
	return nil, session.ErrNotFound
}
func (f *fakeFlowStore) PutClientRegistration(_ context.Context, _ *session.ClientRegistration) error {
	return nil
}

// --- ListTemplatesFlow -----------------------------------------------------

func TestListTemplates_NoAccount_TransfersToSelect(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be called before account is selected")
	})
	f := NewListTemplatesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999"}

	res, err := f.Handle(context.Background(), Input{
		Phone:   "5511999",
		Session: sess,
		Intent:  "list_templates",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Kind != KindTransfer || res.NextIntent != "select_account" {
		t.Fatalf("expected transfer to select_account, got %#v", res)
	}
	if res.NextState == nil || res.NextState.Data["return_to"] != "list_templates" {
		t.Fatalf("missing return_to=list_templates in transfer state: %#v", res.NextState)
	}
}

func TestListTemplates_WithAccount_RendersList(t *testing.T) {
	body := `{"data":[
		{"id":"tpl1","type":"templates","attributes":{"name":"Modelo A","created":"2025-12-18T16:37:23.000Z"}},
		{"id":"tpl2","type":"templates","attributes":{"name":"Modelo B","created":"2026-01-10T00:00:00.000Z"}}
	]}`
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/templates" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("X-Account-Key"); got != "acct-1" {
			t.Errorf("X-Account-Key=%q want acct-1", got)
		}
		_, _ = w.Write([]byte(body))
	})
	f := NewListTemplatesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}

	ctx := clicksign.WithSession(context.Background(), sess)
	res, err := f.Handle(ctx, Input{Phone: "5511999", Session: sess, Intent: "list_templates"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindChoose {
		t.Fatalf("expected KindChoose, got %v", res.Kind)
	}
	if res.Interactive == nil || len(res.Interactive.Items) != 2 {
		t.Fatalf("unexpected interactive: %#v", res.Interactive)
	}
	if res.Interactive.Items[0].ID != "tpl1" {
		t.Fatalf("first item ID=%q want tpl1", res.Interactive.Items[0].ID)
	}
	if !strings.Contains(res.Interactive.Items[0].Description, "18/12/2025") {
		t.Fatalf("missing formatted date: %#v", res.Interactive.Items[0])
	}
}

func TestListTemplates_Empty(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	f := NewListTemplatesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", PreferredAccount: "acct-1"}
	res, err := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone for empty list, got %v", res.Kind)
	}
}

func TestListTemplates_MultiAccountClearsPreferred(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"Multiplas contas Clicksign estao disponiveis"}]}`))
	})
	f := NewListTemplatesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", PreferredAccount: "stale-acct"}

	res, err := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindTransfer || res.NextIntent != "select_account" {
		t.Fatalf("expected transfer back to select_account, got %#v", res)
	}
	if sess.PreferredAccount != "" {
		t.Fatalf("expected PreferredAccount to be cleared, got %q", sess.PreferredAccount)
	}
}

// --- SelectAccountFlow -----------------------------------------------------

func TestSelectAccount_ByIndex(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/accounts" {
			t.Errorf("path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"a1","type":"accounts","attributes":{"name":"Pessoal","key":"acct-pessoal"}},
			{"id":"a2","type":"accounts","attributes":{"name":"Empresa","key":"acct-empresa"}},
			{"id":"a3","type":"accounts","attributes":{"name":"Integration","key":"acct-int"}}
		]}`))
	})
	f := NewSelectAccountFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999"}
	ents := map[string]any{"account_index": 3}

	res, err := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess, Intent: "select_account", Entities: ents})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v", res.Kind)
	}
	if sess.PreferredAccount != "acct-int" {
		t.Fatalf("PreferredAccount=%q want acct-int", sess.PreferredAccount)
	}
}

func TestSelectAccount_ByIndex_OutOfRange_AsksAgain(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"a1","type":"accounts","attributes":{"name":"A","key":"k1"}},
			{"id":"a2","type":"accounts","attributes":{"name":"B","key":"k2"}}
		]}`))
	})
	f := NewSelectAccountFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999"}

	res, _ := f.Handle(context.Background(), Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "select_account",
		Entities: map[string]any{"account_index": 7},
	})
	if res.Kind != KindChoose {
		t.Fatalf("expected re-ask via KindChoose, got %v", res.Kind)
	}
	if sess.PreferredAccount != "" {
		t.Fatalf("PreferredAccount should remain empty after invalid index, got %q", sess.PreferredAccount)
	}
}

func TestSelectAccount_SingleAccount_AutoPicks(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"a1","type":"accounts","attributes":{"name":"Only","key":"only"}}
		]}`))
	})
	f := NewSelectAccountFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999"}

	// Mimic "transfer from list_templates" by passing return_to in state.
	state := &session.FlowState{FlowID: "select_account", Step: "starting", Data: map[string]any{"return_to": "list_templates"}}
	res, _ := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess, Intent: "select_account", State: state})

	if res.Kind != KindTransfer || res.NextIntent != "list_templates" {
		t.Fatalf("expected immediate transfer back to list_templates, got %#v", res)
	}
	if sess.PreferredAccount != "only" {
		t.Fatalf("PreferredAccount=%q", sess.PreferredAccount)
	}
}

func TestSelectAccount_ClickValidatesKey(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"a1","type":"accounts","attributes":{"name":"A","key":"k1"}},
			{"id":"a2","type":"accounts","attributes":{"name":"B","key":"k2"}}
		]}`))
	})
	f := NewSelectAccountFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999"}
	state := &session.FlowState{FlowID: "select_account", Step: "awaiting_choice", Data: map[string]any{"return_to": "list_templates"}}

	// Valid click → transfer.
	res, _ := f.Handle(context.Background(), Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "select_account",
		State:    state,
		Interact: &InteractiveReply{ListItemID: "k2"},
	})
	if res.Kind != KindTransfer || res.NextIntent != "list_templates" {
		t.Fatalf("expected transfer to list_templates, got %#v", res)
	}
	if sess.PreferredAccount != "k2" {
		t.Fatalf("PreferredAccount=%q", sess.PreferredAccount)
	}

	// Invalid click → re-ask.
	sess.PreferredAccount = ""
	res, _ = f.Handle(context.Background(), Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "select_account",
		State:    state,
		Interact: &InteractiveReply{ListItemID: "ghost"},
	})
	if res.Kind != KindChoose {
		t.Fatalf("expected KindChoose for unknown key, got %v", res.Kind)
	}
}

func TestSelectAccount_ZeroAccounts(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	f := NewSelectAccountFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999"}
	res, _ := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess, Intent: "select_account"})
	if res.Kind != KindError {
		t.Fatalf("expected KindError for zero accounts, got %v", res.Kind)
	}
}
