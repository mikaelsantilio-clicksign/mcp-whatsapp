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
	"strings"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/llm"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

const (
	EventOAuthSuccess    = "oauth_success"
	EventOAuthFailed     = "oauth_failed"
	EventSessionExpired  = "session_expired"
)

// Notifier pushes proactive messages to n8n. When the webhook URL is unset
// every call becomes a no-op so the rest of the system keeps working.
type Notifier struct {
	logger        *slog.Logger
	url           string
	token         string
	http          *http.Client
	logPayload    bool
}

func NewNotifier(logger *slog.Logger, url, token string) *Notifier {
	return &Notifier{
		logger: logger,
		url:    url,
		token:  token,
		http:   &http.Client{Timeout: 5 * time.Second},
	}
}

// EnablePayloadLogging toggles structured logging of the outgoing body on
// the next send. Intended for debugging multi-account / pending-account
// flows where the on-wire payload differs from the happy path. See
// Config.N8NDebugLogPayload.
func (n *Notifier) EnablePayloadLogging(enabled bool) {
	if n == nil {
		return
	}
	n.logPayload = enabled
}

type payload struct {
	Event       string         `json:"event"`
	PhoneNumber string         `json:"phone_number"`
	Reply       string         `json:"reply"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// webhookAccount is the wire shape of one Clicksign account in the
// `accounts` array sent to n8n. We keep it intentionally minimal: only
// what n8n needs to render a chooser.
type webhookAccount struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// OAuthSuccess pushes the login confirmation to n8n. Two distinct flows:
//
//   - Single-account / auto-selected: accountKey is set, pending is empty.
//     Reply is the canonical "you're in" message and metadata.account_key
//     carries the selected key for downstream nodes.
//   - Multi-account pending selection: accountKey is empty, pending lists
//     the candidates. Reply is the disambiguation message and metadata
//     carries `requires_account_selection=true` + the accounts array so
//     n8n can render the chooser (e.g. interactive list message).
func (n *Notifier) OAuthSuccess(ctx context.Context, phone, accountKey string, pending []session.PendingAccount) error {
	md := map[string]any{}
	reply := llm.OAuthSuccess()

	switch {
	case len(pending) > 0:
		accounts := make([]webhookAccount, 0, len(pending))
		for _, a := range pending {
			accounts = append(accounts, webhookAccount{Key: a.Key, Name: a.Name})
		}
		md["requires_account_selection"] = true
		md["accounts"] = accounts
		reply = accountSelectionPrompt(pending)
	case accountKey != "":
		md["account_key"] = accountKey
	}

	return n.send(ctx, payload{
		Event:       EventOAuthSuccess,
		PhoneNumber: phone,
		Reply:       reply,
		Metadata:    md,
	})
}

// accountSelectionPrompt is the user-facing message rendered in WhatsApp
// when the user has multiple accounts. It enumerates them so the user can
// reply with the ordinal ("1"), the name ("a Acme") or even the key
// directly — the LLM will resolve any of those via select_account.
func accountSelectionPrompt(accounts []session.PendingAccount) string {
	var sb strings.Builder
	sb.WriteString("Pronto, conexão feita! ✅\n\n")
	sb.WriteString("Você tem mais de uma conta Clicksign vinculada. Qual delas você quer usar agora?\n\n")
	for i, a := range accounts {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, a.Name)
	}
	sb.WriteString("\nResponda com o nome ou o número da conta.")
	return sb.String()
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
	// Optional debug: log the exact body we're about to POST. We log
	// AFTER encoding (so what we log matches what goes on the wire) and
	// trim the trailing newline json.Encoder appends.
	if n.logPayload && n.logger != nil {
		raw := strings.TrimRight(buf.String(), "\n")
		n.logger.Info("n8n_notify_payload",
			slog.String("event", body.Event),
			slog.String("phone_hash", logging.HashPhone(body.PhoneNumber)),
			slog.String("url", n.url),
			slog.Int("reply_len", len(body.Reply)),
			slog.String("body", raw),
		)
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

