package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"

	"github.com/clicksign/whatsapp-mcp/internal/config"
	"github.com/clicksign/whatsapp-mcp/internal/conv"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/mcpclient"
)

// Conversation implements api.Conversation by orchestrating an OpenAI chat
// completion loop that can call tools from a remote MCP server.
type Conversation struct {
	cfg    *config.Config
	logger *slog.Logger
	mgr    *mcpclient.Manager
	oai    openai.Client
}

func NewConversation(cfg *config.Config, logger *slog.Logger, mgr *mcpclient.Manager) *Conversation {
	cli := openai.NewClient(option.WithAPIKey(cfg.OpenAIAPIKey))
	return &Conversation{
		cfg:    cfg,
		logger: logger,
		mgr:    mgr,
		oai:    cli,
	}
}

// Run satisfies conv.Conversation. It opens a per-call MCP session, lists
// tools (cached at the manager level), and runs a tool-calling loop against
// OpenAI.
func (c *Conversation) Run(ctx context.Context, in conv.Input) (conv.Output, error) {
	phoneHash := logging.HashPhone(in.Phone)

	conn, err := c.mgr.Open(ctx, in.Phone)
	if err != nil {
		if errors.Is(err, mcpclient.ErrAuthExpired) {
			return conv.Output{}, conv.ErrSessionExpired
		}
		return conv.Output{}, fmt.Errorf("mcp open: %w", err)
	}
	defer conn.Close()

	mcpTools, err := c.mgr.ListTools(ctx, conn)
	if err != nil {
		if errors.Is(err, mcpclient.ErrAuthExpired) {
			return conv.Output{}, conv.ErrSessionExpired
		}
		return conv.Output{}, fmt.Errorf("list tools: %w", err)
	}
	openaiTools, err := mcpclient.ToOpenAITools(mcpTools)
	if err != nil {
		return conv.Output{}, fmt.Errorf("tools schema: %w", err)
	}

	tools := make([]openai.ChatCompletionToolParam, 0, len(openaiTools))
	for _, t := range openaiTools {
		tools = append(tools, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Function.Name,
				Description: openai.String(t.Function.Description),
				Parameters:  shared.FunctionParameters(t.Function.Parameters),
			},
		})
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(SystemPrompt()),
		openai.UserMessage(buildUserContent(in)),
	}

	traces := make([]conv.ToolCallTrace, 0)

	for iter := 0; iter < c.cfg.OpenAIMaxToolIterations; iter++ {
		req := openai.ChatCompletionNewParams{
			Model:    shared.ChatModel(c.cfg.OpenAIModel),
			Messages: messages,
		}
		if len(tools) > 0 {
			req.Tools = tools
		}

		callCtx, cancel := context.WithTimeout(ctx, c.cfg.OpenAITimeout())
		resp, err := c.oai.Chat.Completions.New(callCtx, req)
		cancel()
		if err != nil {
			return conv.Output{ToolCalls: traces}, fmt.Errorf("openai chat: %w", err)
		}
		if len(resp.Choices) == 0 {
			return conv.Output{ToolCalls: traces}, errors.New("openai: empty choices")
		}
		choice := resp.Choices[0]
		assistantMsg := choice.Message

		// No tool calls: this is the final reply.
		if len(assistantMsg.ToolCalls) == 0 {
			reply := strings.TrimSpace(assistantMsg.Content)
			if reply == "" {
				reply = LLMFailure()
			}
			return conv.Output{Reply: reply, ToolCalls: traces}, nil
		}

		// Persist the assistant turn (with its tool_calls) so the next request
		// can reference them via tool_call_id.
		messages = append(messages, assistantMsg.ToParam())

		// Execute each tool_call in order and append a tool message for each.
		for _, tc := range assistantMsg.ToolCalls {
			name := tc.Function.Name
			args := map[string]any{}
			if raw := tc.Function.Arguments; raw != "" {
				if err := json.Unmarshal([]byte(raw), &args); err != nil {
					c.logger.Warn("tool_args_decode_failed",
						slog.String("phone_hash", phoneHash),
						slog.String("tool", name),
						slog.String("err", err.Error()),
					)
				}
			}

			c.logger.Info("tool_call",
				slog.String("phone_hash", phoneHash),
				slog.String("tool", name),
				slog.Int("iter", iter),
			)

			result, callErr := c.mgr.CallTool(ctx, conn, name, args)
			trace := conv.ToolCallTrace{Name: name}
			var toolPayload string
			switch {
			case errors.Is(callErr, mcpclient.ErrAuthExpired):
				return conv.Output{ToolCalls: traces}, conv.ErrSessionExpired
			case callErr != nil:
				trace.OK = false
				trace.Err = callErr.Error()
				toolPayload = jsonError("tool call failed", callErr.Error())
				c.logger.Warn("tool_call_failed",
					slog.String("phone_hash", phoneHash),
					slog.String("tool", name),
					slog.String("err", callErr.Error()),
				)
			default:
				trace.OK = !result.IsError
				if result.IsError {
					trace.Err = mcpclient.ExtractText(result)
				}
				toolPayload = mcpclient.ExtractText(result)
				if toolPayload == "" {
					toolPayload = "{}"
				}
			}
			traces = append(traces, trace)
			messages = append(messages, openai.ToolMessage(toolPayload, tc.ID))
		}
	}

	return conv.Output{
		Reply:     MaxIterations(),
		ToolCalls: traces,
	}, nil
}

func buildUserContent(in conv.Input) string {
	if len(in.Attachments) == 0 {
		return in.Message
	}
	var sb strings.Builder
	sb.WriteString(in.Message)
	sb.WriteString("\n\n[Anexos enviados pelo usuário no WhatsApp]\n")
	for i, a := range in.Attachments {
		sb.WriteString(fmt.Sprintf("%d. %s", i+1, a.Filename))
		if a.MimeType != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", a.MimeType))
		}
		if a.URL != "" {
			sb.WriteString(fmt.Sprintf(" - URL: %s", a.URL))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func jsonError(kind, detail string) string {
	b, _ := json.Marshal(map[string]any{
		"error":  kind,
		"detail": detail,
	})
	return string(b)
}
