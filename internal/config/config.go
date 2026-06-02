package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

// Config holds every runtime knob this service knows about. We document
// each field next to its declaration so the README can stay focused on
// architecture instead of repeating env-var prose.
type Config struct {
	// HTTP
	Port     int    `mapstructure:"port"`
	LogLevel string `mapstructure:"log_level"`

	// Endpoint público
	APIStaticToken string `mapstructure:"api_static_token"`
	// PublicBaseURL is the https origin n8n and Cognito reach (ngrok in
	// dev, the real backend host in prod). Used to build redirect_uri
	// and the /c/{token} short link.
	PublicBaseURL string `mapstructure:"public_base_url"`

	// OAuth2 — Direct mode is the default (and only fully-supported)
	// mode since Phase 5. Legacy "mcp" mode is kept behind OAuthMode in
	// case anyone wants to bring back the MCP fa\u00e7ade DCR path.
	OAuthMode         string `mapstructure:"oauth_mode"`
	OAuthAuthorizeURL string `mapstructure:"oauth_authorize_url"`
	OAuthTokenURL     string `mapstructure:"oauth_token_url"`
	OAuthClientID     string `mapstructure:"oauth_client_id"`
	OAuthClientSecret string `mapstructure:"oauth_client_secret"`
	OAuthScopes       string `mapstructure:"oauth_scopes"`
	OAuthRedirectPath string `mapstructure:"oauth_redirect_path"`
	StateHMACSecret   string `mapstructure:"state_hmac_secret"`
	PKCETTLSeconds    int    `mapstructure:"pkce_ttl_seconds"`

	// Legacy MCP fa\u00e7ade endpoints. Kept for OAuthMode="mcp". The
	// production deployment uses OAuthMode="direct" and ignores these.
	MCPServerBaseURL string `mapstructure:"mcp_server_base_url"`
	MCPEndpointPath  string `mapstructure:"mcp_endpoint_path"`

	// OpenAI
	OpenAIAPIKey         string `mapstructure:"openai_api_key"`
	OpenAIModel          string `mapstructure:"openai_model"`
	OpenAITimeoutSeconds int    `mapstructure:"openai_timeout_seconds"`

	// Classifier (cheap LLM gate before the NLU; classifies a message as
	// on_topic / meta_help / off_topic).
	ClassifierEnabled         bool   `mapstructure:"classifier_enabled"`
	ClassifierModel           string `mapstructure:"classifier_model"`
	ClassifierTimeoutSeconds  int    `mapstructure:"classifier_timeout_seconds"`
	ClassifierCacheTTLSeconds int    `mapstructure:"classifier_cache_ttl_seconds"`
	ClassifierContextTurns    int    `mapstructure:"classifier_context_turns"`

	// n8n callback (proactive messages — e.g. OAuth success).
	N8NWebhookURL   string `mapstructure:"n8n_webhook_url"`
	N8NWebhookToken string `mapstructure:"n8n_webhook_token"`

	// Storage
	SessionBackend    string `mapstructure:"session_backend"`
	DynamoDBTableName string `mapstructure:"dynamodb_table_name"`
	DynamoDBEndpoint  string `mapstructure:"dynamodb_endpoint"`
	AWSRegion         string `mapstructure:"aws_region"`

	// Clicksign REST API (flow pipeline backbone).
	ClicksignAPIBaseURL        string `mapstructure:"clicksign_api_base_url"`
	ClicksignAPITimeoutSeconds int    `mapstructure:"clicksign_api_timeout_seconds"`

	// NLU LLM — extracts intent + entities from user messages.
	NLUModel          string `mapstructure:"nlu_model"`
	NLUTimeoutSeconds int    `mapstructure:"nlu_timeout_seconds"`
}

func (c *Config) RedirectURI() string {
	return strings.TrimRight(c.PublicBaseURL, "/") + c.OAuthRedirectPath
}

func (c *Config) PKCETTL() time.Duration {
	return time.Duration(c.PKCETTLSeconds) * time.Second
}

func (c *Config) OpenAITimeout() time.Duration {
	return time.Duration(c.OpenAITimeoutSeconds) * time.Second
}

func (c *Config) ClassifierTimeout() time.Duration {
	return time.Duration(c.ClassifierTimeoutSeconds) * time.Second
}

func (c *Config) ClassifierCacheTTL() time.Duration {
	return time.Duration(c.ClassifierCacheTTLSeconds) * time.Second
}

func (c *Config) ClicksignAPITimeout() time.Duration {
	return time.Duration(c.ClicksignAPITimeoutSeconds) * time.Second
}

func (c *Config) NLUTimeout() time.Duration {
	return time.Duration(c.NLUTimeoutSeconds) * time.Second
}

// OAuthDirect reports whether the OAuth path skips DCR/MCP and talks
// straight to the Clicksign Cognito. This is the default since Phase 3+
// (and the only fully-supported path since Phase 5).
func (c *Config) OAuthDirect() bool {
	mode := strings.ToLower(strings.TrimSpace(c.OAuthMode))
	// Empty defaults to "direct" so a fresh deployment never accidentally
	// goes through the MCP fa\u00e7ade (meant for external MCP clients, not
	// our backend).
	return mode == "" || mode == "direct"
}

// OAuthScopesOrDefault returns the scopes string for the /authorize
// call, with a sensible default.
func (c *Config) OAuthScopesOrDefault() string {
	if s := strings.TrimSpace(c.OAuthScopes); s != "" {
		return s
	}
	return "openid email phone"
}

// MCPEndpointURL is the (legacy) MCP fa\u00e7ade endpoint, used only when
// OAuthMode="mcp". Returns an empty string when the base URL is unset
// so callers can skip the legacy bootstrap.
func (c *Config) MCPEndpointURL() string {
	if strings.TrimSpace(c.MCPServerBaseURL) == "" {
		return ""
	}
	return strings.TrimRight(c.MCPServerBaseURL, "/") + c.MCPEndpointPath
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	v := viper.New()
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	v.SetDefault("port", 8080)
	v.SetDefault("log_level", "info")

	// OAuth defaults — staging Cognito.
	v.SetDefault("oauth_mode", "direct")
	v.SetDefault("oauth_authorize_url", "https://oauth2.clicksign.dev/login")
	v.SetDefault("oauth_token_url", "https://oauth2.clicksign.dev/oauth2/token")
	v.SetDefault("oauth_scopes", "openid email phone")
	v.SetDefault("oauth_redirect_path", "/oauth2/callback")
	v.SetDefault("pkce_ttl_seconds", 300)

	// Legacy MCP fa\u00e7ade defaults — only used when OAUTH_MODE=mcp.
	v.SetDefault("mcp_server_base_url", "https://mcp-api-tavola-v3-6.clicksign.dev")
	v.SetDefault("mcp_endpoint_path", "/mcp/oauth2")

	// LLM defaults.
	v.SetDefault("openai_model", "gpt-4o-mini")
	v.SetDefault("openai_timeout_seconds", 60)

	v.SetDefault("classifier_enabled", true)
	v.SetDefault("classifier_model", "gpt-4o-mini")
	v.SetDefault("classifier_timeout_seconds", 10)
	v.SetDefault("classifier_cache_ttl_seconds", 60)
	v.SetDefault("classifier_context_turns", 4)

	v.SetDefault("session_backend", "memory")
	v.SetDefault("dynamodb_table_name", "whatsapp_mcp_sessions")
	v.SetDefault("aws_region", "us-east-1")

	// Clicksign REST API defaults — staging. Flip to
	// https://app.clicksign.com/api/v3 for production.
	v.SetDefault("clicksign_api_base_url", "https://4.clicksign.dev/api/v3")
	v.SetDefault("clicksign_api_timeout_seconds", 20)

	v.SetDefault("nlu_model", "gpt-4o-mini")
	v.SetDefault("nlu_timeout_seconds", 15)

	for _, key := range []string{
		"port", "log_level",
		"api_static_token", "public_base_url",
		"oauth_mode", "oauth_authorize_url", "oauth_token_url",
		"oauth_client_id", "oauth_client_secret", "oauth_scopes",
		"oauth_redirect_path", "state_hmac_secret", "pkce_ttl_seconds",
		"mcp_server_base_url", "mcp_endpoint_path",
		"openai_api_key", "openai_model", "openai_timeout_seconds",
		"classifier_enabled", "classifier_model", "classifier_timeout_seconds",
		"classifier_cache_ttl_seconds", "classifier_context_turns",
		"n8n_webhook_url", "n8n_webhook_token",
		"session_backend", "dynamodb_table_name", "dynamodb_endpoint", "aws_region",
		"clicksign_api_base_url", "clicksign_api_timeout_seconds",
		"nlu_model", "nlu_timeout_seconds",
	} {
		_ = v.BindEnv(key, strings.ToUpper(key))
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("config unmarshal: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var missing []string
	if c.APIStaticToken == "" {
		missing = append(missing, "API_STATIC_TOKEN")
	}
	if c.PublicBaseURL == "" {
		missing = append(missing, "PUBLIC_BASE_URL")
	}
	if c.StateHMACSecret == "" {
		missing = append(missing, "STATE_HMAC_SECRET")
	}
	if c.OpenAIAPIKey == "" {
		missing = append(missing, "OPENAI_API_KEY")
	}
	if c.OAuthDirect() {
		if strings.TrimSpace(c.OAuthClientID) == "" {
			missing = append(missing, "OAUTH_CLIENT_ID")
		}
		if strings.TrimSpace(c.OAuthClientSecret) == "" {
			missing = append(missing, "OAUTH_CLIENT_SECRET")
		}
		if strings.TrimSpace(c.OAuthAuthorizeURL) == "" {
			missing = append(missing, "OAUTH_AUTHORIZE_URL")
		}
		if strings.TrimSpace(c.OAuthTokenURL) == "" {
			missing = append(missing, "OAUTH_TOKEN_URL")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	if len(c.StateHMACSecret) < 16 {
		return errors.New("STATE_HMAC_SECRET must be at least 16 chars")
	}
	return nil
}
