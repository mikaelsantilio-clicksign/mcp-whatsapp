// Package llm hosts the user-facing reply templates rendered by the
// HTTP layer and the n8n notifier in deterministic paths (auth, errors,
// off-topic, capabilities). The legacy MCP+LLM conversation pipeline
// used to live here too; it was removed in Phase 5 when the project
// committed to the NLU + Guided Flow pipeline (see
// IMPLEMENTATION_PLAN_OPTION_B.md). The package was kept as a single
// home for these strings so any future copy review touches one file.
package llm

import "fmt"

// AuthRequired is the first-touch onboarding reply for users who never
// completed OAuth on this backend.
func AuthRequired(shortURL string) string {
	return fmt.Sprintf(
		"Olá! Para eu poder te ajudar com seus envelopes da Clicksign, primeiro preciso que você conecte sua conta. É rápido:\n\n"+
			"1. Toque no link abaixo\n"+
			"2. Faça login na Clicksign\n"+
			"3. Volte aqui no WhatsApp\n\n"+
			"👉 %s\n\n"+
			"O link expira em 5 minutos e é só seu — evite compartilhar.",
		shortURL,
	)
}

// SessionExpired is rendered when a refresh token also fails or the
// backend simply doesn't have a usable session anymore.
func SessionExpired(shortURL string) string {
	return fmt.Sprintf(
		"Sua sessão com a Clicksign expirou. Para continuar, conecte novamente:\n\n👉 %s\n\n"+
			"Esse link expira em 5 minutos.",
		shortURL,
	)
}

// RefreshFailed kept for proactive notifications: when the backend
// realises the refresh token died asynchronously (out-of-band logout
// from the Clicksign side, revoked client, etc.) it sends this reply.
func RefreshFailed(shortURL string) string {
	return fmt.Sprintf(
		"Precisei desconectar sua conta por segurança. Para continuar:\n\n👉 %s",
		shortURL,
	)
}

// OAuthSuccess is the message pushed proactively through n8n right
// after the user finishes the browser flow.
func OAuthSuccess() string {
	return "Pronto! Sua conta Clicksign está conectada ✅\n\n" +
		"É só me dizer o que precisa — por exemplo: \"liste meus templates\" ou \"envie um envelope para fulano@x.com\"."
}

// OAuthFailed is the inverse of OAuthSuccess (kept for symmetry; the
// n8n side calls it when our callback redirects with an error).
func OAuthFailed() string {
	return "Não consegui concluir sua conexão com a Clicksign. Pode tentar novamente? Se persistir, me avise."
}

// UpstreamTimeout is surfaced when the Clicksign REST API takes too
// long. The flow pipeline returns this via humanAPIError on 5xx codes.
func UpstreamTimeout() string {
	return "Tive um problema temporário para falar com a Clicksign. Você pode tentar de novo em alguns segundos?"
}

// InvalidInput is returned when the inbound JSON body is malformed or
// missing the required fields (phone_number, message OR interactive_reply).
func InvalidInput() string {
	return "Não recebi seus dados corretamente. Tente enviar novamente?"
}

// InternalError is the bland fallback for panics / unexpected errors.
func InternalError() string {
	return "Tive um problema interno. Tente de novo em alguns segundos, por favor."
}

// OffTopic is the reply for messages the classifier flagged as not
// related to Clicksign. Conservative wording so we never antagonise the
// user — they may just be experimenting with the bot.
func OffTopic() string {
	return "Eu só ajudo com envelopes, documentos, templates e assinaturas da Clicksign. " +
		"Em que posso ajudar nesse contexto?"
}

// Capabilities is the static reply for "meta_help" intents — saudações
// e perguntas sobre o próprio assistente. We dropped the dynamic
// LLM-backed responder when the legacy MCP pipeline went away — the
// flow pipeline has a known, finite set of actions so a static list is
// always accurate.
func Capabilities() string {
	return "Oi! Eu te ajudo com Clicksign aqui pelo WhatsApp. Algumas coisas que posso fazer:\n" +
		"• Listar os templates da sua conta\n" +
		"• Listar os envelopes (com filtro por status, se quiser)\n" +
		"• Consultar o status de um envelope específico\n" +
		"• Criar envelopes a partir de um PDF ou de um template\n" +
		"• Adicionar signatários (nome, e-mail, papel)\n" +
		"• Excluir um envelope em rascunho\n" +
		"• Trocar de conta Clicksign quando você tem mais de uma\n\n" +
		"Exemplos do que você pode mandar:\n" +
		"• \"lista os templates\"\n" +
		"• \"qual o status do envelope Contrato 1?\"\n" +
		"• \"envia esse PDF, nome do envelope Contrato 1, signatário Mikael, mikael@x.com, parte\""
}
