package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	// HTTP
	Port     int    `mapstructure:"port"`
	LogLevel string `mapstructure:"log_level"`

	// Endpoint publico
	APIStaticToken string `mapstructure:"api_static_token"`
	PublicBaseURL  string `mapstructure:"public_base_url"`

	// MCP Server (Clicksign) — used when OAuthMode=="mcp" (the legacy DCR
	// path that goes through the Clicksign MCP fa\u00e7ade). When OAuthMode is
	// "direct" these fields are ignored.
	MCPServerBaseURL string `mapstructure:"mcp_server_base_url"`
	MCPEndpointPath  string `mapstructure:"mcp_endpoint_path"`
	MCPOAuthScopes   string `mapstructure:"mcp_oauth_scopes"`

	// OAuth2 / DCR
	OAuthRedirectPath string `mapstructure:"oauth_redirect_path"`
	StateHMACSecret   string `mapstructure:"state_hmac_secret"`
	PKCETTLSeconds    int    `mapstructure:"pkce_ttl_seconds"`

	// OAuthMode selects the OAuth flow:
	//   - "direct" (default): talk straight to the Clicksign Cognito with a
	//     pre-registered confidential client. Requires OAuthAuthorizeURL,
	//     OAuthTokenURL, OAuthClientID and OAuthClientSecret to be set.
	//   - "mcp" (legacy): use the Clicksign MCP server as an OAuth fa\u00e7ade,
	//     register a public client dynamically (DCR), and let the fa\u00e7ade
	//     talk to Cognito on our behalf.
	OAuthMode string `mapstructure:"oauth_mode"`

	// Direct-mode endpoints. Defaults to the staging Cognito.
	OAuthAuthorizeURL string `mapstructure:"oauth_authorize_url"`
	OAuthTokenURL     string `mapstructure:"oauth_token_url"`
	// OAuthClientID / OAuthClientSecret are the confidential client
	// credentials issued by Clicksign for our app. Only used in direct
	// mode.
	OAuthClientID     string `mapstructure:"oauth_client_id"`
	OAuthClientSecret string `mapstructure:"oauth_client_secret"`
	// OAuthScopes is the space-separated list requested at /login. The
	// Cognito user pool decides what is granted. Defaults to "openid email phone".
	OAuthScopes string `mapstructure:"oauth_scopes"`

	// OpenAI
	OpenAIAPIKey             string `mapstructure:"openai_api_key"`
	OpenAIModel              string `mapstructure:"openai_model"`
	OpenAIMaxToolIterations  int    `mapstructure:"openai_max_tool_iterations"`
	OpenAITimeoutSeconds     int    `mapstructure:"openai_timeout_seconds"`

	// Classifier (intent gate before main LLM)
	ClassifierEnabled        bool   `mapstructure:"classifier_enabled"`
	ClassifierModel          string `mapstructure:"classifier_model"`
	ClassifierTimeoutSeconds int    `mapstructure:"classifier_timeout_seconds"`
	ClassifierCacheTTLSeconds int   `mapstructure:"classifier_cache_ttl_seconds"`
	ClassifierContextTurns   int    `mapstructure:"classifier_context_turns"`

	// MetaHelp responder (cheap LLM for greetings / capability questions)
	MetaHelpEnabled        bool   `mapstructure:"meta_help_enabled"`
	MetaHelpModel          string `mapstructure:"meta_help_model"`
	MetaHelpTimeoutSeconds int    `mapstructure:"meta_help_timeout_seconds"`

	// n8n callback
	N8NWebhookURL   string `mapstructure:"n8n_webhook_url"`
	N8NWebhookToken string `mapstructure:"n8n_webhook_token"`

	// Storage
	SessionBackend     string `mapstructure:"session_backend"`
	DynamoDBTableName  string `mapstructure:"dynamodb_table_name"`
	DynamoDBEndpoint   string `mapstructure:"dynamodb_endpoint"`
	AWSRegion          string `mapstructure:"aws_region"`

	// Pipeline switch: "legacy" (MCP+LLM tool-calling) or "flow" (NLU + Guided Flow).
	// During the Option B migration both paths coexist behind this flag.
	Pipeline string `mapstructure:"pipeline"`

	// Clicksign REST API (Option B)
	ClicksignAPIBaseURL        string `mapstructure:"clicksign_api_base_url"`
	ClicksignAPITimeoutSeconds int    `mapstructure:"clicksign_api_timeout_seconds"`

	// NLU LLM (Option B) — extracts intent + entities from user messages
	NLUModel          string `mapstructure:"nlu_model"`
	NLUTimeoutSeconds int    `mapstructure:"nlu_timeout_seconds"`
}

func (c *Config) RedirectURI() string {
	return strings.TrimRight(c.PublicBaseURL, "/") + c.OAuthRedirectPath
}

func (c *Config) MCPEndpointURL() string {
	return strings.TrimRight(c.MCPServerBaseURL, "/") + c.MCPEndpointPath
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

func (c *Config) MetaHelpTimeout() time.Duration {
	return time.Duration(c.MetaHelpTimeoutSeconds) * time.Second
}

func (c *Config) ClicksignAPITimeout() time.Duration {
	return time.Duration(c.ClicksignAPITimeoutSeconds) * time.Second
}

func (c *Config) NLUTimeout() time.Duration {
	return time.Duration(c.NLUTimeoutSeconds) * time.Second
}

// PipelineFlow reports whether the new NLU + Guided Flow pipeline is active.
// Defaults to false ("legacy") so the existing MCP+LLM tool-calling path stays
// in charge until the flag is explicitly flipped to "flow".
func (c *Config) PipelineFlow() bool {
	return strings.EqualFold(strings.TrimSpace(c.Pipeline), "flow")
}

// OAuthDirect reports whether the OAuth path skips DCR/MCP and talks
// straight to the Clicksign Cognito. This is the default since Phase 3+.
func (c *Config) OAuthDirect() bool {
	mode := strings.ToLower(strings.TrimSpace(c.OAuthMode))
	// Empty defaults to "direct" so a fresh deployment never accidentally
	// goes through the MCP fa\u00e7ade (which is meant for external MCP clients,
	// not our backend).
	return mode == "" || mode == "direct"
}

// OAuthScopesOrDefault returns the scopes string for the /authorize call,
// falling back to MCPOAuthScopes (legacy var) and then to a sensible default.
func (c *Config) OAuthScopesOrDefault() string {
	if s := strings.TrimSpace(c.OAuthScopes); s != "" {
		return s
	}
	if s := strings.TrimSpace(c.MCPOAuthScopes); s != "" {
		return s
	}
	return "openid email phone"
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	v := viper.New()
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	v.SetDefault("port", 8080)
	v.SetDefault("log_level", "info")
	v.SetDefault("mcp_server_base_url", "https://mcp-api-tavola-v3-6.clicksign.dev")
	v.SetDefault("mcp_endpoint_path", "/mcp/oauth2")
	v.SetDefault("mcp_oauth_scopes", "openid email phone")
	v.SetDefault("oauth_redirect_path", "/oauth2/callback")
	v.SetDefault("pkce_ttl_seconds", 300)
	v.SetDefault("openai_model", "gpt-4o-mini")
	v.SetDefault("openai_max_tool_iterations", 8)
	v.SetDefault("openai_timeout_seconds", 60)
	v.SetDefault("classifier_enabled", true)
	v.SetDefault("classifier_model", "gpt-4o-mini")
	v.SetDefault("classifier_timeout_seconds", 10)
	v.SetDefault("classifier_cache_ttl_seconds", 60)
	v.SetDefault("classifier_context_turns", 4)
	v.SetDefault("meta_help_enabled", true)
	v.SetDefault("meta_help_model", "gpt-4o-mini")
	v.SetDefault("meta_help_timeout_seconds", 10)
	v.SetDefault("session_backend", "memory")
	v.SetDefault("dynamodb_table_name", "whatsapp_mcp_sessions")
	v.SetDefault("aws_region", "us-east-1")
	v.SetDefault("pipeline", "flow")
	// Staging defaults — flip CLICKSIGN_API_BASE_URL to
	// https://app.clicksign.com/api/v3 in production.
	v.SetDefault("clicksign_api_base_url", "https://4.clicksign.dev/api/v3")
	v.SetDefault("clicksign_api_timeout_seconds", 20)
	v.SetDefault("nlu_model", "gpt-4o-mini")
	v.SetDefault("nlu_timeout_seconds", 15)
	v.SetDefault("oauth_mode", "direct")
	v.SetDefault("oauth_authorize_url", "https://oauth2.clicksign.dev/login")
	v.SetDefault("oauth_token_url", "https://oauth2.clicksign.dev/oauth2/token")
	v.SetDefault("oauth_scopes", "openid email phone")

	for _, key := range []string{
		"port", "log_level",
		"api_static_token", "public_base_url",
		"mcp_server_base_url", "mcp_endpoint_path", "mcp_oauth_scopes",
		"oauth_redirect_path", "state_hmac_secret", "pkce_ttl_seconds",
		"openai_api_key", "openai_model", "openai_max_tool_iterations", "openai_timeout_seconds",
		"classifier_enabled", "classifier_model", "classifier_timeout_seconds", "classifier_cache_ttl_seconds", "classifier_context_turns",
		"meta_help_enabled", "meta_help_model", "meta_help_timeout_seconds",
		"n8n_webhook_url", "n8n_webhook_token",
		"session_backend", "dynamodb_table_name", "dynamodb_endpoint", "aws_region",
		"pipeline",
		"clicksign_api_base_url", "clicksign_api_timeout_seconds",
		"nlu_model", "nlu_timeout_seconds",
		"oauth_mode", "oauth_authorize_url", "oauth_token_url",
		"oauth_client_id", "oauth_client_secret", "oauth_scopes",
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
