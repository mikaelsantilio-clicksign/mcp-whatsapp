// Package classifier decides whether an incoming WhatsApp message belongs
// to the Clicksign domain (on-topic) or should be politely refused
// (off-topic). Used as a cheap gate in front of the main LLM+MCP pipeline
// to bound cost and reduce abuse surface.
//
// The package is intentionally dependency-free of the LLM and session
// packages: it only consumes/returns plain types. The llm package adapts
// its session.ChatTurn history into []HistoryTurn before calling.
package classifier

import "context"

// Intent enumerates the categories the classifier can return.
// Adding a new category means: extend the prompt, the switch in
// Conversation.Run, and (optionally) introduce a new static reply.
type Intent string

const (
	// IntentOnTopic is a Clicksign operation or a follow-up to one.
	IntentOnTopic Intent = "on_topic"
	// IntentMetaHelp is a greeting or a question about the assistant
	// itself ("o que você faz?", "oi", "/help"). Routed to a static
	// capabilities reply without invoking the main LLM or MCP.
	IntentMetaHelp Intent = "meta_help"
	// IntentOffTopic is out-of-scope (math, jailbreak, smalltalk
	// unrelated to Clicksign). Routed to a polite refusal.
	IntentOffTopic Intent = "off_topic"
)

// Verdict is the decision produced by a Classifier plus a short
// reason string (for logging/debug).
type Verdict struct {
	Intent Intent
	Reason string
}

// HistoryTurn is a minimal representation of a recent conversation turn
// passed as context to the classifier. Only user/assistant roles matter
// for intent classification; tool roles are filtered upstream.
type HistoryTurn struct {
	Role    string // "user" | "assistant"
	Content string
}

// Classifier returns a verdict for a given message + recent context.
// Implementations should be safe to call concurrently.
type Classifier interface {
	Classify(ctx context.Context, message string, recent []HistoryTurn) (Verdict, error)
}

// AlwaysOnTopic is a no-op classifier used when the feature is disabled
// (e.g. local development) or as a fallback after irrecoverable errors.
type AlwaysOnTopic struct{}

func (AlwaysOnTopic) Classify(_ context.Context, _ string, _ []HistoryTurn) (Verdict, error) {
	return Verdict{Intent: IntentOnTopic, Reason: "classifier-disabled"}, nil
}
