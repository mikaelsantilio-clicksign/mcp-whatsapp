package tools

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
)

const (
	bulkResourceType      = "envelope_bulk_creations"
	defaultRemindInterval = 3
	defaultLocale         = "pt-BR"
)

// buildBulkRequestFromTemplate decodes the LLM-provided args into an
// EnvelopeBulkCreationRequest configured with a template (the
// document.template branch of BulkDocument is populated).
func buildBulkRequestFromTemplate(args map[string]any) (clicksign.EnvelopeBulkCreationRequest, error) {
	env, err := parseEnvelope(args)
	if err != nil {
		return clicksign.EnvelopeBulkCreationRequest{}, err
	}
	doc, err := parseTemplateDocument(args)
	if err != nil {
		return clicksign.EnvelopeBulkCreationRequest{}, err
	}
	signers, err := parseSigners(args)
	if err != nil {
		return clicksign.EnvelopeBulkCreationRequest{}, err
	}
	notif := parseNotifications(args)

	return clicksign.EnvelopeBulkCreationRequest{
		Data: clicksign.EnvelopeBulkCreationData{
			Type: bulkResourceType,
			Attributes: clicksign.EnvelopeBulkCreationAttributes{
				Envelope:      env,
				Document:      doc,
				Signers:       signers,
				Notifications: notif,
			},
		},
	}, nil
}

// buildBulkRequestFromFile is the file_url counterpart: ContentBase64 is
// pre-filled from the bytes the FileFetcher already downloaded, and the
// filename comes either from args or from the URL path.
func buildBulkRequestFromFile(args map[string]any, fileBytes []byte, mime, derivedFilename string) (clicksign.EnvelopeBulkCreationRequest, error) {
	env, err := parseEnvelope(args)
	if err != nil {
		return clicksign.EnvelopeBulkCreationRequest{}, err
	}
	docArgs, _ := args["document"].(map[string]any)
	filename := strings.TrimSpace(getString(docArgs, "filename"))
	if filename == "" {
		filename = derivedFilename
	}
	if filename == "" {
		return clicksign.EnvelopeBulkCreationRequest{}, errors.New("document.filename is required and could not be derived from file_url")
	}

	dataURI := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(fileBytes))
	doc := clicksign.BulkDocument{
		Filename:      filename,
		ContentBase64: dataURI,
	}
	if meta, ok := docArgs["metadata"].(map[string]any); ok {
		doc.Metadata = meta
	}

	signers, err := parseSigners(args)
	if err != nil {
		return clicksign.EnvelopeBulkCreationRequest{}, err
	}
	notif := parseNotifications(args)

	return clicksign.EnvelopeBulkCreationRequest{
		Data: clicksign.EnvelopeBulkCreationData{
			Type: bulkResourceType,
			Attributes: clicksign.EnvelopeBulkCreationAttributes{
				Envelope:      env,
				Document:      doc,
				Signers:       signers,
				Notifications: notif,
			},
		},
	}, nil
}

// parseEnvelope extracts the `envelope` argument with defaults.
func parseEnvelope(args map[string]any) (clicksign.BulkEnvelope, error) {
	raw, ok := args["envelope"].(map[string]any)
	if !ok {
		return clicksign.BulkEnvelope{}, errors.New("envelope is required")
	}
	name := strings.TrimSpace(getString(raw, "name"))
	if name == "" {
		return clicksign.BulkEnvelope{}, errors.New("envelope.name is required")
	}
	env := clicksign.BulkEnvelope{
		Name:           name,
		Locale:         defaultLocale,
		RemindInterval: defaultRemindInterval,
	}
	if v, ok := raw["remind_interval"].(float64); ok && v > 0 {
		env.RemindInterval = int(v)
	}
	if meta, ok := raw["metadata"].(map[string]any); ok {
		env.Metadata = meta
	}
	return env, nil
}

// parseTemplateDocument extracts `document` for the template flow. It
// expects document.template.{key,data} and document.filename.
func parseTemplateDocument(args map[string]any) (clicksign.BulkDocument, error) {
	raw, ok := args["document"].(map[string]any)
	if !ok {
		return clicksign.BulkDocument{}, errors.New("document is required")
	}
	filename := strings.TrimSpace(getString(raw, "filename"))
	if filename == "" {
		return clicksign.BulkDocument{}, errors.New("document.filename is required")
	}
	tplRaw, ok := raw["template"].(map[string]any)
	if !ok {
		return clicksign.BulkDocument{}, errors.New("document.template is required")
	}
	key := strings.TrimSpace(getString(tplRaw, "key"))
	if key == "" {
		return clicksign.BulkDocument{}, errors.New("document.template.key is required")
	}
	data, _ := tplRaw["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	doc := clicksign.BulkDocument{
		Filename: filename,
		Template: &clicksign.BulkTemplate{Key: key, Data: data},
	}
	if meta, ok := raw["metadata"].(map[string]any); ok {
		doc.Metadata = meta
	}
	return doc, nil
}

func parseSigners(args map[string]any) ([]clicksign.BulkSigner, error) {
	raw, ok := args["signers"].([]any)
	if !ok || len(raw) == 0 {
		return nil, errors.New("signers is required and must contain at least one entry")
	}
	out := make([]clicksign.BulkSigner, 0, len(raw))
	for i, entry := range raw {
		obj, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("signers[%d] must be an object", i)
		}
		name := strings.TrimSpace(getString(obj, "name"))
		if name == "" {
			return nil, fmt.Errorf("signers[%d].name is required", i)
		}
		s := clicksign.BulkSigner{
			Name:        name,
			Email:       strings.TrimSpace(getString(obj, "email")),
			PhoneNumber: strings.TrimSpace(getString(obj, "phone_number")),
			Birthday:    strings.TrimSpace(getString(obj, "birthday")),
		}
		s.Refusable = getBool(obj, "refusable", true)
		s.HasDocumentation = getBool(obj, "has_documentation", false)
		if doc := strings.TrimSpace(getString(obj, "documentation")); doc != "" {
			s.Documentation = doc
			s.HasDocumentation = true
		}
		reqsRaw, ok := obj["requirements"].([]any)
		if !ok || len(reqsRaw) == 0 {
			return nil, fmt.Errorf("signers[%d].requirements is required", i)
		}
		reqs := make([]clicksign.BulkRequirement, 0, len(reqsRaw))
		for j, r := range reqsRaw {
			robj, ok := r.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("signers[%d].requirements[%d] must be an object", i, j)
			}
			action := strings.TrimSpace(getString(robj, "action"))
			if action == "" {
				return nil, fmt.Errorf("signers[%d].requirements[%d].action is required", i, j)
			}
			reqs = append(reqs, clicksign.BulkRequirement{
				Action: action,
				Role:   strings.TrimSpace(getString(robj, "role")),
				Auth:   strings.TrimSpace(getString(robj, "auth")),
				Pages:  strings.TrimSpace(getString(robj, "pages")),
			})
		}
		s.Requirements = reqs
		out = append(out, s)
	}
	return out, nil
}

func parseNotifications(args map[string]any) clicksign.BulkNotification {
	raw, ok := args["notifications"].(map[string]any)
	if !ok {
		return clicksign.BulkNotification{}
	}
	return clicksign.BulkNotification{Message: strings.TrimSpace(getString(raw, "message"))}
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func getBool(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[key].(bool)
	if !ok {
		return def
	}
	return v
}
