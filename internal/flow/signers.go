package flow

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/nlu"
)

// allowedRequirementRoles is a subset of Clicksign's role allowlist
// (Signer::VALID_ACTIONS in tavola). We keep the same list as the
// mcp-api-tavola-v3 server so the API will not reject our payload.
//
// Source: clicksign/mcp-api-tavola-v3/internal/mcp/validation.go.
var allowedRequirementRoles = map[string]bool{
	"account_holder": true, "accountant": true, "administrator": true, "approve": true,
	"associate": true, "attorney": true, "bailee": true, "borrower": true, "broker": true,
	"building_manager": true, "buyer": true, "collateral_provider": true, "comforter": true,
	"condominium_member": true, "consented": true, "consenting": true,
	"consenting_intervenor": true, "consignee": true, "contractee": true, "contractor": true,
	"co_responsible": true, "creditor": true, "debtor": true, "director": true,
	"distracted": true, "distracting": true, "donee": true, "donor": true,
	"employee": true, "employer": true, "endorsee": true, "endorser": true,
	"first_amending": true, "franchisee": true, "franchisor": true, "grantee": true,
	"grantor": true, "guarantor": true, "guarantor_spouse": true, "insurance_broker": true,
	"insured": true, "intermediary": true, "intervening": true, "intervening_guarantor": true,
	"issuer": true, "joint_debtor": true, "landlord": true, "lawyer": true,
	"legal_guardian": true, "legal_representative": true, "lender": true, "lessee": true,
	"lessor": true, "licensed": true, "licensor": true, "manager": true, "owner": true,
	"party": true, "partner": true, "pledged": true, "president": true, "ratify": true,
	"real_estate_broker": true, "receipt": true, "resident": true, "second_amending": true,
	"secretary": true, "secured": true, "seller": true, "service_provider": true,
	"sign": true, "surveyor": true, "surety": true, "tenant": true, "transferee": true,
	"transferor": true, "treasurer": true, "validator": true, "witness": true,
}

// roleAliases maps the common Portuguese (and a few English) terms to the
// canonical role we will send to Clicksign. Only unambiguous mappings are
// included — when the user types "parte" we still map to "party" because
// that's the most-used role for arbitrary signatories and the API never
// rejects it. Source: same as allowedRequirementRoles.
var roleAliases = map[string]string{
	"part":      "party",
	"parte":     "party",
	"partes":    "party",
	"parties":   "party",
	"witnesses": "witness",

	"signatario":  "sign",
	"signatário":  "sign",
	"signatarios": "sign",
	"signatários": "sign",
	"assinante":   "sign",
	"assinantes":  "sign",

	"testemunha":          "witness",
	"testemunhas":         "witness",
	"aprovador":           "approve",
	"aprovadora":          "approve",
	"aprovadores":         "approve",
	"comprador":           "buyer",
	"compradora":          "buyer",
	"vendedor":            "seller",
	"vendedora":           "seller",
	"contratante":         "contractor",
	"contratado":          "contractee",
	"contratada":          "contractee",
	"locador":             "lessor",
	"locadora":            "lessor",
	"locatario":           "lessee",
	"locataria":           "lessee",
	"locatário":           "lessee",
	"outorgante":          "grantor",
	"outorgado":           "grantee",
	"outorgada":           "grantee",
	"representante legal": "legal_representative",
	"representante_legal": "legal_representative",
	"fiador":              "guarantor",
	"fiadora":             "guarantor",
	"devedor":             "debtor",
	"devedora":            "debtor",
	"credor":              "creditor",
	"credora":             "creditor",
	"socio":               "partner",
	"sócio":               "partner",
	"socia":               "partner",
	"sócia":               "partner",
	"diretor":             "director",
	"diretora":            "director",
	"administrador":       "administrator",
	"administradora":      "administrator",
	"advogado":            "lawyer",
	"advogada":            "lawyer",
	"procurador":          "attorney",
	"procuradora":         "attorney",
	"inquilino":           "tenant",
	"inquilina":           "tenant",
	"proprietario":        "owner",
	"proprietaria":        "owner",
	"funcionario":         "employee",
	"funcionaria":         "employee",
	"doador":              "donor",
	"doadora":             "donor",
	"donatario":           "donee",
	"donataria":           "donee",
	"gerente":             "manager",
	"presidente":          "president",
	"secretario":          "secretary",
	"secretaria":          "secretary",
	"tesoureiro":          "treasurer",
	"tesoureira":          "treasurer",
	"corretor":            "broker",
	"corretora":           "broker",
}

// NormalizeRole lowercases + trims + applies roleAliases. The returned
// string is what we send to Clicksign in requirements[].role.
func NormalizeRole(raw string) string {
	r := strings.TrimSpace(strings.ToLower(raw))
	if canonical, ok := roleAliases[r]; ok {
		return canonical
	}
	return r
}

// IsValidRole reports whether the normalised role is accepted by Clicksign.
func IsValidRole(normalised string) bool {
	return allowedRequirementRoles[normalised]
}

// emailRegex is a pragmatic email validator (good enough for chat input).
// Same shape as in the MCP server validator.
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// IsValidEmail returns true when s looks like a syntactically valid email.
func IsValidEmail(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && len(s) <= 254 && emailRegex.MatchString(s)
}

// IsValidFullName returns true when the user supplied at least two
// space-separated words and no digits. Matches the MCP server rule.
func IsValidFullName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if len(strings.Fields(name)) < 2 {
		return false
	}
	for _, r := range name {
		if r >= '0' && r <= '9' {
			return false
		}
	}
	return true
}

// ErrSignerInvalid is returned by ValidateSigner for any rule violation.
// The wrapped message is user-facing (pt-BR) so flows can echo it as-is.
var ErrSignerInvalid = errors.New("signer invalid")

// SignerInput is the input shape for validation. It mirrors nlu.Signer
// because that's what the NLU emits, but lives in this package so other
// callers can build it without importing nlu.
type SignerInput struct {
	Name        string
	Email       string
	PhoneNumber string
	Role        string // raw text from the user; will be normalised
}

// ValidatedSigner is a signer that already passed validation. Used by
// envelope_builder.go to map into clicksign.BulkSigner.
type ValidatedSigner struct {
	Name        string
	Email       string
	PhoneNumber string
	Role        string // canonical, allowlisted Clicksign role
}

// ValidateSigners normalises and validates the slice. It returns the list
// of validated signers or a user-friendly aggregated error. We default
// missing role to "sign" so a tone-down "envia esse pra Mikael mike@x.com"
// still works without the user typing a role.
func ValidateSigners(in []SignerInput) ([]ValidatedSigner, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%w: informe pelo menos 1 signatário (nome completo + e-mail)", ErrSignerInvalid)
	}
	out := make([]ValidatedSigner, 0, len(in))
	var problems []string
	for i, s := range in {
		idx := i + 1
		name := strings.TrimSpace(s.Name)
		email := strings.TrimSpace(s.Email)
		phone := strings.TrimSpace(s.PhoneNumber)
		rawRole := strings.TrimSpace(s.Role)
		role := NormalizeRole(rawRole)
		if role == "" {
			role = "sign"
		}

		if !IsValidFullName(name) {
			problems = append(problems, fmt.Sprintf("signatário %d: nome precisa ter pelo menos nome e sobrenome (sem números)", idx))
			continue
		}
		if !IsValidEmail(email) {
			problems = append(problems, fmt.Sprintf("signatário %d (*%s*): e-mail %q parece inválido", idx, name, email))
			continue
		}
		if !IsValidRole(role) {
			problems = append(problems, fmt.Sprintf("signatário %d (*%s*): papel %q não é aceito; use por exemplo signatário, parte, testemunha, aprovador", idx, name, rawRole))
			continue
		}
		out = append(out, ValidatedSigner{
			Name:        name,
			Email:       email,
			PhoneNumber: phone,
			Role:        role,
		})
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%w:\n• %s", ErrSignerInvalid, strings.Join(problems, "\n• "))
	}
	return out, nil
}

// SignersFromNLU converts the NLU-emitted signer rows into SignerInput.
// Accepts the loosely-typed form ([]nlu.Signer) and the map[string]any
// form (when the NLU verdict has been round-tripped through JSON).
func SignersFromNLU(value any) []SignerInput {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case []nlu.Signer:
		out := make([]SignerInput, 0, len(v))
		for _, s := range v {
			out = append(out, SignerInput{
				Name:        s.Name,
				Email:       s.Email,
				PhoneNumber: s.PhoneNumber,
				Role:        s.Role,
			})
		}
		return out
	case []any:
		out := make([]SignerInput, 0, len(v))
		for _, raw := range v {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, SignerInput{
				Name:        getString(m, "name"),
				Email:       getString(m, "email"),
				PhoneNumber: getString(m, "phone_number"),
				Role:        getString(m, "role"),
			})
		}
		return out
	}
	return nil
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// toBulkSigners converts validated rows into the Clicksign bulk-creation
// signer shape. Every signer gets the canonical 2-requirement combo:
// agree+role (qualification) + provide_evidence with auth=email
// (authentication channel). This keeps the API call deterministic.
func toBulkSigners(in []ValidatedSigner) []clicksign.BulkSigner {
	out := make([]clicksign.BulkSigner, 0, len(in))
	for _, s := range in {
		out = append(out, clicksign.BulkSigner{
			Name:                    s.Name,
			Email:                   s.Email,
			PhoneNumber:             s.PhoneNumber,
			LocationRequiredEnabled: false,
			HasDocumentation:        false,
			Refusable:               true,
			CommunicateEvents: &clicksign.BulkCommunicateEvents{
				SignatureRequest:  "email",
				SignatureReminder: "email",
				DocumentSigned:    "email",
			},
			Requirements: []clicksign.BulkRequirement{
				{Action: "provide_evidence", Auth: "email"},
				{Action: "agree", Role: s.Role},
			},
		})
	}
	return out
}
