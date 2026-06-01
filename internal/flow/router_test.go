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

func TestRouter_ActiveFlowOverridesIntent(t *testing.T) {
	lt := &stubFlow{id: "list_templates", res: Result{Kind: KindDone, Reply: "lt"}}
	sa := &stubFlow{id: "select_account", res: Result{Kind: KindDone, Reply: "sa"}}
	r := NewRouter(nil, lt, sa)

	state := &session.FlowState{FlowID: "select_account", Step: "awaiting_choice"}
	res, err := r.Handle(context.Background(), Input{Intent: "list_templates", State: state})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Reply != "sa" {
		t.Fatalf("expected active flow to handle the turn (sa), got %q", res.Reply)
	}
	if lt.calls != 0 {
		t.Fatalf("list_templates should not be called when select_account is active")
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
