// Package conv defines the dependency-free contract between the HTTP layer
// (api) and the LLM/MCP pipeline (llm). Keeping it in its own package avoids
// import cycles between api and llm.
package conv

import (
	"context"
	"errors"
)

// Attachment mirrors api.Attachment without coupling the llm package to the
// HTTP layer.
type Attachment struct {
	URL      string
	MimeType string
	Filename string
}

// Input is the data the conversation pipeline needs from one incoming message.
type Input struct {
	Phone       string
	Message     string
	Attachments []Attachment
}

// Output is what the pipeline produces.
type Output struct {
	Reply     string
	ToolCalls []ToolCallTrace
}

// ToolCallTrace is a lightweight record for observability and the HTTP response.
type ToolCallTrace struct {
	Name string
	OK   bool
	Err  string
}

// Conversation is the interface the HTTP handler depends on.
type Conversation interface {
	Run(ctx context.Context, in Input) (Output, error)
}

// ErrSessionExpired indicates the user needs to re-authenticate via OAuth.
var ErrSessionExpired = errors.New("conv: session expired, needs re-auth")
