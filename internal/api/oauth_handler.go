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
//
// When AccountKey is set, the user is fully ready (single-account login or
// auto-selected) and n8n should reply with the success message. When the
// PendingAccounts slice is populated instead, n8n must render an account
// chooser in WhatsApp — see internal/n8n for the payload shape.
type N8NNotifier interface {
	OAuthSuccess(ctx context.Context, phone, accountKey string, pending []session.PendingAccount) error
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

	// Resolve the Clicksign accounts the OAuth grant has access to. The
	// outcome dictates the user-facing flow:
	//   - 0 accounts (or list failure) → log + proceed; the LLM will hit
	//     401/403 on the first tool call and the user can re-auth.
	//   - 1 account                    → auto-select; no friction.
	//   - 2+ accounts                  → leave AccountKey empty and stash
	//     the candidates in PendingAccounts so the conversation layer can
	//     inject a disambiguation preamble + n8n can render a chooser.
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
		case len(accounts) == 1:
			sess.AccountKey = accounts[0].Attributes.Key
			h.logger.Info("oauth_callback_account_auto_selected",
				slog.String("phone_hash", logging.HashPhone(pending.PhoneNumber)),
				slog.String("account_name", accounts[0].Attributes.Name),
			)
		default:
			sess.PendingAccounts = make([]session.PendingAccount, 0, len(accounts))
			for _, a := range accounts {
				sess.PendingAccounts = append(sess.PendingAccounts, session.PendingAccount{
					Key:  a.Attributes.Key,
					Name: a.Attributes.Name,
				})
			}
			h.logger.Info("oauth_callback_pending_account_selection",
				slog.String("phone_hash", logging.HashPhone(pending.PhoneNumber)),
				slog.Int("accounts_count", len(accounts)),
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
		pendingAccounts := append([]session.PendingAccount(nil), sess.PendingAccounts...)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := h.n8n.OAuthSuccess(ctx, pending.PhoneNumber, accountKey, pendingAccounts); err != nil {
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
