package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/config"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/oauth"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// N8NNotifier is the minimal interface the OAuth handler needs to optionally
// notify n8n that an OAuth flow succeeded.
type N8NNotifier interface {
	OAuthSuccess(ctx context.Context, phone string, accountKey string) error
}

type OAuthHandler struct {
	cfg     *config.Config
	logger  *slog.Logger
	store   session.Store
	oauth   *oauth.Client
	signer  *oauth.StateSigner
	n8n     N8NNotifier
}

func NewOAuthHandler(
	cfg *config.Config,
	logger *slog.Logger,
	store session.Store,
	oauthClient *oauth.Client,
	signer *oauth.StateSigner,
	n8n N8NNotifier,
) *OAuthHandler {
	return &OAuthHandler{
		cfg:    cfg,
		logger: logger,
		store:  store,
		oauth:  oauthClient,
		signer: signer,
		n8n:    n8n,
	}
}

// Callback handles GET /oauth2/callback.
func (h *OAuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		h.logger.Warn("oauth_callback_error",
			slog.String("error", errParam),
			slog.String("error_description", q.Get("error_description")),
		)
		h.renderExpired(w, http.StatusBadRequest)
		return
	}
	state := q.Get("state")
	code := q.Get("code")
	if state == "" || code == "" {
		h.renderExpired(w, http.StatusBadRequest)
		return
	}

	if _, err := h.signer.Verify(state); err != nil {
		h.logger.Warn("oauth_state_invalid", slog.String("err", err.Error()))
		h.renderExpired(w, http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	pending, err := h.store.GetPendingByState(ctx, state)
	if err != nil {
		h.logger.Warn("oauth_pending_missing", slog.String("err", err.Error()))
		h.renderExpired(w, http.StatusBadRequest)
		return
	}

	clientID, err := h.resolveOAuthClientID(ctx)
	if err != nil {
		h.logger.Error("oauth_client_id_unavailable", slog.String("err", err.Error()))
		h.renderExpired(w, http.StatusInternalServerError)
		return
	}

	token, err := h.oauth.ExchangeCode(ctx, clientID, h.cfg.RedirectURI(), code, pending.CodeVerifier)
	if err != nil {
		h.logger.Error("oauth_code_exchange_failed",
			slog.String("err", err.Error()),
			slog.String("phone_hash", logging.HashPhone(pending.PhoneNumber)),
		)
		h.renderExpired(w, http.StatusBadGateway)
		return
	}

	sess := &session.Session{
		PhoneNumber:  pending.PhoneNumber,
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := h.store.PutSession(ctx, sess); err != nil {
		h.logger.Error("oauth_session_put_failed", slog.String("err", err.Error()))
		h.renderExpired(w, http.StatusInternalServerError)
		return
	}
	_ = h.store.DeletePending(ctx, state)

	h.logger.Info("oauth_success",
		slog.String("phone_hash", logging.HashPhone(pending.PhoneNumber)),
		slog.Time("expires_at", sess.ExpiresAt),
	)

	if h.n8n != nil {
		// Fire-and-forget with short timeout to keep the redirect snappy.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := h.n8n.OAuthSuccess(ctx, pending.PhoneNumber, ""); err != nil {
				h.logger.Warn("n8n_notify_failed", slog.String("err", err.Error()))
			}
		}()
	}

	h.renderSuccess(w)
}

// resolveOAuthClientID returns the OAuth client_id for this deployment.
// In direct mode it comes from config; in MCP/legacy mode from the DCR
// record persisted at bootstrap.
func (h *OAuthHandler) resolveOAuthClientID(ctx context.Context) (string, error) {
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

// ShortLink handles GET /c/{token}, redirecting to the real authorize URL.
func (h *OAuthHandler) ShortLink(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Path
	// chi will route /c/{token}; we pull from URL path manually so the
	// handler can be wired through chi.URLParam too. The chi mux registers
	// this handler at "/c/{token}" and exposes the param via chi.URLParam.
	token = extractShortLinkToken(r)
	if token == "" {
		h.renderExpired(w, http.StatusBadRequest)
		return
	}

	pending, err := h.store.GetPendingByLinkToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrExpired) {
			h.renderExpired(w, http.StatusGone)
			return
		}
		h.logger.Error("shortlink_lookup_failed", slog.String("err", err.Error()))
		h.renderExpired(w, http.StatusInternalServerError)
		return
	}
	if pending.AuthorizeURL == "" {
		h.renderExpired(w, http.StatusGone)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, pending.AuthorizeURL, http.StatusFound)
}

func (h *OAuthHandler) renderSuccess(w http.ResponseWriter) {
	body, err := webFS.ReadFile("templates/success.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *OAuthHandler) renderExpired(w http.ResponseWriter, status int) {
	body, err := webFS.ReadFile("templates/link_expired.html")
	if err != nil {
		http.Error(w, "link expired", status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
