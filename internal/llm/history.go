package llm

import (
	"github.com/openai/openai-go"

	"github.com/clicksign/whatsapp-mcp/internal/classifier"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// maxHistoryTurns caps how many ChatTurn entries we persist back to the
// session. The window is sized to balance context quality against token cost
// and DynamoDB row size when we eventually swap the memory store.
const maxHistoryTurns = 30

// toOpenAIMessage rebuilds an OpenAI Chat Completions message from a
// persisted ChatTurn.
//
// Cases handled:
//   - role=user  → openai.UserMessage(content)
//   - role=tool  → openai.ToolMessage(content, tool_call_id)
//   - role=assistant with content only → openai.AssistantMessage(content)
//   - role=assistant with tool_calls (and optional content)
//     → ChatCompletionAssistantMessageParam built manually
func toOpenAIMessage(t session.ChatTurn) openai.ChatCompletionMessageParamUnion {
	switch t.Role {
	case "user":
		return openai.UserMessage(t.Content)
	case "tool":
		return openai.ToolMessage(t.Content, t.ToolCallID)
	case "assistant":
		if len(t.ToolCalls) == 0 {
			return openai.AssistantMessage(t.Content)
		}
		asst := openai.ChatCompletionAssistantMessageParam{}
		if t.Content != "" {
			asst.Content.OfString = openai.String(t.Content)
		}
		asst.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(t.ToolCalls))
		for i, tc := range t.ToolCalls {
			asst.ToolCalls[i].ID = tc.ID
			asst.ToolCalls[i].Function.Name = tc.Name
			asst.ToolCalls[i].Function.Arguments = tc.Arguments
		}
		return openai.ChatCompletionMessageParamUnion{OfAssistant: &asst}
	default:
		// Unknown role: best-effort treat as user note so the LLM has the
		// content but cannot mistake it for an unbalanced assistant turn.
		return openai.UserMessage(t.Content)
	}
}

// truncateHistory returns the last `max` turns of `turns`, ensuring the
// returned slice starts on a "user" turn so the OpenAI API never sees a
// dangling "tool" or "assistant-with-tool_calls" without its matching
// preceding user message. If no user turn exists in the window, returns nil.
func truncateHistory(turns []session.ChatTurn, max int) []session.ChatTurn {
	if max <= 0 {
		return nil
	}
	if len(turns) <= max {
		return alignToUser(turns)
	}
	start := len(turns) - max
	return alignToUser(turns[start:])
}

func alignToUser(turns []session.ChatTurn) []session.ChatTurn {
	for i, t := range turns {
		if t.Role == "user" {
			return turns[i:]
		}
	}
	return nil
}

// classifierContext picks the last N user/assistant turns from the history
// to be sent as context to the intent classifier. Tool messages are skipped
// (noise for intent classification) and assistant turns that produced only
// tool_calls (no text) are skipped too. The result is in chronological
// order (oldest first), capped at maxTurns.
func classifierContext(turns []session.ChatTurn, maxTurns int) []classifier.HistoryTurn {
	if maxTurns <= 0 || len(turns) == 0 {
		return nil
	}
	out := make([]classifier.HistoryTurn, 0, maxTurns)
	// Walk from the end backwards collecting eligible turns.
	for i := len(turns) - 1; i >= 0 && len(out) < maxTurns; i-- {
		t := turns[i]
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		if t.Content == "" {
			continue
		}
		out = append([]classifier.HistoryTurn{{Role: t.Role, Content: t.Content}}, out...)
	}
	return out
}

// extractToolCalls converts an OpenAI assistant tool_calls slice into our
// persistence-friendly ChatToolCall slice.
func extractToolCalls(in []openai.ChatCompletionMessageToolCall) []session.ChatToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.ChatToolCall, len(in))
	for i, tc := range in {
		out[i] = session.ChatToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		}
	}
	return out
}
