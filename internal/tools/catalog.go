package tools

import (
	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
	"github.com/clicksign/whatsapp-mcp/internal/session"
)

// CatalogDeps bundles the dependencies that the tool closures need to
// execute. It is built once at boot and shared by every Run call.
type CatalogDeps struct {
	Clicksign   *clicksign.HTTPClient
	Store       session.Store
	FileFetcher clicksign.FileFetcher
}

// Catalog returns the static list of LLM-facing tools. Order matters only
// for /tools/list rendering on the LLM side; semantics are identical for
// any permutation.
func Catalog(deps CatalogDeps) []Tool {
	return []Tool{
		listEnvelopesTool(deps),
		listTemplatesTool(deps),
		getTemplateFieldsTool(deps),
		createEnvelopeWithTemplateTool(deps),
		createEnvelopeWithFileURLTool(deps),
		selectAccountTool(deps),
	}
}
