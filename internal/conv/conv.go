// Package conv holds the small dependency-free types shared between the
// HTTP layer and the flow pipeline. It used to host the Conversation
// contract for the legacy MCP/LLM pipeline; that's now gone.
package conv

import "errors"

// Attachment mirrors api.Attachment without coupling the flow package to
// the HTTP layer. The flow pipeline reads this when building bulk
// envelope creation payloads.
type Attachment struct {
	URL      string
	MimeType string
	Filename string
}

// ErrSessionExpired indicates the user needs to re-authenticate via
// OAuth. Both the flow pipeline (clicksign.Client) and the HTTP handler
// rely on this sentinel to switch to the `needs_auth` reply path.
var ErrSessionExpired = errors.New("conv: session expired, needs re-auth")
