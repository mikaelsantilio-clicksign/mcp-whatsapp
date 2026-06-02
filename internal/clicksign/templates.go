package clicksign

import (
	"context"
	"fmt"
	"net/http"
)

// ListTemplates lists Clicksign templates available for the session's
// selected account.
func (c *HTTPClient) ListTemplates(ctx context.Context, phone string) ([]Template, error) {
	status, raw, err := c.doForPhone(ctx, phone, request{
		method: http.MethodGet,
		path:   "/templates",
	})
	if err != nil {
		return nil, err
	}
	var out templatesResponse
	if err := decodeOrError(status, raw, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GetTemplateFields lists the variable fields defined in a template. The
// returned `attributes.name` values are the keys expected in
// `document.template.data` when creating an envelope from a template.
func (c *HTTPClient) GetTemplateFields(ctx context.Context, phone, templateID string) ([]TemplateField, error) {
	status, raw, err := c.doForPhone(ctx, phone, request{
		method: http.MethodGet,
		path:   fmt.Sprintf("/templates/%s/template_fields", templateID),
	})
	if err != nil {
		return nil, err
	}
	var out templateFieldsResponse
	if err := decodeOrError(status, raw, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
