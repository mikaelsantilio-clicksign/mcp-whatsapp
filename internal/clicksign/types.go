// Package clicksign implements an HTTP client for the Clicksign REST API
// (https://app.clicksign.com/api/v3). It is used by the Option B "flow"
// pipeline to call Clicksign directly without going through MCP.
//
// Types in this file mirror the JSON:API response/request shapes used by
// Clicksign. They were ported from the clicksign/mcp-api-tavola-v3
// repository (internal/clicksign/types.go) with minor cleanups: we drop
// the OAuth2Credentials encoded-string helper because in this project the
// access token + account key are kept in session.Session and applied via
// HTTP headers (Authorization + X-Account-Key) directly inside Client.do.
package clicksign

// Envelope represents an envelope in Clicksign (JSON:API format).
type Envelope struct {
	ID            string                 `json:"id"`
	Type          string                 `json:"type"`
	Links         map[string]interface{} `json:"links,omitempty"`
	Attributes    EnvelopeAttributes     `json:"attributes"`
	Relationships map[string]interface{} `json:"relationships,omitempty"`
}

// EnvelopeAttributes represents envelope attributes.
type EnvelopeAttributes struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Key       string `json:"key,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Document represents a document in Clicksign (JSON:API format).
type Document struct {
	ID            string                 `json:"id"`
	Type          string                 `json:"type"`
	Links         map[string]interface{} `json:"links,omitempty"`
	Attributes    DocumentAttributes     `json:"attributes"`
	Relationships map[string]interface{} `json:"relationships,omitempty"`
}

// DocumentAttributes represents document attributes.
type DocumentAttributes struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Key       string `json:"key,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Signer represents a signer in Clicksign.
type Signer struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Email     string `json:"email"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// User represents a user from the /users/me endpoint.
type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// OAuth2Account represents an account returned by GET /oauth2/accounts.
type OAuth2Account struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Attributes OAuth2AccountAttributes `json:"attributes"`
}

// OAuth2AccountAttributes represents OAuth2 account attributes.
type OAuth2AccountAttributes struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// Template represents a template in Clicksign (JSON:API format).
type Template struct {
	ID            string                 `json:"id"`
	Type          string                 `json:"type"`
	Links         map[string]interface{} `json:"links,omitempty"`
	Attributes    TemplateAttributes     `json:"attributes"`
	Relationships map[string]interface{} `json:"relationships,omitempty"`
}

// TemplateAttributes represents template attributes.
type TemplateAttributes struct {
	Name     string `json:"name"`
	Color    string `json:"color,omitempty"`
	Created  string `json:"created"`
	Modified string `json:"modified"`
}

// TemplateField represents a field in a template (JSON:API format).
type TemplateField struct {
	ID            string                  `json:"id"`
	Type          string                  `json:"type"`
	Links         map[string]interface{}  `json:"links,omitempty"`
	Attributes    TemplateFieldAttributes `json:"attributes"`
	Relationships map[string]interface{}  `json:"relationships,omitempty"`
}

// TemplateFieldAttributes represents template field attributes.
type TemplateFieldAttributes struct {
	Name     string  `json:"name"`
	Kind     *string `json:"kind"`
	Created  string  `json:"created"`
	Modified string  `json:"modified"`
}

// CreateTemplateRequest is the request body for POST /templates.
// See https://developers.clicksign.com/reference/api-criar-modelo
type CreateTemplateRequest struct {
	Data CreateTemplateData `json:"data"`
}

// CreateTemplateData holds the data object for template creation.
type CreateTemplateData struct {
	Type       string                   `json:"type"` // "templates"
	Attributes CreateTemplateAttributes `json:"attributes"`
}

// CreateTemplateAttributes holds attributes for template creation.
type CreateTemplateAttributes struct {
	Name          string `json:"name"`
	ContentBase64 string `json:"content_base64"`
	Color         string `json:"color,omitempty"`
}

// UpdateTemplateRequest is the request body for PATCH /templates/{id}.
type UpdateTemplateRequest struct {
	Data UpdateTemplateData `json:"data"`
}

// UpdateTemplateData holds the data object for template update.
type UpdateTemplateData struct {
	Type       string                   `json:"type"`
	ID         string                   `json:"id"`
	Attributes UpdateTemplateAttributes `json:"attributes"`
}

// UpdateTemplateAttributes holds attributes for template update.
type UpdateTemplateAttributes struct {
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"`
}

// EnvelopeBulkCreationRequest is the request for envelope bulk creation.
type EnvelopeBulkCreationRequest struct {
	Data EnvelopeBulkCreationData `json:"data"`
}

// EnvelopeBulkCreationData holds the data structure for bulk creation.
type EnvelopeBulkCreationData struct {
	Type       string                         `json:"type"`
	Attributes EnvelopeBulkCreationAttributes `json:"attributes"`
}

// EnvelopeBulkCreationAttributes holds bulk creation attributes.
type EnvelopeBulkCreationAttributes struct {
	Envelope      BulkEnvelope     `json:"envelope"`
	Document      BulkDocument     `json:"document"`
	Signers       []BulkSigner     `json:"signers"`
	Notifications BulkNotification `json:"notifications"`
}

// BulkEnvelope represents envelope configuration in bulk creation.
type BulkEnvelope struct {
	Name              string                 `json:"name"`
	DefaultSubject    string                 `json:"default_subject"`
	Locale            string                 `json:"locale"`
	AutoClose         bool                   `json:"auto_close"`
	RemindInterval    int                    `json:"remind_interval"`
	BlockAfterRefusal bool                   `json:"block_after_refusal"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

// BulkDocument represents document configuration. Either Template or
// ContentBase64 must be set (mutually exclusive per API contract).
type BulkDocument struct {
	Template      *BulkTemplate          `json:"template,omitempty"`
	ContentBase64 string                 `json:"content_base64,omitempty"`
	Filename      string                 `json:"filename"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// BulkTemplate represents template configuration in bulk creation.
type BulkTemplate struct {
	Key  string                 `json:"key"`
	Data map[string]interface{} `json:"data"`
}

// SignatureHost represents the host for presential signature.
type SignatureHost struct {
	Name              string            `json:"name,omitempty"`
	Email             string            `json:"email,omitempty"`
	CommunicateEvents map[string]string `json:"communicate_events,omitempty"`
}

// BulkSigner represents a signer in bulk creation.
type BulkSigner struct {
	Name                    string                 `json:"name"`
	Email                   string                 `json:"email"`
	Birthday                string                 `json:"birthday,omitempty"`
	PhoneNumber             string                 `json:"phone_number,omitempty"`
	LocationRequiredEnabled bool                   `json:"location_required_enabled"`
	HasDocumentation        bool                   `json:"has_documentation"`
	Documentation           string                 `json:"documentation,omitempty"`
	Refusable               bool                   `json:"refusable"`
	CommunicateEvents       *BulkCommunicateEvents `json:"communicate_events,omitempty"`
	Requirements            []BulkRequirement      `json:"requirements,omitempty"`
	SignatureHost           *SignatureHost         `json:"signature_host,omitempty"`
}

// BulkCommunicateEvents represents communication events configuration.
type BulkCommunicateEvents struct {
	SignatureRequest  string `json:"signature_request,omitempty"`
	SignatureReminder string `json:"signature_reminder,omitempty"`
	DocumentSigned    string `json:"document_signed,omitempty"`
}

// BulkRequirement represents a requirement for a signer.
//   - "agree":           qualification — requires "role"
//   - "provide_evidence": authentication — requires "auth"
//   - "rubricate":       rubric — requires "pages" (e.g. "1,3,5")
type BulkRequirement struct {
	Action string `json:"action"`
	Role   string `json:"role,omitempty"`
	Auth   string `json:"auth,omitempty"`
	Pages  string `json:"pages,omitempty"`
}

// BulkNotification represents notification configuration.
type BulkNotification struct {
	Message string `json:"message"`
}

// EnvelopeBulkCreationResponse is the response from envelope bulk creation.
type EnvelopeBulkCreationResponse struct {
	Data EnvelopeBulkCreationResponseData `json:"data"`
}

// EnvelopeBulkCreationResponseData holds the response data.
type EnvelopeBulkCreationResponseData struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		EnvelopeID string `json:"envelope_id"`
		Status     string `json:"status"`
	} `json:"attributes"`
}

// -- internal response wrappers --

type envelopesResponse struct {
	Data  []Envelope             `json:"data"`
	Meta  map[string]interface{} `json:"meta,omitempty"`
	Links map[string]interface{} `json:"links,omitempty"`
}

type envelopeResponse struct {
	Data  Envelope               `json:"data"`
	Meta  map[string]interface{} `json:"meta,omitempty"`
	Links map[string]interface{} `json:"links,omitempty"`
}

type documentsResponse struct {
	Data  []Document             `json:"data"`
	Meta  map[string]interface{} `json:"meta,omitempty"`
	Links map[string]interface{} `json:"links,omitempty"`
}

type documentResponse struct {
	Data  Document               `json:"data"`
	Meta  map[string]interface{} `json:"meta,omitempty"`
	Links map[string]interface{} `json:"links,omitempty"`
}

type templatesResponse struct {
	Data  []Template             `json:"data"`
	Meta  map[string]interface{} `json:"meta,omitempty"`
	Links map[string]interface{} `json:"links,omitempty"`
}

type templateSingleResponse struct {
	Data Template `json:"data"`
}

type templateFieldsResponse struct {
	Data  []TemplateField        `json:"data"`
	Meta  map[string]interface{} `json:"meta,omitempty"`
	Links map[string]interface{} `json:"links,omitempty"`
}

type oauth2AccountsResponse struct {
	Data  []OAuth2Account        `json:"data"`
	Meta  map[string]interface{} `json:"meta,omitempty"`
	Links map[string]interface{} `json:"links,omitempty"`
}
