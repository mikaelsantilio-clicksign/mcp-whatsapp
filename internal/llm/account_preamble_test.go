package llm

import (
	"strings"
	"testing"

	"github.com/clicksign/whatsapp-mcp/internal/session"
)

func TestAccountSelectionPreamble_EmptyReturnsEmpty(t *testing.T) {
	if got := AccountSelectionPreamble(nil); got != "" {
		t.Errorf("nil pending: want empty, got %q", got)
	}
	if got := AccountSelectionPreamble([]session.PendingAccount{}); got != "" {
		t.Errorf("empty pending: want empty, got %q", got)
	}
}

func TestAccountSelectionPreamble_ListsAccountsAndRules(t *testing.T) {
	pending := []session.PendingAccount{
		{Key: "acc-aaa", Name: "Acme S.A."},
		{Key: "acc-bbb", Name: "Beta Ltda"},
	}
	got := AccountSelectionPreamble(pending)
	if got == "" {
		t.Fatal("preamble should not be empty with pending accounts")
	}
	// Must enumerate each account by index and key+name.
	for _, want := range []string{
		"SELEÇÃO DE CONTA PENDENTE",
		"1. acc-aaa → Acme S.A.",
		"2. acc-bbb → Beta Ltda",
		"select_account",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preamble missing %q\n--- got ---\n%s", want, got)
		}
	}
}
