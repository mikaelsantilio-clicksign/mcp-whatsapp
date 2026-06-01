package flow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// --- AddSignerFlow ---------------------------------------------------------

func TestAddSigner_NoAccount_Transfers(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be touched before account is selected")
	})
	f := NewAddSignerFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999"}
	res, err := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess, Intent: "add_signer"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindTransfer || res.NextIntent != "select_account" {
		t.Fatalf("expected transfer to select_account, got %#v", res)
	}
}

func TestAddSigner_MissingEnvelope_AsksWithoutAPICall(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be touched while envelope is missing")
	})
	f := NewAddSignerFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	res, err := f.Handle(context.Background(), Input{
		Phone:   "5511999",
		Session: sess,
		Intent:  "add_signer",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindAsk {
		t.Fatalf("expected KindAsk, got %v (%q)", res.Kind, res.Reply)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "envelope") {
		t.Fatalf("ask should mention envelope, got %q", res.Reply)
	}
	if res.NextState == nil || res.NextState.FlowID != "add_signer" || res.NextState.Step != stepGatheringAddSigner {
		t.Fatalf("expected gathering state, got %#v", res.NextState)
	}
}

func TestAddSigner_FullEntities_BuildsConfirmation(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be touched in confirmation step")
	})
	f := NewAddSignerFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	res, err := f.Handle(context.Background(), Input{
		Phone:   "5511999",
		Session: sess,
		Intent:  "add_signer",
		Entities: map[string]any{
			"envelope_id":   "030f3922-68da-47df-bbe4-068c8f9ae432",
			"envelope_name": "Contrato Stg 1",
			"signers": []any{
				map[string]any{
					"name":  "Maria Souza",
					"email": "maria@empresa.com",
					"role":  "parte",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindConfirm {
		t.Fatalf("expected KindConfirm, got %v (%q)", res.Kind, res.Reply)
	}
	if res.Interactive == nil || res.Interactive.Type != "buttons" {
		t.Fatalf("expected buttons interactive, got %#v", res.Interactive)
	}
	if !strings.Contains(res.Reply, "maria@empresa.com") {
		t.Fatalf("reply should echo email, got %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "Maria Souza") {
		t.Fatalf("reply should echo name, got %q", res.Reply)
	}
	if res.NextState == nil || res.NextState.Step != stepConfirmAddSigner {
		t.Fatalf("expected confirm state, got %#v", res.NextState)
	}
	// role should have been canonicalised from "parte" → "party".
	signer, _ := res.NextState.Data["signer"].(map[string]any)
	if signer == nil || signer["role"] != "party" {
		t.Fatalf("expected canonical role 'party', got %#v", signer)
	}
}

func TestAddSigner_BadEmail_AsksAgain(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be touched while signer is invalid")
	})
	f := NewAddSignerFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	res, err := f.Handle(context.Background(), Input{
		Phone:   "5511999",
		Session: sess,
		Entities: map[string]any{
			"envelope_id": "env-1",
			"signers": []any{
				map[string]any{"name": "Mikael", "email": "nope", "role": "parte"},
			},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindAsk {
		t.Fatalf("expected KindAsk on invalid signer, got %v (%q)", res.Kind, res.Reply)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "nome") && !strings.Contains(strings.ToLower(res.Reply), "e-mail") {
		t.Fatalf("ask should hint at the problem, got %q", res.Reply)
	}
}

func TestAddSigner_ConfirmYes_CallsAPI(t *testing.T) {
	var capturedPath string
	var capturedBody []byte
	var capturedCT string
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedCT = r.Header.Get("Content-Type")
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"sig-1","type":"signers","attributes":{"name":"Maria Souza","email":"maria@empresa.com"}}}`))
	})
	f := NewAddSignerFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	state := &session.FlowState{
		FlowID: "add_signer",
		Step:   stepConfirmAddSigner,
		Data: map[string]any{
			"envelope_id":   "env-1",
			"envelope_name": "Contrato Stg 1",
			"signer": map[string]any{
				"name":  "Maria Souza",
				"email": "maria@empresa.com",
				"role":  "party",
			},
		},
	}
	res, err := f.Handle(clicksign.WithSession(context.Background(), sess), Input{
		Phone:    "5511999",
		Session:  sess,
		State:    state,
		Interact: &InteractiveReply{ButtonID: "confirm_yes"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone after API call, got %v (%q)", res.Kind, res.Reply)
	}
	if capturedPath != "/envelopes/env-1/signers" {
		t.Fatalf("path=%q", capturedPath)
	}
	if !strings.HasPrefix(capturedCT, "application/vnd.api+json") {
		t.Fatalf("content-type=%q", capturedCT)
	}
	// Check the JSON:API envelope shape.
	var payload struct {
		Data struct {
			Type       string `json:"type"`
			Attributes struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("bad body json: %v\n%s", err, string(capturedBody))
	}
	if payload.Data.Type != "signers" {
		t.Fatalf("data.type=%q", payload.Data.Type)
	}
	if payload.Data.Attributes.Email != "maria@empresa.com" || payload.Data.Attributes.Name != "Maria Souza" {
		t.Fatalf("attributes: %+v", payload.Data.Attributes)
	}
	if !strings.Contains(res.Reply, "Maria Souza") {
		t.Fatalf("done reply should echo signer name, got %q", res.Reply)
	}
}

func TestAddSigner_ConfirmNo(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be called when user cancels")
	})
	f := NewAddSignerFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	res, err := f.Handle(context.Background(), Input{
		Phone:   "5511999",
		Session: sess,
		State: &session.FlowState{
			FlowID: "add_signer",
			Step:   stepConfirmAddSigner,
			Data:   map[string]any{"envelope_id": "env-1"},
		},
		Interact: &InteractiveReply{ButtonID: "confirm_no"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v", res.Kind)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "não") && !strings.Contains(strings.ToLower(res.Reply), "nao") {
		t.Fatalf("cancel reply should be friendly, got %q", res.Reply)
	}
}

// --- CancelEnvelopeFlow ----------------------------------------------------

func TestCancelEnvelope_NoAccount_Transfers(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be reached before account")
	})
	f := NewCancelEnvelopeFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999"}
	res, _ := f.Handle(context.Background(), Input{Phone: "5511999", Session: sess, Intent: "cancel_envelope"})
	if res.Kind != KindTransfer || res.NextIntent != "select_account" {
		t.Fatalf("expected transfer to select_account, got %#v", res)
	}
}

func TestCancelEnvelope_DraftEnvelope_BuildsConfirmation(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET on pre-flight, got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":{"id":"env-1","type":"envelopes","attributes":{"name":"Contrato Stg 1","status":"draft"}}}`))
	})
	f := NewCancelEnvelopeFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	res, err := f.Handle(clicksign.WithSession(context.Background(), sess), Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "cancel_envelope",
		Entities: map[string]any{"envelope_id": "env-1"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Kind != KindConfirm {
		t.Fatalf("expected KindConfirm for draft envelope, got %v (%q)", res.Kind, res.Reply)
	}
	if res.Interactive == nil || res.Interactive.Type != "buttons" {
		t.Fatalf("expected buttons, got %#v", res.Interactive)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "exclus") {
		t.Fatalf("confirm card should mention deletion, got %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "Contrato Stg 1") {
		t.Fatalf("confirm card should echo envelope name, got %q", res.Reply)
	}
}

func TestCancelEnvelope_RunningEnvelope_RefusesEarly(t *testing.T) {
	// API call is allowed (GetEnvelope), but DELETE must NOT happen.
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			t.Fatalf("DELETE must not be called for running envelopes")
		}
		_, _ = w.Write([]byte(`{"data":{"id":"env-1","type":"envelopes","attributes":{"name":"Em curso","status":"running"}}}`))
	})
	f := NewCancelEnvelopeFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	res, _ := f.Handle(clicksign.WithSession(context.Background(), sess), Input{
		Phone:    "5511999",
		Session:  sess,
		Intent:   "cancel_envelope",
		Entities: map[string]any{"envelope_id": "env-1"},
	})
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone with explanation, got %v (%q)", res.Kind, res.Reply)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "rascunho") {
		t.Fatalf("reply should explain the draft-only rule, got %q", res.Reply)
	}
}

func TestCancelEnvelope_ConfirmYes_CallsDelete(t *testing.T) {
	var deleted bool
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/envelopes/env-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	})
	f := NewCancelEnvelopeFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	state := &session.FlowState{
		FlowID: "cancel_envelope",
		Step:   stepConfirmCancel,
		Data: map[string]any{
			"envelope_id":   "env-1",
			"envelope_name": "Contrato Stg 1",
		},
	}
	res, err := f.Handle(clicksign.WithSession(context.Background(), sess), Input{
		Phone:    "5511999",
		Session:  sess,
		State:    state,
		Interact: &InteractiveReply{ButtonID: "confirm_yes"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !deleted {
		t.Fatal("DELETE was not issued")
	}
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone after delete, got %v (%q)", res.Kind, res.Reply)
	}
	if !strings.Contains(res.Reply, "Contrato Stg 1") {
		t.Fatalf("done reply should echo envelope name, got %q", res.Reply)
	}
}

func TestCancelEnvelope_ConfirmYes_403FromAPI(t *testing.T) {
	// Simulate the race: pre-flight showed draft, but by the time DELETE
	// arrives the envelope is already running.
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("only DELETE expected in confirm phase, got %s", r.Method)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"code":"forbidden","status":403,"title":"Verificação","detail":"envelope não está com status draft"}]}`))
	})
	f := NewCancelEnvelopeFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	state := &session.FlowState{
		FlowID: "cancel_envelope",
		Step:   stepConfirmCancel,
		Data:   map[string]any{"envelope_id": "env-1", "envelope_name": "C1"},
	}
	res, err := f.Handle(clicksign.WithSession(context.Background(), sess), Input{
		Phone:    "5511999",
		Session:  sess,
		State:    state,
		Interact: &InteractiveReply{ButtonID: "confirm_yes"},
	})
	// 403 is surfaced as a business error so we expect a friendly reply,
	// not a Go error returned from Handle.
	if err != nil {
		t.Fatalf("unexpected Handle error: %v", err)
	}
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone with explanation, got %v (%q)", res.Kind, res.Reply)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "rascunho") &&
		!strings.Contains(strings.ToLower(res.Reply), "andamento") {
		t.Fatalf("reply should explain the limitation, got %q", res.Reply)
	}
}

func TestCancelEnvelope_ConfirmNo(t *testing.T) {
	cs, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no API call expected on cancel, got %s %s", r.Method, r.URL.Path)
	})
	f := NewCancelEnvelopeFlow(cs)
	sess := &session.Session{PhoneNumber: "5511999", AccessToken: "tok", PreferredAccount: "acct-1"}
	res, _ := f.Handle(context.Background(), Input{
		Phone:   "5511999",
		Session: sess,
		State: &session.FlowState{
			FlowID: "cancel_envelope",
			Step:   stepConfirmCancel,
			Data:   map[string]any{"envelope_id": "env-1"},
		},
		Interact: &InteractiveReply{ButtonID: "confirm_no"},
	})
	if res.Kind != KindDone {
		t.Fatalf("expected KindDone, got %v", res.Kind)
	}
	if !strings.Contains(strings.ToLower(res.Reply), "manti") {
		t.Fatalf("friendly cancel reply expected, got %q", res.Reply)
	}
}
