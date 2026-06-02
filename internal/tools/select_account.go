package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

func containsAccount(accounts []session.PendingAccount, key string) bool {
	for _, a := range accounts {
		if a.Key == key {
			return true
		}
	}
	return false
}

func selectAccountTool(d CatalogDeps) Tool {
	return Tool{
		Name: "select_account",
		Description: "Choose which Clicksign account to use in the current session. Use the account_key returned " +
			"by the previous account list when more than one account is available. The selection persists for the " +
			"entire session and is reflected in subsequent list_envelopes / create_envelope_with_* calls.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account_key": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "account_key returned by the previous account list response.",
				},
			},
			"required": []string{"account_key"},
		},
		Run: func(ctx context.Context, phone string, args map[string]any) (string, error) {
			key := strings.TrimSpace(getString(args, "account_key"))
			if key == "" {
				return "", errors.New("account_key is required")
			}
			sess, err := d.Store.GetSession(ctx, phone)
			if err != nil {
				return "", clicksign.ErrAuthExpired
			}
			// When the session has a pending list, reject keys that are
			// not in it: this protects against LLM hallucinations that
			// would silently set an invalid X-Account-Key and cause
			// confusing 403s on the next call.
			if len(sess.PendingAccounts) > 0 {
				if !containsAccount(sess.PendingAccounts, key) {
					return "", fmt.Errorf("account_key %q is not in the pending selection list", key)
				}
			}
			sess.AccountKey = key
			sess.PendingAccounts = nil
			sess.UpdatedAt = time.Now().UTC()
			if err := d.Store.PutSession(ctx, sess); err != nil {
				return "", fmt.Errorf("persist session: %w", err)
			}
			return fmt.Sprintf(`{"ok":true,"account_key":%q}`, key), nil
		},
	}
}
