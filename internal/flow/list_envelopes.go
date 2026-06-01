package flow

import (
	"context"
	"fmt"
	"strings"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/conv"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// ListEnvelopesFlow renders the envelopes of the currently selected
// Clicksign account as a WhatsApp list message. When the user taps a row
// the next turn transfers to envelope_status to display details.
//
// The NLU may surface filter_status ("pending", "running", etc.) — we
// apply it client-side because the portable subset of the REST API does
// not expose status as a query parameter at this point.
type ListEnvelopesFlow struct {
	cs *clicksign.Client
}

func NewListEnvelopesFlow(cs *clicksign.Client) *ListEnvelopesFlow {
	return &ListEnvelopesFlow{cs: cs}
}

func (f *ListEnvelopesFlow) ID() string { return "list_envelopes" }

const stepAwaitingEnvelopeChoice = "awaiting_envelope_choice"

func (f *ListEnvelopesFlow) Handle(ctx context.Context, in Input) (Result, error) {
	// Case 1: user tapped a row in our own list message → transfer to
	// envelope_status with the chosen id pre-populated.
	if in.Interact != nil && in.State != nil && in.State.Step == stepAwaitingEnvelopeChoice {
		key := strings.TrimSpace(in.Interact.ListItemID)
		if key == "" {
			return f.askWithList(ctx, in)
		}
		return Result{
			Kind:         KindTransfer,
			NextIntent:   "envelope_status",
			NextEntities: map[string]any{"envelope_id": key},
		}, nil
	}

	// Need an account before listing anything.
	if strings.TrimSpace(in.Session.PreferredAccount) == "" {
		return transferToSelectAccount("list_envelopes"), nil
	}
	return f.askWithList(ctx, in)
}

// askWithList fetches envelopes and renders the list (filtered when the
// NLU provided filter_status). Empty result becomes a Done with a tip.
func (f *ListEnvelopesFlow) askWithList(ctx context.Context, in Input) (Result, error) {
	envelopes, err := f.cs.ListEnvelopes(ctx, in.Phone)
	if err != nil {
		return f.errorOrAuth(err)
	}

	filter := normalizeStatusFilter(stringEntity(in.Entities, "filter_status"))
	filtered := filterEnvelopesByStatus(envelopes, filter)

	if len(filtered) == 0 {
		return Result{
			Kind:  KindDone,
			Reply: emptyEnvelopesReply(filter, len(envelopes) == 0),
		}, nil
	}

	items := make([]InteractiveItem, 0, len(filtered))
	for _, e := range filtered {
		name := strings.TrimSpace(e.Attributes.Name)
		if name == "" {
			name = "(sem nome)"
		}
		statusLabel := envelopeStatusLabel(e.Attributes.Status)
		desc := statusLabel
		if d := formatShortDate(e.Attributes.CreatedAt); d != "" {
			desc = fmt.Sprintf("%s • %s", statusLabel, d)
		}
		items = append(items, InteractiveItem{
			ID:          e.ID,
			Title:       truncate(name, maxListTitle),
			Description: truncate(desc, maxListDescription),
		})
		if len(items) == maxListItems {
			break
		}
	}

	header := "Envelopes"
	if filter != "" {
		header = fmt.Sprintf("Envelopes (%s)", envelopeStatusLabel(filter))
	}

	reply := fmt.Sprintf("Encontrei %d envelope(s).", len(filtered))
	if len(filtered) > maxListItems {
		reply = fmt.Sprintf("Encontrei %d envelope(s) (mostrando %d).", len(filtered), maxListItems)
	}

	next := &session.FlowState{
		FlowID: "list_envelopes",
		Step:   stepAwaitingEnvelopeChoice,
	}
	return Result{
		Kind:  KindChoose,
		Reply: reply,
		Interactive: &InteractivePayload{
			Type:   "list",
			Header: header,
			Body:   "Toque em um envelope para ver detalhes.",
			Items:  items,
		},
		NextState: next,
	}, nil
}

func (f *ListEnvelopesFlow) errorOrAuth(err error) (Result, error) {
	if errIs(err, conv.ErrSessionExpired, clicksign.ErrInvalidToken) {
		return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
	}
	if errIs(err, clicksign.ErrMultiAccount) {
		// Stale PreferredAccount in session — caller (router) detected
		// the issue too late. Re-ask the user.
		return transferToSelectAccount("list_envelopes"), nil
	}
	return errorResult("Não consegui listar seus envelopes agora. Tenta de novo em alguns segundos."), err
}

// filterEnvelopesByStatus returns the envelopes whose Status (case-insensitive)
// matches the canonical filter. Empty filter is a passthrough.
func filterEnvelopesByStatus(envs []clicksign.Envelope, canonical string) []clicksign.Envelope {
	if canonical == "" {
		return envs
	}
	out := make([]clicksign.Envelope, 0, len(envs))
	for _, e := range envs {
		if strings.EqualFold(strings.TrimSpace(e.Attributes.Status), canonical) {
			out = append(out, e)
		}
	}
	return out
}

// emptyEnvelopesReply chooses the right friendly text for the zero-results
// branch depending on whether the absence is total or just for the filter.
func emptyEnvelopesReply(filter string, totalIsZero bool) string {
	if totalIsZero {
		return "Você não tem envelopes nessa conta ainda. Que tal criar um? Posso te ajudar."
	}
	if filter == "" {
		return "Não encontrei envelopes."
	}
	return fmt.Sprintf("Nenhum envelope com status *%s* no momento.", envelopeStatusLabel(filter))
}

var _ Flow = (*ListEnvelopesFlow)(nil)
