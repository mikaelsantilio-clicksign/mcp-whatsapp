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

	// MCP Server (Clicksign)
	MCPServerBaseURL string `mapstructure:"mcp_server_base_url"`
	MCPEndpointPath  string `mapstructure:"mcp_endpoint_path"`
	MCPOAuthScopes   string `mapstructure:"mcp_oauth_scopes"`

	// OAuth2 / DCR
	OAuthRedirectPath string `mapstructure:"oauth_redirect_path"`
	StateHMACSecret   string `mapstructure:"state_hmac_secret"`
	PKCETTLSeconds    int    `mapstructure:"pkce_ttl_seconds"`

	// OpenAI
	OpenAIAPIKey             string `mapstructure:"openai_api_key"`
	OpenAIModel              string `mapstructure:"openai_model"`
	OpenAIMaxToolIterations  int    `mapstructure:"openai_max_tool_iterations"`
	OpenAITimeoutSeconds     int    `mapstructure:"openai_timeout_seconds"`

	// n8n callback
	N8NWebhookURL   string `mapstructure:"n8n_webhook_url"`
	N8NWebhookToken string `mapstructure:"n8n_webhook_token"`

	// Storage
	SessionBackend     string `mapstructure:"session_backend"`
	DynamoDBTableName  string `mapstructure:"dynamodb_table_name"`
	DynamoDBEndpoint   string `mapstructure:"dynamodb_endpoint"`
	AWSRegion          string `mapstructure:"aws_region"`
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
	v.SetDefault("session_backend", "memory")
	v.SetDefault("dynamodb_table_name", "whatsapp_mcp_sessions")
	v.SetDefault("aws_region", "us-east-1")

	for _, key := range []string{
		"port", "log_level",
		"api_static_token", "public_base_url",
		"mcp_server_base_url", "mcp_endpoint_path", "mcp_oauth_scopes",
		"oauth_redirect_path", "state_hmac_secret", "pkce_ttl_seconds",
		"openai_api_key", "openai_model", "openai_max_tool_iterations", "openai_timeout_seconds",
		"n8n_webhook_url", "n8n_webhook_token",
		"session_backend", "dynamodb_table_name", "dynamodb_endpoint", "aws_region",
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
	if len(missing) > 0 {
		return fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	if len(c.StateHMACSecret) < 16 {
		return errors.New("STATE_HMAC_SECRET must be at least 16 chars")
	}
	return nil
}
