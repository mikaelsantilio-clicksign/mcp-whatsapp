package nlu

import (
	"testing"
)

func TestParseVerdict_ListTemplates(t *testing.T) {
	raw := `{"intent":"list_templates","entities":{"account_key":null,"account_index":null,"envelope_id":null,"envelope_name":null,"template_id":null,"template_name":null,"pdf_url":null,"filter_status":null,"signers":null},"confidence":"high"}`
	v, err := ParseVerdict(raw)
	if err != nil {
		t.Fatalf("ParseVerdict: %v", err)
	}
	if v.Intent != IntentListTemplates {
		t.Fatalf("intent=%q want %q", v.Intent, IntentListTemplates)
	}
	if v.Confidence != ConfHigh {
		t.Fatalf("confidence=%q want high", v.Confidence)
	}
	if v.Entities.AccountKey != nil || v.Entities.AccountIndex != nil {
		t.Fatalf("expected empty entities, got %#v", v.Entities)
	}
}

func TestParseVerdict_SelectAccountIndex(t *testing.T) {
	raw := `{"intent":"select_account","entities":{"account_index":3},"confidence":"high"}`
	v, err := ParseVerdict(raw)
	if err != nil {
		t.Fatalf("ParseVerdict: %v", err)
	}
	if v.Intent != IntentSelectAccount {
		t.Fatalf("intent=%q want select_account", v.Intent)
	}
	if v.Entities.AccountIndex == nil || *v.Entities.AccountIndex != 3 {
		t.Fatalf("expected account_index=3, got %#v", v.Entities.AccountIndex)
	}
}

func TestParseVerdict_CreateEnvelopePDF(t *testing.T) {
	raw := `{
		"intent":"create_envelope_pdf",
		"entities":{
			"envelope_name":"Contrato 1",
			"pdf_url":"https://x.com/c.pdf",
			"signers":[{"name":"Mikael Nunes","email":"mikael@x.com","role":"party"}]
		},
		"confidence":"high"
	}`
	v, err := ParseVerdict(raw)
	if err != nil {
		t.Fatalf("ParseVerdict: %v", err)
	}
	if v.Intent != IntentCreateEnvelopePDF {
		t.Fatalf("intent=%q", v.Intent)
	}
	if v.Entities.EnvelopeName == nil || *v.Entities.EnvelopeName != "Contrato 1" {
		t.Fatalf("envelope_name unset: %#v", v.Entities.EnvelopeName)
	}
	if v.Entities.PDFURL == nil || *v.Entities.PDFURL != "https://x.com/c.pdf" {
		t.Fatalf("pdf_url unset: %#v", v.Entities.PDFURL)
	}
	if len(v.Entities.Signers) != 1 || v.Entities.Signers[0].Email != "mikael@x.com" {
		t.Fatalf("signers wrong: %#v", v.Entities.Signers)
	}
}

func TestParseVerdict_UnknownIntent(t *testing.T) {
	raw := `{"intent":"weather","confidence":"low"}`
	v, err := ParseVerdict(raw)
	if err != nil {
		t.Fatalf("ParseVerdict: %v", err)
	}
	if v.Intent != IntentUnknown {
		t.Fatalf("expected unknown for unrecognised intent, got %q", v.Intent)
	}
	if v.Confidence != ConfLow {
		t.Fatalf("confidence=%q", v.Confidence)
	}
}

func TestParseVerdict_MarkdownFences(t *testing.T) {
	raw := "```json\n{\"intent\":\"list_envelopes\",\"confidence\":\"medium\"}\n```"
	v, err := ParseVerdict(raw)
	if err != nil {
		t.Fatalf("ParseVerdict: %v", err)
	}
	if v.Intent != IntentListEnvelopes {
		t.Fatalf("intent=%q", v.Intent)
	}
	if v.Confidence != ConfMedium {
		t.Fatalf("confidence=%q", v.Confidence)
	}
}

func TestParseVerdict_MissingConfidence_DefaultsMedium(t *testing.T) {
	raw := `{"intent":"list_templates"}`
	v, err := ParseVerdict(raw)
	if err != nil {
		t.Fatalf("ParseVerdict: %v", err)
	}
	if v.Confidence != ConfMedium {
		t.Fatalf("expected confidence to default to medium, got %q", v.Confidence)
	}
}

func TestEntities_AsMap_DropsNils(t *testing.T) {
	idx := 2
	name := "x"
	e := Entities{
		AccountIndex: &idx,
		EnvelopeName: &name,
	}
	m := e.AsMap()
	if v, ok := m["account_index"]; !ok || v.(int) != 2 {
		t.Fatalf("account_index=%v ok=%v", v, ok)
	}
	if v, ok := m["envelope_name"]; !ok || v.(string) != "x" {
		t.Fatalf("envelope_name=%v ok=%v", v, ok)
	}
	if _, ok := m["account_key"]; ok {
		t.Fatal("account_key should not be present")
	}
}

func TestParseVerdict_EmptyBody(t *testing.T) {
	if _, err := ParseVerdict(""); err == nil {
		t.Fatal("expected error for empty body")
	}
}
