package api

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/classifier"
	"github.com/clicksign/whatsapp-mcp/internal/config"
	"github.com/clicksign/whatsapp-mcp/internal/flow"
	"github.com/clicksign/whatsapp-mcp/internal/nlu"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// recordingFlow captures the Input it received so a test can assert how
// the pipeline shaped it (was ActiveFlow cleared? was the intent passed
// through?) before dispatch.
type recordingFlow struct {
	id  string
	got flow.Input
	res flow.Result
}

func (f *recordingFlow) ID() string { return f.id }

func (f *recordingFlow) Handle(_ context.Context, in flow.Input) (flow.Result, error) {
	f.got = in
	return f.res, nil
}

func newTestPipeline(t *testing.T, intent nlu.Intent, conf nlu.Confidence, flows ...flow.Flow) (*FlowPipeline, session.Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := session.NewMemoryStore()
	router := flow.NewRouter(logger, flows...)
	cfg := &config.Config{ClassifierContextTurns: 4}
	pipeline := NewFlowPipeline(
		cfg, logger, store,
		classifier.AlwaysOnTopic{},
		nlu.Static{V: nlu.Verdict{Intent: intent, Confidence: conf}},
		router,
	)
	return pipeline, store
}

// Regression test for the bug found on 2026-06-02: a user who left a
// flow open (e.g. list_templates / awaiting_template_choice) and then
// typed a NEW request ("liste meus envelopes") was getting routed back
// to the old flow because the router used to pin the active flow above
// any free-text intent.
func TestFlowPipeline_FreeTextWithNewIntentClearsStaleFlow(t *testing.T) {
	target := &recordingFlow{id: "list_envelopes", res: flow.Result{Kind: flow.KindDone, Reply: "envelopes ok"}}
	stale := &recordingFlow{id: "list_templates", res: flow.Result{Kind: flow.KindDone, Reply: "should not be called"}}
	pipeline, store := newTestPipeline(t, "list_envelopes", nlu.ConfHigh, target, stale)

	sess := &session.Session{
		PhoneNumber: "+5511",
		ActiveFlow: &session.FlowState{
			FlowID:  "list_templates",
			Step:    "awaiting_template_choice",
			AskedAt: time.Now().Add(-30 * time.Second), // fresh — TTL would not catch it
		},
	}
	if err := store.PutSession(context.Background(), sess); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := pipeline.Run(context.Background(), MessageRequest{
		PhoneNumber: "+5511",
		Message:     "liste meus envelopes",
	}, sess)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Reply != "envelopes ok" {
		t.Fatalf("expected new intent to win, got reply=%q", resp.Reply)
	}
	if stale.got.Phone != "" {
		t.Fatalf("stale flow should not have been called, but got Input=%#v", stale.got)
	}
}

// TTL: a free-text turn after the ActiveFlow has been pending for too
// long should clear it before dispatch, so the target flow sees a
// clean State == nil.
func TestFlowPipeline_ExpiresStaleActiveFlowAfterTTL(t *testing.T) {
	target := &recordingFlow{id: "list_envelopes", res: flow.Result{Kind: flow.KindDone, Reply: "ok"}}
	pipeline, store := newTestPipeline(t, "list_envelopes", nlu.ConfHigh, target)

	sess := &session.Session{
		PhoneNumber: "+5511",
		ActiveFlow: &session.FlowState{
			FlowID:  "list_templates",
			Step:    "awaiting_template_choice",
			// 12 min > 10 min TTL.
			AskedAt: time.Now().Add(-12 * time.Minute),
		},
	}
	if err := store.PutSession(context.Background(), sess); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := pipeline.Run(context.Background(), MessageRequest{
		PhoneNumber: "+5511",
		Message:     "liste meus envelopes",
	}, sess)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if target.got.State != nil {
		t.Fatalf("expected stale active flow to be cleared before dispatch, got State=%#v", target.got.State)
	}
	if sess.ActiveFlow != nil {
		t.Fatalf("expected sess.ActiveFlow to be nil after TTL expiry, got %#v", sess.ActiveFlow)
	}
}

// A click should always be routed to the active flow, no matter how
// old it is — the user is actively interacting, so the state is
// definitively still relevant. The TTL must NOT fire for clicks.
func TestFlowPipeline_ClickRespectsActiveFlowEvenAfterTTL(t *testing.T) {
	target := &recordingFlow{id: "list_templates", res: flow.Result{Kind: flow.KindDone, Reply: "templated"}}
	other := &recordingFlow{id: "list_envelopes", res: flow.Result{Kind: flow.KindDone, Reply: "should not be hit"}}
	pipeline, store := newTestPipeline(t, "list_envelopes", nlu.ConfHigh, target, other)

	old := time.Now().Add(-2 * time.Hour)
	sess := &session.Session{
		PhoneNumber: "+5511",
		ActiveFlow: &session.FlowState{
			FlowID:  "list_templates",
			Step:    "awaiting_template_choice",
			AskedAt: old,
		},
	}
	if err := store.PutSession(context.Background(), sess); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := pipeline.Run(context.Background(), MessageRequest{
		PhoneNumber:      "+5511",
		InteractiveReply: &flow.InteractiveReply{ListItemID: "some-template-uuid"},
	}, sess)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Reply != "templated" {
		t.Fatalf("expected click to land in active flow, got %q", resp.Reply)
	}
	if other.got.Phone != "" {
		t.Fatalf("other flow should not be called for a click that belongs to active flow")
	}
}

// Free text with the NLU verdict == "unknown" should keep the active
// flow alive so short replies like "sim"/"ok" still work as
// continuations of the multi-step flow.
func TestFlowPipeline_UnknownIntentFallsBackToActiveFlow(t *testing.T) {
	target := &recordingFlow{id: "create_envelope_pdf", res: flow.Result{Kind: flow.KindAsk, Reply: "still gathering"}}
	pipeline, store := newTestPipeline(t, nlu.IntentUnknown, nlu.ConfLow, target)

	sess := &session.Session{
		PhoneNumber: "+5511",
		ActiveFlow: &session.FlowState{
			FlowID:  "create_envelope_pdf",
			Step:    "awaiting_signers",
			AskedAt: time.Now().Add(-1 * time.Minute),
		},
	}
	if err := store.PutSession(context.Background(), sess); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := pipeline.Run(context.Background(), MessageRequest{
		PhoneNumber: "+5511",
		Message:     "sim",
	}, sess)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Reply != "still gathering" {
		t.Fatalf("expected active flow to interpret unknown-intent text, got %q", resp.Reply)
	}
	if target.got.State == nil || target.got.State.FlowID != "create_envelope_pdf" {
		t.Fatalf("expected active flow state to be passed through, got %#v", target.got.State)
	}
}
