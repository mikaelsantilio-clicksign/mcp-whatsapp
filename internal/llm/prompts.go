// Package llm exposes prompts loaded from sibling .md files via //go:embed.
// Edit the .md files in `prompts/` to tweak the wording; `air` will pick up
// the change and rebuild automatically.
package llm

import _ "embed"

//go:embed prompts/system.md
var systemPromptPTBR string

//go:embed prompts/meta_help.md
var metaHelpPromptTemplate string

// SystemPrompt returns the canonical pt-BR system prompt used by the main
// Conversation LLM (the one with tool-calling enabled).
func SystemPrompt() string { return systemPromptPTBR }

// MetaHelpPromptTemplate returns the system prompt template used by the
// auxiliary "meta_help" LLM. The "{{TOOLS}}" placeholder is replaced at
// runtime with the (filtered, translated) list of available MCP tools.
func MetaHelpPromptTemplate() string { return metaHelpPromptTemplate }
