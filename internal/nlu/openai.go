package nlu

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

//go:embed prompts/nlu.md
var nluSystemPrompt string

// OpenAINLU is an Extractor backed by OpenAI Chat Completions in JSON mode.
type OpenAINLU struct {
	logger  *slog.Logger
	client  openai.Client
	model   string
	timeout time.Duration
}

// OpenAIConfig groups the inputs needed to build an OpenAINLU.
type OpenAIConfig struct {
	APIKey  string
	Model   string        // e.g. "gpt-4o-mini"
	Timeout time.Duration // per call
}

// NewOpenAI constructs an OpenAINLU with sensible defaults.
func NewOpenAI(logger *slog.Logger, cfg OpenAIConfig) *OpenAINLU {
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &OpenAINLU{
		logger:  logger,
		client:  openai.NewClient(option.WithAPIKey(cfg.APIKey)),
		model:   cfg.Model,
		timeout: cfg.Timeout,
	}
}

// Extract implements Extractor by calling the OpenAI API and parsing the
// returned JSON.
func (n *OpenAINLU) Extract(ctx context.Context, message string, recent []HistoryTurn) (Verdict, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return Verdict{Intent: IntentUnknown, Confidence: ConfLow}, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()

	resp, err := n.client.Chat.Completions.New(callCtx, openai.ChatCompletionNewParams{
		Model:       shared.ChatModel(n.model),
		Temperature: openai.Float(0),
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
		},
		MaxCompletionTokens: openai.Int(220),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(nluSystemPrompt),
			openai.UserMessage(buildUserContent(message, recent)),
		},
	})
	if err != nil {
		return Verdict{}, fmt.Errorf("nlu: openai call: %w", err)
	}
	if len(resp.Choices) == 0 {
		return Verdict{}, errors.New("nlu: empty choices")
	}
	raw := strings.TrimSpace(resp.Choices[0].Message.Content)
	v, err := ParseVerdict(raw)
	if err != nil {
		return Verdict{}, fmt.Errorf("nlu parse (%q): %w", truncate(raw, 200), err)
	}
	return v, nil
}

func buildUserContent(message string, recent []HistoryTurn) string {
	var sb strings.Builder
	if len(recent) > 0 {
		sb.WriteString("CONTEXTO RECENTE (mais antigo primeiro):\n")
		for _, t := range recent {
			role := strings.TrimSpace(t.Role)
			if role == "" {
				role = "user"
			}
			content := strings.TrimSpace(t.Content)
			if content == "" {
				continue
			}
			if len(content) > 400 {
				content = content[:400] + "…"
			}
			sb.WriteString(role)
			sb.WriteString(": ")
			sb.WriteString(content)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("MENSAGEM ATUAL:\n")
	sb.WriteString(message)
	return sb.String()
}

// rawVerdict is the wire-format the model emits. We parse permissively
// (some fields nullable, accept missing keys, tolerate markdown fences).
type rawVerdict struct {
	Intent     string `json:"intent"`
	Confidence string `json:"confidence"`
	Entities   *struct {
		AccountKey   *string         `json:"account_key,omitempty"`
		AccountIndex *json.Number    `json:"account_index,omitempty"`
		EnvelopeID   *string         `json:"envelope_id,omitempty"`
		EnvelopeName *string         `json:"envelope_name,omitempty"`
		TemplateID   *string         `json:"template_id,omitempty"`
		TemplateName *string         `json:"template_name,omitempty"`
		PDFURL       *string         `json:"pdf_url,omitempty"`
		FilterStatus *string         `json:"filter_status,omitempty"`
		Signers      json.RawMessage `json:"signers,omitempty"`
	} `json:"entities,omitempty"`
}

// ParseVerdict converts the raw JSON returned by the LLM into a typed
// Verdict. It is exported for unit testing without an OpenAI key.
func ParseVerdict(raw string) (Verdict, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Verdict{}, errors.New("empty body")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var rv rawVerdict
	if err := dec.Decode(&rv); err != nil {
		return Verdict{}, err
	}

	intent := normalizeIntent(rv.Intent)
	conf := normalizeConfidence(rv.Confidence)

	v := Verdict{Intent: intent, Confidence: conf}
	if rv.Entities != nil {
		v.Entities = Entities{
			AccountKey:   trimStringPtr(rv.Entities.AccountKey),
			AccountIndex: parseIntPtr(rv.Entities.AccountIndex),
			EnvelopeID:   trimStringPtr(rv.Entities.EnvelopeID),
			EnvelopeName: trimStringPtr(rv.Entities.EnvelopeName),
			TemplateID:   trimStringPtr(rv.Entities.TemplateID),
			TemplateName: trimStringPtr(rv.Entities.TemplateName),
			PDFURL:       trimStringPtr(rv.Entities.PDFURL),
			FilterStatus: trimStringPtr(rv.Entities.FilterStatus),
		}
		if len(rv.Entities.Signers) > 0 && string(rv.Entities.Signers) != "null" {
			var signers []Signer
			if err := json.Unmarshal(rv.Entities.Signers, &signers); err == nil {
				v.Entities.Signers = signers
			}
		}
	}
	return v, nil
}

func normalizeIntent(s string) Intent {
	switch Intent(strings.ToLower(strings.TrimSpace(s))) {
	case IntentListTemplates,
		IntentListEnvelopes,
		IntentEnvelopeStatus,
		IntentCreateEnvelopeTmpl,
		IntentCreateEnvelopePDF,
		IntentAddSigner,
		IntentSelectAccount,
		IntentCancelEnvelope:
		return Intent(strings.ToLower(strings.TrimSpace(s)))
	default:
		return IntentUnknown
	}
}

func normalizeConfidence(s string) Confidence {
	switch Confidence(strings.ToLower(strings.TrimSpace(s))) {
	case ConfHigh:
		return ConfHigh
	case ConfMedium:
		return ConfMedium
	case ConfLow:
		return ConfLow
	default:
		return ConfMedium // safe default
	}
}

func trimStringPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func parseIntPtr(n *json.Number) *int {
	if n == nil {
		return nil
	}
	i, err := n.Int64()
	if err != nil {
		return nil
	}
	v := int(i)
	return &v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
