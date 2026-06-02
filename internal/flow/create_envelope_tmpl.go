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

// CreateEnvelopeTmplFlow walks the user through creating an envelope from
// an existing Clicksign template.
//
// State machine (same shape as CreateEnvelopePDFFlow so the user "feels"
// a consistent UX):
//
//  1. gathering — collect envelope_name, template_id and signers. The
//     template can be supplied by NLU as template_id or by hopping out
//     to list_templates with return_to=create_envelope_tmpl.
//  2. awaiting_confirm — render snapshot, wait for confirm_yes/no.
//
// Template variables (the `data` map for filling placeholders) are out
// of scope for the MVP — we send an empty map and Clicksign uses default
// values. A future flow can extend this.
type CreateEnvelopeTmplFlow struct {
	cs *clicksign.Client
}

func NewCreateEnvelopeTmplFlow(cs *clicksign.Client) *CreateEnvelopeTmplFlow {
	return &CreateEnvelopeTmplFlow{cs: cs}
}

func (f *CreateEnvelopeTmplFlow) ID() string { return "create_envelope_tmpl" }

const (
	stepGatheringTmpl = "gathering"
	stepConfirmTmpl   = "awaiting_confirm"
)

const (
	tmplKeyName       = "envelope_name"
	tmplKeyTemplateID = "template_id"
	tmplKeySigners    = "signers"
)

func (f *CreateEnvelopeTmplFlow) Handle(ctx context.Context, in Input) (Result, error) {
	if in.State != nil && in.State.Step == stepConfirmTmpl && isButtonClick(in.Interact) {
		return f.handleConfirmClick(ctx, in)
	}

	if strings.TrimSpace(in.Session.PreferredAccount) == "" {
		return transferToSelectAccount("create_envelope_tmpl"), nil
	}

	data := mergeTmplEntities(in)

	// If template is still missing, transfer to list_templates so the
	// user can pick (it will hop back here with template_id populated).
	if getDataString(data, tmplKeyTemplateID) == "" {
		return Result{
			Kind:       KindTransfer,
			NextIntent: "list_templates",
			NextState: &session.FlowState{
				FlowID:  "list_templates",
				Step:    "starting",
				AskedAt: time.Now().UTC(),
				Data:    map[string]any{"return_to": "create_envelope_tmpl"},
			},
		}, nil
	}

	missing := missingTmplFields(data)
	if len(missing) > 0 {
		return askForMissingTmplFields(data, missing), nil
	}

	return f.buildConfirmation(data)
}

func (f *CreateEnvelopeTmplFlow) handleConfirmClick(ctx context.Context, in Input) (Result, error) {
	switch in.Interact.ButtonID {
	case "confirm_yes":
		return f.runBulkCreate(ctx, in)
	case "confirm_no":
		return Result{
			Kind:  KindDone,
			Reply: "Tudo bem, cancelei a criação. Quando quiser, é só me chamar de novo.",
		}, nil
	default:
		data := flowDataCopy(in.State.Data)
		if missing := missingTmplFields(data); len(missing) > 0 {
			return askForMissingTmplFields(data, missing), nil
		}
		return f.buildConfirmation(data)
	}
}

func (f *CreateEnvelopeTmplFlow) runBulkCreate(ctx context.Context, in Input) (Result, error) {
	data := flowDataCopy(in.State.Data)
	draft, err := draftFromTmplData(data)
	if err != nil {
		return errorResult("Tive um problema interpretando os dados que você passou. Pode tentar de novo?"), err
	}
	req, berr := BuildBulkRequest(draft, nil, "")
	if berr != nil {
		return errorResult(fmt.Sprintf("Não consegui montar a requisição: %s", berr.Error())), berr
	}

	resp, aerr := f.cs.CreateEnvelopeBulk(ctx, in.Phone, req)
	if aerr != nil {
		if errors.Is(aerr, conv.ErrSessionExpired) || errors.Is(aerr, clicksign.ErrInvalidToken) {
			return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
		}
		if errors.Is(aerr, clicksign.ErrMultiAccount) {
			return transferToSelectAccount("create_envelope_tmpl"), nil
		}
		return Result{
			Kind:      KindError,
			Reply:     fmt.Sprintf("A Clicksign recusou a criação: %s\nQuer tentar de novo?", humanAPIError(aerr)),
			NextState: in.State,
		}, aerr
	}

	envID := resp.Data.Attributes.EnvelopeID
	if envID == "" {
		envID = resp.Data.ID
	}
	return Result{
		Kind:  KindDone,
		Reply: fmt.Sprintf("Pronto! Envelope *%s* criado a partir do template. Os signatários vão receber por e-mail.\nID: `%s`", draft.Name, envID),
	}, nil
}

// --- gathering & validation helpers ----------------------------------------

func mergeTmplEntities(in Input) map[string]any {
	data := flowDataCopy(nil)
	if in.State != nil {
		data = flowDataCopy(in.State.Data)
	}
	if v := stringEntity(in.Entities, "envelope_name"); v != "" {
		data[tmplKeyName] = v
	}
	if v := stringEntity(in.Entities, "template_id"); v != "" {
		data[tmplKeyTemplateID] = v
	}
	if raw, ok := in.Entities[tmplKeySigners]; ok {
		newSigners := signersToMaps(SignersFromNLU(raw))
		if len(newSigners) > 0 {
			existing, _ := data[tmplKeySigners].([]any)
			data[tmplKeySigners] = mergeSignerMaps(existing, newSigners)
		}
	}
	return data
}

func missingTmplFields(data map[string]any) []string {
	var missing []string

	if getDataString(data, tmplKeyName) == "" {
		missing = append(missing, "*Nome do envelope* (ex.: \"Contrato Stg 1\")")
	}

	signerInputs := SignersFromNLU(data[tmplKeySigners])
	if len(signerInputs) == 0 {
		missing = append(missing, "*Pelo menos 1 signatário* com nome completo, e-mail e papel")
	} else if _, verr := ValidateSigners(signerInputs); verr != nil {
		missing = append(missing, sanitizeValidationErr(verr))
	}
	return missing
}

func askForMissingTmplFields(data map[string]any, missing []string) Result {
	body := "Pra criar esse envelope ainda preciso de:\n• " + strings.Join(missing, "\n• ") + "\n\nManda numa só mensagem por favor."
	return Result{
		Kind:  KindAsk,
		Reply: body,
		NextState: &session.FlowState{
			FlowID:  "create_envelope_tmpl",
			Step:    stepGatheringTmpl,
			AskedAt: time.Now().UTC(),
			Data:    data,
		},
	}
}

func (f *CreateEnvelopeTmplFlow) buildConfirmation(data map[string]any) (Result, error) {
	draft, err := draftFromTmplData(data)
	if err != nil {
		return errorResult(err.Error()), err
	}
	summary := renderConfirmSummaryTmpl(draft)
	return Result{
		Kind:  KindConfirm,
		Reply: summary,
		Interactive: &InteractivePayload{
			Type: "buttons",
			Body: summary,
			Items: []InteractiveItem{
				{ID: "confirm_yes", Title: "Confirmar"},
				{ID: "confirm_no", Title: "Cancelar"},
			},
		},
		NextState: &session.FlowState{
			FlowID:  "create_envelope_tmpl",
			Step:    stepConfirmTmpl,
			AskedAt: time.Now().UTC(),
			Data:    data,
		},
	}, nil
}

func draftFromTmplData(data map[string]any) (EnvelopeDraft, error) {
	name := getDataString(data, tmplKeyName)
	if name == "" {
		return EnvelopeDraft{}, fmt.Errorf("nome do envelope ainda não foi informado")
	}
	tmplID := getDataString(data, tmplKeyTemplateID)
	if tmplID == "" {
		return EnvelopeDraft{}, fmt.Errorf("template ainda não foi escolhido")
	}
	signers := SignersFromNLU(data[tmplKeySigners])
	validated, verr := ValidateSigners(signers)
	if verr != nil {
		return EnvelopeDraft{}, verr
	}
	withRaw := make([]ValidatedSignerWithRaw, 0, len(validated))
	for i, v := range validated {
		raw := ""
		if i < len(signers) {
			raw = signers[i].Role
		}
		withRaw = append(withRaw, ValidatedSignerWithRaw{ValidatedSigner: v, RoleRaw: raw})
	}
	return EnvelopeDraft{
		Name:     name,
		Document: DocumentDraft{TemplateID: tmplID},
		Signers:  withRaw,
	}, nil
}

func renderConfirmSummaryTmpl(d EnvelopeDraft) string {
	var sb strings.Builder
	sb.WriteString("Vou criar este envelope. Confirma?\n\n")
	sb.WriteString("📄 *")
	sb.WriteString(d.Name)
	sb.WriteString("*\n")
	sb.WriteString("A partir do template: `")
	sb.WriteString(d.Document.TemplateID)
	sb.WriteString("`\n\n👥 Signatários (")
	sb.WriteString(fmt.Sprintf("%d", len(d.Signers)))
	sb.WriteString("):\n")
	for _, s := range d.Signers {
		roleLabel := s.RoleRaw
		if strings.TrimSpace(roleLabel) == "" {
			roleLabel = s.Role
		}
		sb.WriteString("• ")
		sb.WriteString(s.Name)
		sb.WriteString(" (")
		sb.WriteString(s.Email)
		sb.WriteString(") — ")
		sb.WriteString(roleLabel)
		sb.WriteString("\n")
	}
	out := sb.String()
	if len(out) > 1000 {
		out = out[:1000] + "…"
	}
	return out
}

var _ Flow = (*CreateEnvelopeTmplFlow)(nil)
