package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestDirectClient_BuildAuthorizeURL_NoDiscovery proves the direct client
// composes the /authorize URL straight from its configured endpoint
// without issuing any HTTP call (no discovery roundtrip).
func TestDirectClient_BuildAuthorizeURL_NoDiscovery(t *testing.T) {
	c := NewDirectClient(DirectConfig{
		AuthorizationURL: "https://oauth2.clicksign.dev/login",
		TokenURL:         "https://oauth2.clicksign.dev/oauth2/token",
		ClientID:         "client-abc",
		ClientSecret:     "shh",
	})
	if !c.IsDirect() {
		t.Fatalf("expected IsDirect=true")
	}

	got, err := c.BuildAuthorizeURL("client-abc", "https://app.example.com/oauth2/callback", "state-xyz", "chal-123", "openid email")
	if err != nil {
		t.Fatalf("build authorize url: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Host != "oauth2.clicksign.dev" || u.Path != "/login" {
		t.Fatalf("unexpected base: %s", got)
	}
	q := u.Query()
	if q.Get("client_id") != "client-abc" {
		t.Fatalf("client_id: %s", q.Get("client_id"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("pkce method missing")
	}
	if q.Get("scope") != "openid email" {
		t.Fatalf("scope: %s", q.Get("scope"))
	}
	if q.Get("response_type") != "code" {
		t.Fatalf("response_type: %s", q.Get("response_type"))
	}
}

// TestDirectClient_ExchangeCode_SendsClientSecret verifies that in
// direct mode the token endpoint receives both client_id and
// client_secret in the form body (matching Cognito + the MCP fa\u00e7ade
// proxyTokenForm behaviour).
func TestDirectClient_ExchangeCode_SendsClientSecret(t *testing.T) {
	var gotForm url.Values
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "AT",
			"refresh_token": "RT",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	c := NewDirectClient(DirectConfig{
		AuthorizationURL: "https://example.com/login",
		TokenURL:         srv.URL,
		ClientID:         "client-abc",
		ClientSecret:     "shh-very-secret",
	})

	tr, err := c.ExchangeCode(context.Background(), "ignored-overridden-id", "https://app.example/cb", "code-1", "verifier-1")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if tr.AccessToken != "AT" {
		t.Fatalf("access token: %q", tr.AccessToken)
	}
	if !strings.HasPrefix(gotCT, "application/x-www-form-urlencoded") {
		t.Fatalf("content-type: %s", gotCT)
	}
	if gotForm.Get("grant_type") != "authorization_code" {
		t.Fatalf("grant_type: %s", gotForm.Get("grant_type"))
	}
	if gotForm.Get("client_id") != "client-abc" {
		t.Fatalf("client_id must be overridden to direct config: %s", gotForm.Get("client_id"))
	}
	if gotForm.Get("client_secret") != "shh-very-secret" {
		t.Fatalf("client_secret missing or wrong: %s", gotForm.Get("client_secret"))
	}
	if gotForm.Get("code") != "code-1" || gotForm.Get("code_verifier") != "verifier-1" {
		t.Fatalf("code/verifier missing: %#v", gotForm)
	}
}

// TestDirectClient_RefreshToken_SendsClientSecret mirrors the above for
// refresh_token grants.
func TestDirectClient_RefreshToken_SendsClientSecret(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "AT2",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	c := NewDirectClient(DirectConfig{
		AuthorizationURL: "https://example.com/login",
		TokenURL:         srv.URL,
		ClientID:         "cid",
		ClientSecret:     "csec",
	})

	if _, err := c.RefreshToken(context.Background(), "cid", "RT"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if gotForm.Get("grant_type") != "refresh_token" {
		t.Fatalf("grant_type: %s", gotForm.Get("grant_type"))
	}
	if gotForm.Get("client_secret") != "csec" {
		t.Fatalf("client_secret missing")
	}
	if gotForm.Get("refresh_token") != "RT" {
		t.Fatalf("refresh_token missing")
	}
}

// TestDirectClient_RegisterDynamic_Disabled ensures DCR is explicitly
// rejected in direct mode (so callers can detect misconfiguration).
func TestDirectClient_RegisterDynamic_Disabled(t *testing.T) {
	c := NewDirectClient(DirectConfig{
		AuthorizationURL: "https://example.com/login",
		TokenURL:         "https://example.com/token",
		ClientID:         "cid",
		ClientSecret:     "csec",
	})
	_, err := c.RegisterDynamic(context.Background(), "https://app/cb", "openid")
	if err == nil {
		t.Fatalf("expected DCR to be disabled in direct mode")
	}
}

// TestMCPClient_TokenRequest_DoesNotInjectSecret guards against accidental
// leakage of a client_secret on the MCP/legacy path (DCR clients use
// token_endpoint_auth_method=none).
func TestMCPClient_TokenRequest_DoesNotInjectSecret(t *testing.T) {
	var gotForm url.Values
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "AT", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	mdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 r.Host,
			"authorization_endpoint": "https://example/authorize",
			"token_endpoint":         tokenSrv.URL,
			"registration_endpoint":  "https://example/register",
		})
	}))
	defer mdSrv.Close()

	c := NewClient(mdSrv.URL)
	if _, err := c.ExchangeCode(context.Background(), "dcr-client", "https://app/cb", "c", "v"); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if gotForm.Get("client_secret") != "" {
		t.Fatalf("client_secret must NOT be sent on the MCP path: %s", gotForm.Get("client_secret"))
	}
	if gotForm.Get("client_id") != "dcr-client" {
		t.Fatalf("client_id: %s", gotForm.Get("client_id"))
	}
}
