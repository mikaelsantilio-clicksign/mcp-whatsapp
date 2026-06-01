package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/config"
	"github.com/clicksign/whatsapp-mcp/internal/conv"
	"github.com/clicksign/whatsapp-mcp/internal/flow"
	"github.com/clicksign/whatsapp-mcp/internal/llm"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/oauth"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

type MessagesHandler struct {
	cfg          *config.Config
	logger       *slog.Logger
	store        session.Store
	oauth        *oauth.Client
	signer       *oauth.StateSigner
	conversation conv.Conversation
	// flowPipeline is set when cfg.PipelineFlow() returns true. When nil,
	// the handler falls back to the legacy conversation pipeline.
	flowPipeline *FlowPipeline
	idempotency  *idempotencyCache
}

func NewMessagesHandler(
	cfg *config.Config,
	logger *slog.Logger,
	store session.Store,
	oauthClient *oauth.Client,
	signer *oauth.StateSigner,
	conversation conv.Conversation,
	flowPipeline *FlowPipeline,
) *MessagesHandler {
	return &MessagesHandler{
		cfg:          cfg,
		logger:       logger,
		store:        store,
		oauth:        oauthClient,
		signer:       signer,
		conversation: conversation,
		flowPipeline: flowPipeline,
		idempotency:  newIdempotencyCache(60 * time.Second),
	}
}

type Attachment struct {
	URL      string `json:"url"`
	MimeType string `json:"mime_type,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// MessageRequest is the inbound payload from n8n. Either Message or
// InteractiveReply must be present (a clique-only turn omits Message).
// See docs/N8N_INTEGRATION_CONTRACT.md for full details.
type MessageRequest struct {
	PhoneNumber      string                 `json:"phone_number"`
	Message          string                 `json:"message,omitempty"`
	InteractiveReply *flow.InteractiveReply `json:"interactive_reply,omitempty"`
	Attachments      []Attachment           `json:"attachments,omitempty"`
	ConversationID   string                 `json:"conversation_id,omitempty"`
	MessageID        string                 `json:"message_id,omitempty"`
}

func (h *MessagesHandler) Post(w http.ResponseWriter, r *http.Request) {
	var req MessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, MessageResponse{
			Status: "error",
			Reply:  llm.InvalidInput(),
			Error:  &errorBody{Code: "INVALID_INPUT", Details: err.Error()},
		})
		return
	}
	req.PhoneNumber = strings.TrimSpace(req.PhoneNumber)
	req.Message = strings.TrimSpace(req.Message)
	hasInteractive := req.InteractiveReply != nil &&
		(strings.TrimSpace(req.InteractiveReply.ListItemID) != "" ||
			strings.TrimSpace(req.InteractiveReply.ButtonID) != "")

	if req.PhoneNumber == "" {
		writeJSON(w, http.StatusBadRequest, MessageResponse{
			Status: "error",
			Reply:  llm.InvalidInput(),
			Error:  &errorBody{Code: "INVALID_INPUT", Details: "phone_number is required"},
		})
		return
	}
	if req.Message == "" && !hasInteractive {
		writeJSON(w, http.StatusBadRequest, MessageResponse{
			Status: "error",
			Reply:  llm.InvalidInput(),
			Error:  &errorBody{Code: "INVALID_INPUT", Details: "message or interactive_reply is required"},
		})
		return
	}

	ctx := r.Context()
	phoneHash := logging.HashPhone(req.PhoneNumber)

	if req.MessageID != "" && h.idempotency.SeenRecently(req.MessageID) {
		h.logger.Info("message_duplicate_skipped",
			slog.String("phone_hash", phoneHash),
			slog.String("message_id", req.MessageID),
		)
		writeJSON(w, http.StatusOK, MessageResponse{
			Status: "ok",
			Reply:  "",
		})
		return
	}

	h.logger.Info("message_received",
		slog.String("phone_hash", phoneHash),
		slog.Int("attachments", len(req.Attachments)),
		slog.String("request_id", RequestIDFrom(ctx)),
	)

	sess, err := h.store.GetSession(ctx, req.PhoneNumber)
	switch {
	case errors.Is(err, session.ErrNotFound):
		h.respondNeedsAuth(ctx, w, req.PhoneNumber, llm.AuthRequired)
		return
	case err != nil:
		h.logger.Error("session_lookup_failed", slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, MessageResponse{
			Status: "error",
			Reply:  llm.InternalError(),
			Error:  &errorBody{Code: "INTERNAL_ERROR"},
		})
		return
	}

	// Pipeline switch (Option B). When PIPELINE=flow we delegate to the
	// NLU + Guided Flow pipeline; otherwise we keep the legacy MCP +
	// LLM tool-calling path live so the migration is reversible.
	if h.cfg.PipelineFlow() && h.flowPipeline != nil {
		h.runFlowPipeline(ctx, w, req, sess, phoneHash)
		return
	}

	// Legacy path.
	if h.conversation == nil {
		writeJSON(w, http.StatusOK, MessageResponse{
			Status: "ok",
			Reply:  fmt.Sprintf("(stub) Sessão encontrada para você. Mensagem: %q", req.Message),
		})
		_ = sess
		return
	}

	out, err := h.conversation.Run(ctx, conv.Input{
		Phone:       req.PhoneNumber,
		Message:     req.Message,
		Attachments: convertAttachments(req.Attachments),
	})
	if err != nil {
		if errors.Is(err, conv.ErrSessionExpired) {
			h.respondNeedsAuth(ctx, w, req.PhoneNumber, llm.SessionExpired)
			return
		}
		h.logger.Error("conversation_failed",
			slog.String("err", err.Error()),
			slog.String("phone_hash", phoneHash),
		)
		writeJSON(w, http.StatusOK, MessageResponse{
			Status: "error",
			Reply:  llm.UpstreamTimeout(),
			Error:  &errorBody{Code: "UPSTREAM_TIMEOUT", Details: err.Error()},
		})
		return
	}
	traces := make([]ToolCallTrace, 0, len(out.ToolCalls))
	for _, t := range out.ToolCalls {
		traces = append(traces, ToolCallTrace{Name: t.Name, OK: t.OK, Err: t.Err})
	}
	writeJSON(w, http.StatusOK, MessageResponse{
		Status:    "ok",
		Reply:     out.Reply,
		ToolCalls: traces,
	})
}

// runFlowPipeline invokes the Option B pipeline (NLU + Guided Flow) and
// writes the resulting MessageResponse.
func (h *MessagesHandler) runFlowPipeline(
	ctx context.Context,
	w http.ResponseWriter,
	req MessageRequest,
	sess *session.Session,
	phoneHash string,
) {
	resp, err := h.flowPipeline.Run(ctx, req, sess)
	if err != nil {
		if errors.Is(err, conv.ErrSessionExpired) {
			h.respondNeedsAuth(ctx, w, req.PhoneNumber, llm.SessionExpired)
			return
		}
		h.logger.Error("flow_pipeline_failed",
			slog.String("err", err.Error()),
			slog.String("phone_hash", phoneHash),
		)
		writeJSON(w, http.StatusOK, MessageResponse{
			Status: "error",
			Reply:  llm.InternalError(),
			Error:  &errorBody{Code: "INTERNAL_ERROR", Details: err.Error()},
		})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func convertAttachments(in []Attachment) []conv.Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]conv.Attachment, 0, len(in))
	for _, a := range in {
		out = append(out, conv.Attachment{URL: a.URL, MimeType: a.MimeType, Filename: a.Filename})
	}
	return out
}

func (h *MessagesHandler) respondNeedsAuth(ctx context.Context, w http.ResponseWriter, phone string, replyBuilder func(string) string) {
	pkce, err := oauth.NewPKCE()
	if err != nil {
		h.internalError(w, err)
		return
	}
	state, _, err := h.signer.New(phone, h.cfg.PKCETTL())
	if err != nil {
		h.internalError(w, err)
		return
	}
	linkToken, err := oauth.NewLinkToken()
	if err != nil {
		h.internalError(w, err)
		return
	}
	clientID, err := h.resolveOAuthClientID(ctx)
	if err != nil {
		h.logger.Error("oauth_client_id_unavailable", slog.String("err", err.Error()))
		h.internalError(w, err)
		return
	}
	authorizeURL, err := h.oauth.BuildAuthorizeURL(clientID, h.cfg.RedirectURI(), state, pkce.Challenge, h.cfg.OAuthScopesOrDefault())
	if err != nil {
		h.internalError(w, err)
		return
	}
	pending := &session.Pending{
		State:        state,
		LinkToken:    linkToken,
		AuthorizeURL: authorizeURL,
		PhoneNumber:  phone,
		CodeVerifier: pkce.Verifier,
		ExpiresAt:    time.Now().Add(h.cfg.PKCETTL()).UTC(),
	}
	if err := h.store.PutPending(ctx, pending); err != nil {
		h.internalError(w, err)
		return
	}
	shortURL := strings.TrimRight(h.cfg.PublicBaseURL, "/") + "/c/" + linkToken
	writeJSON(w, http.StatusOK, MessageResponse{
		Status:       "needs_auth",
		Reply:        replyBuilder(shortURL),
		AuthorizeURL: authorizeURL,
	})
}

// resolveOAuthClientID picks the right client_id depending on the OAuth
// mode. In direct mode the value comes from config (a pre-registered
// confidential client). In MCP/legacy mode we fetch the DCR record.
func (h *MessagesHandler) resolveOAuthClientID(ctx context.Context) (string, error) {
	if h.cfg.OAuthDirect() {
		id := strings.TrimSpace(h.cfg.OAuthClientID)
		if id == "" {
			return "", fmt.Errorf("OAUTH_CLIENT_ID is empty in direct mode")
		}
		return id, nil
	}
	reg, err := h.store.GetClientRegistration(ctx)
	if err != nil {
		return "", err
	}
	return reg.ClientID, nil
}

func (h *MessagesHandler) internalError(w http.ResponseWriter, err error) {
	h.logger.Error("messages_internal_error", slog.String("err", err.Error()))
	writeJSON(w, http.StatusInternalServerError, MessageResponse{
		Status: "error",
		Reply:  llm.InternalError(),
		Error:  &errorBody{Code: "INTERNAL_ERROR"},
	})
}

