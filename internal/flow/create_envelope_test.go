package flow

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// stubFetcher is a FileFetcher that returns canned bytes + mime.
type stubFetcher struct {
	bytes []byte
	mime  string
	err   error
	calls int
}

func (s *stubFetcher) Fetch(_ context.Context, _ string) ([]byte, string, error) {
	s.calls++
	return s.bytes, s.mime, s.err
}

// --- CreateEnvelopePDFFlow -------------------------------------------------

func TestCreateEnvelopePDF_NoAccount_Transfers(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("API should not be called before account is selected")
	})
	f := NewCreateEnvelopePDFFlow(cs, &stubFetcher{})
	sess := &session.Session{PhoneNumber: "5511999"}
	res, _ := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess, Intent: "create_envelope_pdf"})
	if res.Kind != KindTransfer || res.NextIntent != "select_account" {
		t.Fatalf("expected transfer to select_account, got %#v", res)
	}
}

func TestCreateEnvelopePDF_MissingFields_AsksWithoutAPICall(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("API should not be called during gathering")
	})
	f := NewCreateEnvelopePDFFlow(cs, &stubFetcher{})
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}

	res, _ := f.Handle(context.Background(), Input{
		Phone:   "5511999",
		Session: sess,
		Intent:  "create_envelope_pdf",
		// Only the name; missing pdf_url and signers.
		Entities: map[string]any{"envelope_name": "Contrato 1"},
	})
	if res.Kind != KindAsk {
		t.Fatalf("expected KindAsk, got %v", res.Kind)
	}
	if !strings.Contains(res.Reply, "PDF") {
		t.Fatalf("missing PDF hint in ask: %q", res.Reply)
	}
	if res.NextState == nil || res.NextState.Step != stepGatheringPDF {
		t.Fatalf("expected gathering NextState, got %#v", res.NextState)
	}
	// Partial data should be persisted.
	if got, _ := res.NextState.Data[pdfKeyName].(string); got != "Contrato 1" {
		t.Fatalf("Data not persisted, got %#v", res.NextState.Data)
	}
}

func TestCreateEnvelopePDF_GathersAcrossTurns(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("API should not be called until confirm")
	})
	f := NewCreateEnvelopePDFFlow(cs, &stubFetcher{})
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}

	// Turn 1: just the name.
	res1, _ := f.Handle(context.Background(), Input{
		Phone:    "5511999",
		Session:  sess,
		Entities: map[string]any{"envelope_name": "Contrato 1"},
	})
	if res1.Kind != KindAsk {
		t.Fatalf("turn 1: expected KindAsk, got %v", res1.Kind)
	}

	// Turn 2: pdf_url + signers, resume from turn1 state.
	res2, _ := f.Handle(context.Background(), Input{
		Phone:   "5511999",
		Session: sess,
		State:   res1.NextState,
		Entities: map[string]any{
			"pdf_url": "https://x.com/c.pdf",
			"signers": []any{
				map[string]any{"name": "Mikael Nunes", "email": "m@x.com", "role": "parte"},
			},
		},
	})
	if res2.Kind != KindConfirm {
		t.Fatalf("turn 2: expected KindConfirm, got %v: %#v", res2.Kind, res2)
	}
	if res2.Interactive == nil || res2.Interactive.Type != "buttons" {
		t.Fatalf("expected buttons interactive, got %#v", res2.Interactive)
	}
	if len(res2.Interactive.Items) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(res2.Interactive.Items))
	}
	if res2.NextState == nil || res2.NextState.Step != stepConfirmPDF {
		t.Fatalf("expected awaiting_confirm, got %#v", res2.NextState)
	}
}

func TestCreateEnvelopePDF_ConfirmYes_CallsAPI(t *testing.T) {
	var captured map[string]any
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/envelope_bulk_creations" {
			t.Errorf("path=%q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"data":{"id":"bc-1","type":"envelope_bulk_creations","attributes":{"envelope_id":"env-NEW","status":"queued"}}}`))
	})
	fetcher := &stubFetcher{bytes: []byte("%PDF-1.4"), mime: "application/pdf"}
	f := NewCreateEnvelopePDFFlow(cs, fetcher)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	ctx := clicksign.WithSession(context.Background(), sess)

	state := &session.FlowState{
		FlowID: "create_envelope_pdf",
		Step:   stepConfirmPDF,
		Data: map[string]any{
			pdfKeyName: "Contrato STG 1",
			pdfKeyURL:  "https://x.com/c.pdf",
			pdfKeySigners: []any{
				map[string]any{"name": "Mikael Nunes", "email": "m@x.com", "role": "parte"},
			},
		},
	}
	res, err := f.Handle(ctx, Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "create_envelope_pdf",
		State:    state,
		Interact: &InteractiveReply{ButtonID: "confirm_yes"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v: %#v", res.Kind, res)
	}
	if !strings.Contains(res.Reply, "env-NEW") {
		t.Fatalf("reply missing envelope id: %q", res.Reply)
	}
	if fetcher.calls != 1 {
		t.Fatalf("fetcher should be called once, got %d", fetcher.calls)
	}
	if captured == nil {
		t.Fatal("API never received body")
	}
}

func TestCreateEnvelopePDF_ConfirmNo(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("API should not be called on cancel")
	})
	f := NewCreateEnvelopePDFFlow(cs, &stubFetcher{})
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	state := &session.FlowState{FlowID: "create_envelope_pdf", Step: stepConfirmPDF, Data: map[string]any{
		pdfKeyName:    "X",
		pdfKeyURL:     "https://x.com/c.pdf",
		pdfKeySigners: []any{map[string]any{"name": "A B", "email": "a@b.co", "role": "sign"}},
	}}

	res, _ := f.Handle(context.Background(), Input{
		Phone:    "5511999",
		Session:  sess,
		State:    state,
		Interact: &InteractiveReply{ButtonID: "confirm_no"},
	})
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v", res.Kind)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "cancelei") {
		t.Fatalf("expected cancel message, got %q", res.Reply)
	}
	if res.NextState != nil {
		t.Fatal("NextState should be nil after cancel")
	}
}

func TestCreateEnvelopePDF_FetchFailure_KeepsState(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("API should not be called after fetch failure")
	})
	fetcher := &stubFetcher{err: testErr("download failed")}
	f := NewCreateEnvelopePDFFlow(cs, fetcher)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	state := &session.FlowState{FlowID: "create_envelope_pdf", Step: stepConfirmPDF, Data: map[string]any{
		pdfKeyName:    "X",
		pdfKeyURL:     "https://x.com/c.pdf",
		pdfKeySigners: []any{map[string]any{"name": "A B", "email": "a@b.co", "role": "sign"}},
	}}

	res, _ := f.Handle(context.Background(), Input{
		Phone:    "5511999",
		Session:  sess,
		State:    state,
		Interact: &InteractiveReply{ButtonID: "confirm_yes"},
	})
	if res.Kind != KindError {
		t.Fatalf("expected KindError, got %v", res.Kind)
	}
	if res.NextState == nil {
		t.Fatal("NextState should be preserved so user can retry")
	}
	if !strings.Contains(res.Reply, "Não consegui baixar") {
		t.Fatalf("expected friendly error, got %q", res.Reply)
	}
}

func TestCreateEnvelopePDF_FromAttachment(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("API should not be called during gathering")
	})
	f := NewCreateEnvelopePDFFlow(cs, &stubFetcher{})
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}

	res, _ := f.Handle(context.Background(), Input{
		Phone:   "5511999",
		Session: sess,
		Attachments: []Attachment{
			{URL: "https://x.com/from-whatsapp.pdf", MimeType: "application/pdf"},
		},
		Entities: map[string]any{
			"envelope_name": "Contrato",
			"signers": []any{
				map[string]any{"name": "A B", "email": "a@b.co", "role": "parte"},
			},
		},
	})
	if res.Kind != KindConfirm {
		t.Fatalf("expected KindConfirm (attachment counts as pdf_url), got %v", res.Kind)
	}
	if got, _ := res.NextState.Data[pdfKeyURL].(string); !strings.Contains(got, "from-whatsapp.pdf") {
		t.Fatalf("expected attachment URL persisted, got %v", res.NextState.Data[pdfKeyURL])
	}
}

// --- CreateEnvelopeTmplFlow ------------------------------------------------

func TestCreateEnvelopeTmpl_NoTemplate_TransfersToList(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("API should not be called")
	})
	f := NewCreateEnvelopeTmplFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", PreferredAccount: "acct-1"}
	res, _ := f.Handle(context.Background(), Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "create_envelope_tmpl",
		Entities: map[string]any{"envelope_name": "Y"},
	})
	if res.Kind != KindTransfer || res.NextIntent != "list_templates" {
		t.Fatalf("expected transfer to list_templates, got %#v", res)
	}
	if res.NextState == nil || res.NextState.Data["return_to"] != "create_envelope_tmpl" {
		t.Fatalf("expected return_to in transfer state, got %#v", res.NextState)
	}
}

func TestCreateEnvelopeTmpl_MissingSigners_Asks(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("API should not be called")
	})
	f := NewCreateEnvelopeTmplFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", PreferredAccount: "acct-1"}

	res, _ := f.Handle(context.Background(), Input{
		Phone:    "5511999",
		Session:  sess,
		Entities: map[string]any{"template_id": "tpl-1", "envelope_name": "Contrato"},
	})
	if res.Kind != KindAsk {
		t.Fatalf("expected KindAsk, got %v", res.Kind)
	}
	if !strings.Contains(res.Reply, "signatário") {
		t.Fatalf("expected mention of signatário: %q", res.Reply)
	}
}

func TestCreateEnvelopeTmpl_ConfirmYes_CallsAPI(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/envelope_bulk_creations" {
			t.Errorf("path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":{"id":"bc-2","type":"envelope_bulk_creations","attributes":{"envelope_id":"env-FROM-TMPL","status":"queued"}}}`))
	})
	f := NewCreateEnvelopeTmplFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	ctx := clicksign.WithSession(context.Background(), sess)
	state := &session.FlowState{
		FlowID: "create_envelope_tmpl",
		Step:   stepConfirmTmpl,
		Data: map[string]any{
			tmplKeyName:       "Contrato",
			tmplKeyTemplateID: "tpl-1",
			tmplKeySigners: []any{
				map[string]any{"name": "A B", "email": "a@b.co", "role": "sign"},
			},
		},
	}

	res, _ := f.Handle(ctx, Input{
		Phone:    "5511999",
		Session:  sess,
		State:    state,
		Interact: &InteractiveReply{ButtonID: "confirm_yes"},
	})
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v: %#v", res.Kind, res)
	}
	if !strings.Contains(res.Reply, "env-FROM-TMPL") {
		t.Fatalf("missing envelope id in reply: %q", res.Reply)
	}
}

// --- list_templates returns chosen template via transfer -------------------

func TestListTemplates_ClickWithReturnTo_TransfersBack(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("API should not be called on click")
	})
	f := NewListTemplatesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", PreferredAccount: "acct-1"}
	state := &session.FlowState{
		FlowID: "list_templates",
		Step:   stepAwaitingTemplateChoice,
		Data:   map[string]any{"return_to": "create_envelope_tmpl"},
	}
	res, _ := f.Handle(context.Background(), Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "list_templates",
		State:    state,
		Interact: &InteractiveReply{ListItemID: "tpl-CHOSEN"},
	})
	if res.Kind != KindTransfer || res.NextIntent != "create_envelope_tmpl" {
		t.Fatalf("expected transfer to create_envelope_tmpl, got %#v", res)
	}
	if got, _ := res.NextEntities["template_id"].(string); got != "tpl-CHOSEN" {
		t.Fatalf("template_id=%q want tpl-CHOSEN", got)
	}
}

// --- helpers ---------------------------------------------------------------

type testErr string

func (e testErr) Error() string { return string(e) }
