package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"

	"github.com/clicksign/whatsapp-mcp/internal/classifier"
	"github.com/clicksign/whatsapp-mcp/internal/config"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/tools"
)

// MetaHelpResponder generates the natural-language reply for messages
// classified as "meta_help" (greetings, thanks, capability questions).
//
// It calls a cheap chat model (no tools) with a dedicated system prompt that
// gets dynamically grounded with the current set of tools the bot exposes.
// This keeps the reply factually aligned with what the bot can actually do,
// and prevents the main LLM (which has tool-calling enabled) from being
// invoked for a "oi".
//
// Failure semantics: any error (timeout, OpenAI down) is logged and the
// caller falls back to the static Capabilities() reply. The bot stays
// useful even when this auxiliary path is broken.
type MetaHelpResponder struct {
	logger  *slog.Logger
	client  openai.Client
	model   string
	timeout time.Duration
	tools   tools.Runner
}

func NewMetaHelpResponder(cfg *config.Config, logger *slog.Logger, runner tools.Runner) *MetaHelpResponder {
	timeout := cfg.MetaHelpTimeout()
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	model := cfg.MetaHelpModel
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &MetaHelpResponder{
		logger:  logger,
		client:  openai.NewClient(option.WithAPIKey(cfg.OpenAIAPIKey)),
		model:   model,
		timeout: timeout,
		tools:   runner,
	}
}

// Respond returns a freshly-generated reply for the meta_help intent.
// `recent` is the same conversation context used by the classifier (last few
// user/assistant turns) — it lets the model phrase follow-ups naturally
// (e.g. "obrigado pela ajuda com o envelope").
func (r *MetaHelpResponder) Respond(
	ctx context.Context,
	phone, message string,
	recent []classifier.HistoryTurn,
) (string, error) {
	phoneHash := logging.HashPhone(phone)

	// The tools catalogue is fully static and cheap to enumerate, so we
	// always pull a fresh copy. If listing fails we fall back to a generic
	// placeholder in the prompt — the LLM has a hard-coded default list in
	// the system prompt as a safety net.
	toolList, listErr := r.tools.List(ctx, phone)
	if listErr != nil {
		r.logger.Debug("meta_help_tools_unavailable",
			slog.String("phone_hash", phoneHash),
			slog.String("err", listErr.Error()),
		)
		toolList = nil
	}
	toolsSummary := summarizeToolsForMetaPrompt(toolList)
	systemPrompt := strings.ReplaceAll(MetaHelpPromptTemplate(), "{{TOOLS}}", toolsSummary)

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
	}
	for _, t := range recent {
		switch t.Role {
		case "user":
			messages = append(messages, openai.UserMessage(t.Content))
		case "assistant":
			messages = append(messages, openai.AssistantMessage(t.Content))
		}
	}
	messages = append(messages, openai.UserMessage(message))

	callCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	resp, err := r.client.Chat.Completions.New(callCtx, openai.ChatCompletionNewParams{
		Model:               shared.ChatModel(r.model),
		Temperature:         openai.Float(0.4),
		MaxCompletionTokens: openai.Int(220),
		Messages:            messages,
	})
	if err != nil {
		return "", fmt.Errorf("meta_help: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("meta_help: empty choices")
	}
	out := strings.TrimSpace(resp.Choices[0].Message.Content)
	if out == "" {
		return "", errors.New("meta_help: empty content")
	}

	r.logger.Debug("meta_help_generated",
		slog.String("phone_hash", phoneHash),
		slog.Int("tools_in_prompt", len(toolList)),
		slog.Int("reply_len", len(out)),
	)
	return out, nil
}

// summarizeToolsForMetaPrompt renders the tool list as "- name: description"
// lines for injection into the meta_help system prompt. The LLM is instructed
// (in the prompt) to translate these names into natural Portuguese actions
// and never to expose the raw `snake_case` names to the user.
//
// Empty list yields a placeholder that triggers the fallback default list
// inside the prompt itself.
func summarizeToolsForMetaPrompt(toolList []tools.Tool) string {
	if len(toolList) == 0 {
		return "(lista indisponível no momento — use as capacidades padrão descritas nas diretrizes)"
	}
	var sb strings.Builder
	for _, t := range toolList {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(t.Description)
		if len(desc) > 240 {
			desc = desc[:240] + "…"
		}
		sb.WriteString("- ")
		sb.WriteString(name)
		if desc != "" {
			sb.WriteString(": ")
			sb.WriteString(desc)
		}
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		return "(lista indisponível no momento — use as capacidades padrão descritas nas diretrizes)"
	}
	return sb.String()
}
