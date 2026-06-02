package flow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/conv"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// SelectAccountFlow handles the "user has multiple Clicksign accounts and
// must choose one" scenario.
//
// It can be reached in three ways:
//
//  1. NLU classified the message as intent=select_account (e.g. the user
//     said "use a conta 3"). When entities.account_index is set, we
//     resolve the index against the live account list and finish.
//  2. Another flow detected the multi-account error and transferred to
//     us with Data["return_to"]=<originator>. We list accounts and let
//     the user pick.
//  3. The previous turn was the list-message and the current turn is an
//     interactive_reply with the chosen account key.
type SelectAccountFlow struct {
	cs *clicksign.Client
}

func NewSelectAccountFlow(cs *clicksign.Client) *SelectAccountFlow {
	return &SelectAccountFlow{cs: cs}
}

func (f *SelectAccountFlow) ID() string { return "select_account" }

// Step names persisted in FlowState.Step.
const (
	stepAwaitingChoice = "awaiting_choice"
)

func (f *SelectAccountFlow) Handle(ctx context.Context, in Input) (Result, error) {
	// Case 1: the user just tapped a row in our list message.
	if in.Interact != nil && in.State != nil && in.State.Step == stepAwaitingChoice {
		return f.handleClick(ctx, in)
	}

	// Case 2: NLU surfaced an account_index from text like "use a conta 3".
	if idx := intEntity(in.Entities, "account_index"); idx > 0 {
		return f.handleByIndex(ctx, in, idx)
	}

	// Case 3 (default): ask via list message.
	return f.askForChoice(ctx, in)
}

// handleClick validates the chosen account key, persists it, and either
// transfers back to the originator or completes the turn.
func (f *SelectAccountFlow) handleClick(ctx context.Context, in Input) (Result, error) {
	key := strings.TrimSpace(in.Interact.ListItemID)
	if key == "" {
		// User clicked a button (not a list row) by mistake — re-ask.
		return f.askForChoice(ctx, in)
	}
	accounts, err := f.cs.ListAccounts(ctx, in.Phone)
	if err != nil {
		return f.errorOrAuth("Não consegui validar a conta agora. Tenta de novo em alguns segundos.", err)
	}
	acc := accountByKey(accounts, key)
	if acc == nil {
		return f.askForChoiceWithList(in, accounts), nil
	}
	in.Session.PreferredAccount = acc.Attributes.Key

	returnTo := flowDataString(in.State.Data, "return_to")
	if returnTo != "" {
		return Result{
			Kind:       KindTransfer,
			NextIntent: returnTo,
			// NextState=nil clears the active flow; the originator decides
			// what to do next (typically run its query immediately).
		}, nil
	}
	return Result{
		Kind:  KindDone,
		Reply: fmt.Sprintf("Conta selecionada: *%s*. Em que posso ajudar?", strings.TrimSpace(acc.Attributes.Name)),
	}, nil
}

// handleByIndex resolves a 1-based index entity against the live account list.
func (f *SelectAccountFlow) handleByIndex(ctx context.Context, in Input, idx int) (Result, error) {
	accounts, err := f.cs.ListAccounts(ctx, in.Phone)
	if err != nil {
		return f.errorOrAuth("Não consegui listar suas contas agora. Tenta de novo em alguns segundos.", err)
	}
	if idx > len(accounts) {
		return f.askForChoiceWithList(in, accounts), nil
	}
	acc := accounts[idx-1]
	in.Session.PreferredAccount = acc.Attributes.Key

	// Index-based selection doesn't have a return_to (NLU intent was
	// "select_account" rather than a transfer); just finish.
	return Result{
		Kind:  KindDone,
		Reply: fmt.Sprintf("Conta selecionada: *%s*. Em que posso ajudar?", strings.TrimSpace(acc.Attributes.Name)),
	}, nil
}

// askForChoice fetches the account list and renders it as a list message.
// When the user has 0 or 1 accounts we short-circuit without bothering them.
func (f *SelectAccountFlow) askForChoice(ctx context.Context, in Input) (Result, error) {
	accounts, err := f.cs.ListAccounts(ctx, in.Phone)
	if err != nil {
		return f.errorOrAuth("Não consegui listar suas contas agora. Tenta de novo em alguns segundos.", err)
	}
	return f.askForChoiceWithList(in, accounts), nil
}

func (f *SelectAccountFlow) askForChoiceWithList(in Input, accounts []clicksign.OAuth2Account) Result {
	if len(accounts) == 0 {
		return Result{
			Kind:  KindError,
			Reply: "Sua sessão Clicksign não tem nenhuma conta associada. Verifique sua conta no painel Clicksign.",
		}
	}
	returnTo := ""
	if in.State != nil {
		returnTo = flowDataString(in.State.Data, "return_to")
	}
	if len(accounts) == 1 {
		in.Session.PreferredAccount = accounts[0].Attributes.Key
		if returnTo != "" {
			return Result{Kind: KindTransfer, NextIntent: returnTo}
		}
		return Result{
			Kind:  KindDone,
			Reply: fmt.Sprintf("Conta selecionada: *%s*. Em que posso ajudar?", strings.TrimSpace(accounts[0].Attributes.Name)),
		}
	}
	next := &session.FlowState{
		FlowID:  "select_account",
		Step:    stepAwaitingChoice,
		AskedAt: time.Now().UTC(),
	}
	if returnTo != "" {
		next.Data = map[string]any{"return_to": returnTo}
	}
	return Result{
		Kind:        KindChoose,
		Reply:       "Você tem mais de uma conta na Clicksign. Em qual quer trabalhar?",
		Interactive: buildAccountList("Toque em uma conta para continuar.", accounts),
		NextState:   next,
	}
}

// errorOrAuth surfaces a friendly Reply but distinguishes auth errors so
// the messages_handler can drive the user to re-authenticate.
func (f *SelectAccountFlow) errorOrAuth(reply string, err error) (Result, error) {
	if errIs(err, conv.ErrSessionExpired, clicksign.ErrInvalidToken) {
		return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
	}
	return errorResult(reply), err
}

// intEntity reads a numeric entity from the NLU's map[string]any payload.
// Returns 0 when missing/wrong type. The NLU encodes ints as Go int (see
// nlu.Entities.AsMap) but we tolerate float64 (JSON default) as well.
func intEntity(ents map[string]any, key string) int {
	if ents == nil {
		return 0
	}
	switch v := ents[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// statically assert the interface; helps refactors.
var _ Flow = (*SelectAccountFlow)(nil)
