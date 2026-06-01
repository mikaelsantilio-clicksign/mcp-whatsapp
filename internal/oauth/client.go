package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client wraps OAuth2 + DCR operations.
//
// Two operating modes are supported:
//
//   - MCP fa\u00e7ade ("legacy"): the issuer is the Clicksign MCP server, we
//     run RFC 8414 discovery against it, perform RFC 7591 dynamic client
//     registration, and proxy code/refresh exchanges through it. Used by
//     external MCP clients (Cursor, ChatGPT, etc).
//
//   - Direct ("direct"): we talk straight to the Clicksign Cognito user
//     pool with a pre-registered confidential client. There is no
//     discovery hit and the token endpoint receives client_id +
//     client_secret in the form body (same pattern the MCP fa\u00e7ade uses
//     internally — see clicksign/mcp-api-tavola-v3/cmd/server/oauth_facade.go
//     proxyTokenForm).
//
// Backend services (this project) should use Direct mode so we don't
// depend on the MCP server's uptime nor on DCR.
type Client struct {
	httpClient *http.Client

	// issuerBase is the .well-known root for MCP mode; ignored when
	// direct != nil.
	issuerBase string
	metadata   *AuthorizationServerMetadata

	// direct holds the static OAuth endpoints + confidential client
	// credentials when the client is operating in direct mode.
	direct *DirectConfig
}

// DirectConfig groups the static endpoints + credentials used by a
// confidential OAuth client registered manually in Clicksign Cognito.
type DirectConfig struct {
	// AuthorizationURL is the full URL the user's browser is redirected to.
	// For Clicksign staging this is https://oauth2.clicksign.dev/login.
	AuthorizationURL string
	// TokenURL is the OAuth2 token endpoint that receives both
	// authorization_code and refresh_token grants. For staging this is
	// https://oauth2.clicksign.dev/oauth2/token.
	TokenURL string
	// ClientID + ClientSecret are the confidential credentials issued by
	// Clicksign for this app. We send them in the form body (Cognito
	// accepts both form-body and Basic auth; form-body matches the MCP
	// fa\u00e7ade behaviour).
	ClientID     string
	ClientSecret string
}

// NewClient builds a Client in MCP-fa\u00e7ade mode. issuerBase is the MCP
// server origin (e.g. https://mcp-api-tavola-v3-6.clicksign.dev).
func NewClient(issuerBase string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		issuerBase: strings.TrimRight(issuerBase, "/"),
	}
}

// NewDirectClient builds a Client that talks straight to a confidential
// OAuth provider (Clicksign Cognito). No discovery is performed and DCR
// is unavailable.
func NewDirectClient(cfg DirectConfig) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		direct:     &cfg,
		// Synthetic metadata so callers that go through Discover() still
		// get a useful struct.
		metadata: &AuthorizationServerMetadata{
			Issuer:                strings.TrimSuffix(cfg.AuthorizationURL, "/login"),
			AuthorizationEndpoint: cfg.AuthorizationURL,
			TokenEndpoint:         cfg.TokenURL,
		},
	}
}

// IsDirect reports whether the client is in direct mode.
func (c *Client) IsDirect() bool { return c.direct != nil }

// Discover loads the OAuth2 authorization server metadata. In direct
// mode it returns the static endpoints set at construction (no HTTP).
func (c *Client) Discover(ctx context.Context) (*AuthorizationServerMetadata, error) {
	if c.metadata != nil {
		return c.metadata, nil
	}
	if c.direct != nil {
		// Shouldn't happen — NewDirectClient pre-seeds metadata — but be
		// defensive in case a future refactor blanks it.
		c.metadata = &AuthorizationServerMetadata{
			AuthorizationEndpoint: c.direct.AuthorizationURL,
			TokenEndpoint:         c.direct.TokenURL,
		}
		return c.metadata, nil
	}
	u := c.issuerBase + "/.well-known/oauth-authorization-server"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("discovery: %s: %s", resp.Status, string(body))
	}
	var md AuthorizationServerMetadata
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		return nil, fmt.Errorf("discovery decode: %w", err)
	}
	c.metadata = &md
	return &md, nil
}

// RegisterDynamic performs RFC 7591 Dynamic Client Registration. Not
// supported in direct mode (we already have a confidential client).
func (c *Client) RegisterDynamic(ctx context.Context, redirectURI, scope string) (*RegistrationResponse, error) {
	if c.direct != nil {
		return nil, errors.New("dcr: not available in direct mode")
	}
	md, err := c.Discover(ctx)
	if err != nil {
		return nil, err
	}
	if md.RegistrationEndpoint == "" {
		return nil, errors.New("dcr: registration_endpoint not advertised by issuer")
	}
	body := RegistrationRequest{
		RedirectURIs:            []string{redirectURI},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		Scope:                   scope,
		ClientName:              "whatsapp-mcp",
	}
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.RegistrationEndpoint, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dcr: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dcr: %s: %s", resp.Status, string(raw))
	}
	var out RegistrationResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("dcr decode: %w", err)
	}
	return &out, nil
}

// BuildAuthorizeURL constructs the /authorize URL with PKCE + state.
func (c *Client) BuildAuthorizeURL(clientID, redirectURI, state, codeChallenge, scope string) (string, error) {
	md, err := c.Discover(context.Background())
	if err != nil {
		return "", err
	}
	u, err := url.Parse(md.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("scope", scope)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ExchangeCode exchanges an authorization code (with PKCE verifier) for tokens.
func (c *Client) ExchangeCode(ctx context.Context, clientID, redirectURI, code, codeVerifier string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", codeVerifier)
	return c.tokenRequest(ctx, form)
}

// RefreshToken exchanges a refresh_token for a new access_token.
func (c *Client) RefreshToken(ctx context.Context, clientID, refreshToken string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	return c.tokenRequest(ctx, form)
}

func (c *Client) tokenRequest(ctx context.Context, form url.Values) (*TokenResponse, error) {
	md, err := c.Discover(ctx)
	if err != nil {
		return nil, err
	}
	// Direct (confidential client) mode authenticates by sending
	// client_id + client_secret in the form body, matching the pattern
	// the MCP fa\u00e7ade uses against Cognito (proxyTokenForm). We always
	// override client_id here so callers can't accidentally send a stale
	// DCR id.
	if c.direct != nil {
		form.Set("client_id", c.direct.ClientID)
		form.Set("client_secret", c.direct.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		var errResp TokenErrorResponse
		if json.Unmarshal(raw, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("token: %s: %s", errResp.Error, errResp.ErrorDescription)
		}
		return nil, fmt.Errorf("token: %s: %s", resp.Status, string(raw))
	}
	var tr TokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("token decode: %w", err)
	}
	tr.IssuedAt = time.Now().UTC()
	return &tr, nil
}
