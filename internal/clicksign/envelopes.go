package clicksign

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// ListOAuth2AccountsWithToken lists the accounts the OAuth2 grant has access
// to using an explicit access_token (the session may not exist yet during
// the OAuth callback).
func (c *HTTPClient) ListOAuth2AccountsWithToken(ctx context.Context, accessToken string) ([]OAuth2Account, error) {
	status, raw, err := c.doWithToken(ctx, accessToken, "", request{
		method: http.MethodGet,
		path:   "/oauth2/accounts",
	}, "")
	if err != nil {
		return nil, err
	}
	var out oauth2AccountsResponse
	if err := decodeOrError(status, raw, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// ListOAuth2Accounts is the session-bound variant used by select_account /
// re-list flows after the user is already authenticated.
func (c *HTTPClient) ListOAuth2Accounts(ctx context.Context, phone string) ([]OAuth2Account, error) {
	status, raw, err := c.doForPhone(ctx, phone, request{
		method:         http.MethodGet,
		path:           "/oauth2/accounts",
		skipAccountKey: true,
	})
	if err != nil {
		return nil, err
	}
	var out oauth2AccountsResponse
	if err := decodeOrError(status, raw, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// ListEnvelopes lists envelopes for the session's selected account. Both
// filters are optional: pass empty string / 0 to omit.
func (c *HTTPClient) ListEnvelopes(ctx context.Context, phone, statusFilter string, limit int) ([]Envelope, error) {
	q := url.Values{}
	if statusFilter != "" {
		q.Set("status", statusFilter)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	status, raw, err := c.doForPhone(ctx, phone, request{
		method: http.MethodGet,
		path:   "/envelopes",
		query:  q,
	})
	if err != nil {
		return nil, err
	}
	var out envelopesResponse
	if err := decodeOrError(status, raw, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// CreateEnvelopeBulkCreation hits POST /envelope_bulk_creations with the
// fully-built payload. It powers both create_envelope_with_template and
// create_envelope_with_file_url (the caller decides which BulkDocument
// shape to build).
func (c *HTTPClient) CreateEnvelopeBulkCreation(
	ctx context.Context,
	phone string,
	req EnvelopeBulkCreationRequest,
) (*EnvelopeBulkCreationResponse, error) {
	body, err := encodeJSON(req)
	if err != nil {
		return nil, err
	}
	status, raw, err := c.doForPhone(ctx, phone, request{
		method:      http.MethodPost,
		path:        "/envelope_bulk_creations",
		contentType: "application/json",
		body:        body,
	})
	if err != nil {
		return nil, err
	}
	var out EnvelopeBulkCreationResponse
	if err := decodeOrError(status, raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
