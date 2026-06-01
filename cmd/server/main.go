package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/clicksign/whatsapp-mcp/internal/api"
	"github.com/clicksign/whatsapp-mcp/internal/classifier"
	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/config"
	"github.com/clicksign/whatsapp-mcp/internal/flow"
	"github.com/clicksign/whatsapp-mcp/internal/llm"
	"github.com/clicksign/whatsapp-mcp/internal/logging"
	"github.com/clicksign/whatsapp-mcp/internal/mcpclient"
	"github.com/clicksign/whatsapp-mcp/internal/n8n"
	"github.com/clicksign/whatsapp-mcp/internal/nlu"
	"github.com/clicksign/whatsapp-mcp/internal/oauth"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logging.New(cfg.LogLevel)
	slog.SetDefault(logger)

	store := session.NewMemoryStore()

	var oauthClient *oauth.Client
	if cfg.OAuthDirect() {
		oauthClient = oauth.NewDirectClient(oauth.DirectConfig{
			AuthorizationURL: cfg.OAuthAuthorizeURL,
			TokenURL:         cfg.OAuthTokenURL,
			ClientID:         cfg.OAuthClientID,
			ClientSecret:     cfg.OAuthClientSecret,
		})
		logger.Info("oauth_mode_direct",
			slog.String("authorize_url", cfg.OAuthAuthorizeURL),
			slog.String("token_url", cfg.OAuthTokenURL),
			slog.String("client_id_prefix", prefix(cfg.OAuthClientID, 8)),
		)
	} else {
		oauthClient = oauth.NewClient(cfg.MCPServerBaseURL)
		logger.Info("oauth_mode_mcp",
			slog.String("mcp_base_url", cfg.MCPServerBaseURL),
		)
	}
	signer := oauth.NewStateSigner(cfg.StateHMACSecret)

	// DCR is only needed in MCP/legacy mode. In direct mode our client is
	// already confidential and registered manually.
	if !cfg.OAuthDirect() {
		bootCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := bootstrapOAuthClient(bootCtx, logger, cfg, oauthClient, store); err != nil {
			// Don't fail hard — log and continue. The /api/messages handler will
			// re-attempt when needed (DCR is on the critical path of needs_auth).
			logger.Error("oauth_bootstrap_failed", slog.String("err", err.Error()))
		}
	}

	mcpManager := mcpclient.NewManager(cfg, logger, store, oauthClient)

	var intentClassifier classifier.Classifier = classifier.AlwaysOnTopic{}
	if cfg.ClassifierEnabled {
		intentClassifier = classifier.NewOpenAI(logger, classifier.OpenAIConfig{
			APIKey:   cfg.OpenAIAPIKey,
			Model:    cfg.ClassifierModel,
			Timeout:  cfg.ClassifierTimeout(),
			CacheTTL: cfg.ClassifierCacheTTL(),
		})
		logger.Info("classifier_enabled",
			slog.String("model", cfg.ClassifierModel),
			slog.Int("context_turns", cfg.ClassifierContextTurns),
		)
	} else {
		logger.Warn("classifier_disabled_all_messages_billed_to_main_llm")
	}

	var metaResponder *llm.MetaHelpResponder
	if cfg.MetaHelpEnabled {
		metaResponder = llm.NewMetaHelpResponder(cfg, logger, mcpManager)
		logger.Info("meta_help_enabled",
			slog.String("model", cfg.MetaHelpModel),
		)
	} else {
		logger.Info("meta_help_disabled_using_static_capabilities")
	}

	conversation := llm.NewConversation(cfg, logger, store, mcpManager, intentClassifier, metaResponder)
	notifier := n8n.NewNotifier(logger, cfg.N8NWebhookURL, cfg.N8NWebhookToken)

	// Option B pipeline wiring. Built unconditionally so we can flip the
	// PIPELINE flag at runtime without rebuilding; the messages_handler
	// uses cfg.PipelineFlow() to choose between legacy and flow.
	flowPipeline := buildFlowPipeline(cfg, logger, store, oauthClient, intentClassifier)
	logger.Info("flow_pipeline_built",
		slog.Bool("active", cfg.PipelineFlow()),
		slog.String("clicksign_base_url", cfg.ClicksignAPIBaseURL),
		slog.String("nlu_model", cfg.NLUModel),
	)

	messages := api.NewMessagesHandler(cfg, logger, store, oauthClient, signer, conversation, flowPipeline)
	oauthHandler := api.NewOAuthHandler(cfg, logger, store, oauthClient, signer, notifier)
	health := api.NewHealthHandler(cfg)

	r := chi.NewRouter()
	r.Use(api.RequestID())
	r.Use(api.AccessLog(logger))
	r.Use(api.Recover(logger))

	r.Get("/healthz", health.Get)
	r.Get(cfg.OAuthRedirectPath, oauthHandler.Callback)
	r.Get("/c/{token}", oauthHandler.ShortLink)

	r.Group(func(r chi.Router) {
		r.Use(api.StaticBearer(cfg.APIStaticToken))
		r.Post("/api/messages", messages.Post)
	})

	addr := ":" + strconv.Itoa(cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server_starting",
			slog.String("addr", addr),
			slog.String("redirect_uri", cfg.RedirectURI()),
			slog.String("mcp_endpoint", cfg.MCPEndpointURL()),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("server_shutting_down")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// bootstrapOAuthClient ensures we have a DCR client_id persisted. Runs once
// per fresh deployment; subsequent restarts will reuse the stored value if
// the session store is persistent (DynamoDB). With the in-memory store this
// runs on every boot.
func bootstrapOAuthClient(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	client *oauth.Client,
	store session.Store,
) error {
	if existing, err := store.GetClientRegistration(ctx); err == nil && existing.ClientID != "" {
		logger.Info("oauth_client_existing",
			slog.String("client_id_prefix", prefix(existing.ClientID, 8)),
			slog.Time("registered_at", existing.RegisteredAt),
		)
		return nil
	}

	md, err := client.Discover(ctx)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	logger.Info("oauth_discovery_ok",
		slog.String("issuer", md.Issuer),
		slog.String("authorization_endpoint", md.AuthorizationEndpoint),
		slog.String("token_endpoint", md.TokenEndpoint),
		slog.String("registration_endpoint", md.RegistrationEndpoint),
	)

	reg, err := client.RegisterDynamic(ctx, cfg.RedirectURI(), cfg.MCPOAuthScopes)
	if err != nil {
		return fmt.Errorf("dcr: %w", err)
	}
	logger.Info("oauth_dcr_ok",
		slog.String("client_id_prefix", prefix(reg.ClientID, 8)),
	)
	return store.PutClientRegistration(ctx, &session.ClientRegistration{
		ClientID:                reg.ClientID,
		RegisteredAt:            time.Now().UTC(),
		TokenEndpointAuthMethod: reg.TokenEndpointAuthMethod,
		RedirectURIs:            reg.RedirectURIs,
	})
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// buildFlowPipeline wires the NLU + Guided Flow pipeline (Option B). Built
// even when PIPELINE=legacy so flipping the flag is a no-restart change;
// when an OpenAI key is absent the NLU degrades to a Static extractor that
// always returns intent=unknown (the router then falls back gracefully).
func buildFlowPipeline(
	cfg *config.Config,
	logger *slog.Logger,
	store session.Store,
	oauthClient *oauth.Client,
	intentClassifier classifier.Classifier,
) *api.FlowPipeline {
	cs := clicksign.NewClient(clicksign.Config{
		BaseURL: cfg.ClicksignAPIBaseURL,
		Timeout: cfg.ClicksignAPITimeout(),
	}, logger, store, oauthClient)

	fetcher := clicksign.NewHTTPFileFetcher(clicksign.FetcherConfig{
		Logger: logger,
		// AllowHTTP / AllowPrivateIPs stay false in production. The
		// n8n integration contract requires every URL to be https://
		// public storage.
	})

	var nluExt nlu.Extractor = nlu.Static{V: nlu.Verdict{Intent: nlu.IntentUnknown, Confidence: nlu.ConfLow}}
	if cfg.OpenAIAPIKey != "" {
		nluExt = nlu.NewOpenAI(logger, nlu.OpenAIConfig{
			APIKey:  cfg.OpenAIAPIKey,
			Model:   cfg.NLUModel,
			Timeout: cfg.NLUTimeout(),
		})
	}

	router := flow.NewRouter(logger,
		flow.NewSelectAccountFlow(cs),
		flow.NewListTemplatesFlow(cs),
		flow.NewListEnvelopesFlow(cs),
		flow.NewEnvelopeStatusFlow(cs),
		flow.NewCreateEnvelopePDFFlow(cs, fetcher),
		flow.NewCreateEnvelopeTmplFlow(cs),
		flow.NewAddSignerFlow(cs),
		flow.NewCancelEnvelopeFlow(cs),
	)
	return api.NewFlowPipeline(cfg, logger, store, intentClassifier, nluExt, router)
}
