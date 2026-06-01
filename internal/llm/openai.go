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

	"github.com/clicksign/whatsapp-mcp/internal/classifier"
	"github.com/clicksign/whatsapp-mcp/internal/config"
	"github.com/clicksign/whatsapp-mcp/internal/conv"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/mcpclient"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// Conversation implements conv.Conversation by orchestrating an OpenAI chat
// completion loop that can call tools from a remote MCP server. It also
// persists the per-user message history in the session store so subsequent
// requests have context of prior turns, and gates incoming messages through
// a cheap intent classifier before invoking the main loop.
type Conversation struct {
	cfg        *config.Config
	logger     *slog.Logger
	store      session.Store
	mgr        *mcpclient.Manager
	oai        openai.Client
	classifier classifier.Classifier
	metaHelp   *MetaHelpResponder // optional; nil → fall back to static Capabilities()
}

func NewConversation(
	cfg *config.Config,
	logger *slog.Logger,
	store session.Store,
	mgr *mcpclient.Manager,
	cls classifier.Classifier,
	meta *MetaHelpResponder,
) *Conversation {
	cli := openai.NewClient(option.WithAPIKey(cfg.OpenAIAPIKey))
	if cls == nil {
		cls = classifier.AlwaysOnTopic{}
	}
	return &Conversation{
		cfg:        cfg,
		logger:     logger,
		store:      store,
		mgr:        mgr,
		oai:        cli,
		classifier: cls,
		metaHelp:   meta,
	}
}

// Run satisfies conv.Conversation. It opens a per-call MCP session, lists
// tools (cached at the manager level), and runs a tool-calling loop against
// OpenAI.
func (c *Conversation) Run(ctx context.Context, in conv.Input) (conv.Output, error) {
	phoneHash := logging.HashPhone(in.Phone)

	// Load prior history (if any). We need it both for classifier context
	// (to disambiguate short replies like "sim"/"use a conta 3") and to
	// rehydrate the OpenAI message array later.
	var priorHistory []session.ChatTurn
	if sess, err := c.store.GetSession(ctx, in.Phone); err == nil {
		priorHistory = sess.History
	}

	// Intent gate: cheap classifier in front of the main loop. Three
	// outcomes:
	//   - on_topic  → proceed to MCP + main LLM (default path)
	//   - meta_help → static Capabilities() reply, no MCP, no LLM
	//   - off_topic → static OffTopic() reply, no MCP, no LLM
	// Meta_help and off_topic exchanges are NOT persisted to history,
	// so the next legitimate message starts from a clean slate.
	recent := classifierContext(priorHistory, c.cfg.ClassifierContextTurns)
	verdict, vErr := c.classifier.Classify(ctx, in.Message, recent)
	if vErr != nil {
		// Fail open: a broken classifier should NOT block legitimate users.
		c.logger.Warn("classifier_failed_fail_open",
			slog.String("phone_hash", phoneHash),
			slog.String("err", vErr.Error()),
		)
	} else {
		switch verdict.Intent {
		case classifier.IntentMetaHelp:
			c.logger.Info("message_meta_help",
				slog.String("phone_hash", phoneHash),
				slog.String("reason", verdict.Reason),
			)
			if c.metaHelp != nil {
				if reply, err := c.metaHelp.Respond(ctx, in.Phone, in.Message, recent); err == nil {
					return conv.Output{Reply: reply}, nil
				} else {
					// LLM-backed reply failed — log and fall through to the
					// static template so the user always gets something useful.
					c.logger.Warn("meta_help_failed_static_fallback",
						slog.String("phone_hash", phoneHash),
						slog.String("err", err.Error()),
					)
				}
			}
			return conv.Output{Reply: Capabilities()}, nil
		case classifier.IntentOffTopic:
			c.logger.Info("message_off_topic",
				slog.String("phone_hash", phoneHash),
				slog.String("reason", verdict.Reason),
			)
			return conv.Output{Reply: OffTopic()}, nil
		default:
			c.logger.Debug("message_on_topic",
				slog.String("phone_hash", phoneHash),
				slog.String("reason", verdict.Reason),
			)
		}
	}

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

	// priorHistory was loaded above for the classifier; reuse it now to
	// rehydrate the OpenAI message array.
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(SystemPrompt()),
	}
	for _, t := range priorHistory {
		messages = append(messages, toOpenAIMessage(t))
	}

	userTurn := session.ChatTurn{Role: "user", Content: buildUserContent(in)}
	messages = append(messages, openai.UserMessage(userTurn.Content))

	// newTurns accumulates the turns produced in this Run that should be
	// appended to the persisted history when we finish successfully.
	newTurns := []session.ChatTurn{userTurn}
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
			newTurns = append(newTurns, session.ChatTurn{Role: "assistant", Content: reply})
			c.persistHistory(ctx, in.Phone, priorHistory, newTurns)
			return conv.Output{Reply: reply, ToolCalls: traces}, nil
		}

		// Persist the assistant turn (with its tool_calls) so the next request
		// can reference them via tool_call_id.
		messages = append(messages, assistantMsg.ToParam())
		newTurns = append(newTurns, session.ChatTurn{
			Role:      "assistant",
			Content:   assistantMsg.Content,
			ToolCalls: extractToolCalls(assistantMsg.ToolCalls),
		})

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
			newTurns = append(newTurns, session.ChatTurn{
				Role:       "tool",
				Content:    toolPayload,
				ToolCallID: tc.ID,
			})
		}
	}

	// Loop exhausted — return a generic reply but do NOT persist the
	// half-finished history, since the next request would start from an
	// inconsistent state (assistant tool_call with no matching final
	// assistant reply).
	return conv.Output{
		Reply:     MaxIterations(),
		ToolCalls: traces,
	}, nil
}

// persistHistory appends newly produced turns to the prior history, truncates
// to the configured maximum and writes the session back. Failures are logged
// but not propagated — saving history is a best-effort enhancement, not a
// correctness requirement for the current response.
func (c *Conversation) persistHistory(ctx context.Context, phone string, prior, newTurns []session.ChatTurn) {
	merged := make([]session.ChatTurn, 0, len(prior)+len(newTurns))
	merged = append(merged, prior...)
	merged = append(merged, newTurns...)
	trimmed := truncateHistory(merged, maxHistoryTurns)

	sess, err := c.store.GetSession(ctx, phone)
	if err != nil {
		c.logger.Warn("history_persist_session_missing",
			slog.String("phone_hash", logging.HashPhone(phone)),
			slog.String("err", err.Error()),
		)
		return
	}
	sess.History = trimmed
	if err := c.store.PutSession(ctx, sess); err != nil {
		c.logger.Warn("history_persist_failed",
			slog.String("phone_hash", logging.HashPhone(phone)),
			slog.String("err", err.Error()),
		)
		return
	}
	c.logger.Debug("history_persisted",
		slog.String("phone_hash", logging.HashPhone(phone)),
		slog.Int("turns", len(trimmed)),
	)
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
