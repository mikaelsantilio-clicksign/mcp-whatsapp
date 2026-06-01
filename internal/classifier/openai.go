package classifier

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

//go:embed prompts/classifier.md
var classifierSystemPrompt string

// OpenAIClassifier calls the OpenAI Chat Completions API (with JSON mode)
// using a small/cheap model to gate incoming messages.
type OpenAIClassifier struct {
	logger  *slog.Logger
	client  openai.Client
	model   string
	timeout time.Duration
	cache   *cache
}

// OpenAIConfig groups the inputs needed to build an OpenAIClassifier.
type OpenAIConfig struct {
	APIKey         string
	Model          string        // e.g. "gpt-4o-mini"
	Timeout        time.Duration // per call
	CacheTTL       time.Duration // verdict cache TTL
}

func NewOpenAI(logger *slog.Logger, cfg OpenAIConfig) *OpenAIClassifier {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 60 * time.Second
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	return &OpenAIClassifier{
		logger:  logger,
		client:  openai.NewClient(option.WithAPIKey(cfg.APIKey)),
		model:   cfg.Model,
		timeout: cfg.Timeout,
		cache:   newCache(cfg.CacheTTL),
	}
}

// Classify implements Classifier.
func (c *OpenAIClassifier) Classify(ctx context.Context, message string, recent []HistoryTurn) (Verdict, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return Verdict{Intent: IntentOffTopic, Reason: "empty"}, nil
	}

	key := fingerprintKey(message, recent)
	if v, ok := c.cache.Get(key); ok {
		return v, nil
	}

	userContent := buildClassifierUserContent(message, recent)
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.client.Chat.Completions.New(callCtx, openai.ChatCompletionNewParams{
		Model:       shared.ChatModel(c.model),
		Temperature: openai.Float(0),
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
		},
		MaxCompletionTokens: openai.Int(80),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(classifierSystemPrompt),
			openai.UserMessage(userContent),
		},
	})
	if err != nil {
		return Verdict{}, fmt.Errorf("classifier: %w", err)
	}
	if len(resp.Choices) == 0 {
		return Verdict{}, errors.New("classifier: empty choices")
	}

	raw := strings.TrimSpace(resp.Choices[0].Message.Content)
	v, err := parseVerdict(raw)
	if err != nil {
		return Verdict{}, fmt.Errorf("classifier parse (%q): %w", raw, err)
	}
	c.cache.Put(key, v)
	return v, nil
}

func buildClassifierUserContent(message string, recent []HistoryTurn) string {
	var sb strings.Builder
	if len(recent) > 0 {
		sb.WriteString("CONTEXTO RECENTE (mais antigo primeiro):\n")
		for _, t := range recent {
			role := t.Role
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

type rawVerdict struct {
	Intent string `json:"intent"`
	Reason string `json:"reason"`
	// Legacy field tolerated for backwards-compat with older prompt variants.
	OnTopic *bool `json:"on_topic,omitempty"`
}

func parseVerdict(raw string) (Verdict, error) {
	raw = strings.TrimSpace(raw)
	// Be tolerant of accidental markdown fences.
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var rv rawVerdict
	if err := json.Unmarshal([]byte(raw), &rv); err != nil {
		return Verdict{}, err
	}

	switch Intent(strings.ToLower(strings.TrimSpace(rv.Intent))) {
	case IntentOnTopic:
		return Verdict{Intent: IntentOnTopic, Reason: rv.Reason}, nil
	case IntentMetaHelp:
		return Verdict{Intent: IntentMetaHelp, Reason: rv.Reason}, nil
	case IntentOffTopic:
		return Verdict{Intent: IntentOffTopic, Reason: rv.Reason}, nil
	}
	// Legacy fallback if the model regressed to the old schema.
	if rv.OnTopic != nil {
		if *rv.OnTopic {
			return Verdict{Intent: IntentOnTopic, Reason: rv.Reason}, nil
		}
		return Verdict{Intent: IntentOffTopic, Reason: rv.Reason}, nil
	}
	return Verdict{}, errors.New("missing or unknown intent field")
}
