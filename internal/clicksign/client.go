// Package clicksign is a thin REST client for the Clicksign public API
// (https://app.clicksign.com/api/v3) used to execute the tools exposed to
// the LLM. It owns the per-request OAuth Bearer + X-Account-Key injection
// and a refresh-on-401 path mirroring the one we previously had in the
// retired internal/mcpclient package.
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
	"net/url"
	"strings"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/oauth"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

var (
	// ErrAuthExpired is returned when the stored session is missing or the
	// refresh_token grant fails permanently. Callers should propagate this
	// to the API layer so it can issue a fresh authorize URL.
	ErrAuthExpired = errors.New("clicksign: auth expired")
	// ErrAPI is returned for any non-2xx status that is not 401.
	ErrAPI = errors.New("clicksign: api error")
)

// HTTPClient is the concrete REST client.
type HTTPClient struct {
	baseURL string
	http    *http.Client
	logger  *slog.Logger
	store   session.Store
	oauth   *oauth.Client
}

// NewHTTPClient creates a new REST client. The store/oauth pair is used to
// transparently refresh access tokens on 401.
func NewHTTPClient(
	baseURL string,
	timeout time.Duration,
	logger *slog.Logger,
	store session.Store,
	oauthClient *oauth.Client,
) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
		logger:  logger,
		store:   store,
		oauth:   oauthClient,
	}
}

// request is a description of a single HTTP call, suitable to be re-executed
// after a token refresh (the body is kept as []byte so we can build a fresh
// io.Reader each attempt).
type request struct {
	method      string
	path        string
	query       url.Values
	contentType string
	body        []byte
	// skipAccountKey omits the X-Account-Key header even if the session has
	// one. Used by /oauth2/accounts where the header is not applicable.
	skipAccountKey bool
}

// doForPhone executes the request using the session's access token. On 401
// it refreshes once and retries. Returns the raw response body and status.
func (c *HTTPClient) doForPhone(ctx context.Context, phone string, r request) (int, []byte, error) {
	status, body, err := c.attempt(ctx, phone, r)
	if err != nil {
		return status, body, err
	}
	if status != http.StatusUnauthorized {
		return status, body, nil
	}
	if rErr := c.refresh(ctx, phone); rErr != nil {
		return status, body, ErrAuthExpired
	}
	return c.attempt(ctx, phone, r)
}

func (c *HTTPClient) attempt(ctx context.Context, phone string, r request) (int, []byte, error) {
	sess, err := c.store.GetSession(ctx, phone)
	if err != nil {
		return 0, nil, ErrAuthExpired
	}
	return c.doWithToken(ctx, sess.AccessToken, ifAccountKey(sess, r.skipAccountKey), r, logging.HashPhone(phone))
}

func ifAccountKey(s *session.Session, skip bool) string {
	if skip {
		return ""
	}
	return s.AccountKey
}

// doWithToken executes the request with an explicit access token / account
// key combo (no session lookup). Used by ListOAuth2AccountsWithToken during
// the OAuth callback, before the session is persisted.
func (c *HTTPClient) doWithToken(
	ctx context.Context,
	accessToken, accountKey string,
	r request,
	phoneHash string,
) (int, []byte, error) {
	endpoint := c.baseURL + r.path
	if len(r.query) > 0 {
		endpoint += "?" + r.query.Encode()
	}
	var body io.Reader
	if len(r.body) > 0 {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, endpoint, body)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if accountKey != "" {
		req.Header.Set("X-Account-Key", accountKey)
	}
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}

	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	c.logger.Debug("clicksign_api",
		slog.String("phone_hash", phoneHash),
		slog.String("method", r.method),
		slog.String("path", r.path),
		slog.Int("status", resp.StatusCode),
		slog.Duration("elapsed", time.Since(started)),
	)
	return resp.StatusCode, raw, nil
}

// refresh exchanges the stored refresh_token for a new access_token and
// persists it on the session.
func (c *HTTPClient) refresh(ctx context.Context, phone string) error {
	sess, err := c.store.GetSession(ctx, phone)
	if err != nil || sess.RefreshToken == "" {
		return ErrAuthExpired
	}
	reg, err := c.store.GetClientRegistration(ctx)
	if err != nil {
		return ErrAuthExpired
	}
	token, err := c.oauth.RefreshToken(ctx, reg.ClientID, sess.RefreshToken)
	if err != nil {
		c.logger.Warn("oauth_refresh_failed",
			slog.String("phone_hash", logging.HashPhone(phone)),
			slog.String("err", err.Error()),
		)
		_ = c.store.DeleteSession(ctx, phone)
		return ErrAuthExpired
	}
	sess.AccessToken = token.AccessToken
	if token.RefreshToken != "" {
		sess.RefreshToken = token.RefreshToken
	}
	sess.ExpiresAt = token.ExpiresAt()
	sess.UpdatedAt = time.Now().UTC()
	if err := c.store.PutSession(ctx, sess); err != nil {
		return ErrAuthExpired
	}
	c.logger.Info("oauth_refreshed", slog.String("phone_hash", logging.HashPhone(phone)))
	return nil
}

// apiError wraps a non-2xx status into ErrAPI preserving the body for the
// LLM to inspect.
func apiError(status int, body []byte) error {
	return fmt.Errorf("%w: status %d body=%s", ErrAPI, status, string(body))
}

// decodeOrError unmarshals body into out when status is 2xx, otherwise it
// returns apiError. Used by every endpoint helper.
func decodeOrError(status int, raw []byte, out any) error {
	if status < 200 || status >= 300 {
		return apiError(status, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}
