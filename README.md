# whatsapp-mcp

Bridge HTTP em Go que recebe mensagens do WhatsApp via n8n, autentica o usuário no MCP server da Clicksign (OAuth2 + DCR) e responde com uma LLM (OpenAI) usando as tools do MCP server como um cliente MCP.

> Veja o desenho completo em [`IMPLEMENTATION_PLAN.md`](./IMPLEMENTATION_PLAN.md).

## Fluxo resumido

```
WhatsApp → n8n → POST /api/messages (Bearer estático)
   ├─ sem sessão  → reply com link curto (https://host/c/XXXX) → 302 → /oauth2/authorize da Clicksign
   └─ com sessão  → MCP client (Bearer OAuth) ↔ OpenAI tool-calling ↔ tools Clicksign

GET /oauth2/callback → troca code → tokens persistidos → success.html + webhook n8n
GET /c/{link_token}  → 302 → authorize_url real
```

## Componentes

- `cmd/server` — bootstrap (discovery + DCR + HTTP server)
- `internal/config` — Viper + envs
- `internal/logging` — slog JSON + hash de phone para logs
- `internal/session` — `Store` interface + `MemoryStore` (sessões, pending OAuth, DCR singleton)
- `internal/oauth` — PKCE, state HMAC, DCR, authorize/token/refresh, short link tokens
- `internal/api` — handlers HTTP (`/api/messages`, `/oauth2/callback`, `/c/{token}`, `/healthz`), middleware (bearer, request-id, access log, recover), idempotência
- `internal/mcpclient` — wrapper do `mark3labs/mcp-go` streamable HTTP com injeção de Bearer e refresh on 401
- `internal/llm` — OpenAI tool-calling loop + replies estáticos em pt-BR + system prompt
- `internal/conv` — contrato sem deps entre `api` e `llm` (quebra ciclo)
- `internal/n8n` — webhook outbound (`oauth_success`, `session_expired`)

## Setup

```bash
cp .env.example .env
# preencha API_STATIC_TOKEN, STATE_HMAC_SECRET, OPENAI_API_KEY, PUBLIC_BASE_URL
make run            # ou: go run ./cmd/server
```

Para HTTPS público durante o hackathon (necessário para o redirect_uri OAuth):

```bash
ngrok http 8080
# ajuste PUBLIC_BASE_URL para o https do ngrok
```

## Endpoints

| Método | Caminho | Auth | Descrição |
|---|---|---|---|
| `POST` | `/api/messages` | Bearer estático | recebe mensagens do n8n |
| `GET`  | `/oauth2/callback` | — | termina o fluxo OAuth2 |
| `GET`  | `/c/{token}` | — | shortlink → 302 para `authorize_url` |
| `GET`  | `/healthz` | — | health check |

## Contrato `POST /api/messages`

Request:

```json
{
  "phone_number": "+5511999999999",
  "message": "liste meus templates",
  "attachments": [{"url": "https://...", "mime_type": "application/pdf", "filename": "x.pdf"}],
  "message_id": "wamid.xxx"
}
```

Response (`needs_auth`):

```json
{
  "status": "needs_auth",
  "reply": "Olá! Para eu poder te ajudar… 👉 https://host/c/QKGP4BECQDPMU",
  "authorize_url": "https://mcp-api-tavola-v3-6.clicksign.dev/oauth2/authorize?..."
}
```

Response (`ok`):

```json
{
  "status": "ok",
  "reply": "Encontrei 3 templates: …",
  "tool_calls": [{"name": "list_templates", "ok": true}]
}
```

## Comandos

```bash
make run             # roda o servidor
make dev             # roda com Air (hot reload)
make build           # bin/whatsapp-mcp
make test            # testes unitários
make lint            # go vet + gofmt
make docker          # docker build
```

## Status (MVP)

- [x] Fase 0: scaffolding (config, logging, chi, /healthz, Dockerfile, Makefile)
- [x] Fase 1: session store em memória + bearer estático
- [x] Fase 2: OAuth2 client (PKCE + state HMAC + DCR + authorize + token + refresh) + callback + shortlink + needs_auth
- [x] Fase 3: MCP client (mark3labs/mcp-go) com Bearer injetado + refresh-on-401 + cache de tools
- [x] Fase 4: loop de tool-calling com OpenAI + replies estáticos em pt-BR + system prompt
- [x] Fase 5: webhook n8n (`oauth_success`, `session_expired`) + idempotência por `message_id`
- [ ] Fase 6 (opcional): backend DynamoDB
- [ ] Fase 7 (opcional): observabilidade avançada / deploy
