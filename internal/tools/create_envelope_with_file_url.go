package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func createEnvelopeWithFileURLTool(d CatalogDeps) Tool {
	return Tool{
		Name: "create_envelope_with_file_url",
		Description: "Create and send an envelope from a public HTTPS file URL. The server downloads, validates and " +
			"base64-encodes the file (pdf, jpg, jpeg, png, txt, doc, docx) before submitting it to Clicksign. " +
			"Required inputs: envelope.name, document.file_url and at least one signer with name + requirements. " +
			"document.filename is optional — when omitted, derived from the URL path; provide explicitly when the URL has no usable basename.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"envelope": envelopeSchemaWithRemind(),
				"document": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_url": map[string]any{
							"type":        "string",
							"format":      "uri",
							"description": "Public HTTPS URL of the file. Schemes other than https are rejected by default.",
						},
						"filename": map[string]any{
							"type":        "string",
							"description": "Optional. File name with an accepted extension (.pdf, .jpg, .jpeg, .png, .txt, .doc, .docx).",
						},
						"metadata": map[string]any{
							"type":        "object",
							"description": "Optional metadata.",
						},
					},
					"required": []string{"file_url"},
				},
				"signers":       signersSchema(),
				"notifications": notificationsSchema(),
			},
			"required": []string{"envelope", "document", "signers"},
		},
		Run: func(ctx context.Context, phone string, args map[string]any) (string, error) {
			if d.FileFetcher == nil {
				return "", errors.New("create_envelope_with_file_url unavailable: FileFetcher is not configured")
			}
			docArgs, _ := args["document"].(map[string]any)
			fileURL := strings.TrimSpace(getString(docArgs, "file_url"))
			if fileURL == "" {
				return "", errors.New("document.file_url is required")
			}
			data, mime, derivedFilename, err := d.FileFetcher.Fetch(ctx, fileURL)
			if err != nil {
				return "", fmt.Errorf("fetch file: %w", err)
			}
			req, err := buildBulkRequestFromFile(args, data, mime, derivedFilename)
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
