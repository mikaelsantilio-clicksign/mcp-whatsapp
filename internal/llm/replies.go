package llm

import "fmt"

// User-facing reply templates in pt-BR. These are used in deterministic paths
// (auth required, errors, max iterations, etc.) so we avoid LLM cost and keep
// the wording predictable. The `ok` path uses the model's own output.

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

func SessionExpired(shortURL string) string {
	return fmt.Sprintf(
		"Sua sessão com a Clicksign expirou. Para continuar, conecte novamente:\n\n👉 %s\n\n"+
			"Esse link expira em 5 minutos.",
		shortURL,
	)
}

func RefreshFailed(shortURL string) string {
	return fmt.Sprintf(
		"Precisei desconectar sua conta por segurança. Para continuar:\n\n👉 %s",
		shortURL,
	)
}

func OAuthSuccess() string {
	return "Pronto! Sua conta Clicksign está conectada ✅\n\n" +
		"É só me dizer o que precisa — por exemplo: \"liste meus templates\" ou \"envie um envelope para fulano@x.com\"."
}

func OAuthFailed() string {
	return "Não consegui concluir sua conexão com a Clicksign. Pode tentar novamente? Se persistir, me avise."
}

func UpstreamTimeout() string {
	return "Tive um problema temporário para falar com a Clicksign. Você pode tentar de novo em alguns segundos?"
}

func LLMFailure() string {
	return "Não consegui interpretar sua mensagem agora. Pode tentar reformular?"
}

func MaxIterations() string {
	return "Acho que estamos andando em círculos. Pode resumir o que você precisa em uma frase?"
}

func InvalidInput() string {
	return "Não recebi seus dados corretamente. Tente enviar novamente?"
}

func InternalError() string {
	return "Tive um problema interno. Tente de novo em alguns segundos, por favor."
}
