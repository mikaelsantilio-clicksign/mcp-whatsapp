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

// MessagesHandler serves POST /api/messages — the single inbound
// endpoint from n8n. It always delegates the heavy lifting to the
// FlowPipeline (Option B); the legacy MCP/LLM conversation pipeline
// was removed in Phase 5.
type MessagesHandler struct {
	cfg          *config.Config
	logger       *slog.Logger
	store        session.Store
	oauth        *oauth.Client
	signer       *oauth.StateSigner
	flowPipeline *FlowPipeline
	idempotency  *idempotencyCache
}

func NewMessagesHandler(
	cfg *config.Config,
	logger *slog.Logger,
	store session.Store,
	oauthClient *oauth.Client,
	signer *oauth.StateSigner,
	flowPipeline *FlowPipeline,
) *MessagesHandler {
	return &MessagesHandler{
		cfg:          cfg,
		logger:       logger,
		store:        store,
		oauth:        oauthClient,
		signer:       signer,
		flowPipeline: flowPipeline,
		idempotency:  newIdempotencyCache(60 * time.Second),
	}
}

// Attachment is the wire-format attachment item in the inbound payload.
// Kept distinct from flow.Attachment so the HTTP layer can evolve
// independently of the flow contract.
type Attachment struct {
	URL      string `json:"url"`
	MimeType string `json:"mime_type,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// MessageRequest is the inbound payload from n8n. Either Message or
// InteractiveReply must be present (a click-only turn omits Message).
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

	// Idempotency: WhatsApp + n8n can deliver the same wamid more than
	// once on retries. We keep a 60s in-memory dedup window so the
	// flow side never runs the same envelope creation twice in a row.
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

	if h.flowPipeline == nil {
		// Defensive: a misconfigured boot would leave us without a
		// pipeline. Better surface a clear error than silently 200.
		h.logger.Error("flow_pipeline_missing", slog.String("phone_hash", phoneHash))
		writeJSON(w, http.StatusInternalServerError, MessageResponse{
			Status: "error",
			Reply:  llm.InternalError(),
			Error:  &errorBody{Code: "INTERNAL_ERROR", Details: "flow pipeline not initialised"},
		})
		return
	}

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
// mode. In direct mode (default since Phase 3+) the value comes from
// config; in legacy MCP mode it comes from the DCR record persisted at
// bootstrap.
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
