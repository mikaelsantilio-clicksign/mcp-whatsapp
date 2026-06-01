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

// CreateEnvelopePDFFlow walks the user through creating an envelope from
// a PDF (URL or WhatsApp attachment). The conversation has two visible
// steps:
//
//   1. gathering — collect envelope_name, pdf_url and signers. Each turn
//      merges new entities with what we already have. When something is
//      still missing we send a KindAsk listing what we need.
//   2. awaiting_confirm — present a snapshot of the envelope and ask the
//      user to tap "Confirmar" or "Cancelar". On confirm we download the
//      file and call POST /envelope_bulk_creations.
//
// State that survives across turns lives in FlowState.Data (JSON-safe).
type CreateEnvelopePDFFlow struct {
	cs      *clicksign.Client
	fetcher clicksign.FileFetcher
}

func NewCreateEnvelopePDFFlow(cs *clicksign.Client, fetcher clicksign.FileFetcher) *CreateEnvelopePDFFlow {
	return &CreateEnvelopePDFFlow{cs: cs, fetcher: fetcher}
}

func (f *CreateEnvelopePDFFlow) ID() string { return "create_envelope_pdf" }

const (
	stepGatheringPDF = "gathering"
	stepConfirmPDF   = "awaiting_confirm"
)

func (f *CreateEnvelopePDFFlow) Handle(ctx context.Context, in Input) (Result, error) {
	// Confirmation click handling comes first; it is the only path that
	// actually calls the Clicksign API.
	if in.State != nil && in.State.Step == stepConfirmPDF && isButtonClick(in.Interact) {
		return f.handleConfirmClick(ctx, in)
	}

	if strings.TrimSpace(in.Session.PreferredAccount) == "" {
		return transferToSelectAccount("create_envelope_pdf"), nil
	}

	data := mergePDFEntities(in)
	missing := missingPDFFields(data)
	if len(missing) > 0 {
		return askForMissingPDFFields(data, missing), nil
	}

	// All required entities present → build a draft, validate signers
	// and ask for confirmation. We persist the draft fields so confirm
	// doesn't need to re-parse them.
	return f.buildConfirmation(data)
}

// handleConfirmClick reads ButtonID and either runs the bulk creation or
// cancels gracefully. The session.ActiveFlow is cleared by the router
// based on the returned NextState.
func (f *CreateEnvelopePDFFlow) handleConfirmClick(ctx context.Context, in Input) (Result, error) {
	switch in.Interact.ButtonID {
	case "confirm_yes":
		return f.runBulkCreate(ctx, in)
	case "confirm_no":
		return Result{
			Kind:  KindDone,
			Reply: "Tudo bem, cancelei a criação. Quando quiser, é só me chamar de novo.",
		}, nil
	default:
		// Stray button — re-render the confirm card.
		data := flowDataCopy(in.State.Data)
		missing := missingPDFFields(data)
		if len(missing) > 0 {
			return askForMissingPDFFields(data, missing), nil
		}
		return f.buildConfirmation(data)
	}
}

// runBulkCreate downloads the file, builds the wire payload and calls
// the Clicksign API. Errors are mapped into friendly Replies; we keep
// the session.ActiveFlow alive on download/validation failures so the
// user can retry without rebuilding from scratch.
func (f *CreateEnvelopePDFFlow) runBulkCreate(ctx context.Context, in Input) (Result, error) {
	data := flowDataCopy(in.State.Data)
	draft, err := draftFromPDFData(data)
	if err != nil {
		return errorResult("Tive um problema interpretando os dados que você passou. Pode tentar de novo?"), err
	}

	bytes, mime, ferr := f.fetcher.Fetch(ctx, draft.Document.FileURL)
	if ferr != nil {
		return Result{
			Kind:      KindError,
			Reply:     fmt.Sprintf("Não consegui baixar o arquivo. %s\nManda outro link ou anexa o PDF de novo, por favor.", humanFetchErr(ferr)),
			NextState: in.State,
		}, nil
	}

	req, berr := BuildBulkRequest(draft, bytes, mime)
	if berr != nil {
		return errorResult(fmt.Sprintf("Não consegui montar a requisição: %s", berr.Error())), berr
	}

	resp, aerr := f.cs.CreateEnvelopeBulk(ctx, in.Phone, req)
	if aerr != nil {
		if errors.Is(aerr, conv.ErrSessionExpired) || errors.Is(aerr, clicksign.ErrInvalidToken) {
			return Result{Kind: KindError, Reply: ""}, conv.ErrSessionExpired
		}
		if errors.Is(aerr, clicksign.ErrMultiAccount) {
			return transferToSelectAccount("create_envelope_pdf"), nil
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
		Reply: fmt.Sprintf("Pronto! Envelope *%s* criado com sucesso. Os signatários vão receber por e-mail.\nID: `%s`", draft.Name, envID),
	}, nil
}

// --- gathering & validation helpers ----------------------------------------

// pdfDataKeys are the entries we persist in FlowState.Data between turns.
const (
	pdfKeyName    = "envelope_name"
	pdfKeyURL     = "pdf_url"
	pdfKeySigners = "signers"
)

// mergePDFEntities combines the new NLU entities + attachments with the
// previously-persisted data so the user can fill the form gradually.
// Newer values win; signers from the new turn append (not replace) to the
// existing list when both are non-empty.
func mergePDFEntities(in Input) map[string]any {
	data := flowDataCopy(nil)
	if in.State != nil {
		data = flowDataCopy(in.State.Data)
	}

	if v := stringEntity(in.Entities, "envelope_name"); v != "" {
		data[pdfKeyName] = v
	}
	if v := stringEntity(in.Entities, "pdf_url"); v != "" {
		data[pdfKeyURL] = v
	}
	// Attachments[0] is the canonical "user attached a file" hint.
	if data[pdfKeyURL] == nil && len(in.Attachments) > 0 && strings.TrimSpace(in.Attachments[0].URL) != "" {
		data[pdfKeyURL] = in.Attachments[0].URL
	}
	if raw, ok := in.Entities[pdfKeySigners]; ok {
		newSigners := signersToMaps(SignersFromNLU(raw))
		if len(newSigners) > 0 {
			existing, _ := data[pdfKeySigners].([]any)
			merged := mergeSignerMaps(existing, newSigners)
			data[pdfKeySigners] = merged
		}
	}
	return data
}

// missingPDFFields returns the user-readable labels of fields still
// missing or invalid. Signers are validated here so we can list any
// per-row errors immediately (rather than waiting for the API to reject).
func missingPDFFields(data map[string]any) []string {
	var missing []string

	name := getDataString(data, pdfKeyName)
	if name == "" {
		missing = append(missing, "*Nome do envelope* (ex.: \"Contrato Stg 1\")")
	}

	url := getDataString(data, pdfKeyURL)
	if url == "" {
		missing = append(missing, "*PDF do contrato* (cola a URL ou anexa o arquivo)")
	}

	signerInputs := SignersFromNLU(data[pdfKeySigners])
	if len(signerInputs) == 0 {
		missing = append(missing, "*Pelo menos 1 signatário* com nome completo, e-mail e papel (ex.: \"Mikael Nunes, mikael@x.com, parte\")")
	} else if _, verr := ValidateSigners(signerInputs); verr != nil {
		// Surface the per-row validation errors verbatim.
		missing = append(missing, sanitizeValidationErr(verr))
	}

	return missing
}

// askForMissingPDFFields persists the partial data and asks the user
// for what's still needed.
func askForMissingPDFFields(data map[string]any, missing []string) Result {
	body := "Pra criar esse envelope ainda preciso de:\n• " + strings.Join(missing, "\n• ") + "\n\nManda numa só mensagem por favor."
	return Result{
		Kind:  KindAsk,
		Reply: body,
		NextState: &session.FlowState{
			FlowID:  "create_envelope_pdf",
			Step:    stepGatheringPDF,
			AskedAt: time.Now().UTC(),
			Data:    data,
		},
	}
}

// buildConfirmation turns the persisted data into a draft, renders the
// summary, and sets up KindConfirm with quick reply buttons.
func (f *CreateEnvelopePDFFlow) buildConfirmation(data map[string]any) (Result, error) {
	draft, err := draftFromPDFData(data)
	if err != nil {
		return errorResult(err.Error()), err
	}

	summary := renderConfirmSummary(draft)
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
			FlowID:  "create_envelope_pdf",
			Step:    stepConfirmPDF,
			AskedAt: time.Now().UTC(),
			Data:    data,
		},
	}, nil
}

// draftFromPDFData runs final validation and assembles an EnvelopeDraft.
// Returns an error suitable for end-user display when something is off.
func draftFromPDFData(data map[string]any) (EnvelopeDraft, error) {
	name := getDataString(data, pdfKeyName)
	if name == "" {
		return EnvelopeDraft{}, fmt.Errorf("nome do envelope ainda não foi informado")
	}
	url := getDataString(data, pdfKeyURL)
	if url == "" {
		return EnvelopeDraft{}, fmt.Errorf("o PDF do envelope ainda não foi informado")
	}
	signers := SignersFromNLU(data[pdfKeySigners])
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
		Document: DocumentDraft{FileURL: url},
		Signers:  withRaw,
	}, nil
}

// renderConfirmSummary builds a friendly pt-BR text the user can read
// before tapping Confirmar. We keep it tight to fit the WhatsApp body
// field (which is limited to 1024 chars).
func renderConfirmSummary(d EnvelopeDraft) string {
	var sb strings.Builder
	sb.WriteString("Vou criar este envelope. Confirma?\n\n")
	sb.WriteString("📄 *")
	sb.WriteString(d.Name)
	sb.WriteString("*\n")
	sb.WriteString("Documento: ")
	if base := DeriveFilenameFromURL(d.Document.FileURL); base != "" {
		sb.WriteString(base)
	} else {
		sb.WriteString("PDF anexado")
	}
	sb.WriteString("\n\n👥 Signatários (")
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
	// WhatsApp button body is capped at 1024 chars; truncate aggressively.
	out := sb.String()
	if len(out) > 1000 {
		out = out[:1000] + "…"
	}
	return out
}

// --- generic helpers used by both pdf and tmpl flows -----------------------

// isButtonClick reports whether the interactive reply carries a button id
// (as opposed to a list item id).
func isButtonClick(ir *InteractiveReply) bool {
	return ir != nil && strings.TrimSpace(ir.ButtonID) != ""
}

// flowDataCopy returns a shallow copy of the persisted Data map, with a
// non-nil result so callers can always assign into it.
func flowDataCopy(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// getDataString reads a string field. The keys we persist always come
// from getDataString writes — but the same Data map is mutated by NLU
// entities, so we tolerate the value being typed loosely.
func getDataString(data map[string]any, key string) string {
	if v, ok := data[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// signersToMaps converts SignerInput rows into the same map[string]any
// shape the NLU emits, so the merge logic can store them uniformly.
func signersToMaps(in []SignerInput) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, map[string]any{
			"name":         s.Name,
			"email":        s.Email,
			"phone_number": s.PhoneNumber,
			"role":         s.Role,
		})
	}
	return out
}

// mergeSignerMaps deduplicates by email (case-insensitive). New rows
// replace existing ones with the same email, otherwise they append.
func mergeSignerMaps(existing []any, incoming []any) []any {
	byEmail := map[string]int{}
	merged := make([]any, 0, len(existing)+len(incoming))
	for _, r := range existing {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		merged = append(merged, m)
		if e := strings.ToLower(getString(m, "email")); e != "" {
			byEmail[e] = len(merged) - 1
		}
	}
	for _, r := range incoming {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		email := strings.ToLower(getString(m, "email"))
		if email != "" {
			if idx, ok := byEmail[email]; ok {
				merged[idx] = m
				continue
			}
		}
		merged = append(merged, m)
		if email != "" {
			byEmail[email] = len(merged) - 1
		}
	}
	return merged
}

// sanitizeValidationErr removes the "signer invalid:" sentinel prefix and
// returns the user-facing list.
func sanitizeValidationErr(err error) string {
	s := err.Error()
	const prefix = "signer invalid:"
	if i := strings.Index(s, prefix); i >= 0 {
		s = strings.TrimSpace(s[i+len(prefix):])
	}
	return s
}

// humanFetchErr trims internal verbiage from a FileFetcher error so the
// user sees just the actionable bit.
func humanFetchErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// We prefix our own messages with "file_url ... — ..." or "arquivo ...".
	// Strip the noisy bits.
	if i := strings.Index(msg, ": "); i > 0 && i < 30 {
		return msg[i+2:]
	}
	return msg
}

// humanAPIError tries to extract a useful sentence from an *APIError or a
// generic error message. We never echo the raw JSON body to the user.
func humanAPIError(err error) string {
	var apiErr *clicksign.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 400:
			return "alguns campos não passaram na validação (verifique nome, e-mail e papel dos signatários)"
		case 401, 403:
			return "sua sessão Clicksign expirou ou perdeu acesso a esta conta"
		case 422:
			return "a Clicksign retornou erro de regra de negócio (ex.: papel inválido para a conta)"
		case 429:
			return "muitas requisições — tenta em alguns minutos"
		case 500, 502, 503, 504:
			return "a Clicksign está com instabilidade no momento"
		}
		return fmt.Sprintf("erro %d da Clicksign", apiErr.Status)
	}
	return "erro ao chamar a Clicksign"
}

var _ Flow = (*CreateEnvelopePDFFlow)(nil)
