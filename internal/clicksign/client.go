package clicksign

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/conv"
	"github.com/clicksign/whatsapp-mcp/internal/oauth"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// DefaultBaseURL is the production Clicksign API base URL. The MCP server
// (clicksign/mcp-api-tavola-v3) uses the same value as its default.
const DefaultBaseURL = "https://app.clicksign.com/api/v3"

// ctxKey is a private type used as a context.Value key so callers cannot
// accidentally collide with our key.
type ctxKey int

const sessionCtxKey ctxKey = 0

// WithSession scopes a *session.Session to ctx so the Client reads it
// instead of fetching from the Store on the next call.
//
// Use this when an in-process workflow (a Flow) has already mutated the
// Session in memory and wants its mutations to be visible immediately to
// the HTTP client (typically: a freshly-chosen PreferredAccount that has
// not been persisted yet).
//
// The Store is still authoritative for refresh: when a 401 is returned,
// refresh() reads the latest Session from the Store, rotates the tokens
// and writes back. The context-scoped Session is left untouched so the
// caller decides when (and whether) to refresh it from the Store.
func WithSession(ctx context.Context, sess *session.Session) context.Context {
	if sess == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionCtxKey, sess)
}

func sessionFromContext(ctx context.Context) *session.Session {
	v, _ := ctx.Value(sessionCtxKey).(*session.Session)
	return v
}

// Content types per JSON:API contract documented in Clicksign:
//
//   - Envelopes and signers expect plain application/json.
//   - Templates and notifications use application/vnd.api+json.
const (
	contentJSON   = "application/json"
	contentVndAPI = "application/vnd.api+json"
)

// Client is the Clicksign REST API client used by the Option B "flow"
// pipeline. It pulls the bearer token from session.Store at call time,
// transparently refreshes on 401, and applies the X-Account-Key header
// based on session.Session.PreferredAccount.
type Client struct {
	httpc   *http.Client
	baseURL string
	logger  *slog.Logger
	store   session.Store
	oauth   *oauth.Client

	// clientID is the DCR client_id needed when calling oauth.Client.RefreshToken.
	// We look it up once via session.Store at construction time and cache it.
	clientIDOnce sync.Once
	clientID     string
	clientIDErr  error

	// refreshLocks serialises refresh attempts per phone number to avoid
	// herd thundering when many requests fail with 401 at once.
	refreshLocks sync.Map // map[string]*sync.Mutex
}

// Config is the configuration knob set used by main.go to build a Client.
type Config struct {
	BaseURL string
	Timeout time.Duration
}

// NewClient constructs a Client. baseURL defaults to DefaultBaseURL when empty.
func NewClient(cfg Config, logger *slog.Logger, store session.Store, oa *oauth.Client) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		httpc:   &http.Client{Timeout: timeout},
		baseURL: base,
		logger:  logger,
		store:   store,
		oauth:   oa,
	}
}

// --- low-level transport ---------------------------------------------------

// do executes a single HTTP request against Clicksign carrying the user's
// bearer token and X-Account-Key (when set). It handles two implicit
// behaviours:
//
//  1. On 401 it attempts one token refresh and retries once.
//  2. It maps multi-account API errors to *MultiAccountError so callers
//     can branch on it via errors.As / errors.Is(ErrMultiAccount).
//
// body is closed by the caller; for retry, we serialise to a byte slice.
func (c *Client) do(ctx context.Context, phone, method, path, contentType string, body []byte) ([]byte, int, error) {
	endpoint := method + " " + path
	resp, status, err := c.doOnce(ctx, phone, method, path, contentType, body)
	if err != nil {
		return nil, 0, err
	}
	if status == http.StatusUnauthorized {
		if rerr := c.refresh(ctx, phone); rerr != nil {
			return nil, status, conv.ErrSessionExpired
		}
		resp, status, err = c.doOnce(ctx, phone, method, path, contentType, body)
		if err != nil {
			return nil, 0, err
		}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return resp, status, ErrInvalidToken
	}
	if status >= http.StatusInternalServerError {
		return resp, status, fmt.Errorf("%w: status %d body=%s", ErrServiceUnavailable, status, truncate(string(resp), 256))
	}
	if status >= 400 {
		if isMultiAccountErr(resp) {
			return resp, status, &MultiAccountError{}
		}
		return resp, status, &APIError{Status: status, Endpoint: endpoint, Body: resp}
	}
	return resp, status, nil
}

func (c *Client) doOnce(ctx context.Context, phone, method, path, contentType string, body []byte) ([]byte, int, error) {
	sess := sessionFromContext(ctx)
	if sess == nil {
		s, err := c.store.GetSession(ctx, phone)
		if err != nil {
			return nil, 0, conv.ErrSessionExpired
		}
		sess = s
	}
	if strings.TrimSpace(sess.AccessToken) == "" {
		return nil, 0, conv.ErrSessionExpired
	}

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("clicksign: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(sess.AccessToken))
	if accKey := strings.TrimSpace(sess.PreferredAccount); accKey != "" {
		req.Header.Set("X-Account-Key", accKey)
	}
	if contentType != "" && len(body) > 0 {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("clicksign: request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("clicksign: read body: %w", err)
	}
	c.logger.Debug("clicksign api response",
		slog.String("endpoint", method+" "+path),
		slog.Int("status", resp.StatusCode),
	)
	return raw, resp.StatusCode, nil
}

// isMultiAccountErr matches the Portuguese-language hint Clicksign returns
// when a user with multiple accounts calls the API without X-Account-Key.
// The detection is intentionally conservative: we only flag the response
// when we see the canonical phrase; otherwise the error stays as APIError.
func isMultiAccountErr(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	s := strings.ToLower(string(body))
	if strings.Contains(s, "multiplas contas") {
		return true
	}
	if strings.Contains(s, "multiple") && strings.Contains(s, "account") {
		return true
	}
	if strings.Contains(s, "x-account-key") {
		return true
	}
	return false
}

// refresh acquires a per-phone lock and uses oauth.Client.RefreshToken to
// rotate the access token. The new tokens are persisted back to the store
// so subsequent doOnce calls see the fresh credentials.
func (c *Client) refresh(ctx context.Context, phone string) error {
	mu := c.lockFor(phone)
	mu.Lock()
	defer mu.Unlock()

	sess, err := c.store.GetSession(ctx, phone)
	if err != nil || strings.TrimSpace(sess.RefreshToken) == "" {
		return conv.ErrSessionExpired
	}
	clientID, err := c.lookupClientID(ctx)
	if err != nil {
		return err
	}
	tok, err := c.oauth.RefreshToken(ctx, clientID, sess.RefreshToken)
	if err != nil {
		c.logger.Warn("clicksign refresh failed",
			slog.String("err", err.Error()),
		)
		return conv.ErrSessionExpired
	}
	sess.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		sess.RefreshToken = tok.RefreshToken
	}
	if tok.ExpiresIn > 0 {
		sess.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UTC()
	}
	sess.UpdatedAt = time.Now().UTC()
	if err := c.store.PutSession(ctx, sess); err != nil {
		c.logger.Warn("clicksign refresh persist failed",
			slog.String("err", err.Error()),
		)
		return conv.ErrSessionExpired
	}
	return nil
}

func (c *Client) lockFor(phone string) *sync.Mutex {
	if v, ok := c.refreshLocks.Load(phone); ok {
		return v.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := c.refreshLocks.LoadOrStore(phone, mu)
	return actual.(*sync.Mutex)
}

func (c *Client) lookupClientID(ctx context.Context) (string, error) {
	c.clientIDOnce.Do(func() {
		reg, err := c.store.GetClientRegistration(ctx)
		if err != nil {
			c.clientIDErr = fmt.Errorf("clicksign: dcr lookup: %w", err)
			return
		}
		c.clientID = reg.ClientID
	})
	if c.clientIDErr != nil {
		return "", c.clientIDErr
	}
	return c.clientID, nil
}

// --- high-level methods ----------------------------------------------------

// ValidateToken hits GET /users/me; useful as a smoke test post-OAuth.
func (c *Client) ValidateToken(ctx context.Context, phone string) (*User, error) {
	raw, _, err := c.do(ctx, phone, http.MethodGet, "/users/me", "", nil)
	if err != nil {
		return nil, err
	}
	var u User
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, fmt.Errorf("clicksign: decode /users/me: %w", err)
	}
	return &u, nil
}

// ListAccounts returns the list of Clicksign accounts the authenticated
// user can act on. Used when a flow needs to render the multi-account
// picker.
func (c *Client) ListAccounts(ctx context.Context, phone string) ([]OAuth2Account, error) {
	raw, _, err := c.do(ctx, phone, http.MethodGet, "/oauth2/accounts", "", nil)
	if err != nil {
		return nil, err
	}
	var resp oauth2AccountsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("clicksign: decode /oauth2/accounts: %w", err)
	}
	return resp.Data, nil
}

// ListTemplates returns all templates available in the currently selected
// account. Returns ErrMultiAccount if PreferredAccount is unset and the
// user has multiple accounts.
func (c *Client) ListTemplates(ctx context.Context, phone string) ([]Template, error) {
	raw, _, err := c.do(ctx, phone, http.MethodGet, "/templates", "", nil)
	if err != nil {
		return nil, err
	}
	var resp templatesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("clicksign: decode /templates: %w", err)
	}
	return resp.Data, nil
}

// GetTemplateFields returns the variable fields of a template.
func (c *Client) GetTemplateFields(ctx context.Context, phone, templateID string) ([]TemplateField, error) {
	raw, _, err := c.do(ctx, phone, http.MethodGet, fmt.Sprintf("/templates/%s/template_fields", templateID), "", nil)
	if err != nil {
		return nil, err
	}
	var resp templateFieldsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("clicksign: decode template_fields: %w", err)
	}
	return resp.Data, nil
}

// CreateTemplate creates a new template from a base64-encoded document.
func (c *Client) CreateTemplate(ctx context.Context, phone string, req CreateTemplateRequest) (*Template, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("clicksign: marshal CreateTemplate: %w", err)
	}
	raw, _, err := c.do(ctx, phone, http.MethodPost, "/templates", contentVndAPI, body)
	if err != nil {
		return nil, err
	}
	var resp templateSingleResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("clicksign: decode create template: %w", err)
	}
	return &resp.Data, nil
}

// UpdateTemplate patches metadata (name/color) of an existing template.
func (c *Client) UpdateTemplate(ctx context.Context, phone, templateID string, req UpdateTemplateRequest) (*Template, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("clicksign: marshal UpdateTemplate: %w", err)
	}
	raw, _, err := c.do(ctx, phone, http.MethodPatch, fmt.Sprintf("/templates/%s", templateID), contentVndAPI, body)
	if err != nil {
		return nil, err
	}
	var resp templateSingleResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("clicksign: decode update template: %w", err)
	}
	return &resp.Data, nil
}

// DeleteTemplate removes a template.
func (c *Client) DeleteTemplate(ctx context.Context, phone, templateID string) error {
	_, _, err := c.do(ctx, phone, http.MethodDelete, fmt.Sprintf("/templates/%s", templateID), "", nil)
	if err != nil && !errors.Is(err, ErrInvalidToken) {
		// 204 No Content is the success path; do() returns nil for 2xx.
		return err
	}
	return nil
}

// ListEnvelopes returns all envelopes in the selected account.
func (c *Client) ListEnvelopes(ctx context.Context, phone string) ([]Envelope, error) {
	raw, _, err := c.do(ctx, phone, http.MethodGet, "/envelopes", "", nil)
	if err != nil {
		return nil, err
	}
	var resp envelopesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("clicksign: decode /envelopes: %w", err)
	}
	return resp.Data, nil
}

// GetEnvelope returns details about a specific envelope.
func (c *Client) GetEnvelope(ctx context.Context, phone, envelopeID string) (*Envelope, error) {
	raw, _, err := c.do(ctx, phone, http.MethodGet, fmt.Sprintf("/envelopes/%s", envelopeID), "", nil)
	if err != nil {
		return nil, err
	}
	var resp envelopeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("clicksign: decode /envelopes/{id}: %w", err)
	}
	return &resp.Data, nil
}

// ListEnvelopeDocuments lists documents within a given envelope.
func (c *Client) ListEnvelopeDocuments(ctx context.Context, phone, envelopeID string) ([]Document, error) {
	raw, _, err := c.do(ctx, phone, http.MethodGet, fmt.Sprintf("/envelopes/%s/documents", envelopeID), "", nil)
	if err != nil {
		return nil, err
	}
	var resp documentsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("clicksign: decode envelope documents: %w", err)
	}
	return resp.Data, nil
}

// GetEnvelopeDocument returns details about a single document of an envelope.
func (c *Client) GetEnvelopeDocument(ctx context.Context, phone, envelopeID, documentID string) (*Document, error) {
	raw, _, err := c.do(ctx, phone, http.MethodGet, fmt.Sprintf("/envelopes/%s/documents/%s", envelopeID, documentID), "", nil)
	if err != nil {
		return nil, err
	}
	var resp documentResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("clicksign: decode envelope document: %w", err)
	}
	return &resp.Data, nil
}

// CreateEnvelopeBulk creates an envelope + document + signers + notifications
// in a single call. This is the recommended path for both
// "create_envelope_with_template" and "create_envelope_with_file_url" flows.
func (c *Client) CreateEnvelopeBulk(ctx context.Context, phone string, req EnvelopeBulkCreationRequest) (*EnvelopeBulkCreationResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("clicksign: marshal bulk creation: %w", err)
	}
	raw, _, err := c.do(ctx, phone, http.MethodPost, "/envelope_bulk_creations", contentJSON, body)
	if err != nil {
		return nil, err
	}
	var resp EnvelopeBulkCreationResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("clicksign: decode bulk creation: %w", err)
	}
	return &resp, nil
}

// NotifyEnvelope sends a reminder to every pending signer in the envelope.
func (c *Client) NotifyEnvelope(ctx context.Context, phone, envelopeID string) error {
	_, _, err := c.do(ctx, phone, http.MethodPost, fmt.Sprintf("/envelopes/%s/notifications", envelopeID), contentVndAPI, []byte("{}"))
	return err
}

// NotifyEnvelopeSigner sends a reminder to a specific signer.
func (c *Client) NotifyEnvelopeSigner(ctx context.Context, phone, envelopeID, signerID string) error {
	_, _, err := c.do(ctx, phone, http.MethodPost, fmt.Sprintf("/envelopes/%s/signers/%s/notifications", envelopeID, signerID), contentVndAPI, []byte("{}"))
	return err
}
