package session

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("session: not found")
	ErrExpired  = errors.New("session: expired")
)

// Session represents an authenticated user (keyed by phone_number).
type Session struct {
	PhoneNumber  string     `json:"phone_number"`
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	ExpiresAt    time.Time  `json:"expires_at"`
	// AccountKey is the X-Account-Key header sent on every REST call.
	// Empty when the user has multiple accounts and hasn't picked one yet.
	AccountKey string `json:"account_key,omitempty"`
	// PendingAccounts lists the candidate accounts when the user has >1
	// Clicksign account on the OAuth grant. It is populated by the OAuth
	// callback and cleared by the select_account tool. While non-empty,
	// the conversation layer injects a high-priority preamble into the
	// system prompt so the LLM asks the user to pick one.
	PendingAccounts []PendingAccount `json:"pending_accounts,omitempty"`
	History         []ChatTurn       `json:"history,omitempty"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

// PendingAccount is a candidate Clicksign account awaiting user selection.
// We persist only what we need to render the disambiguation prompt: the
// stable account_key (passed to X-Account-Key) and the display name.
type PendingAccount struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// ChatTurn is one persisted message in the conversation history with the
// LLM. It is mapped to/from OpenAI Chat Completions messages by the llm
// package. We keep this dependency-free so the session package stays
// independent of the LLM SDK.
type ChatTurn struct {
	// Role is one of "user", "assistant", "tool".
	Role string `json:"role"`
	// Content is the textual content. Optional for assistant turns that
	// only have tool_calls.
	Content string `json:"content,omitempty"`
	// ToolCalls is populated for assistant turns that invoked tools.
	ToolCalls []ChatToolCall `json:"tool_calls,omitempty"`
	// ToolCallID is populated for "tool" role turns and references the id
	// of the tool_call from the prior assistant message.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ChatToolCall mirrors the subset of OpenAI's tool_call we need to replay.
type ChatToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Pending represents an in-flight OAuth2 authorization (state + PKCE).
type Pending struct {
	State        string    `json:"state"`
	LinkToken    string    `json:"link_token"`
	AuthorizeURL string    `json:"authorize_url"`
	PhoneNumber  string    `json:"phone_number"`
	CodeVerifier string    `json:"code_verifier"`
	Nonce        string    `json:"nonce"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ClientRegistration is the singleton record holding the dynamically
// registered OAuth client_id (DCR) for our service.
type ClientRegistration struct {
	ClientID                string    `json:"client_id"`
	RegisteredAt            time.Time `json:"registered_at"`
	TokenEndpointAuthMethod string    `json:"token_endpoint_auth_method"`
	RedirectURIs            []string  `json:"redirect_uris"`
}

// Store is the persistence interface for sessions, pending OAuth flows and the
// dynamic client registration record.
type Store interface {
	// Sessions
	GetSession(ctx context.Context, phoneNumber string) (*Session, error)
	PutSession(ctx context.Context, s *Session) error
	DeleteSession(ctx context.Context, phoneNumber string) error

	// Pending OAuth flows (indexed both by state and link_token)
	PutPending(ctx context.Context, p *Pending) error
	GetPendingByState(ctx context.Context, state string) (*Pending, error)
	GetPendingByLinkToken(ctx context.Context, linkToken string) (*Pending, error)
	DeletePending(ctx context.Context, state string) error

	// DCR singleton
	GetClientRegistration(ctx context.Context) (*ClientRegistration, error)
	PutClientRegistration(ctx context.Context, r *ClientRegistration) error
}
