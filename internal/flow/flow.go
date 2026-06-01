// Package flow implements the NLU + Guided Flow pipeline (Option B).
//
// A Flow is a small state machine. The Router routes inbound messages to
// the appropriate Flow based on the user's NLU-extracted intent (or the
// active flow when one is open). Each Flow returns a Result indicating
// what the WhatsApp side should show next (text, list, buttons, etc.)
// and an optional NextState that gets persisted in session.Session.
package flow

import (
	"context"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// Kind is the shape of the next user-facing message.
type Kind int

const (
	KindAsk      Kind = iota // ask for free text (e.g. "qual o nome do envelope?")
	KindChoose               // show a list to pick from
	KindConfirm              // yes/no with quick reply buttons
	KindDone                 // operation completed, nothing else to do
	KindError                // unrecoverable error; surface Reply as-is
	KindTransfer             // hand control off to another flow
)

// Input is the structured request a Flow receives. It is built by the
// pipeline from MessageRequest + NLU verdict + session state.
type Input struct {
	Phone       string
	Session     *session.Session
	Intent      string
	Entities    map[string]any
	State       *session.FlowState // nil when starting fresh
	Interact    *InteractiveReply  // non-nil when this turn came from a click
	Attachments []Attachment
}

// Result is what a Flow returns to the pipeline; the messages_handler
// translates this into the wire MessageResponse.
type Result struct {
	Kind        Kind
	Reply       string
	Interactive *InteractivePayload
	// NextState is what gets persisted in session.Session.ActiveFlow. nil
	// means "clear the active flow" (operation done or unrecoverable).
	NextState *session.FlowState
	// NextIntent is set only when Kind == KindTransfer to tell the router
	// which flow should take over without waiting for another user turn.
	NextIntent string
	// NextEntities can carry data from the transferring flow to the next.
	NextEntities map[string]any
	Trace        []TraceStep
}

// Flow is implemented by every state machine in this package.
type Flow interface {
	ID() string
	Handle(ctx context.Context, in Input) (Result, error)
}

// --- wire-facing types --------------------------------------------------
//
// These are deliberately defined here (and re-used by internal/api) so the
// Result returned by a Flow can be serialised straight into the HTTP
// response without an intermediate conversion struct.

// Attachment mirrors the inbound MessageRequest.attachments[] item.
type Attachment struct {
	URL      string `json:"url"`
	MimeType string `json:"mime_type,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// InteractivePayload describes a WhatsApp interactive message (list or
// quick-reply buttons). The n8n workflow maps this to the proper
// WhatsApp Business API call.
type InteractivePayload struct {
	Type   string            `json:"type"` // "list" | "buttons"
	Header string            `json:"header,omitempty"`
	Body   string            `json:"body,omitempty"`
	Footer string            `json:"footer,omitempty"`
	Items  []InteractiveItem `json:"items"`
}

// InteractiveItem is a single row (list) or a single button.
type InteractiveItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// InteractiveReply is the inbound counterpart of InteractivePayload.
// Exactly one of ListItemID / ButtonID is populated.
type InteractiveReply struct {
	ListItemID string `json:"list_item_id,omitempty"`
	ButtonID   string `json:"button_id,omitempty"`
}

// FlowStateDigest is a minimal projection of the active flow surfaced to
// the n8n side so the next turn (typically a click) can be correlated.
// The full state stays server-side in session.Session.ActiveFlow.
type FlowStateDigest struct {
	FlowID  string    `json:"flow_id"`
	Step    string    `json:"step"`
	AskedAt time.Time `json:"asked_at"`
}

// TraceStep records one observable event during request processing.
type TraceStep struct {
	Kind     string `json:"kind"` // "classifier" | "nlu" | "api_call" | "flow_decision"
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Duration string `json:"duration,omitempty"`
	Err      string `json:"err,omitempty"`
}

// DigestFromState extracts a digest from the persisted FlowState. Returns
// nil when state is nil (operation completed).
func DigestFromState(s *session.FlowState) *FlowStateDigest {
	if s == nil {
		return nil
	}
	return &FlowStateDigest{
		FlowID:  s.FlowID,
		Step:    s.Step,
		AskedAt: s.AskedAt,
	}
}
