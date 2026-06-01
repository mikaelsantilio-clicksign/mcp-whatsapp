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

// --- ListEnvelopesFlow -----------------------------------------------------

func TestListEnvelopes_NoAccount_TransfersToSelect(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be called before account is selected")
	})
	f := NewListEnvelopesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999"}
	res, err := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess, Intent: "list_envelopes"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindTransfer || res.NextIntent != "select_account" {
		t.Fatalf("expected transfer to select_account, got %#v", res)
	}
}

func TestListEnvelopes_RendersListWithStatus(t *testing.T) {
	body := `{"data":[
		{"id":"e1","type":"envelopes","attributes":{"name":"Contrato 1","status":"pending","created_at":"2026-05-15T10:00:00.000Z"}},
		{"id":"e2","type":"envelopes","attributes":{"name":"Contrato 2","status":"running","created_at":"2026-05-20T10:00:00.000Z"}}
	]}`
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/envelopes" {
			t.Errorf("path=%q want /envelopes", r.URL.Path)
		}
		if h := r.Header.Get("X-Account-Key"); h != "acct-1" {
			t.Errorf("X-Account-Key=%q want acct-1", h)
		}
		_, _ = w.Write([]byte(body))
	})
	f := NewListEnvelopesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}

	ctx := clicksign.WithSession(context.Background(), sess)
	res, err := f.Handle(ctx, Input{Phone: "5511999", Session: sess, Intent: "list_envelopes"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindChoose {
		t.Fatalf("expected KindChoose, got %v", res.Kind)
	}
	if len(res.Interactive.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res.Interactive.Items))
	}
	if res.Interactive.Items[0].ID != "e1" {
		t.Fatalf("first item id=%q want e1", res.Interactive.Items[0].ID)
	}
	if !strings.Contains(res.Interactive.Items[0].Description, "Pendente") {
		t.Fatalf("expected pt-BR status label, got %q", res.Interactive.Items[0].Description)
	}
	if !strings.Contains(res.Interactive.Items[0].Description, "15/05/2026") {
		t.Fatalf("expected formatted date, got %q", res.Interactive.Items[0].Description)
	}
	if res.NextState == nil || res.NextState.Step != stepAwaitingEnvelopeChoice {
		t.Fatalf("expected NextState awaiting_envelope_choice, got %#v", res.NextState)
	}
}

func TestListEnvelopes_FilterStatus(t *testing.T) {
	body := `{"data":[
		{"id":"e1","type":"envelopes","attributes":{"name":"A","status":"pending"}},
		{"id":"e2","type":"envelopes","attributes":{"name":"B","status":"running"}},
		{"id":"e3","type":"envelopes","attributes":{"name":"C","status":"running"}}
	]}`
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	f := NewListEnvelopesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}

	ctx := clicksign.WithSession(context.Background(), sess)
	res, _ := f.Handle(ctx, Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "list_envelopes",
		Entities: map[string]any{"filter_status": "running"},
	})
	if res.Kind != KindChoose {
		t.Fatalf("expected KindChoose, got %v", res.Kind)
	}
	if len(res.Interactive.Items) != 2 {
		t.Fatalf("expected 2 running items, got %d", len(res.Interactive.Items))
	}
	if !strings.Contains(res.Interactive.Header, "Em andamento") {
		t.Fatalf("header should reflect the filter, got %q", res.Interactive.Header)
	}
}

func TestListEnvelopes_EmptyWithFilter(t *testing.T) {
	body := `{"data":[
		{"id":"e1","type":"envelopes","attributes":{"name":"A","status":"closed"}}
	]}`
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	f := NewListEnvelopesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}

	ctx := clicksign.WithSession(context.Background(), sess)
	res, _ := f.Handle(ctx, Input{
		Phone:    "5511999",
		Session:  sess,
		Entities: map[string]any{"filter_status": "pending"},
	})
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v", res.Kind)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "pendente") {
		t.Fatalf("expected reply to mention the filter, got %q", res.Reply)
	}
}

func TestListEnvelopes_TotallyEmpty(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	f := NewListEnvelopesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	ctx := clicksign.WithSession(context.Background(), sess)
	res, _ := f.Handle(ctx, Input{Phone: "5511999", Session: sess})
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v", res.Kind)
	}
}

func TestListEnvelopes_ClickTransfersToStatus(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("API should not be called on click")
	})
	f := NewListEnvelopesFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	state := &session.FlowState{FlowID: "list_envelopes", Step: stepAwaitingEnvelopeChoice}

	res, err := f.Handle(context.Background(), Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "list_envelopes",
		State:    state,
		Interact: &InteractiveReply{ListItemID: "env-abc"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindTransfer || res.NextIntent != "envelope_status" {
		t.Fatalf("expected transfer to envelope_status, got %#v", res)
	}
	if got, _ := res.NextEntities["envelope_id"].(string); got != "env-abc" {
		t.Fatalf("envelope_id=%q want env-abc", got)
	}
}

// --- EnvelopeStatusFlow ----------------------------------------------------

func TestEnvelopeStatus_NoAccount_Transfers(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be called")
	})
	f := NewEnvelopeStatusFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999"}
	res, _ := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess, Intent: "envelope_status"})
	if res.Kind != KindTransfer || res.NextIntent != "select_account" {
		t.Fatalf("expected transfer to select_account, got %#v", res)
	}
}

func TestEnvelopeStatus_NoHint_TransfersToList(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be called without entities")
	})
	f := NewEnvelopeStatusFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", PreferredAccount: "acct-1"}
	res, _ := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess, Intent: "envelope_status"})
	if res.Kind != KindTransfer || res.NextIntent != "list_envelopes" {
		t.Fatalf("expected transfer to list_envelopes, got %#v", res)
	}
}

func TestEnvelopeStatus_ByID(t *testing.T) {
	body := `{"data":{"id":"env-1","type":"envelopes","attributes":{"name":"Contrato Mike","status":"running","created_at":"2026-05-15T10:00:00.000Z"}}}`
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/envelopes/env-1" {
			t.Errorf("path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	})
	f := NewEnvelopeStatusFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	ctx := clicksign.WithSession(context.Background(), sess)

	res, err := f.Handle(ctx, Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "envelope_status",
		Entities: map[string]any{"envelope_id": "env-1"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v", res.Kind)
	}
	if !strings.Contains(res.Reply, "Contrato Mike") {
		t.Fatalf("reply missing envelope name: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "Em andamento") {
		t.Fatalf("reply missing pt-BR status: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "15/05/2026") {
		t.Fatalf("reply missing date: %q", res.Reply)
	}
}

func TestEnvelopeStatus_ByName_SingleMatch(t *testing.T) {
	calls := 0
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/envelopes":
			_, _ = w.Write([]byte(`{"data":[
				{"id":"env-1","type":"envelopes","attributes":{"name":"Contrato STG 1","status":"pending"}}
			]}`))
		case "/envelopes/env-1":
			_, _ = w.Write([]byte(`{"data":{"id":"env-1","type":"envelopes","attributes":{"name":"Contrato STG 1","status":"pending"}}}`))
		default:
			t.Errorf("unexpected path=%q", r.URL.Path)
		}
	})
	f := NewEnvelopeStatusFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	ctx := clicksign.WithSession(context.Background(), sess)

	res, _ := f.Handle(ctx, Input{
		Phone:    "5511999",
		Session:  sess,
		Entities: map[string]any{"envelope_name": "stg 1"},
	})
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v: %#v", res.Kind, res)
	}
	if calls != 2 {
		t.Fatalf("expected 2 API calls (list + detail), got %d", calls)
	}
}

func TestEnvelopeStatus_ByName_MultipleMatches_AsksChoice(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/envelopes" {
			t.Errorf("path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"e1","type":"envelopes","attributes":{"name":"Contrato 1","status":"pending"}},
			{"id":"e2","type":"envelopes","attributes":{"name":"Contrato 2","status":"running"}}
		]}`))
	})
	f := NewEnvelopeStatusFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	ctx := clicksign.WithSession(context.Background(), sess)

	res, _ := f.Handle(ctx, Input{
		Phone:    "5511999",
		Session:  sess,
		Entities: map[string]any{"envelope_name": "Contrato"},
	})
	if res.Kind != KindChoose {
		t.Fatalf("expected KindChoose, got %v", res.Kind)
	}
	if len(res.Interactive.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res.Interactive.Items))
	}
	if res.NextState == nil || res.NextState.Step != stepAwaitingEnvelopeForStatus {
		t.Fatalf("expected NextState awaiting_envelope_for_status, got %#v", res.NextState)
	}
}

func TestEnvelopeStatus_ByName_NoMatch(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"e1","type":"envelopes","attributes":{"name":"Recibo","status":"closed"}}
		]}`))
	})
	f := NewEnvelopeStatusFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	ctx := clicksign.WithSession(context.Background(), sess)
	res, _ := f.Handle(ctx, Input{
		Phone:    "5511999",
		Session:  sess,
		Entities: map[string]any{"envelope_name": "totalmente inexistente"},
	})
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone with friendly message, got %v", res.Kind)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "não encontrei") {
		t.Fatalf("expected 'não encontrei' message, got %q", res.Reply)
	}
}

func TestEnvelopeStatus_ClickResolvesEnvelope(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/envelopes/env-clicked" {
			t.Errorf("path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":{"id":"env-clicked","type":"envelopes","attributes":{"name":"Clicado","status":"closed"}}}`))
	})
	f := NewEnvelopeStatusFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	ctx := clicksign.WithSession(context.Background(), sess)
	state := &session.FlowState{FlowID: "envelope_status", Step: stepAwaitingEnvelopeForStatus}

	res, _ := f.Handle(ctx, Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "envelope_status",
		State:    state,
		Interact: &InteractiveReply{ListItemID: "env-clicked"},
	})
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v", res.Kind)
	}
	if !strings.Contains(res.Reply, "Clicado") {
		t.Fatalf("missing name in reply: %q", res.Reply)
	}
}

func TestEnvelopeStatus_404IsSoftReply(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"not found"}]}`))
	})
	f := NewEnvelopeStatusFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	ctx := clicksign.WithSession(context.Background(), sess)
	res, _ := f.Handle(ctx, Input{
		Phone:    "5511999",
		Session:  sess,
		Entities: map[string]any{"envelope_id": "missing"},
	})
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone for 404, got %v", res.Kind)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "não encontrei") {
		t.Fatalf("expected 'não encontrei' message, got %q", res.Reply)
	}
}

// --- helpers (sanity) ------------------------------------------------------

func TestEnvelopeStatusLabel(t *testing.T) {
	if envelopeStatusLabel("pending") != "Pendente" {
		t.Fatal("pending->Pendente")
	}
	if envelopeStatusLabel("CANCELLED") != "Cancelado" {
		t.Fatal("CANCELLED (case-insensitive)")
	}
	if envelopeStatusLabel("") != "Sem status" {
		t.Fatal("empty->Sem status")
	}
	if envelopeStatusLabel("brand_new") != "brand_new" {
		t.Fatal("unknown should passthrough")
	}
}

func TestNormalizeStatusFilter(t *testing.T) {
	if normalizeStatusFilter("Pendente") != "pending" {
		t.Fatal("Pendente->pending")
	}
	if normalizeStatusFilter("em andamento") != "running" {
		t.Fatal("em andamento->running")
	}
	if normalizeStatusFilter("garbage") != "" {
		t.Fatal("unknown->empty")
	}
}

// --- helper to silence "unused" if test set ever shrinks --------------------

var _ = json.Marshal
