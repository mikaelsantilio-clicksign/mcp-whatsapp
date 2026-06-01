package llm

import (
	"testing"

	"github.com/clicksign/whatsapp-mcp/internal/session"
)

func TestTruncateHistory_ShorterThanMax(t *testing.T) {
	turns := []session.ChatTurn{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	got := truncateHistory(turns, 30)
	if len(got) != 2 {
		t.Fatalf("expected unchanged length, got %d", len(got))
	}
}

func TestTruncateHistory_DropsTrailingTailToFirstUser(t *testing.T) {
	// 6 turns; max=3 → window starts at index 3 which is "tool".
	// Should advance forward to the next "user" (index 4? — depends on layout).
	turns := []session.ChatTurn{
		{Role: "user", Content: "1"},
		{Role: "assistant", ToolCalls: []session.ChatToolCall{{ID: "a"}}},
		{Role: "tool", Content: "r", ToolCallID: "a"},
		{Role: "assistant", Content: "done1"},
		{Role: "user", Content: "2"},
		{Role: "assistant", Content: "done2"},
	}
	got := truncateHistory(turns, 3)
	// window = last 3 turns = ["assistant done1", "user 2", "assistant done2"]
	// must start at user → drops the leading assistant.
	if len(got) != 2 {
		t.Fatalf("expected 2 turns after alignment, got %d (%v)", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "2" {
		t.Fatalf("expected first to be user '2', got %+v", got[0])
	}
}

func TestTruncateHistory_NoUserInWindowReturnsNil(t *testing.T) {
	turns := []session.ChatTurn{
		{Role: "user", Content: "1"},
		{Role: "assistant", ToolCalls: []session.ChatToolCall{{ID: "a"}}},
		{Role: "tool", ToolCallID: "a"},
		{Role: "assistant", ToolCalls: []session.ChatToolCall{{ID: "b"}}},
		{Role: "tool", ToolCallID: "b"},
	}
	got := truncateHistory(turns, 2)
	// window is the last 2 turns (assistant + tool), no user → must drop all.
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestTruncateHistory_ZeroMax(t *testing.T) {
	if got := truncateHistory([]session.ChatTurn{{Role: "user"}}, 0); got != nil {
		t.Fatalf("expected nil for max=0, got %+v", got)
	}
}

func TestToOpenAIMessage_AssistantWithToolCalls(t *testing.T) {
	turn := session.ChatTurn{
		Role: "assistant",
		ToolCalls: []session.ChatToolCall{
			{ID: "call_1", Name: "list_templates", Arguments: "{}"},
		},
	}
	msg := toOpenAIMessage(turn)
	if msg.OfAssistant == nil {
		t.Fatalf("expected OfAssistant to be set")
	}
	if len(msg.OfAssistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(msg.OfAssistant.ToolCalls))
	}
	if msg.OfAssistant.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool_call id mismatch: %s", msg.OfAssistant.ToolCalls[0].ID)
	}
	if msg.OfAssistant.ToolCalls[0].Function.Name != "list_templates" {
		t.Fatalf("function name mismatch: %s", msg.OfAssistant.ToolCalls[0].Function.Name)
	}
}

func TestToOpenAIMessage_ToolRole(t *testing.T) {
	turn := session.ChatTurn{Role: "tool", Content: "{\"ok\":true}", ToolCallID: "call_1"}
	msg := toOpenAIMessage(turn)
	if msg.OfTool == nil {
		t.Fatalf("expected OfTool to be set")
	}
	if msg.OfTool.ToolCallID != "call_1" {
		t.Fatalf("tool_call_id mismatch: %s", msg.OfTool.ToolCallID)
	}
}

func TestToOpenAIMessage_UserRole(t *testing.T) {
	turn := session.ChatTurn{Role: "user", Content: "olá"}
	msg := toOpenAIMessage(turn)
	if msg.OfUser == nil {
		t.Fatalf("expected OfUser to be set")
	}
}

func TestClassifierContext_FiltersToolAndEmpty(t *testing.T) {
	turns := []session.ChatTurn{
		{Role: "user", Content: "lista os templates"},
		{Role: "assistant", ToolCalls: []session.ChatToolCall{{ID: "a", Name: "list_templates"}}}, // no content → skip
		{Role: "tool", Content: "{\"err\": \"multi accounts\"}", ToolCallID: "a"},                  // tool role → skip
		{Role: "assistant", Content: "voce tem 3 contas..."},
		{Role: "user", Content: "use a conta 3"},
	}
	got := classifierContext(turns, 4)
	if len(got) != 3 {
		t.Fatalf("expected 3 turns (user, assistant text, user), got %d: %+v", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "lista os templates" {
		t.Fatalf("unexpected first turn: %+v", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content != "voce tem 3 contas..." {
		t.Fatalf("unexpected second turn: %+v", got[1])
	}
	if got[2].Role != "user" || got[2].Content != "use a conta 3" {
		t.Fatalf("unexpected last turn: %+v", got[2])
	}
}

func TestClassifierContext_CapsAtMaxTurns(t *testing.T) {
	turns := []session.ChatTurn{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
		{Role: "assistant", Content: "d"},
		{Role: "user", Content: "e"},
	}
	got := classifierContext(turns, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 turns (capped), got %d", len(got))
	}
	// Should be the LATEST two in chronological order.
	if got[0].Content != "d" || got[1].Content != "e" {
		t.Fatalf("unexpected order: %+v", got)
	}
}

func TestClassifierContext_ZeroOrEmpty(t *testing.T) {
	if got := classifierContext(nil, 4); got != nil {
		t.Fatalf("expected nil for empty history, got %+v", got)
	}
	if got := classifierContext([]session.ChatTurn{{Role: "user", Content: "x"}}, 0); got != nil {
		t.Fatalf("expected nil for maxTurns=0, got %+v", got)
	}
}
