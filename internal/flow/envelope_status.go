package flow

import (
	"context"
	"fmt"
	"strings"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/conv"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// EnvelopeStatusFlow renders details of a single envelope.
//
// Resolution rules (in priority order):
//
//  1. Entities.envelope_id is set     → GetEnvelope.
//  2. Entities.envelope_name is set   → ListEnvelopes + fuzzy contains.
//     - 1 match  → GetEnvelope.
//     - N matches → choose list.
//     - 0 matches → friendly "didn't find" + offer to list all.
//  3. Otherwise → transfer to list_envelopes with return_to=envelope_status.
type EnvelopeStatusFlow struct {
	cs *clicksign.Client
}

func NewEnvelopeStatusFlow(cs *clicksign.Client) *EnvelopeStatusFlow {
	return &EnvelopeStatusFlow{cs: cs}
}

func (f *EnvelopeStatusFlow) ID() string { return "envelope_status" }

const stepAwaitingEnvelopeForStatus = "awaiting_envelope_for_status"

func (f *EnvelopeStatusFlow) Handle(ctx context.Context, in Input) (Result, error) {
	// Case 1: user tapped a row from our own disambiguation list.
	if in.Interact != nil && in.State != nil && in.State.Step == stepAwaitingEnvelopeForStatus {
		key := strings.TrimSpace(in.Interact.ListItemID)
		if key != "" {
			return f.fetchAndRender(ctx, in, key)
		}
	}

	if strings.TrimSpace(in.Session.PreferredAccount) == "" {
		return transferToSelectAccount("envelope_status"), nil
	}

	if id := stringEntity(in.Entities, "envelope_id"); id != "" {
		return f.fetchAndRender(ctx, in, id)
	}

	if name := stringEntity(in.Entities, "envelope_name"); name != "" {
		return f.resolveByName(ctx, in, name)
	}

	// No hint at all — let list_envelopes take over so the user picks.
	return Result{
		Kind:       KindTransfer,
		NextIntent: "list_envelopes",
	}, nil
}

func (f *EnvelopeStatusFlow) fetchAndRender(ctx context.Context, in Input, id string) (Result, error) {
	env, err := f.cs.GetEnvelope(ctx, in.Phone, id)
	if err != nil {
		if errIs(err, conv.ErrSessionExpired, clicksign.ErrInvalidToken) {
			return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
		}
		if errIs(err, clicksign.ErrMultiAccount) {
			return transferToSelectAccount("envelope_status"), nil
		}
		// 404 (envelope from another account or deleted) lands here as
		// a generic APIError. We surface a soft "didn't find" rather
		// than the raw API error so the user can try again or pick.
		return Result{
			Kind:  KindDone,
			Reply: "Não encontrei esse envelope na conta selecionada. Quer ver a lista completa?",
		}, nil
	}
	return Result{
		Kind:  KindDone,
		Reply: formatEnvelopeDetails(env),
	}, nil
}

// resolveByName performs a case-insensitive substring match on the
// envelope name. When the match is ambiguous we render a list so the
// user disambiguates with a tap.
func (f *EnvelopeStatusFlow) resolveByName(ctx context.Context, in Input, name string) (Result, error) {
	envelopes, err := f.cs.ListEnvelopes(ctx, in.Phone)
	if err != nil {
		if errIs(err, conv.ErrSessionExpired, clicksign.ErrInvalidToken) {
			return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
		}
		if errIs(err, clicksign.ErrMultiAccount) {
			return transferToSelectAccount("envelope_status"), nil
		}
		return errorResult("Não consegui consultar seus envelopes agora. Tenta de novo em alguns segundos."), err
	}

	matches := matchEnvelopesByName(envelopes, name)
	switch len(matches) {
	case 0:
		return Result{
			Kind:  KindDone,
			Reply: fmt.Sprintf("Não encontrei envelope com nome contendo *%s*. Quer ver a lista completa?", strings.TrimSpace(name)),
		}, nil
	case 1:
		return f.fetchAndRender(ctx, in, matches[0].ID)
	}

	items := make([]InteractiveItem, 0, len(matches))
	for _, e := range matches {
		statusLabel := envelopeStatusLabel(e.Attributes.Status)
		desc := statusLabel
		if d := formatShortDate(e.Attributes.CreatedAt); d != "" {
			desc = fmt.Sprintf("%s • %s", statusLabel, d)
		}
		envName := strings.TrimSpace(e.Attributes.Name)
		if envName == "" {
			envName = "(sem nome)"
		}
		items = append(items, InteractiveItem{
			ID:          e.ID,
			Title:       truncate(envName, maxListTitle),
			Description: truncate(desc, maxListDescription),
		})
		if len(items) == maxListItems {
			break
		}
	}
	return Result{
		Kind:  KindChoose,
		Reply: fmt.Sprintf("Encontrei %d envelopes com nome parecido. Qual você quer?", len(matches)),
		Interactive: &InteractivePayload{
			Type:   "list",
			Header: "Qual envelope?",
			Body:   "Toque pra ver detalhes.",
			Items:  items,
		},
		NextState: &session.FlowState{
			FlowID: "envelope_status",
			Step:   stepAwaitingEnvelopeForStatus,
		},
	}, nil
}

// matchEnvelopesByName returns envelopes whose Name contains query as a
// substring (case-insensitive, accent-insensitive would be nicer but is
// scope creep for the hackathon — leave for later).
func matchEnvelopesByName(envs []clicksign.Envelope, query string) []clicksign.Envelope {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	out := make([]clicksign.Envelope, 0)
	for _, e := range envs {
		if strings.Contains(strings.ToLower(e.Attributes.Name), q) {
			out = append(out, e)
		}
	}
	return out
}

// formatEnvelopeDetails renders an envelope into a short pt-BR text
// suitable for a WhatsApp message. We keep it tight (4-5 lines) to fit
// on the screen without scrolling.
func formatEnvelopeDetails(e *clicksign.Envelope) string {
	var sb strings.Builder
	name := strings.TrimSpace(e.Attributes.Name)
	if name == "" {
		name = "(sem nome)"
	}
	emoji := envelopeStatusEmoji(e.Attributes.Status)
	if emoji != "" {
		sb.WriteString(emoji)
		sb.WriteString(" ")
	}
	sb.WriteString("*")
	sb.WriteString(name)
	sb.WriteString("*\n")
	sb.WriteString("Status: ")
	sb.WriteString(envelopeStatusLabel(e.Attributes.Status))
	if d := formatShortDate(e.Attributes.CreatedAt); d != "" {
		sb.WriteString("\nCriado em ")
		sb.WriteString(d)
	}
	if d := formatShortDate(e.Attributes.UpdatedAt); d != "" && d != formatShortDate(e.Attributes.CreatedAt) {
		sb.WriteString("\nAtualizado em ")
		sb.WriteString(d)
	}
	sb.WriteString("\nID: `")
	sb.WriteString(e.ID)
	sb.WriteString("`")
	return sb.String()
}

var _ Flow = (*EnvelopeStatusFlow)(nil)
