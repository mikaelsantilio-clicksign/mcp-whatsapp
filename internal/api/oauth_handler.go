package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/config"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/oauth"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// AccountLister is the minimal interface the OAuth handler needs to resolve
// the user's default Clicksign account right after the token exchange.
type AccountLister interface {
	ListOAuth2AccountsWithToken(ctx context.Context, accessToken string) ([]clicksign.OAuth2Account, error)
}

// N8NNotifier is the minimal interface the OAuth handler needs to optionally
// notify n8n that an OAuth flow succeeded.
type N8NNotifier interface {
	OAuthSuccess(ctx context.Context, phone string, accountKey string) error
}

type OAuthHandler struct {
	cfg      *config.Config
	logger   *slog.Logger
	store    session.Store
	oauth    *oauth.Client
	signer   *oauth.StateSigner
	n8n      N8NNotifier
	accounts AccountLister
}

func NewOAuthHandler(
	cfg *config.Config,
	logger *slog.Logger,
	store session.Store,
	oauthClient *oauth.Client,
	signer *oauth.StateSigner,
	n8n N8NNotifier,
	accounts AccountLister,
) *OAuthHandler {
	return &OAuthHandler{
		cfg:      cfg,
		logger:   logger,
		store:    store,
		oauth:    oauthClient,
		signer:   signer,
		n8n:      n8n,
		accounts: accounts,
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

	reg, err := h.store.GetClientRegistration(ctx)
	if err != nil {
		h.logger.Error("oauth_client_registration_missing", slog.String("err", err.Error()))
		h.renderExpired(w, http.StatusInternalServerError)
		return
	}

	token, err := h.oauth.ExchangeCode(ctx, reg.ClientID, h.cfg.RedirectURI(), code, pending.CodeVerifier)
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

	// Resolve the default account so subsequent tool calls already carry
	// X-Account-Key. Best-effort: failures are logged but do not block
	// login — the LLM can recover via the select_account tool.
	if h.accounts != nil {
		acctCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		accounts, accErr := h.accounts.ListOAuth2AccountsWithToken(acctCtx, token.AccessToken)
		cancel()
		switch {
		case accErr != nil:
			h.logger.Warn("oauth_callback_list_accounts_failed",
				slog.String("phone_hash", logging.HashPhone(pending.PhoneNumber)),
				slog.String("err", accErr.Error()),
			)
		case len(accounts) == 0:
			h.logger.Warn("oauth_callback_no_accounts",
				slog.String("phone_hash", logging.HashPhone(pending.PhoneNumber)),
			)
		default:
			sess.AccountKey = accounts[0].Attributes.Key
			h.logger.Info("oauth_callback_account_selected",
				slog.String("phone_hash", logging.HashPhone(pending.PhoneNumber)),
				slog.Int("accounts_count", len(accounts)),
				slog.String("account_name", accounts[0].Attributes.Name),
			)
		}
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
		accountKey := sess.AccountKey
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := h.n8n.OAuthSuccess(ctx, pending.PhoneNumber, accountKey); err != nil {
				h.logger.Warn("n8n_notify_failed", slog.String("err", err.Error()))
			}
		}()
	}

	h.renderSuccess(w)
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
