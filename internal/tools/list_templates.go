package tools

import (
	"context"
	"encoding/json"
)

func listTemplatesTool(d CatalogDeps) Tool {
	return Tool{
		Name:        "list_templates",
		Description: "List Clicksign templates available for the currently selected account.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Run: func(ctx context.Context, phone string, _ map[string]any) (string, error) {
			tpls, err := d.Clicksign.ListTemplates(ctx, phone)
			if err != nil {
				return "", err
			}
			b, _ := json.Marshal(map[string]any{"templates": tpls})
			return string(b), nil
		},
	}
}
