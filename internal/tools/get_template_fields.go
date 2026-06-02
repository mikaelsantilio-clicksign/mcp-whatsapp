package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

func getTemplateFieldsTool(d CatalogDeps) Tool {
	return Tool{
		Name: "get_template_fields",
		Description: "List the variable fields defined in a Clicksign template. Required input: template_id " +
			"(UUID from list_templates). Use this before create_envelope_with_template so the user can fill in " +
			"the right variables (template.data) — each returned field's name is a key expected in template.data.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"template_id": map[string]any{
					"type":        "string",
					"format":      "uuid",
					"description": "Template UUID returned by list_templates.",
				},
			},
			"required": []string{"template_id"},
		},
		Run: func(ctx context.Context, phone string, args map[string]any) (string, error) {
			id := strings.TrimSpace(getString(args, "template_id"))
			if id == "" {
				return "", errors.New("template_id is required")
			}
			fields, err := d.Clicksign.GetTemplateFields(ctx, phone, id)
			if err != nil {
				return "", err
			}
			b, _ := json.Marshal(map[string]any{"template_fields": fields})
			return string(b), nil
		},
	}
}
