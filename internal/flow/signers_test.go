package flow

import (
	"strings"
	"testing"

	"github.com/clicksign/whatsapp-mcp/internal/nlu"
)

func TestNormalizeRole(t *testing.T) {
	cases := map[string]string{
		"parte":               "party",
		"PARTES":              "party",
		"Testemunha":          "witness",
		"signatário":          "sign",
		"Comprador":           "buyer",
		"representante legal": "legal_representative",
		"sign":                "sign",
		"unknown_one":         "unknown_one",
	}
	for in, want := range cases {
		if got := NormalizeRole(in); got != want {
			t.Errorf("NormalizeRole(%q)=%q want %q", in, got, want)
		}
	}
}

func TestIsValidRole(t *testing.T) {
	if !IsValidRole("party") || !IsValidRole("witness") || !IsValidRole("sign") {
		t.Fatal("canonical roles should be valid")
	}
	if IsValidRole("hacker") {
		t.Fatal("garbage role should be rejected")
	}
}

func TestIsValidEmail(t *testing.T) {
	good := []string{"a@b.co", "mikael.santilio@clicksign.com", "x+y@x.dev"}
	bad := []string{"", "no-at", "@no-local.com", "a@b", "a@b.c"}
	for _, e := range good {
		if !IsValidEmail(e) {
			t.Errorf("IsValidEmail(%q) want true", e)
		}
	}
	for _, e := range bad {
		if IsValidEmail(e) {
			t.Errorf("IsValidEmail(%q) want false", e)
		}
	}
}

func TestIsValidFullName(t *testing.T) {
	if !IsValidFullName("Mikael Nunes") {
		t.Fatal("two words should pass")
	}
	if IsValidFullName("Mikael") {
		t.Fatal("single word should fail")
	}
	if IsValidFullName("Mikael 123") {
		t.Fatal("digits should fail")
	}
}

func TestValidateSigners_Happy(t *testing.T) {
	in := []SignerInput{
		{Name: "Mikael Nunes", Email: "mikael@x.com", Role: "parte"},
		{Name: "Ana Silva", Email: "ana@x.com", Role: "testemunha"},
	}
	out, err := ValidateSigners(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
	if out[0].Role != "party" {
		t.Fatalf("first signer role=%q want party", out[0].Role)
	}
	if out[1].Role != "witness" {
		t.Fatalf("second signer role=%q want witness", out[1].Role)
	}
}

func TestValidateSigners_Empty(t *testing.T) {
	_, err := ValidateSigners(nil)
	if err == nil {
		t.Fatal("expected error for empty signer list")
	}
}

func TestValidateSigners_DefaultsRoleToSign(t *testing.T) {
	in := []SignerInput{{Name: "Mikael Nunes", Email: "m@x.co"}}
	out, err := ValidateSigners(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].Role != "sign" {
		t.Fatalf("default role=%q want sign", out[0].Role)
	}
}

func TestValidateSigners_AggregatesAllProblems(t *testing.T) {
	in := []SignerInput{
		{Name: "Onlyfirstname", Email: "x@x.co", Role: "parte"},
		{Name: "Ana Silva", Email: "bad-email", Role: "parte"},
		{Name: "Bruno Costa", Email: "b@x.co", Role: "hacker_role"},
	}
	_, err := ValidateSigners(in)
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	msg := err.Error()
	for _, want := range []string{"signatário 1", "signatário 2", "signatário 3", "hacker_role"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in error: %s", want, msg)
		}
	}
}

func TestSignersFromNLU_TypedSlice(t *testing.T) {
	v := []nlu.Signer{{Name: "X Y", Email: "x@y.com", Role: "parte"}}
	out := SignersFromNLU(v)
	if len(out) != 1 || out[0].Email != "x@y.com" {
		t.Fatalf("unexpected: %#v", out)
	}
}

func TestSignersFromNLU_MapSlice(t *testing.T) {
	v := []any{
		map[string]any{"name": "X Y", "email": "x@y.com", "role": "parte"},
	}
	out := SignersFromNLU(v)
	if len(out) != 1 || out[0].Name != "X Y" {
		t.Fatalf("unexpected: %#v", out)
	}
}

func TestToBulkSigners_HasCanonicalRequirements(t *testing.T) {
	in := []ValidatedSigner{{Name: "X Y", Email: "x@y.com", Role: "party"}}
	out := toBulkSigners(in)
	if len(out) != 1 {
		t.Fatalf("len=%d", len(out))
	}
	if len(out[0].Requirements) != 2 {
		t.Fatalf("requirements len=%d want 2", len(out[0].Requirements))
	}
	// Must contain provide_evidence+email and agree+role.
	var foundEvidence, foundAgree bool
	for _, r := range out[0].Requirements {
		if r.Action == "provide_evidence" && r.Auth == "email" {
			foundEvidence = true
		}
		if r.Action == "agree" && r.Role == "party" {
			foundAgree = true
		}
	}
	if !foundEvidence || !foundAgree {
		t.Fatalf("missing canonical requirements: %#v", out[0].Requirements)
	}
}
