package tools

// envelopeSchemaWithRemind is the JSON-Schema object shared by both
// create_envelope_with_* tools' `envelope` argument.
func envelopeSchemaWithRemind() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Envelope settings.",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Envelope name (required by Clicksign).",
			},
			"remind_interval": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Optional. Days between reminders. Defaults to 3 when omitted.",
			},
		},
		"required": []string{"name"},
	}
}

// signersSchema describes the array of signers accepted by both creation
// tools. Detailed contact + requirements rules live in the description so
// the LLM can pick the right channel (email/sms/whatsapp) without us
// re-encoding all the conditional logic in JSON-Schema.
func signersSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"minItems":    1,
		"description": "At least one signer. Each signer must include name and requirements (one with action=agree and a role, one with action=provide_evidence and an auth). The contact channel is conditional: provide email when any auth=email, or phone_number when auth=sms or auth=whatsapp.",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Full name with first name and last name (no digits).",
				},
				"email": map[string]any{
					"type":        "string",
					"format":      "email",
					"description": "Signer email. Required only when any requirement uses auth=email.",
				},
				"phone_number": map[string]any{
					"type":        "string",
					"description": "Signer phone (10 or 11 digits). Required only when any requirement uses auth=sms or auth=whatsapp.",
				},
				"requirements": map[string]any{
					"type":        "array",
					"minItems":    2,
					"description": "At least two items: one with action=agree and role=sign|witness|...; one with action=provide_evidence and auth=email|sms|whatsapp|...",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"action": map[string]any{"type": "string"},
							"role":   map[string]any{"type": "string"},
							"auth":   map[string]any{"type": "string"},
						},
						"required": []string{"action"},
					},
				},
			},
			"required": []string{"name", "requirements"},
		},
	}
}

func notificationsSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Optional notification settings. Omit when no custom message is needed.",
		"properties": map[string]any{
			"message": map[string]any{
				"type":        "string",
				"description": "Message sent to signers.",
			},
		},
	}
}
