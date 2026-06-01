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

// ListTemplatesFlow lists the templates of the currently-selected
// Clicksign account.
//
// Dispatch logic:
//
//   - No PreferredAccount in session → transfer to select_account with
//     Data["return_to"]="list_templates" so the user picks first.
//   - With PreferredAccount → call Clicksign and render the templates as
//     a WhatsApp list message.
//   - If the API still returns multi-account (stale PreferredAccount),
//     clear it and re-transfer to select_account.
type ListTemplatesFlow struct {
	cs *clicksign.Client
}

func NewListTemplatesFlow(cs *clicksign.Client) *ListTemplatesFlow {
	return &ListTemplatesFlow{cs: cs}
}

func (f *ListTemplatesFlow) ID() string { return "list_templates" }

const stepAwaitingTemplateChoice = "awaiting_template_choice"

func (f *ListTemplatesFlow) Handle(ctx context.Context, in Input) (Result, error) {
	// When the user taps a row of a list we previously rendered, hand
	// the chosen template_id back to whichever flow asked us to pick.
	if in.Interact != nil && in.State != nil && in.State.Step == stepAwaitingTemplateChoice {
		key := strings.TrimSpace(in.Interact.ListItemID)
		if key != "" {
			returnTo := flowDataString(in.State.Data, "return_to")
			if returnTo != "" {
				return Result{
					Kind:         KindTransfer,
					NextIntent:   returnTo,
					NextEntities: map[string]any{"template_id": key},
				}, nil
			}
			// Standalone listing → friendly confirmation, no next step.
			return Result{
				Kind:  KindDone,
				Reply: fmt.Sprintf("Template selecionado (`%s`). O que você quer fazer com ele? Por exemplo: \"crie um envelope com esse template\".", key),
			}, nil
		}
	}

	if strings.TrimSpace(in.Session.PreferredAccount) == "" {
		return transferToSelectAccount("list_templates"), nil
	}

	templates, err := f.cs.ListTemplates(ctx, in.Phone)
	if err != nil {
		if errIs(err, clicksign.ErrMultiAccount) {
			// PreferredAccount is set but the API rejected it
			// (rotated/removed/etc). Clear and re-ask.
			in.Session.PreferredAccount = ""
			return transferToSelectAccount("list_templates"), nil
		}
		if errIs(err, conv.ErrSessionExpired, clicksign.ErrInvalidToken) {
			return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
		}
		return errorResult("Não consegui listar os templates agora. Tenta de novo em alguns segundos."), err
	}

	if len(templates) == 0 {
		return Result{
			Kind:  KindDone,
			Reply: "Você ainda não tem templates cadastrados nessa conta. Crie um no painel da Clicksign ou me peça para criar.",
		}, nil
	}

	items := make([]InteractiveItem, 0, len(templates))
	for _, t := range templates {
		name := strings.TrimSpace(t.Attributes.Name)
		if name == "" {
			name = "(sem nome)"
		}
		desc := formatTemplateCreated(t.Attributes.Created)
		items = append(items, InteractiveItem{
			ID:          t.ID,
			Title:       truncate(name, maxListTitle),
			Description: truncate(fmt.Sprintf("Criado em %s", desc), maxListDescription),
		})
		if len(items) == maxListItems {
			break
		}
	}

	reply := fmt.Sprintf("Templates da sua conta (%d encontrado(s)):", len(templates))
	if len(templates) > maxListItems {
		reply = fmt.Sprintf("Templates da sua conta (mostrando %d de %d):", maxListItems, len(templates))
	}

	// Carry return_to forward when we got here via a transfer — that
	// lets the user's click trip back to the originating flow with the
	// chosen template_id already populated.
	next := &session.FlowState{
		FlowID:  "list_templates",
		Step:    stepAwaitingTemplateChoice,
		AskedAt: time.Now().UTC(),
	}
	if in.State != nil {
		if rt := flowDataString(in.State.Data, "return_to"); rt != "" {
			next.Data = map[string]any{"return_to": rt}
		}
	}

	return Result{
		Kind:  KindChoose,
		Reply: reply,
		Interactive: &InteractivePayload{
			Type:   "list",
			Header: "Templates disponíveis",
			Body:   "Toque em um template para começar um envelope, ou diga \"voltar\".",
			Items:  items,
		},
		NextState: next,
	}, nil
}

// transferToSelectAccount builds a KindTransfer Result that hands the turn
// off to SelectAccountFlow and asks it to return here once the user picks.
func transferToSelectAccount(returnTo string) Result {
	return Result{
		Kind:       KindTransfer,
		NextIntent: "select_account",
		NextState: &session.FlowState{
			FlowID:  "select_account",
			Step:    "starting",
			AskedAt: time.Now().UTC(),
			Data:    map[string]any{"return_to": returnTo},
		},
	}
}

var _ Flow = (*ListTemplatesFlow)(nil)
