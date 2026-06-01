package flow

import (
	"errors"
	"strings"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
)

// WhatsApp interactive message hard limits. We truncate eagerly so the
// n8n side never has to reject our payload.
const (
	maxListTitle       = 24
	maxListDescription = 72
	maxListItems       = 10 // per WhatsApp; the backend chunks when exceeded
)

// errorResult is a small constructor used by flows to return user-visible
// errors. The Go error returned by Flow.Handle is for logging; Reply is
// what the user sees.
func errorResult(reply string) Result {
	return Result{Kind: KindError, Reply: reply}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	// 1-char headroom for ellipsis when n is large enough.
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// buildAccountList renders an OAuth2 account slice as a WhatsApp list
// payload. Each row id is the account key (UUID) so the next inbound
// turn carries it back in interactive_reply.list_item_id.
func buildAccountList(reply string, accounts []clicksign.OAuth2Account) *InteractivePayload {
	items := make([]InteractiveItem, 0, len(accounts))
	for _, a := range accounts {
		if strings.TrimSpace(a.Attributes.Key) == "" {
			continue
		}
		items = append(items, InteractiveItem{
			ID:    a.Attributes.Key,
			Title: truncate(a.Attributes.Name, maxListTitle),
		})
		if len(items) == maxListItems {
			break
		}
	}
	return &InteractivePayload{
		Type:   "list",
		Header: "Escolha sua conta",
		Body:   reply,
		Items:  items,
	}
}

// accountByKey finds an account by its UUID key. Returns nil when not found.
func accountByKey(accounts []clicksign.OAuth2Account, key string) *clicksign.OAuth2Account {
	for i := range accounts {
		if accounts[i].Attributes.Key == key {
			return &accounts[i]
		}
	}
	return nil
}

// formatTemplateCreated turns a Clicksign timestamp ("2025-12-18T16:37:23.000Z")
// into a short pt-BR date ("18/12/2025"). When the input is unparseable we
// return the original string to avoid hiding information.
func formatTemplateCreated(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.Format("02/01/2006")
		}
	}
	return s
}

// flowDataString safely reads a string from a FlowState.Data map.
func flowDataString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	if v, ok := data[key].(string); ok {
		return v
	}
	return ""
}

// errIs returns true when err matches any of the targets.
func errIs(err error, targets ...error) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}
