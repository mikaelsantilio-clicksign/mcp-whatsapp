package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/conv"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// CancelEnvelopeFlow lets the user delete an envelope that is still in
// draft status (DELETE /envelopes/{id}). Clicksign v3 doesn't expose an
// endpoint to cancel a running envelope, so we make that limitation
// explicit to the user when the API returns 403.
//
// Visible steps:
//
//  1. gathering           — collect envelope (id or name).
//  2. awaiting_choice     — disambiguate when name matches several rows.
//  3. awaiting_confirm    — destructive action requires an explicit
//                           "Sim, excluir" button click.
//
// We persist only "envelope_id" and "envelope_name" in FlowState.Data.
type CancelEnvelopeFlow struct {
	cs *clicksign.Client
}

func NewCancelEnvelopeFlow(cs *clicksign.Client) *CancelEnvelopeFlow {
	return &CancelEnvelopeFlow{cs: cs}
}

func (f *CancelEnvelopeFlow) ID() string { return "cancel_envelope" }

const (
	stepGatheringCancel        = "gathering"
	stepAwaitingEnvelopeCancel = "awaiting_envelope_choice"
	stepConfirmCancel          = "awaiting_confirm"
)

func (f *CancelEnvelopeFlow) Handle(ctx context.Context, in Input) (Result, error) {
	// Destructive confirmation — the API is only called here.
	if in.State != nil && in.State.Step == stepConfirmCancel && isButtonClick(in.Interact) {
		return f.handleConfirmClick(ctx, in)
	}

	// Disambiguation: user picked an envelope from the list we sent.
	if in.State != nil && in.State.Step == stepAwaitingEnvelopeCancel && in.Interact != nil {
		key := strings.TrimSpace(in.Interact.ListItemID)
		if key != "" {
			data := flowDataCopy(in.State.Data)
			data["envelope_id"] = key
			return f.advance(ctx, in, data)
		}
	}

	if strings.TrimSpace(in.Session.PreferredAccount) == "" {
		return transferToSelectAccount("cancel_envelope"), nil
	}

	data := mergeCancelEnvelopeEntities(in)
	return f.advance(ctx, in, data)
}

func (f *CancelEnvelopeFlow) advance(ctx context.Context, in Input, data map[string]any) (Result, error) {
	envID := getDataString(data, "envelope_id")
	envName := getDataString(data, "envelope_name")

	if envID == "" {
		if envName != "" {
			return f.resolveByName(ctx, in, data, envName)
		}
		return Result{
			Kind:      KindAsk,
			Reply:     "Qual envelope você quer excluir? Me passa o nome ou o ID.",
			NextState: keepCancelState(data),
		}, nil
	}

	// We need a current snapshot so the confirm card shows the envelope
	// name + status, AND so we don't try to cancel something that no
	// longer exists. The Get is also a free auth check.
	env, err := f.cs.GetEnvelope(ctx, in.Phone, envID)
	if err != nil {
		if errIs(err, conv.ErrSessionExpired, clicksign.ErrInvalidToken) {
			return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
		}
		if errIs(err, clicksign.ErrMultiAccount) {
			return transferToSelectAccount("cancel_envelope"), nil
		}
		// 404 (envelope from another account or deleted already).
		var apiErr *clicksign.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return Result{
				Kind:  KindDone,
				Reply: "Esse envelope não existe (ou está em outra conta). Quer ver a lista pra escolher?",
			}, nil
		}
		return errorResult("Não consegui consultar esse envelope agora. Tenta de novo em alguns segundos."), err
	}

	// Pre-flight: warn the user when the envelope is NOT in draft,
	// because the API will refuse with a 403. Clicksign v3 has no
	// "cancel running envelope" endpoint, only "delete draft envelope".
	status := strings.ToLower(strings.TrimSpace(env.Attributes.Status))
	if status != "" && status != "draft" {
		return Result{
			Kind: KindDone,
			Reply: fmt.Sprintf(
				"Esse envelope está em *%s*. A API da Clicksign só permite cancelar/excluir envelopes em *Rascunho*.\nPra cancelar um envelope já em andamento, abra ele no painel da Clicksign.",
				envelopeStatusLabel(env.Attributes.Status),
			),
		}, nil
	}

	if n := strings.TrimSpace(env.Attributes.Name); n != "" {
		data["envelope_name"] = n
	}
	return buildCancelConfirmation(data, env), nil
}

func (f *CancelEnvelopeFlow) handleConfirmClick(ctx context.Context, in Input) (Result, error) {
	data := flowDataCopy(in.State.Data)
	switch in.Interact.ButtonID {
	case "confirm_yes":
		return f.runCancel(ctx, in, data)
	case "confirm_no":
		return Result{
			Kind:  KindDone,
			Reply: "Beleza, mantive o envelope intacto.",
		}, nil
	default:
		return f.advance(ctx, in, data)
	}
}

func (f *CancelEnvelopeFlow) runCancel(ctx context.Context, in Input, data map[string]any) (Result, error) {
	envID := getDataString(data, "envelope_id")
	if envID == "" {
		return errorResult("Faltou o ID do envelope. Recomeça o pedido, por favor."), nil
	}
	in.Session.PhoneNumber = in.Phone
	apiCtx := clicksign.WithSession(ctx, in.Session)
	if err := f.cs.DeleteEnvelope(apiCtx, in.Phone, envID); err != nil {
		if errIs(err, conv.ErrSessionExpired, clicksign.ErrInvalidToken) {
			return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
		}
		if errIs(err, clicksign.ErrMultiAccount) {
			return transferToSelectAccount("cancel_envelope"), nil
		}
		var apiErr *clicksign.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.Status {
			case 403:
				// Should be rare because we pre-flight via GetEnvelope,
				// but a race (envelope went from draft → running between
				// the Get and the Delete) lands here.
				return Result{
					Kind:  KindDone,
					Reply: "A Clicksign recusou: esse envelope não está mais em rascunho. Pra cancelar um envelope em andamento, abra ele no painel.",
				}, nil
			case 404:
				return Result{
					Kind:  KindDone,
					Reply: "Esse envelope já não existe mais — possivelmente já foi excluído.",
				}, nil
			}
		}
		return errorResult(fmt.Sprintf("Não consegui excluir o envelope: %s.", humanAPIError(err))), err
	}

	envName := getDataString(data, "envelope_name")
	if envName != "" {
		return Result{Kind: KindDone, Reply: fmt.Sprintf("Pronto, excluí o envelope *%s*.", envName)}, nil
	}
	return Result{Kind: KindDone, Reply: "Pronto, envelope excluído."}, nil
}

func (f *CancelEnvelopeFlow) resolveByName(ctx context.Context, in Input, data map[string]any, name string) (Result, error) {
	envelopes, err := f.cs.ListEnvelopes(ctx, in.Phone)
	if err != nil {
		if errIs(err, conv.ErrSessionExpired, clicksign.ErrInvalidToken) {
			return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
		}
		if errIs(err, clicksign.ErrMultiAccount) {
			return transferToSelectAccount("cancel_envelope"), nil
		}
		return errorResult("Não consegui consultar seus envelopes agora. Tenta de novo em alguns segundos."), err
	}
	matches := matchEnvelopesByName(envelopes, name)
	switch len(matches) {
	case 0:
		delete(data, "envelope_name")
		return Result{
			Kind:      KindAsk,
			Reply:     fmt.Sprintf("Não achei envelope com nome contendo *%s*. Me passa o ID do envelope ou o nome exato?", strings.TrimSpace(name)),
			NextState: keepCancelState(data),
		}, nil
	case 1:
		data["envelope_id"] = matches[0].ID
		if n := strings.TrimSpace(matches[0].Attributes.Name); n != "" {
			data["envelope_name"] = n
		}
		return f.advance(ctx, in, data)
	}
	return Result{
		Kind:  KindChoose,
		Reply: fmt.Sprintf("Encontrei %d envelopes com nome parecido. Qual deles você quer excluir?", len(matches)),
		Interactive: &InteractivePayload{
			Type:   "list",
			Header: "Qual envelope?",
			Body:   "Toque pra escolher.",
			Items:  envelopeChoiceItems(matches),
		},
		NextState: &session.FlowState{
			FlowID:  "cancel_envelope",
			Step:    stepAwaitingEnvelopeCancel,
			AskedAt: time.Now().UTC(),
			Data:    data,
		},
	}, nil
}

func mergeCancelEnvelopeEntities(in Input) map[string]any {
	data := flowDataCopy(nil)
	if in.State != nil {
		data = flowDataCopy(in.State.Data)
	}
	if id := stringEntity(in.Entities, "envelope_id"); id != "" {
		data["envelope_id"] = id
	}
	if name := stringEntity(in.Entities, "envelope_name"); name != "" {
		data["envelope_name"] = name
	}
	return data
}

func buildCancelConfirmation(data map[string]any, env *clicksign.Envelope) Result {
	name := strings.TrimSpace(env.Attributes.Name)
	if name == "" {
		name = "(sem nome)"
	}
	statusLabel := envelopeStatusLabel(env.Attributes.Status)
	var sb strings.Builder
	sb.WriteString("⚠️ *Confirmar exclusão de envelope*\n\n")
	sb.WriteString("Nome: *")
	sb.WriteString(name)
	sb.WriteString("*\n")
	sb.WriteString("Status: ")
	sb.WriteString(statusLabel)
	sb.WriteString("\nID: `")
	sb.WriteString(env.ID)
	sb.WriteString("`\n\n")
	sb.WriteString("Essa ação não pode ser desfeita. Confirma?")
	return Result{
		Kind:  KindConfirm,
		Reply: sb.String(),
		Interactive: &InteractivePayload{
			Type: "buttons",
			Body: "Tem certeza?",
			Items: []InteractiveItem{
				{ID: "confirm_yes", Title: "Sim, excluir"},
				{ID: "confirm_no", Title: "Não"},
			},
		},
		NextState: &session.FlowState{
			FlowID:  "cancel_envelope",
			Step:    stepConfirmCancel,
			AskedAt: time.Now().UTC(),
			Data:    data,
		},
	}
}

func keepCancelState(data map[string]any) *session.FlowState {
	return &session.FlowState{
		FlowID:  "cancel_envelope",
		Step:    stepGatheringCancel,
		AskedAt: time.Now().UTC(),
		Data:    data,
	}
}

var _ Flow = (*CancelEnvelopeFlow)(nil)
