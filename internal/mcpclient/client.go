// Package mcpclient wraps the mark3labs/mcp-go streamable HTTP client with
// per-user OAuth bearer injection, transparent token refresh on 401 and a
// small in-memory tools cache.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/clicksign/whatsapp-mcp/internal/config"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/oauth"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// ErrAuthExpired is returned when refresh fails permanently and the user
// must re-authenticate. Callers should propagate ErrSessionExpired to the API
// layer so it can issue a fresh authorize URL.
var ErrAuthExpired = errors.New("mcpclient: auth expired")

// Manager owns the configuration shared by all per-request MCP sessions and
// caches the tools list.
type Manager struct {
	cfg    *config.Config
	logger *slog.Logger
	store  session.Store
	oauth  *oauth.Client

	toolsMu     sync.RWMutex
	toolsCache  []mcp.Tool
	toolsExpiry time.Time
	toolsTTL    time.Duration
}

func NewManager(cfg *config.Config, logger *slog.Logger, store session.Store, oauthClient *oauth.Client) *Manager {
	return &Manager{
		cfg:      cfg,
		logger:   logger,
		store:    store,
		oauth:    oauthClient,
		toolsTTL: 5 * time.Minute,
	}
}

// Conn is a short-lived MCP client tied to a single phone/session. Always
// call Close() when done.
type Conn struct {
	mgr   *Manager
	phone string
	c     *mcpclient.Client
}

// Open creates and initializes a streamable HTTP MCP client for the given
// phone, using its stored access token. Returns ErrAuthExpired if the
// session is missing.
func (m *Manager) Open(ctx context.Context, phone string) (*Conn, error) {
	sess, err := m.store.GetSession(ctx, phone)
	if err != nil {
		return nil, ErrAuthExpired
	}
	headerFn := func(ctx context.Context) map[string]string {
		token, _ := bearerFromContext(ctx)
		if token == "" {
			token = sess.AccessToken
		}
		return map[string]string{
			"Authorization": "Bearer " + token,
		}
	}

	cli, err := mcpclient.NewStreamableHttpClient(
		m.cfg.MCPEndpointURL(),
		transport.WithHTTPHeaderFunc(headerFn),
		transport.WithHTTPTimeout(60*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("mcp client: %w", err)
	}
	if err := cli.Start(ctx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("mcp start: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "whatsapp-mcp", Version: "0.1.0"}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		_ = cli.Close()
		if isUnauthorized(err) {
			return m.openWithRefresh(ctx, phone)
		}
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}

	return &Conn{mgr: m, phone: phone, c: cli}, nil
}

func (m *Manager) openWithRefresh(ctx context.Context, phone string) (*Conn, error) {
	if err := m.refresh(ctx, phone); err != nil {
		return nil, err
	}
	// Recurse once after a successful refresh.
	return m.Open(ctx, phone)
}

// refresh exchanges the refresh_token for a new access_token and updates the
// stored session. Returns ErrAuthExpired on failure.
func (m *Manager) refresh(ctx context.Context, phone string) error {
	sess, err := m.store.GetSession(ctx, phone)
	if err != nil || sess.RefreshToken == "" {
		return ErrAuthExpired
	}
	reg, err := m.store.GetClientRegistration(ctx)
	if err != nil {
		return ErrAuthExpired
	}
	token, err := m.oauth.RefreshToken(ctx, reg.ClientID, sess.RefreshToken)
	if err != nil {
		m.logger.Warn("oauth_refresh_failed",
			slog.String("phone_hash", logging.HashPhone(phone)),
			slog.String("err", err.Error()),
		)
		_ = m.store.DeleteSession(ctx, phone)
		return ErrAuthExpired
	}
	sess.AccessToken = token.AccessToken
	if token.RefreshToken != "" {
		sess.RefreshToken = token.RefreshToken
	}
	sess.ExpiresAt = token.ExpiresAt()
	sess.UpdatedAt = time.Now().UTC()
	if err := m.store.PutSession(ctx, sess); err != nil {
		return ErrAuthExpired
	}
	m.logger.Info("oauth_refreshed", slog.String("phone_hash", logging.HashPhone(phone)))
	return nil
}

// Close closes the underlying transport.
func (c *Conn) Close() error {
	if c == nil || c.c == nil {
		return nil
	}
	return c.c.Close()
}

// ListTools returns the cached list (refreshing it on TTL miss) using this
// connection's credentials. The tools surface is server-global so any
// authenticated session can populate the cache.
func (m *Manager) ListTools(ctx context.Context, c *Conn) ([]mcp.Tool, error) {
	m.toolsMu.RLock()
	if time.Now().Before(m.toolsExpiry) && len(m.toolsCache) > 0 {
		out := append([]mcp.Tool(nil), m.toolsCache...)
		m.toolsMu.RUnlock()
		return out, nil
	}
	m.toolsMu.RUnlock()

	res, err := c.c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		if isUnauthorized(err) {
			if refreshErr := m.refresh(ctx, c.phone); refreshErr != nil {
				return nil, refreshErr
			}
			res, err = c.c.ListTools(ctx, mcp.ListToolsRequest{})
			if err != nil {
				return nil, fmt.Errorf("list_tools (post-refresh): %w", err)
			}
		} else {
			return nil, fmt.Errorf("list_tools: %w", err)
		}
	}

	m.toolsMu.Lock()
	m.toolsCache = append([]mcp.Tool(nil), res.Tools...)
	m.toolsExpiry = time.Now().Add(m.toolsTTL)
	m.toolsMu.Unlock()

	return append([]mcp.Tool(nil), res.Tools...), nil
}

// CallTool invokes a single tool with the given arguments.
func (m *Manager) CallTool(ctx context.Context, c *Conn, name string, args map[string]any) (*mcp.CallToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	res, err := c.c.CallTool(ctx, req)
	if err == nil {
		return res, nil
	}
	if isUnauthorized(err) {
		if refreshErr := m.refresh(ctx, c.phone); refreshErr != nil {
			return nil, refreshErr
		}
		res, err = c.c.CallTool(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("call_tool (post-refresh): %w", err)
		}
		return res, nil
	}
	return nil, fmt.Errorf("call_tool: %w", err)
}

// ExtractText collects all text-content blocks from a CallToolResult as a
// single string. Non-text content is JSON-encoded for the LLM to inspect.
func ExtractText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			return string(b)
		}
	}
	var out string
	for _, content := range res.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			out += tc.Text
			continue
		}
		if b, err := json.Marshal(content); err == nil {
			out += string(b)
		}
	}
	return out
}

func isUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, transport.ErrAuthorizationRequired) || errors.Is(err, transport.ErrOAuthAuthorizationRequired) {
		return true
	}
	var are *transport.AuthorizationRequiredError
	if errors.As(err, &are) {
		return true
	}
	// Fallback: some transports embed the status code in the message.
	msg := err.Error()
	return contains(msg, "401") || contains(msg, "Unauthorized") || contains(msg, "unauthorized")
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

type ctxBearerKey struct{}

func contextWithBearer(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxBearerKey{}, token)
}

func bearerFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxBearerKey{}).(string)
	return v, ok
}
