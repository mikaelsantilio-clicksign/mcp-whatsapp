package tools

import (
	"context"
	"encoding/json"
)

func listEnvelopesTool(d CatalogDeps) Tool {
	return Tool{
		Name:        "list_envelopes",
		Description: "List Clicksign envelopes for the currently selected account. Optional filters: status and limit.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"description": "Optional status filter (e.g. running, closed, canceled). Omit to list all statuses.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     100,
					"description": "Optional maximum number of envelopes to return (1-100).",
				},
			},
		},
		Run: func(ctx context.Context, phone string, args map[string]any) (string, error) {
			statusFilter := getString(args, "status")
			limit := 0
			if v, ok := args["limit"].(float64); ok {
				limit = int(v)
			}
			envs, err := d.Clicksign.ListEnvelopes(ctx, phone, statusFilter, limit)
			if err != nil {
				return "", err
			}
			b, _ := json.Marshal(map[string]any{"envelopes": envs})
			return string(b), nil
		},
	}
}
