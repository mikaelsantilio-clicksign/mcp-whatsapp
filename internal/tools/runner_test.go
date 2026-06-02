package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStaticRunner_ListAndCall(t *testing.T) {
	called := false
	runner := NewStaticRunner([]Tool{
		{
			Name:        "noop",
			Description: "no-op",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			Run: func(ctx context.Context, phone string, args map[string]any) (string, error) {
				called = true
				return `{"ok":true}`, nil
			},
		},
	})

	list, err := runner.List(context.Background(), "+1")
	if err != nil || len(list) != 1 || list[0].Name != "noop" {
		t.Fatalf("List=%+v err=%v", list, err)
	}

	out, err := runner.Call(context.Background(), "+1", "noop", map[string]any{})
	if err != nil || out != `{"ok":true}` || !called {
		t.Fatalf("Call out=%q err=%v called=%v", out, err, called)
	}

	if _, err := runner.Call(context.Background(), "+1", "ghost", nil); err == nil {
		t.Fatalf("expected ErrToolNotFound")
	}
}

func TestBuildBulkRequestFromTemplate(t *testing.T) {
	args := map[string]any{
		"envelope": map[string]any{"name": "Contract", "remind_interval": float64(5)},
		"document": map[string]any{
			"filename": "Contract.docx",
			"template": map[string]any{
				"key":  "tpl-1",
				"data": map[string]any{"name": "Joao"},
			},
		},
		"signers": []any{
			map[string]any{
				"name":  "Joao",
				"email": "j@x.com",
				"requirements": []any{
					map[string]any{"action": "agree", "role": "sign"},
					map[string]any{"action": "provide_evidence", "auth": "email"},
				},
			},
		},
		"notifications": map[string]any{"message": "Por favor assine."},
	}

	req, err := buildBulkRequestFromTemplate(args)
	if err != nil {
		t.Fatalf("buildBulkRequestFromTemplate: %v", err)
	}
	// Bug regression: data.type must be the hyphenated, singular JSON:API
	// resource type — NOT the URL path's snake_case plural.
	if req.Data.Type != "envelope-bulk-creation" {
		t.Errorf("data.type=%q (want envelope-bulk-creation)", req.Data.Type)
	}
	attrs := req.Data.Attributes
	if attrs.Envelope.Name != "Contract" || attrs.Envelope.RemindInterval != 5 {
		t.Errorf("envelope=%+v", attrs.Envelope)
	}
	if attrs.Document.Template == nil || attrs.Document.Template.Key != "tpl-1" {
		t.Errorf("document.template=%+v", attrs.Document.Template)
	}
	if attrs.Document.Filename != "Contract.docx" {
		t.Errorf("document.filename=%q", attrs.Document.Filename)
	}
	if len(attrs.Signers) != 1 || attrs.Signers[0].Email != "j@x.com" {
		t.Errorf("signers=%+v", attrs.Signers)
	}
	if len(attrs.Signers[0].Requirements) != 2 {
		t.Errorf("requirements=%+v", attrs.Signers[0].Requirements)
	}
	if attrs.Notifications.Message != "Por favor assine." {
		t.Errorf("notifications.message=%q", attrs.Notifications.Message)
	}

	// Bug regression: metadata must be present (non-nil) on both envelope
	// and document, with the default source/origin defaults filled in.
	if attrs.Envelope.Metadata == nil {
		t.Fatal("envelope.metadata must not be nil")
	}
	if got := attrs.Envelope.Metadata["source"]; got != "api" {
		t.Errorf("envelope.metadata.source=%v (want api)", got)
	}
	if got := attrs.Envelope.Metadata["origin"]; got != "whatsapp-mcp" {
		t.Errorf("envelope.metadata.origin=%v (want whatsapp-mcp)", got)
	}
	if attrs.Document.Metadata == nil {
		t.Fatal("document.metadata must not be nil")
	}
	if got := attrs.Document.Metadata["source"]; got != "api" {
		t.Errorf("document.metadata.source=%v (want api)", got)
	}

	// JSON round-trip smoke test: verify the encoded body carries both
	// `metadata` keys (not omitted) and the singular type.
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal req: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `"type":"envelope-bulk-creation"`) {
		t.Errorf("encoded body missing singular type, got: %s", body)
	}
	if !strings.Contains(body, `"metadata":{`) {
		t.Errorf("encoded body missing metadata object, got: %s", body)
	}
}

func TestEnsureSourceMetadata_FillsDefaultsAndPreservesExtras(t *testing.T) {
	out := ensureSourceMetadata(nil)
	if out["source"] != "api" || out["origin"] != "whatsapp-mcp" {
		t.Errorf("defaults missing: %+v", out)
	}

	out = ensureSourceMetadata(map[string]any{
		"source": "external-system",
		"extra":  "kept",
	})
	if out["source"] != "external-system" {
		t.Errorf("source should not be overwritten when present: %+v", out)
	}
	if out["origin"] != "whatsapp-mcp" {
		t.Errorf("origin default missing: %+v", out)
	}
	if out["extra"] != "kept" {
		t.Errorf("extra keys were dropped: %+v", out)
	}
}

func TestBuildBulkRequestFromTemplate_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing envelope", map[string]any{}},
		{"missing envelope.name", map[string]any{"envelope": map[string]any{}}},
		{"missing document", map[string]any{"envelope": map[string]any{"name": "x"}}},
		{"missing document.template", map[string]any{
			"envelope": map[string]any{"name": "x"},
			"document": map[string]any{"filename": "x.docx"},
		}},
		{"missing template.key", map[string]any{
			"envelope": map[string]any{"name": "x"},
			"document": map[string]any{"filename": "x.docx", "template": map[string]any{"data": map[string]any{}}},
		}},
		{"missing signers", map[string]any{
			"envelope": map[string]any{"name": "x"},
			"document": map[string]any{"filename": "x.docx", "template": map[string]any{"key": "t", "data": map[string]any{}}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := buildBulkRequestFromTemplate(c.args); err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
		})
	}
}

func TestBuildBulkRequestFromFile(t *testing.T) {
	args := map[string]any{
		"envelope": map[string]any{"name": "Doc"},
		"document": map[string]any{}, // no filename → derived
		"signers": []any{
			map[string]any{
				"name":         "Joao",
				"phone_number": "11999999999",
				"requirements": []any{
					map[string]any{"action": "agree", "role": "sign"},
					map[string]any{"action": "provide_evidence", "auth": "whatsapp"},
				},
			},
		},
	}
	req, err := buildBulkRequestFromFile(args, []byte("hello"), "application/pdf", "from-url.pdf")
	if err != nil {
		t.Fatalf("buildBulkRequestFromFile: %v", err)
	}
	if req.Data.Attributes.Document.Filename != "from-url.pdf" {
		t.Errorf("filename=%q", req.Data.Attributes.Document.Filename)
	}
	if req.Data.Attributes.Document.ContentBase64 == "" || req.Data.Attributes.Document.Template != nil {
		t.Errorf("expected ContentBase64 only, got %+v", req.Data.Attributes.Document)
	}
}
