package tools

import (
	"context"
	"encoding/json"
)

func createEnvelopeWithTemplateTool(d CatalogDeps) Tool {
	return Tool{
		Name: "create_envelope_with_template",
		Description: "Create and send an envelope using a Clicksign template. Required inputs: envelope.name, " +
			"document.template.key + document.template.data, document.filename (.doc/.docx) and at least one " +
			"signer with name + requirements (one qualification action=agree and one authentication " +
			"action=provide_evidence). When auth=email the signer needs an email; when auth=sms or " +
			"auth=whatsapp the signer needs phone_number. notifications.message is optional.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"envelope": envelopeSchemaWithRemind(),
				"document": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"filename": map[string]any{
							"type":        "string",
							"description": "File name ending in .doc or .docx (e.g. \"Contract.docx\").",
						},
						"template": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"key": map[string]any{
									"type":        "string",
									"format":      "uuid",
									"description": "Template UUID returned by list_templates.",
								},
								"data": map[string]any{
									"type":        "object",
									"description": "Map of template variable values keyed by the field names from get_template_fields.",
								},
							},
							"required": []string{"key", "data"},
						},
					},
					"required": []string{"filename", "template"},
				},
				"signers":       signersSchema(),
				"notifications": notificationsSchema(),
			},
			"required": []string{"envelope", "document", "signers"},
		},
		Run: func(ctx context.Context, phone string, args map[string]any) (string, error) {
			req, err := buildBulkRequestFromTemplate(args)
			if err != nil {
				return "", err
			}
			resp, err := d.Clicksign.CreateEnvelopeBulkCreation(ctx, phone, req)
			if err != nil {
				return "", err
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		},
	}
}
