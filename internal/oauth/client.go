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

// Client wraps OAuth2 + DCR operations against the MCP server's OAuth facade.
type Client struct {
	httpClient *http.Client

	// Base URL where the .well-known is served (usually the MCP server base
	// URL itself, since the MCP server is the OAuth facade).
	issuerBase string

	metadata *AuthorizationServerMetadata
}

func NewClient(issuerBase string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		issuerBase: strings.TrimRight(issuerBase, "/"),
	}
}

// Discover loads the OAuth2 authorization server metadata.
func (c *Client) Discover(ctx context.Context) (*AuthorizationServerMetadata, error) {
	if c.metadata != nil {
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

// RegisterDynamic performs RFC 7591 Dynamic Client Registration.
func (c *Client) RegisterDynamic(ctx context.Context, redirectURI, scope string) (*RegistrationResponse, error) {
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
