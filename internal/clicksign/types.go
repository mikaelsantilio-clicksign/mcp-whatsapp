package clicksign

// JSON:API envelope wrappers ---------------------------------------------------

// Envelope is the JSON:API resource for a Clicksign envelope.
type Envelope struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Attributes EnvelopeAttributes `json:"attributes"`
}

type EnvelopeAttributes struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Key       string `json:"key,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Template is the JSON:API resource for a Clicksign template.
type Template struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Attributes TemplateAttributes `json:"attributes"`
}

type TemplateAttributes struct {
	Name     string `json:"name"`
	Color    string `json:"color,omitempty"`
	Created  string `json:"created,omitempty"`
	Modified string `json:"modified,omitempty"`
}

// TemplateField is one variable defined in a template (kind is nullable in
// the upstream payload, hence the pointer).
type TemplateField struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Attributes TemplateFieldAttributes `json:"attributes"`
}

type TemplateFieldAttributes struct {
	Name     string  `json:"name"`
	Kind     *string `json:"kind"`
	Created  string  `json:"created,omitempty"`
	Modified string  `json:"modified,omitempty"`
}

// OAuth2Account is one account the OAuth2 grant has access to.
type OAuth2Account struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Attributes OAuth2AccountAttributes `json:"attributes"`
}

type OAuth2AccountAttributes struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// Bulk creation request -------------------------------------------------------
//
// POST /envelope_bulk_creations accepts a single payload that creates the
// envelope, attaches one document (from a template OR from base64 content),
// adds the signers, sets the requirements and sends notifications. It is the
// single endpoint behind both create_envelope_with_template and
// create_envelope_with_file_url.

type EnvelopeBulkCreationRequest struct {
	Data EnvelopeBulkCreationData `json:"data"`
}

type EnvelopeBulkCreationData struct {
	Type       string                         `json:"type"`
	Attributes EnvelopeBulkCreationAttributes `json:"attributes"`
}

type EnvelopeBulkCreationAttributes struct {
	Envelope      BulkEnvelope     `json:"envelope"`
	Document      BulkDocument     `json:"document"`
	Signers       []BulkSigner     `json:"signers"`
	Notifications BulkNotification `json:"notifications"`
}

type BulkEnvelope struct {
	Name              string         `json:"name"`
	DefaultSubject    string         `json:"default_subject,omitempty"`
	Locale            string         `json:"locale,omitempty"`
	AutoClose         bool           `json:"auto_close"`
	RemindInterval    int            `json:"remind_interval"`
	BlockAfterRefusal bool           `json:"block_after_refusal"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// BulkDocument is the document attached to the envelope. Either Template OR
// ContentBase64 must be populated (the API rejects payloads with both).
type BulkDocument struct {
	Template      *BulkTemplate  `json:"template,omitempty"`
	ContentBase64 string         `json:"content_base64,omitempty"`
	Filename      string         `json:"filename"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

type BulkTemplate struct {
	Key  string         `json:"key"`
	Data map[string]any `json:"data"`
}

type BulkSigner struct {
	Name              string                 `json:"name"`
	Email             string                 `json:"email,omitempty"`
	PhoneNumber       string                 `json:"phone_number,omitempty"`
	Birthday          string                 `json:"birthday,omitempty"`
	HasDocumentation  bool                   `json:"has_documentation"`
	Documentation     string                 `json:"documentation,omitempty"`
	Refusable         bool                   `json:"refusable"`
	CommunicateEvents *BulkCommunicateEvents `json:"communicate_events,omitempty"`
	Requirements      []BulkRequirement      `json:"requirements,omitempty"`
}

type BulkCommunicateEvents struct {
	SignatureRequest  string `json:"signature_request,omitempty"`
	SignatureReminder string `json:"signature_reminder,omitempty"`
	DocumentSigned    string `json:"document_signed,omitempty"`
}

// BulkRequirement actions:
//   - "agree"            (qualification)  — requires Role  (sign, witness, ...)
//   - "provide_evidence" (authentication) — requires Auth  (email, sms, whatsapp, ...)
//   - "rubricate"        (rubric)         — requires Pages (e.g. "1,3,5")
type BulkRequirement struct {
	Action string `json:"action"`
	Role   string `json:"role,omitempty"`
	Auth   string `json:"auth,omitempty"`
	Pages  string `json:"pages,omitempty"`
}

type BulkNotification struct {
	Message string `json:"message,omitempty"`
}

// EnvelopeBulkCreationResponse is the response from POST /envelope_bulk_creations.
type EnvelopeBulkCreationResponse struct {
	Data EnvelopeBulkCreationResponseData `json:"data"`
}

type EnvelopeBulkCreationResponseData struct {
	ID         string                                 `json:"id"`
	Type       string                                 `json:"type"`
	Attributes EnvelopeBulkCreationResponseAttributes `json:"attributes"`
}

type EnvelopeBulkCreationResponseAttributes struct {
	EnvelopeID string `json:"envelope_id"`
	Status     string `json:"status"`
}

// Internal response wrappers ---------------------------------------------------

type envelopesResponse struct {
	Data []Envelope `json:"data"`
}

type templatesResponse struct {
	Data []Template `json:"data"`
}

type templateFieldsResponse struct {
	Data []TemplateField `json:"data"`
}

type oauth2AccountsResponse struct {
	Data []OAuth2Account `json:"data"`
}
