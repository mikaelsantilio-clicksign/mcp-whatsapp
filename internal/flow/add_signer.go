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

// AddSignerFlow walks the user through adding a single signer to an
// existing envelope. The conversation has up to three visible steps:
//
//  1. gathering          — collect envelope (id or name) + signer fields.
//                          Each turn merges new entities with what we
//                          already have.
//  2. awaiting_choice    — the user typed an envelope name that matched
//                          more than one row; we render a list to pick.
//  3. awaiting_confirm   — show a snapshot and ask "Confirmar?" with
//                          quick-reply buttons. Confirmation triggers
//                          POST /envelopes/{id}/signers.
//
// We persist only what we need in FlowState.Data:
//   - "envelope_id"   (string)
//   - "envelope_name" (string, for the confirm summary)
//   - "signer"        (map[string]any with name/email/phone/role)
type AddSignerFlow struct {
	cs *clicksign.Client
}

func NewAddSignerFlow(cs *clicksign.Client) *AddSignerFlow {
	return &AddSignerFlow{cs: cs}
}

func (f *AddSignerFlow) ID() string { return "add_signer" }

const (
	stepGatheringAddSigner       = "gathering"
	stepAwaitingEnvelopeAddSigner = "awaiting_envelope_choice"
	stepConfirmAddSigner         = "awaiting_confirm"
)

func (f *AddSignerFlow) Handle(ctx context.Context, in Input) (Result, error) {
	// Confirm button click → the only path that actually calls the API.
	if in.State != nil && in.State.Step == stepConfirmAddSigner && isButtonClick(in.Interact) {
		return f.handleConfirmClick(ctx, in)
	}

	// Disambiguation: user tapped one of the envelopes we listed.
	if in.State != nil && in.State.Step == stepAwaitingEnvelopeAddSigner && in.Interact != nil {
		key := strings.TrimSpace(in.Interact.ListItemID)
		if key != "" {
			data := flowDataCopy(in.State.Data)
			data["envelope_id"] = key
			return f.advance(ctx, in, data)
		}
	}

	if strings.TrimSpace(in.Session.PreferredAccount) == "" {
		return transferToSelectAccount("add_signer"), nil
	}

	data := mergeAddSignerEntities(in)
	return f.advance(ctx, in, data)
}

// advance decides what the next step is given the current Data snapshot.
// It is called both at the start of the flow and after the user resolves
// a disambiguation tap.
func (f *AddSignerFlow) advance(ctx context.Context, in Input, data map[string]any) (Result, error) {
	envID := getDataString(data, "envelope_id")
	envName := getDataString(data, "envelope_name")

	// Resolve envelope when we only have a name. The list_envelopes call
	// is the same we use for envelope_status — see resolveByName below.
	if envID == "" {
		if envName != "" {
			res, err := f.resolveByName(ctx, in, data, envName)
			if err != nil || res.Kind != KindAsk {
				return res, err
			}
			// resolveByName returned KindAsk only when we should ask the
			// user for an envelope from scratch (couldn't find any). Fall
			// through.
			return res, nil
		}
		return askForEnvelopeAddSigner(data), nil
	}

	// Signer presence + validation.
	signer, ok := data["signer"].(map[string]any)
	if !ok || signer == nil {
		return askForSignerAddSigner(data, ""), nil
	}
	validated, verr := ValidateSigners([]SignerInput{{
		Name:        getString(signer, "name"),
		Email:       getString(signer, "email"),
		PhoneNumber: getString(signer, "phone_number"),
		Role:        getString(signer, "role"),
	}})
	if verr != nil {
		return askForSignerAddSigner(data, sanitizeValidationErr(verr)), nil
	}

	// All data set + validated — build confirmation card.
	return buildAddSignerConfirmation(data, validated[0]), nil
}

func (f *AddSignerFlow) handleConfirmClick(ctx context.Context, in Input) (Result, error) {
	data := flowDataCopy(in.State.Data)
	switch in.Interact.ButtonID {
	case "confirm_yes":
		return f.runAddSigner(ctx, in, data)
	case "confirm_no":
		return Result{
			Kind:  KindDone,
			Reply: "Tudo bem, não adicionei o signatário. Quando quiser, é só me chamar de novo.",
		}, nil
	default:
		// Stray button — re-render the confirm card from persisted data.
		return f.advance(ctx, in, data)
	}
}

func (f *AddSignerFlow) runAddSigner(ctx context.Context, in Input, data map[string]any) (Result, error) {
	envID := getDataString(data, "envelope_id")
	signerMap, _ := data["signer"].(map[string]any)
	if envID == "" || signerMap == nil {
		// Shouldn't happen — buildAddSignerConfirmation only fires when
		// both are present — but be defensive.
		return errorResult("Faltam dados pra adicionar o signatário. Recomeça aí, por favor."), nil
	}

	validated, verr := ValidateSigners([]SignerInput{{
		Name:        getString(signerMap, "name"),
		Email:       getString(signerMap, "email"),
		PhoneNumber: getString(signerMap, "phone_number"),
		Role:        getString(signerMap, "role"),
	}})
	if verr != nil {
		return Result{
			Kind:      KindAsk,
			Reply:     "Acabei vendo um problema nos dados:\n• " + sanitizeValidationErr(verr) + "\nManda os dados de novo, por favor.",
			NextState: keepAddSignerState(data),
		}, verr
	}

	v := validated[0]
	in.Session.PhoneNumber = in.Phone
	apiCtx := clicksign.WithSession(ctx, in.Session)
	result, err := f.cs.AddSigner(apiCtx, in.Phone, envID, clicksign.AddSignerInput{
		Name:  v.Name,
		Email: v.Email,
	})
	if err != nil {
		if errIs(err, conv.ErrSessionExpired, clicksign.ErrInvalidToken) {
			return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
		}
		if errIs(err, clicksign.ErrMultiAccount) {
			return transferToSelectAccount("add_signer"), nil
		}
		// API rejected — keep state so the user can edit & retry.
		return Result{
			Kind:      KindAsk,
			Reply:     fmt.Sprintf("A Clicksign recusou adicionar esse signatário: %s\nQuer corrigir o e-mail/nome e tentar de novo?", humanAPIError(err)),
			NextState: keepAddSignerState(data),
		}, err
	}

	envName := getDataString(data, "envelope_name")
	reply := fmt.Sprintf("Pronto! Adicionei *%s* (%s) ao envelope.", result.Name, result.Email)
	if envName != "" {
		reply = fmt.Sprintf("Pronto! Adicionei *%s* (%s) ao envelope *%s*.", result.Name, result.Email, envName)
	}
	return Result{Kind: KindDone, Reply: reply}, nil
}

// resolveByName mirrors EnvelopeStatusFlow.resolveByName: fuzzy match on
// the listing, then commit / disambiguate / give up.
func (f *AddSignerFlow) resolveByName(ctx context.Context, in Input, data map[string]any, name string) (Result, error) {
	envelopes, err := f.cs.ListEnvelopes(ctx, in.Phone)
	if err != nil {
		if errIs(err, conv.ErrSessionExpired, clicksign.ErrInvalidToken) {
			return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
		}
		if errIs(err, clicksign.ErrMultiAccount) {
			return transferToSelectAccount("add_signer"), nil
		}
		return errorResult("Não consegui consultar seus envelopes agora. Tenta de novo em alguns segundos."), err
	}
	matches := matchEnvelopesByName(envelopes, name)
	switch len(matches) {
	case 0:
		// Drop the bad hint so the user gets a clean "qual envelope?" ask.
		delete(data, "envelope_name")
		return Result{
			Kind:      KindAsk,
			Reply:     fmt.Sprintf("Não achei envelope com nome contendo *%s*. Me passa o ID do envelope ou o nome exato?", strings.TrimSpace(name)),
			NextState: keepAddSignerState(data),
		}, nil
	case 1:
		data["envelope_id"] = matches[0].ID
		if n := strings.TrimSpace(matches[0].Attributes.Name); n != "" {
			data["envelope_name"] = n
		}
		return f.advance(ctx, in, data)
	}
	items := envelopeChoiceItems(matches)
	return Result{
		Kind:  KindChoose,
		Reply: fmt.Sprintf("Encontrei %d envelopes com nome parecido. Em qual deles você quer adicionar o signatário?", len(matches)),
		Interactive: &InteractivePayload{
			Type:   "list",
			Header: "Qual envelope?",
			Body:   "Toque pra escolher.",
			Items:  items,
		},
		NextState: &session.FlowState{
			FlowID:  "add_signer",
			Step:    stepAwaitingEnvelopeAddSigner,
			AskedAt: time.Now().UTC(),
			Data:    data,
		},
	}, nil
}

// mergeAddSignerEntities combines the persisted Data with the entities
// extracted from this turn. We take at most one signer (the first) since
// /envelopes/{id}/signers only accepts a single body per call.
func mergeAddSignerEntities(in Input) map[string]any {
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
	if raw, ok := in.Entities["signers"]; ok {
		fresh := SignersFromNLU(raw)
		if len(fresh) > 0 {
			// Only the first signer is kept; the others are ignored on
			// purpose so the confirm card stays simple.
			s := fresh[0]
			data["signer"] = map[string]any{
				"name":         s.Name,
				"email":        s.Email,
				"phone_number": s.PhoneNumber,
				"role":         s.Role,
			}
		}
	}
	return data
}

func askForEnvelopeAddSigner(data map[string]any) Result {
	return Result{
		Kind:      KindAsk,
		Reply:     "Em qual envelope você quer adicionar o signatário? Me passa o nome ou o ID.",
		NextState: keepAddSignerState(data),
	}
}

func askForSignerAddSigner(data map[string]any, extraReason string) Result {
	prefix := "Me passa o signatário no formato *Nome Completo, email@empresa.com, papel*."
	if extraReason != "" {
		prefix = "Vi um problema:\n• " + extraReason + "\n" + prefix
	}
	return Result{
		Kind:      KindAsk,
		Reply:     prefix,
		NextState: keepAddSignerState(data),
	}
}

func buildAddSignerConfirmation(data map[string]any, v ValidatedSigner) Result {
	envName := getDataString(data, "envelope_name")
	envID := getDataString(data, "envelope_id")
	var sb strings.Builder
	sb.WriteString("Posso adicionar este signatário?\n\n")
	sb.WriteString("*Envelope:* ")
	if envName != "" {
		sb.WriteString(envName)
		sb.WriteString("\n")
	}
	sb.WriteString("`")
	sb.WriteString(envID)
	sb.WriteString("`\n\n")
	sb.WriteString("*Signatário:*\n")
	sb.WriteString("• Nome: ")
	sb.WriteString(v.Name)
	sb.WriteString("\n• E-mail: ")
	sb.WriteString(v.Email)
	sb.WriteString("\n• Papel: ")
	sb.WriteString(v.Role)

	// Normalise persisted signer to the validated form so retry uses the
	// canonical role.
	data["signer"] = map[string]any{
		"name":         v.Name,
		"email":        v.Email,
		"phone_number": v.PhoneNumber,
		"role":         v.Role,
	}
	return Result{
		Kind:  KindConfirm,
		Reply: sb.String(),
		Interactive: &InteractivePayload{
			Type: "buttons",
			Body: "Confere os dados acima.",
			Items: []InteractiveItem{
				{ID: "confirm_yes", Title: "Confirmar"},
				{ID: "confirm_no", Title: "Cancelar"},
			},
		},
		NextState: &session.FlowState{
			FlowID:  "add_signer",
			Step:    stepConfirmAddSigner,
			AskedAt: time.Now().UTC(),
			Data:    data,
		},
	}
}

func keepAddSignerState(data map[string]any) *session.FlowState {
	return &session.FlowState{
		FlowID:  "add_signer",
		Step:    stepGatheringAddSigner,
		AskedAt: time.Now().UTC(),
		Data:    data,
	}
}

// envelopeChoiceItems renders a slice of envelopes into the same list
// layout used by envelope_status. Lives here (rather than helpers.go) to
// keep helpers.go free of envelope-flavoured rendering.
func envelopeChoiceItems(envs []clicksign.Envelope) []InteractiveItem {
	out := make([]InteractiveItem, 0, len(envs))
	for _, e := range envs {
		statusLabel := envelopeStatusLabel(e.Attributes.Status)
		desc := statusLabel
		if d := formatShortDate(e.Attributes.CreatedAt); d != "" {
			desc = fmt.Sprintf("%s • %s", statusLabel, d)
		}
		name := strings.TrimSpace(e.Attributes.Name)
		if name == "" {
			name = "(sem nome)"
		}
		out = append(out, InteractiveItem{
			ID:          e.ID,
			Title:       truncate(name, maxListTitle),
			Description: truncate(desc, maxListDescription),
		})
		if len(out) == maxListItems {
			break
		}
	}
	return out
}

var _ Flow = (*AddSignerFlow)(nil)
