// Package nlu extracts structured intent + entities from a user's
// WhatsApp message. It is the "tradutor" piece of the Option B pipeline:
// the LLM never calls tools nor decides what to say to the user. It only
// produces a JSON Verdict that the flow layer consumes.
package nlu

import (
	"context"
)

// Intent enumerates the actions the user can express. Keep this set in
// sync with the prompt under prompts/nlu.md.
type Intent string

const (
	IntentListTemplates      Intent = "list_templates"
	IntentListEnvelopes      Intent = "list_envelopes"
	IntentEnvelopeStatus     Intent = "envelope_status"
	IntentCreateEnvelopeTmpl Intent = "create_envelope_tmpl"
	IntentCreateEnvelopePDF  Intent = "create_envelope_pdf"
	IntentAddSigner          Intent = "add_signer"
	IntentSelectAccount      Intent = "select_account"
	IntentCancelEnvelope     Intent = "cancel_envelope"
	IntentUnknown            Intent = "unknown"
)

// AllIntents is used by tests and validators.
var AllIntents = []Intent{
	IntentListTemplates,
	IntentListEnvelopes,
	IntentEnvelopeStatus,
	IntentCreateEnvelopeTmpl,
	IntentCreateEnvelopePDF,
	IntentAddSigner,
	IntentSelectAccount,
	IntentCancelEnvelope,
	IntentUnknown,
}

// Confidence is the model's self-rated certainty.
type Confidence string

const (
	ConfHigh   Confidence = "high"
	ConfMedium Confidence = "medium"
	ConfLow    Confidence = "low"
)

// Signer is one row of entities.signers extracted from the message.
type Signer struct {
	Name        string `json:"name,omitempty"`
	Email       string `json:"email,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Role        string `json:"role,omitempty"`
}

// Entities holds every named slot the NLU may extract. Pointers
// (or zero values for collections) flag absence so flows can distinguish
// "user said nothing" from "user said empty".
type Entities struct {
	AccountKey   *string  `json:"account_key,omitempty"`
	AccountIndex *int     `json:"account_index,omitempty"`
	EnvelopeID   *string  `json:"envelope_id,omitempty"`
	EnvelopeName *string  `json:"envelope_name,omitempty"`
	TemplateID   *string  `json:"template_id,omitempty"`
	TemplateName *string  `json:"template_name,omitempty"`
	PDFURL       *string  `json:"pdf_url,omitempty"`
	FilterStatus *string  `json:"filter_status,omitempty"`
	Signers      []Signer `json:"signers,omitempty"`
}

// AsMap projects Entities into a map[string]any suitable for
// flow.Input.Entities. nil-valued fields are dropped so flows can use
// the "comma ok" idiom on the map.
func (e Entities) AsMap() map[string]any {
	out := map[string]any{}
	if e.AccountKey != nil {
		out["account_key"] = *e.AccountKey
	}
	if e.AccountIndex != nil {
		out["account_index"] = *e.AccountIndex
	}
	if e.EnvelopeID != nil {
		out["envelope_id"] = *e.EnvelopeID
	}
	if e.EnvelopeName != nil {
		out["envelope_name"] = *e.EnvelopeName
	}
	if e.TemplateID != nil {
		out["template_id"] = *e.TemplateID
	}
	if e.TemplateName != nil {
		out["template_name"] = *e.TemplateName
	}
	if e.PDFURL != nil {
		out["pdf_url"] = *e.PDFURL
	}
	if e.FilterStatus != nil {
		out["filter_status"] = *e.FilterStatus
	}
	if len(e.Signers) > 0 {
		out["signers"] = e.Signers
	}
	return out
}

// Verdict is the structured NLU output. It mirrors the JSON schema
// described in prompts/nlu.md.
type Verdict struct {
	Intent     Intent     `json:"intent"`
	Entities   Entities   `json:"entities"`
	Confidence Confidence `json:"confidence"`
}

// HistoryTurn is one piece of prior context fed to the NLU. We keep this
// dependency-free (no session.Session) so the package is self-contained.
type HistoryTurn struct {
	Role    string // "user" | "assistant"
	Content string
}

// Extractor is the NLU contract. Production uses OpenAINLU; tests can
// inject any implementation.
type Extractor interface {
	Extract(ctx context.Context, message string, recent []HistoryTurn) (Verdict, error)
}

// Static returns a fixed verdict regardless of the input. Useful for
// development/CI without an OpenAI key.
type Static struct {
	V Verdict
}

// Extract implements Extractor.
func (s Static) Extract(_ context.Context, _ string, _ []HistoryTurn) (Verdict, error) {
	return s.V, nil
}
