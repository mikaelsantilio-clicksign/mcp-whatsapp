package llm

import (
	"fmt"
	"strings"

	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// AccountSelectionPreamble returns a system-prompt prefix that forces the
// LLM to resolve a pending account selection before doing anything else.
// When `pending` is empty, it returns "" so the caller can skip prefixing.
//
// We intentionally render the candidate accounts inside the prompt itself
// (rather than relying on tool calls) for two reasons:
//
//  1. The list is small and stable for the lifetime of the pending state,
//     so paying ~50 tokens once per turn is cheaper than a list_accounts
//     round-trip.
//  2. Putting the full list in the system message makes the LLM resolve
//     fuzzy user replies ("a primeira", "Acme", "conta de teste") into a
//     concrete account_key without any extra hop.
func AccountSelectionPreamble(pending []session.PendingAccount) string {
	if len(pending) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[CONTEXTO PRIORITÁRIO — SELEÇÃO DE CONTA PENDENTE]\n")
	sb.WriteString("O usuário acabou de conectar a Clicksign e tem mais de uma conta vinculada. ")
	sb.WriteString("Antes de chamar qualquer outra ferramenta (list_envelopes, list_templates, create_envelope_*), você DEVE descobrir qual conta usar.\n\n")
	sb.WriteString("Contas disponíveis (key → nome):\n")
	for i, a := range pending {
		fmt.Fprintf(&sb, "  %d. %s → %s\n", i+1, a.Key, a.Name)
	}
	sb.WriteString("\nRegras:\n")
	sb.WriteString("- Se a mensagem atual do usuário identificar uma conta (pelo número da lista, pelo nome, ou pela key exata), chame imediatamente `select_account` com o `account_key` correspondente. Não peça confirmação adicional.\n")
	sb.WriteString("- Se a mensagem não disser nada sobre conta, responda em pt-BR pedindo para o usuário escolher entre as contas listadas acima (mostre o número e o nome de cada uma). NÃO chame nenhuma outra tool nessa rodada.\n")
	sb.WriteString("- Nunca invente account_keys: use apenas as keys exatas listadas acima.\n")
	return sb.String()
}
