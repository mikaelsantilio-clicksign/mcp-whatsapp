package mcpclient

import (
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
)

// OpenAITool mirrors the shape expected by the OpenAI Chat Completions
// `tools` field. We keep it dependency-free here to avoid coupling this
// package with the OpenAI SDK.
type OpenAITool struct {
	Type     string             `json:"type"`
	Function OpenAIToolFunction `json:"function"`
}

type OpenAIToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ToOpenAITools converts a list of MCP tools into OpenAI tool definitions.
// The MCP InputSchema (JSON schema) is forwarded as-is into `parameters`.
func ToOpenAITools(tools []mcp.Tool) ([]OpenAITool, error) {
	out := make([]OpenAITool, 0, len(tools))
	for _, t := range tools {
		params, err := toolParameters(t)
		if err != nil {
			return nil, err
		}
		out = append(out, OpenAITool{
			Type: "function",
			Function: OpenAIToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return out, nil
}

func toolParameters(t mcp.Tool) (map[string]any, error) {
	if t.RawInputSchema != nil && len(t.RawInputSchema) > 0 {
		var m map[string]any
		if err := json.Unmarshal(t.RawInputSchema, &m); err != nil {
			return nil, err
		}
		return m, nil
	}
	b, err := json.Marshal(t.InputSchema)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return m, nil
}
