package llm

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestSummarizeToolsForMetaPrompt_Empty(t *testing.T) {
	got := summarizeToolsForMetaPrompt(nil)
	if !strings.Contains(got, "indisponível") {
		t.Fatalf("expected placeholder for empty list, got %q", got)
	}

	got = summarizeToolsForMetaPrompt([]mcp.Tool{})
	if !strings.Contains(got, "indisponível") {
		t.Fatalf("expected placeholder for empty slice, got %q", got)
	}
}

func TestSummarizeToolsForMetaPrompt_AllBlankNames(t *testing.T) {
	got := summarizeToolsForMetaPrompt([]mcp.Tool{{Name: "  ", Description: "x"}})
	if !strings.Contains(got, "indisponível") {
		t.Fatalf("expected placeholder when every name is blank, got %q", got)
	}
}

func TestSummarizeToolsForMetaPrompt_RendersNameAndDescription(t *testing.T) {
	tools := []mcp.Tool{
		{Name: "list_templates", Description: "List all templates available to the user"},
		{Name: "create_envelope", Description: "Create a new envelope from a PDF"},
	}
	got := summarizeToolsForMetaPrompt(tools)
	if !strings.Contains(got, "- list_templates: List all templates available to the user") {
		t.Fatalf("missing list_templates line, got %q", got)
	}
	if !strings.Contains(got, "- create_envelope: Create a new envelope from a PDF") {
		t.Fatalf("missing create_envelope line, got %q", got)
	}
}

func TestSummarizeToolsForMetaPrompt_TruncatesLongDescription(t *testing.T) {
	long := strings.Repeat("x", 400)
	tools := []mcp.Tool{{Name: "tool_a", Description: long}}
	got := summarizeToolsForMetaPrompt(tools)
	if !strings.Contains(got, "…") {
		t.Fatalf("expected ellipsis on long description, got %q", got)
	}
	if strings.Count(got, "x") > 250 {
		t.Fatalf("expected truncation around 240 chars, got %d x", strings.Count(got, "x"))
	}
}

func TestSummarizeToolsForMetaPrompt_SkipsBlankNamesKeepsRest(t *testing.T) {
	tools := []mcp.Tool{
		{Name: "", Description: "noise"},
		{Name: "real_tool", Description: "real"},
	}
	got := summarizeToolsForMetaPrompt(tools)
	if strings.Contains(got, "noise") {
		t.Fatalf("blank-name tool should be skipped, got %q", got)
	}
	if !strings.Contains(got, "real_tool") {
		t.Fatalf("real tool missing, got %q", got)
	}
}
