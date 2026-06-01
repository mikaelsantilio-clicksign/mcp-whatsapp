package classifier

import "testing"

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantIntent Intent
		wantErr    bool
	}{
		{name: "on_topic", raw: `{"intent": "on_topic", "reason": "clicksign"}`, wantIntent: IntentOnTopic},
		{name: "off_topic", raw: `{"intent": "off_topic", "reason": "math"}`, wantIntent: IntentOffTopic},
		{name: "meta_help", raw: `{"intent": "meta_help", "reason": "saudação"}`, wantIntent: IntentMetaHelp},
		{name: "fenced", raw: "```json\n{\"intent\": \"on_topic\", \"reason\": \"x\"}\n```", wantIntent: IntentOnTopic},
		{name: "uppercase_intent", raw: `{"intent": "ON_TOPIC", "reason": "x"}`, wantIntent: IntentOnTopic},
		// Backward-compat with the legacy on_topic boolean prompt variant.
		{name: "legacy_true", raw: `{"on_topic": true, "reason": "x"}`, wantIntent: IntentOnTopic},
		{name: "legacy_false", raw: `{"on_topic": false, "reason": "x"}`, wantIntent: IntentOffTopic},
		{name: "missing_field", raw: `{"reason": "x"}`, wantErr: true},
		{name: "unknown_intent", raw: `{"intent": "foo", "reason": "x"}`, wantErr: true},
		{name: "garbage", raw: `not json`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parseVerdict(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got verdict=%+v", v)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Intent != tc.wantIntent {
				t.Fatalf("Intent=%q want %q", v.Intent, tc.wantIntent)
			}
		})
	}
}

func TestBuildClassifierUserContent(t *testing.T) {
	got := buildClassifierUserContent("oi", nil)
	if got != "MENSAGEM ATUAL:\noi" {
		t.Fatalf("unexpected no-context content: %q", got)
	}

	got = buildClassifierUserContent("use a conta 3", []HistoryTurn{
		{Role: "user", Content: "lista os templates"},
		{Role: "assistant", Content: "voce tem 3 contas: 1) a 2) b 3) c"},
	})
	want := "CONTEXTO RECENTE (mais antigo primeiro):\n" +
		"user: lista os templates\n" +
		"assistant: voce tem 3 contas: 1) a 2) b 3) c\n" +
		"\n" +
		"MENSAGEM ATUAL:\n" +
		"use a conta 3"
	if got != want {
		t.Fatalf("unexpected content with context:\n--got--\n%s\n--want--\n%s", got, want)
	}
}

func TestFingerprintKeyStability(t *testing.T) {
	a := fingerprintKey("oi", []HistoryTurn{{Role: "user", Content: "x"}})
	b := fingerprintKey("oi", []HistoryTurn{{Role: "user", Content: "x"}})
	if a != b {
		t.Fatalf("expected stable fingerprint")
	}
	c := fingerprintKey("oi", []HistoryTurn{{Role: "user", Content: "y"}})
	if a == c {
		t.Fatalf("expected different fingerprint when context changes")
	}
}

func TestAlwaysOnTopic(t *testing.T) {
	v, err := AlwaysOnTopic{}.Classify(nil, "qualquer coisa", nil)
	if err != nil || v.Intent != IntentOnTopic {
		t.Fatalf("AlwaysOnTopic should always return on_topic; got %+v err=%v", v, err)
	}
}
