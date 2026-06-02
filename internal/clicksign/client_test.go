package clicksign

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/oauth"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// stubStore implements just enough of session.Store to drive the client's
// Bearer + X-Account-Key + refresh paths during the unit tests.
type stubStore struct {
	sess         *session.Session
	reg          *session.ClientRegistration
	putCalls     int
	deleteCalled bool
}

func (s *stubStore) GetSession(_ context.Context, _ string) (*session.Session, error) {
	if s.sess == nil {
		return nil, session.ErrNotFound
	}
	cp := *s.sess
	return &cp, nil
}
func (s *stubStore) PutSession(_ context.Context, sess *session.Session) error {
	s.putCalls++
	cp := *sess
	s.sess = &cp
	return nil
}
func (s *stubStore) DeleteSession(_ context.Context, _ string) error { s.deleteCalled = true; s.sess = nil; return nil }
func (s *stubStore) PutPending(_ context.Context, _ *session.Pending) error { return nil }
func (s *stubStore) GetPendingByState(_ context.Context, _ string) (*session.Pending, error) {
	return nil, session.ErrNotFound
}
func (s *stubStore) GetPendingByLinkToken(_ context.Context, _ string) (*session.Pending, error) {
	return nil, session.ErrNotFound
}
func (s *stubStore) DeletePending(_ context.Context, _ string) error { return nil }
func (s *stubStore) GetClientRegistration(_ context.Context) (*session.ClientRegistration, error) {
	if s.reg == nil {
		return nil, session.ErrNotFound
	}
	cp := *s.reg
	return &cp, nil
}
func (s *stubStore) PutClientRegistration(_ context.Context, r *session.ClientRegistration) error {
	cp := *r
	s.reg = &cp
	return nil
}

func newTestClient(t *testing.T, store session.Store, oauthBase, apiBase string) *HTTPClient {
	t.Helper()
	o := oauth.NewClient(oauthBase)
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	return NewHTTPClient(apiBase, 5*time.Second, logger, store, o)
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(strings.TrimSpace(string(p))); return len(p), nil }

func TestListEnvelopes_HappyPath(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/envelopes" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-1" {
			t.Errorf("Authorization=%q", got)
		}
		if got := r.Header.Get("X-Account-Key"); got != "acc-key" {
			t.Errorf("X-Account-Key=%q", got)
		}
		if got := r.URL.Query().Get("status"); got != "running" {
			t.Errorf("status filter=%q", got)
		}
		_ = json.NewEncoder(w).Encode(envelopesResponse{Data: []Envelope{
			{ID: "env-1", Type: "envelopes", Attributes: EnvelopeAttributes{Name: "Contract", Status: "running"}},
		}})
	}))
	defer api.Close()

	store := &stubStore{sess: &session.Session{
		PhoneNumber: "+5511999999999",
		AccessToken: "access-1",
		AccountKey:  "acc-key",
	}}
	c := newTestClient(t, store, "http://oauth.invalid", api.URL)
	envs, err := c.ListEnvelopes(context.Background(), "+5511999999999", "running", 10)
	if err != nil {
		t.Fatalf("ListEnvelopes: %v", err)
	}
	if len(envs) != 1 || envs[0].ID != "env-1" {
		t.Fatalf("unexpected envs: %+v", envs)
	}
}

func TestListEnvelopes_RefreshOn401(t *testing.T) {
	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		switch r.Header.Get("Authorization") {
		case "Bearer expired-token":
			w.WriteHeader(http.StatusUnauthorized)
		case "Bearer fresh-token":
			_ = json.NewEncoder(w).Encode(envelopesResponse{Data: []Envelope{{ID: "env-2", Type: "envelopes"}}})
		default:
			t.Fatalf("unexpected token: %q", r.Header.Get("Authorization"))
		}
	}))
	defer api.Close()

	mockOAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 r.Host,
				"authorization_endpoint": "http://" + r.Host + "/oauth2/authorize",
				"token_endpoint":         "http://" + r.Host + "/oauth2/token",
				"registration_endpoint":  "http://" + r.Host + "/oauth2/register",
			})
		case "/oauth2/token":
			_ = r.ParseForm()
			if r.FormValue("grant_type") != "refresh_token" {
				t.Fatalf("expected refresh_token grant, got %q", r.FormValue("grant_type"))
			}
			_, _ = w.Write([]byte(`{"access_token":"fresh-token","refresh_token":"refresh-2","token_type":"Bearer","expires_in":3600}`))
		default:
			t.Fatalf("unexpected oauth path: %s", r.URL.Path)
		}
	}))
	defer mockOAuth.Close()

	store := &stubStore{
		sess: &session.Session{PhoneNumber: "+55", AccessToken: "expired-token", RefreshToken: "refresh-1", AccountKey: "acc"},
		reg:  &session.ClientRegistration{ClientID: "dcr_xyz"},
	}
	c := newTestClient(t, store, mockOAuth.URL, api.URL)
	envs, err := c.ListEnvelopes(context.Background(), "+55", "", 0)
	if err != nil {
		t.Fatalf("ListEnvelopes after refresh: %v", err)
	}
	if len(envs) != 1 || envs[0].ID != "env-2" {
		t.Fatalf("unexpected envs after refresh: %+v", envs)
	}
	if apiCalls != 2 {
		t.Errorf("expected 2 API calls (401+retry), got %d", apiCalls)
	}
	if store.sess.AccessToken != "fresh-token" {
		t.Errorf("session not updated after refresh: %q", store.sess.AccessToken)
	}
}

func TestCreateEnvelopeBulkCreation_SerializesTemplatePayload(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/envelope_bulk_creations" || r.Method != http.MethodPost {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type=%q", ct)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		// Cheap structural smoke: data.attributes.document.template.key
		attrs := body["data"].(map[string]any)["attributes"].(map[string]any)
		tpl := attrs["document"].(map[string]any)["template"].(map[string]any)
		if tpl["key"] != "tpl-1" {
			t.Errorf("template.key=%v", tpl["key"])
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"bulk-1","type":"envelope_bulk_creations","attributes":{"envelope_id":"env-99","status":"created"}}}`))
	}))
	defer api.Close()

	store := &stubStore{sess: &session.Session{PhoneNumber: "+1", AccessToken: "tok", AccountKey: "acc"}}
	c := newTestClient(t, store, "http://oauth.invalid", api.URL)
	resp, err := c.CreateEnvelopeBulkCreation(context.Background(), "+1", EnvelopeBulkCreationRequest{
		Data: EnvelopeBulkCreationData{
			Type: "envelope_bulk_creations",
			Attributes: EnvelopeBulkCreationAttributes{
				Envelope: BulkEnvelope{Name: "X", RemindInterval: 3},
				Document: BulkDocument{
					Filename: "x.docx",
					Template: &BulkTemplate{Key: "tpl-1", Data: map[string]any{"name": "Joao"}},
				},
				Signers: []BulkSigner{{Name: "Joao", Email: "j@x.com"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateEnvelopeBulkCreation: %v", err)
	}
	if resp.Data.Attributes.EnvelopeID != "env-99" {
		t.Errorf("envelope_id=%q", resp.Data.Attributes.EnvelopeID)
	}
}
