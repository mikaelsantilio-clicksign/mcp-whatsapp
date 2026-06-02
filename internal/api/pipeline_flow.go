package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/classifier"
	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/config"
	"github.com/clicksign/whatsapp-mcp/internal/conv"
	"github.com/clicksign/whatsapp-mcp/internal/flow"
	"github.com/clicksign/whatsapp-mcp/internal/llm"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/nlu"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// FlowPipeline implements the Option B request pipeline:
//
//	classifier (intent gate) → NLU (intent + entities) → router (flows)
//
// Each Flow returns a typed Result that this pipeline turns into a
// MessageResponse (text, list message or quick-reply buttons). Multi-turn
// flows are kept in session.Session.ActiveFlow and resume on the next
// inbound turn — typically a click on an interactive message which arrives
// as MessageRequest.InteractiveReply.
type FlowPipeline struct {
	cfg        *config.Config
	logger     *slog.Logger
	store      session.Store
	classifier classifier.Classifier
	nluExt     nlu.Extractor
	router     *flow.Router
}

// NewFlowPipeline wires the Option B pipeline. Any of classifier or NLU
// may be nil — the pipeline falls back to "always on-topic" / "intent
// unknown" so the rest still works in dev environments without an OpenAI
// key.
func NewFlowPipeline(
	cfg *config.Config,
	logger *slog.Logger,
	store session.Store,
	cls classifier.Classifier,
	nluExt nlu.Extractor,
	router *flow.Router,
) *FlowPipeline {
	if cls == nil {
		cls = classifier.AlwaysOnTopic{}
	}
	if nluExt == nil {
		nluExt = nlu.Static{V: nlu.Verdict{Intent: nlu.IntentUnknown, Confidence: nlu.ConfLow}}
	}
	return &FlowPipeline{
		cfg:        cfg,
		logger:     logger,
		store:      store,
		classifier: cls,
		nluExt:     nluExt,
		router:     router,
	}
}

// Run handles one inbound /api/messages turn. It is the moral equivalent
// of conv.Conversation.Run in the legacy pipeline.
//
// The returned MessageResponse is "almost ready to send": the caller only
// wires Status="ok" and any AuthorizeURL (for needs_auth, which is a
// different code path).
func (p *FlowPipeline) Run(ctx context.Context, req MessageRequest, sess *session.Session) (MessageResponse, error) {
	phoneHash := logging.HashPhone(req.PhoneNumber)

	// Resolve which input dimension drove this turn (a click vs. a typed
	// message). For clicks we bypass classifier+NLU entirely — the active
	// flow is the only thing that can interpret the id meaningfully.
	if isInteractive(req.InteractiveReply) {
		return p.runWithInteractive(ctx, req, sess, phoneHash)
	}

	// Free-text path: classify to gate the LLM cost, then NLU.
	recent := historyForClassifier(sess.History, p.cfg.ClassifierContextTurns)
	verdict, err := p.classifier.Classify(ctx, req.Message, recent)
	if err != nil {
		// Fail-open: any classifier error degrades to "on_topic". We log
		// so the operator notices the gate is misbehaving but the user
		// keeps a useful bot.
		p.logger.Warn("classifier_failed_open",
			slog.String("phone_hash", phoneHash),
			slog.String("err", err.Error()),
		)
		verdict = classifier.Verdict{Intent: classifier.IntentOnTopic}
	}

	switch verdict.Intent {
	case classifier.IntentOffTopic:
		return MessageResponse{Status: "ok", Reply: llm.OffTopic()}, nil
	case classifier.IntentMetaHelp:
		// Static reply for now. Phase 5 may swap this for a dynamic
		// MetaHelpResponder if we keep one (it requires reading the
		// clicksign API tool catalog rather than the MCP one).
		return MessageResponse{Status: "ok", Reply: llm.Capabilities()}, nil
	}

	return p.runWithText(ctx, req, sess, phoneHash)
}

func (p *FlowPipeline) runWithInteractive(
	ctx context.Context,
	req MessageRequest,
	sess *session.Session,
	phoneHash string,
) (MessageResponse, error) {
	if sess.ActiveFlow == nil {
		// No state to resume → treat the click as a stray. Keep the user
		// moving by asking what they need.
		p.logger.Info("interactive_without_active_flow",
			slog.String("phone_hash", phoneHash),
		)
		return MessageResponse{
			Status: "ok",
			Reply:  "Não tenho uma pergunta em aberto agora. Em que posso ajudar?",
		}, nil
	}
	in := flow.Input{
		Phone:       req.PhoneNumber,
		Session:     sess,
		Intent:      sess.ActiveFlow.FlowID, // resumed flow drives the dispatch
		State:       sess.ActiveFlow,
		Interact:    req.InteractiveReply,
		Attachments: toFlowAttachments(req.Attachments),
	}
	return p.runRouter(ctx, in, sess, phoneHash)
}

func (p *FlowPipeline) runWithText(
	ctx context.Context,
	req MessageRequest,
	sess *session.Session,
	phoneHash string,
) (MessageResponse, error) {
	recent := historyForNLU(sess.History, 4)
	verdict, err := p.nluExt.Extract(ctx, req.Message, recent)
	if err != nil {
		p.logger.Warn("nlu_failed",
			slog.String("phone_hash", phoneHash),
			slog.String("err", err.Error()),
		)
		return MessageResponse{
			Status: "ok",
			Reply:  "Desculpe, tive um problema interno entendendo sua mensagem. Pode reformular?",
		}, nil
	}

	in := flow.Input{
		Phone:       req.PhoneNumber,
		Session:     sess,
		Intent:      string(verdict.Intent),
		Entities:    verdict.Entities.AsMap(),
		State:       sess.ActiveFlow, // even with free text, an open flow may want priority
		Attachments: toFlowAttachments(req.Attachments),
	}
	return p.runRouter(ctx, in, sess, phoneHash)
}

func (p *FlowPipeline) runRouter(
	ctx context.Context,
	in flow.Input,
	sess *session.Session,
	phoneHash string,
) (MessageResponse, error) {
	// Scope the session in ctx so clicksign.Client sees in-memory
	// mutations (typically PreferredAccount set by the flow) without
	// having to round-trip through the store first.
	ctx = clicksign.WithSession(ctx, sess)

	res, err := p.router.Handle(ctx, in)
	switch {
	case err != nil && errors.Is(err, conv.ErrSessionExpired):
		return MessageResponse{}, conv.ErrSessionExpired
	case err != nil && errors.Is(err, flow.ErrUnknownIntent):
		// Friendly fallback already in res.Reply.
		p.logger.Info("flow_unknown_intent",
			slog.String("phone_hash", phoneHash),
			slog.String("intent", in.Intent),
		)
	case err != nil:
		p.logger.Error("flow_failed",
			slog.String("phone_hash", phoneHash),
			slog.String("intent", in.Intent),
			slog.String("err", err.Error()),
		)
	}

	// Persist session changes (PreferredAccount, ActiveFlow, etc.) before
	// emitting the response. The Result.NextState decides what stays open.
	sess.ActiveFlow = res.NextState
	sess.UpdatedAt = time.Now().UTC()
	if perr := p.store.PutSession(ctx, sess); perr != nil {
		p.logger.Warn("session_persist_failed",
			slog.String("phone_hash", phoneHash),
			slog.String("err", perr.Error()),
		)
	}

	return MessageResponse{
		Status:      "ok",
		Reply:       res.Reply,
		Interactive: res.Interactive,
		FlowState:   flow.DigestFromState(res.NextState),
		Trace:       res.Trace,
	}, nil
}

// isInteractive reports whether the inbound payload carries a non-empty
// interactive_reply (list or button id).
func isInteractive(ir *flow.InteractiveReply) bool {
	if ir == nil {
		return false
	}
	return ir.ListItemID != "" || ir.ButtonID != ""
}

// toFlowAttachments converts the wire-format Attachment to flow.Attachment.
// The two types are intentionally separate so the api package can evolve
// the wire format without forcing a change to the flow contract.
func toFlowAttachments(in []Attachment) []flow.Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]flow.Attachment, 0, len(in))
	for _, a := range in {
		out = append(out, flow.Attachment{
			URL:      a.URL,
			MimeType: a.MimeType,
			Filename: a.Filename,
		})
	}
	return out
}

// historyForClassifier projects session.ChatTurn into the classifier's
// dependency-free type. It returns the last `turns` entries (oldest first).
func historyForClassifier(hist []session.ChatTurn, turns int) []classifier.HistoryTurn {
	if turns <= 0 || len(hist) == 0 {
		return nil
	}
	start := len(hist) - turns
	if start < 0 {
		start = 0
	}
	out := make([]classifier.HistoryTurn, 0, len(hist)-start)
	for _, t := range hist[start:] {
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		out = append(out, classifier.HistoryTurn{Role: t.Role, Content: t.Content})
	}
	return out
}

// historyForNLU mirrors historyForClassifier but emits nlu.HistoryTurn.
// Kept separate because the two packages stay decoupled.
func historyForNLU(hist []session.ChatTurn, turns int) []nlu.HistoryTurn {
	if turns <= 0 || len(hist) == 0 {
		return nil
	}
	start := len(hist) - turns
	if start < 0 {
		start = 0
	}
	out := make([]nlu.HistoryTurn, 0, len(hist)-start)
	for _, t := range hist[start:] {
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		out = append(out, nlu.HistoryTurn{Role: t.Role, Content: t.Content})
	}
	return out
}
