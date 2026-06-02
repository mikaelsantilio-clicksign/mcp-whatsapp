package flow

import (
	"context"
	"errors"
	"testing"

	"github.com/clicksign/whatsapp-mcp/internal/session"
)

type stubFlow struct {
	id  string
	res Result
	err error

	gotInput Input
	calls    int
}

func (f *stubFlow) ID() string { return f.id }

func (f *stubFlow) Handle(_ context.Context, in Input) (Result, error) {
	f.calls++
	f.gotInput = in
	return f.res, f.err
}

func TestRouter_PicksByIntent(t *testing.T) {
	lt := &stubFlow{id: "list_templates", res: Result{Kind: KindDone, Reply: "ok"}}
	r := NewRouter(nil, lt)

	res, err := r.Handle(context.Background(), Input{Intent: "list_templates"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Kind != KindDone || res.Reply != "ok" {
		t.Fatalf("unexpected result: %#v", res)
	}
	if lt.calls != 1 {
		t.Fatalf("expected list_templates to be called once, got %d", lt.calls)
	}
}

// A free-text turn carrying a recognised NLU intent must override the
// active flow — the user is allowed to change subject mid-conversation
// ("ah, esquece os templates, lista meus envelopes"). Regression test
// for the bug found on 2026-06-02 where users got stuck in
// list_templates after asking for envelopes.
func TestRouter_FreeTextIntentOverridesActiveFlow(t *testing.T) {
	lt := &stubFlow{id: "list_templates", res: Result{Kind: KindDone, Reply: "lt"}}
	le := &stubFlow{id: "list_envelopes", res: Result{Kind: KindDone, Reply: "le"}}
	r := NewRouter(nil, lt, le)

	state := &session.FlowState{FlowID: "list_templates", Step: "awaiting_template_choice"}
	res, err := r.Handle(context.Background(), Input{Intent: "list_envelopes", State: state})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Reply != "le" {
		t.Fatalf("expected new intent to win over active flow, got %q", res.Reply)
	}
	if lt.calls != 0 {
		t.Fatalf("active flow should NOT be called when free text brings a new intent, called %d times", lt.calls)
	}
}

// A click (Interact != nil) must always go to the active flow — the
// list/button id only has meaning inside the flow that emitted it.
func TestRouter_ClickRoutesToActiveFlow(t *testing.T) {
	lt := &stubFlow{id: "list_templates", res: Result{Kind: KindDone, Reply: "lt"}}
	le := &stubFlow{id: "list_envelopes", res: Result{Kind: KindDone, Reply: "le"}}
	r := NewRouter(nil, lt, le)

	state := &session.FlowState{FlowID: "list_templates", Step: "awaiting_template_choice"}
	// Even if some upstream layer wrongly attached Intent=list_envelopes
	// to a click, the click must go to the active flow.
	res, err := r.Handle(context.Background(), Input{
		Intent:   "list_envelopes",
		State:    state,
		Interact: &InteractiveReply{ListItemID: "some-template-uuid"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Reply != "lt" {
		t.Fatalf("expected click to route to active flow, got %q", res.Reply)
	}
	if le.calls != 0 {
		t.Fatalf("list_envelopes should not see a click meant for list_templates")
	}
}

// Free-text without a recognised intent must fall back to the active
// flow so short replies like "sim" / "ok" during a multi-step flow
// keep working.
func TestRouter_UnknownIntentFallsBackToActiveFlow(t *testing.T) {
	cep := &stubFlow{id: "create_envelope_pdf", res: Result{Kind: KindDone, Reply: "cep"}}
	r := NewRouter(nil, cep)

	state := &session.FlowState{FlowID: "create_envelope_pdf", Step: "awaiting_confirm"}
	res, err := r.Handle(context.Background(), Input{Intent: "unknown", State: state})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Reply != "cep" {
		t.Fatalf("expected active flow to handle unknown-intent text, got %q", res.Reply)
	}
}

// Empty intent + active flow → active flow handles it. Covers the case
// where NLU is disabled or fails open with an empty verdict.
func TestRouter_EmptyIntentFallsBackToActiveFlow(t *testing.T) {
	cep := &stubFlow{id: "create_envelope_pdf", res: Result{Kind: KindDone, Reply: "cep"}}
	r := NewRouter(nil, cep)

	state := &session.FlowState{FlowID: "create_envelope_pdf", Step: "awaiting_signers"}
	res, err := r.Handle(context.Background(), Input{Intent: "", State: state})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Reply != "cep" {
		t.Fatalf("expected active flow to handle empty-intent text, got %q", res.Reply)
	}
}

func TestRouter_UnknownIntent(t *testing.T) {
	r := NewRouter(nil)
	res, err := r.Handle(context.Background(), Input{Intent: "bogus"})
	if !errors.Is(err, ErrUnknownIntent) {
		t.Fatalf("expected ErrUnknownIntent, got %v", err)
	}
	if res.Kind != KindError {
		t.Fatalf("expected KindError, got %v", res.Kind)
	}
}

func TestRouter_TransferChainedInProcess(t *testing.T) {
	// list_templates transfers to select_account when no account is chosen;
	// select_account then completes the turn with a list-choose response.
	lt := &stubFlow{
		id: "list_templates",
		res: Result{
			Kind:       KindTransfer,
			NextIntent: "select_account",
			NextState:  &session.FlowState{FlowID: "select_account", Step: "starting"},
		},
	}
	sa := &stubFlow{
		id:  "select_account",
		res: Result{Kind: KindChoose, Reply: "Escolha sua conta"},
	}
	r := NewRouter(nil, lt, sa)

	res, err := r.Handle(context.Background(), Input{Intent: "list_templates", Session: &session.Session{}})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Reply != "Escolha sua conta" {
		t.Fatalf("expected transferred-to flow's reply, got %q", res.Reply)
	}
	if lt.calls != 1 || sa.calls != 1 {
		t.Fatalf("expected both flows called once each, got lt=%d sa=%d", lt.calls, sa.calls)
	}
	if sa.gotInput.Intent != "select_account" {
		t.Fatalf("transferred input lost intent: %#v", sa.gotInput)
	}
}

func TestRouter_TransferLoopBreaks(t *testing.T) {
	// Two flows that endlessly transfer to each other should be stopped.
	a := &stubFlow{id: "a", res: Result{Kind: KindTransfer, NextIntent: "b"}}
	b := &stubFlow{id: "b", res: Result{Kind: KindTransfer, NextIntent: "a"}}
	r := NewRouter(nil, a, b)

	res, err := r.Handle(context.Background(), Input{Intent: "a"})
	if err == nil {
		t.Fatal("expected error from infinite transfer loop")
	}
	if res.Kind != KindError {
		t.Fatalf("expected KindError, got %v", res.Kind)
	}
}

func TestDigestFromState(t *testing.T) {
	if d := DigestFromState(nil); d != nil {
		t.Fatalf("expected nil digest, got %#v", d)
	}
	s := &session.FlowState{FlowID: "x", Step: "y"}
	d := DigestFromState(s)
	if d == nil || d.FlowID != "x" || d.Step != "y" {
		t.Fatalf("unexpected digest: %#v", d)
	}
}

func TestRouter_Has(t *testing.T) {
	r := NewRouter(nil, &stubFlow{id: "list_templates"})
	if !r.Has("list_templates") {
		t.Fatal("expected Has(list_templates) to be true")
	}
	if r.Has("nope") {
		t.Fatal("expected Has(nope) to be false")
	}
}
