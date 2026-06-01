package clicksign

import (
	"errors"
	"fmt"
)

// Sentinel errors classifying common API failures. Flows match against these
// to render natural-language responses to the user.
var (
	// ErrInvalidToken is returned on 401/403 from Clicksign. The session
	// will usually be marked as expired so the next inbound message
	// triggers a fresh OAuth flow.
	ErrInvalidToken = errors.New("clicksign: invalid token")

	// ErrServiceUnavailable is returned on 5xx. Retryable in theory but
	// the flow typically surfaces a friendly "tente em alguns minutos".
	ErrServiceUnavailable = errors.New("clicksign: service unavailable")

	// ErrMultiAccount is returned when the user has multiple Clicksign
	// accounts and X-Account-Key was not provided (or did not match).
	// The wrapping error type *MultiAccountError carries the available
	// accounts so the flow can render a list message.
	ErrMultiAccount = errors.New("clicksign: multiple accounts available")
)

// MultiAccountError is the typed counterpart of ErrMultiAccount. It carries
// the list of accounts the user can choose from. Flows use errors.As to
// pull this out and turn it into a WhatsApp list message.
type MultiAccountError struct {
	// Accounts is empty until the flow explicitly enriches it (typically
	// by calling Client.ListAccounts after seeing this error). The raw
	// error from the failing endpoint does not include account names.
	Accounts []OAuth2Account
}

func (e *MultiAccountError) Error() string {
	if len(e.Accounts) == 0 {
		return "clicksign: multiple Clicksign accounts available; X-Account-Key required"
	}
	return fmt.Sprintf("clicksign: multiple Clicksign accounts available (%d); X-Account-Key required", len(e.Accounts))
}

func (e *MultiAccountError) Is(target error) bool {
	return target == ErrMultiAccount
}

// APIError captures a non-2xx response that did not match a more specific
// classification. It is intentionally typed so flows can inspect Status
// when needed and so logs can preserve the raw body for debugging.
type APIError struct {
	Status   int
	Endpoint string
	Body     []byte
}

func (e *APIError) Error() string {
	return fmt.Sprintf("clicksign api error: status=%d endpoint=%q body=%s", e.Status, e.Endpoint, truncate(string(e.Body), 512))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
