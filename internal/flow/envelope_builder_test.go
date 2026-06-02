package flow

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDeriveFilenameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://x.com/contrato.pdf":              "contrato.pdf",
		"https://x.com/pasta/Recibo%20Final.docx": "Recibo Final.docx",
		"https://x.com/no-extension":              "",
		"https://x.com/cake.zip":                  "",
		"":                                        "",
		"https://x.com/":                          "",
	}
	for in, want := range cases {
		if got := DeriveFilenameFromURL(in); got != want {
			t.Errorf("DeriveFilenameFromURL(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFilenameFromMIME(t *testing.T) {
	if FilenameFromMIME("application/pdf") != "documento.pdf" {
		t.Fatal("pdf->documento.pdf")
	}
	if FilenameFromMIME("image/png") != "documento.png" {
		t.Fatal("png->documento.png")
	}
	if FilenameFromMIME("application/octet-stream") != "documento.pdf" {
		t.Fatal("unknown defaults to pdf")
	}
}

func TestBuildBulkRequest_PDFPath(t *testing.T) {
	draft := EnvelopeDraft{
		Name: "Contrato STG 1",
		Document: DocumentDraft{
			FileURL: "https://x.com/contrato.pdf",
		},
		Signers: []ValidatedSignerWithRaw{
			{ValidatedSigner: ValidatedSigner{Name: "Mikael Nunes", Email: "m@x.com", Role: "party"}, RoleRaw: "parte"},
		},
	}
	bytes := []byte("%PDF-1.4 fake")
	req, err := BuildBulkRequest(draft, bytes, "application/pdf")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if req.Data.Type != "envelope_bulk_creations" {
		t.Fatalf("type=%q", req.Data.Type)
	}
	attrs := req.Data.Attributes
	if attrs.Envelope.Name != "Contrato STG 1" {
		t.Fatalf("envelope name=%q", attrs.Envelope.Name)
	}
	if attrs.Envelope.Locale != "pt-BR" {
		t.Fatalf("locale=%q", attrs.Envelope.Locale)
	}
	if attrs.Envelope.RemindInterval != 3 {
		t.Fatalf("remind_interval=%d", attrs.Envelope.RemindInterval)
	}
	// Document base64 must be data URI.
	if !strings.HasPrefix(attrs.Document.ContentBase64, "data:application/pdf;base64,") {
		t.Fatalf("missing data URI prefix: %q", attrs.Document.ContentBase64[:40])
	}
	encoded := strings.TrimPrefix(attrs.Document.ContentBase64, "data:application/pdf;base64,")
	decoded, derr := base64.StdEncoding.DecodeString(encoded)
	if derr != nil || string(decoded) != string(bytes) {
		t.Fatalf("base64 roundtrip mismatch")
	}
	if attrs.Document.Filename != "contrato.pdf" {
		t.Fatalf("filename=%q want contrato.pdf", attrs.Document.Filename)
	}
	if len(attrs.Signers) != 1 {
		t.Fatalf("signers len=%d", len(attrs.Signers))
	}
	if attrs.Signers[0].Email != "m@x.com" {
		t.Fatalf("signer email=%q", attrs.Signers[0].Email)
	}
	if attrs.Notifications.Message == "" {
		t.Fatal("notification message should not be empty")
	}
}

func TestBuildBulkRequest_TemplatePath(t *testing.T) {
	draft := EnvelopeDraft{
		Name: "Contrato Tmpl",
		Document: DocumentDraft{
			TemplateID: "tpl-uuid-1",
		},
		Signers: []ValidatedSignerWithRaw{
			{ValidatedSigner: ValidatedSigner{Name: "Ana Silva", Email: "a@x.com", Role: "witness"}},
		},
	}
	req, err := BuildBulkRequest(draft, nil, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	doc := req.Data.Attributes.Document
	if doc.ContentBase64 != "" {
		t.Fatal("template path should not carry base64")
	}
	if doc.Template == nil || doc.Template.Key != "tpl-uuid-1" {
		t.Fatalf("template key=%#v", doc.Template)
	}
	if doc.Template.Data == nil {
		t.Fatal("template.data must be a (possibly empty) object, not nil")
	}
	if !strings.HasSuffix(doc.Filename, ".docx") {
		t.Fatalf("default tmpl filename=%q should end .docx", doc.Filename)
	}
}

func TestBuildBulkRequest_RejectsEmptyDraft(t *testing.T) {
	_, err := BuildBulkRequest(EnvelopeDraft{}, nil, "")
	if err == nil {
		t.Fatal("expected error on empty draft")
	}
}

func TestBuildBulkRequest_PDFWithoutBytesFails(t *testing.T) {
	draft := EnvelopeDraft{
		Name:     "X",
		Document: DocumentDraft{FileURL: "https://x.com/a.pdf"},
		Signers:  []ValidatedSignerWithRaw{{ValidatedSigner: ValidatedSigner{Name: "A B", Email: "a@b.c", Role: "sign"}}},
	}
	_, err := BuildBulkRequest(draft, nil, "application/pdf")
	if err == nil {
		t.Fatal("expected error when PDF bytes missing")
	}
}
