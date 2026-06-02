// Package n8n implements the outbound webhook used to push proactive
// WhatsApp messages back to the user via n8n (e.g. on OAuth success).
package n8n

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/llm"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
)

const (
	EventOAuthSuccess   = "oauth_success"
	EventOAuthFailed    = "oauth_failed"
	EventSessionExpired = "session_expired"
)

// Notifier pushes proactive messages to n8n. When the webhook URL is unset
// every call becomes a no-op so the rest of the system keeps working.
type Notifier struct {
	logger *slog.Logger
	url    string
	token  string
	http   *http.Client
}

func NewNotifier(logger *slog.Logger, url, token string) *Notifier {
	return &Notifier{
		logger: logger,
		url:    url,
		token:  token,
		http:   &http.Client{Timeout: 5 * time.Second},
	}
}

type payload struct {
	Event       string         `json:"event"`
	PhoneNumber string         `json:"phone_number"`
	Reply       string         `json:"reply"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// OAuthSuccess sends a confirmation message to the user via n8n.
func (n *Notifier) OAuthSuccess(ctx context.Context, phone, accountKey string) error {
	md := map[string]any{}
	if accountKey != "" {
		md["account_key"] = accountKey
	}
	return n.send(ctx, payload{
		Event:       EventOAuthSuccess,
		PhoneNumber: phone,
		Reply:       llm.OAuthSuccess(),
		Metadata:    md,
	})
}

// SessionExpired tells the user (proactively) that their session expired.
func (n *Notifier) SessionExpired(ctx context.Context, phone string) error {
	return n.send(ctx, payload{
		Event:       EventSessionExpired,
		PhoneNumber: phone,
		Reply:       llm.SessionExpired("(envie qualquer mensagem aqui para receber um novo link)"),
	})
}

func (n *Notifier) send(ctx context.Context, body payload) error {
	if n == nil || n.url == "" {
		n.debugSkip(body)
		return nil
	}
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("n8n webhook: %s: %s", resp.Status, string(raw))
	}
	if n.logger != nil {
		n.logger.Info("n8n_notify_ok",
			slog.String("event", body.Event),
			slog.String("phone_hash", logging.HashPhone(body.PhoneNumber)),
		)
	}
	return nil
}

func (n *Notifier) debugSkip(body payload) {
	if n == nil || n.logger == nil {
		return
	}
	n.logger.Debug("n8n_notify_skipped_no_url",
		slog.String("event", body.Event),
		slog.String("phone_hash", logging.HashPhone(body.PhoneNumber)),
	)
}
