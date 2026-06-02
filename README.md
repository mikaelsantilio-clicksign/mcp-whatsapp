# whatsapp-mcp

Bridge HTTP em Go que recebe mensagens do WhatsApp via n8n, autentica o usuário direto no OAuth2 da Clicksign (Cognito) e responde através de um pipeline **NLU + Guided Flow** — a LLM extrai `intent + entities`, uma máquina de estados em Go orquestra os passos e chama a **API REST v3 da Clicksign**.

> Histórico de decisões: começamos como MCP client (LLM com tool-calling, OAuth2 via fachada MCP). Em Fase 5 trocamos pelo pipeline Option B, que devolve respostas estruturadas (listas/botões) e elimina a fachada MCP do caminho crítico. Detalhes em [`IMPLEMENTATION_PLAN_OPTION_B.md`](./IMPLEMENTATION_PLAN_OPTION_B.md) e contrato com o time de n8n em [`docs/N8N_INTEGRATION_CONTRACT.md`](./docs/N8N_INTEGRATION_CONTRACT.md).

## Fluxo resumido

```
WhatsApp ─ n8n ─▶ POST /api/messages (Bearer estático)
                    │
                    ├─ sem sessão  ─▶ reply "needs_auth" + shortlink (/c/XXX → 302 → Cognito /login)
                    │                       │
                    │                       ▼
                    │                  usuário loga ─▶ GET /oauth2/callback ─▶ troca code → tokens
                    │                                       │
                    │                                       ▼
                    │                                 webhook n8n (oauth_success)
                    │
                    └─ com sessão ─▶ classifier (off-topic | meta_help | on_topic)
                                          │
                                          ▼
                                       NLU (gpt-4o-mini) → {intent, entities}
                                          │
                                          ▼
                                       Router de flows ─▶ Clicksign REST v3
                                                              (Bearer auto-renovado)
                                          │
                                          ▼
                                    MessageResponse (texto + interactive + flow_state)
```

## Componentes

- `cmd/server` — bootstrap (config, OAuth client, classifier, NLU, router de flows, HTTP server).
- `internal/config` — Viper + `.env` + validação dos campos obrigatórios.
- `internal/logging` — slog JSON + hash do telefone (não logamos PII em claro).
- `internal/session` — `Store` interface + `MemoryStore` (sessão, pending OAuth, DCR singleton — esse último só usado no modo `mcp` legado).
- `internal/oauth` — PKCE, state HMAC, **modo direto** (cliente confidencial pré-registrado no Cognito) e modo `mcp` (DCR via fachada, mantido para rollback).
- `internal/classifier` — gate barato (gpt-4o-mini) que classifica a mensagem como `on_topic`, `meta_help` ou `off_topic` antes de gastar tokens no NLU.
- `internal/nlu` — extrator estruturado (gpt-4o-mini) que devolve `{intent, entities}` em JSON. Prompt em `internal/nlu/prompts/nlu.md`.
- `internal/clicksign` — cliente REST v3 com injeção de Bearer, refresh-on-401, header `X-Account-Key`, parsing de erros JSON:API e `FileFetcher` (download do PDF do usuário com proteção SSRF).
- `internal/flow` — máquina de estados por intent (`select_account`, `list_templates`, `list_envelopes`, `envelope_status`, `create_envelope_pdf`, `create_envelope_tmpl`, `add_signer`, `cancel_envelope`) + router central.
- `internal/api` — handlers HTTP (`/api/messages`, `/oauth2/callback`, `/c/{token}`, `/healthz`), middleware (Bearer, request-id, access log, recover), idempotência por `message_id`, `FlowPipeline` (orquestra classifier → NLU → router).
- `internal/llm` — apenas templates de reply em pt-BR (`AuthRequired`, `SessionExpired`, `OffTopic`, `Capabilities`, etc.). Sem chamadas LLM aqui — a inteligência mora em `classifier` e `nlu`.
- `internal/conv` — sentinela `ErrSessionExpired` + tipo `Attachment` (quebra ciclo entre `api` e `flow`).
- `internal/n8n` — webhook outbound (`oauth_success`, `session_expired`).

## Setup

```bash
cp .env.example .env
# preencha:
#   API_STATIC_TOKEN       — token Bearer que o n8n envia
#   PUBLIC_BASE_URL        — https público (ngrok em dev, host real em prod)
#   STATE_HMAC_SECRET      — ≥ 16 chars, qualquer string aleatória
#   OPENAI_API_KEY
#   OAUTH_CLIENT_ID        — client confidencial Cognito (staging ou prod)
#   OAUTH_CLIENT_SECRET    — idem
make run            # ou: go run ./cmd/server
```

Para HTTPS público durante demos (necessário para o `redirect_uri` aceito pelo Cognito):

```bash
ngrok http 8080
# ajuste PUBLIC_BASE_URL para o https do ngrok
# e cadastre {PUBLIC_BASE_URL}/oauth2/callback como redirect_uri no painel do Cognito
```

### Variáveis de ambiente

| Variável | Default | Obrigatória |
|---|---|---|
| `API_STATIC_TOKEN` | — | sim |
| `PUBLIC_BASE_URL` | — | sim |
| `STATE_HMAC_SECRET` | — | sim (≥ 16 chars) |
| `OPENAI_API_KEY` | — | sim |
| `OAUTH_MODE` | `direct` | — |
| `OAUTH_AUTHORIZE_URL` | `https://oauth2.clicksign.dev/login` | sim em `direct` |
| `OAUTH_TOKEN_URL` | `https://oauth2.clicksign.dev/oauth2/token` | sim em `direct` |
| `OAUTH_CLIENT_ID` | — | sim em `direct` |
| `OAUTH_CLIENT_SECRET` | — | sim em `direct` |
| `OAUTH_SCOPES` | `openid email phone` | — |
| `OAUTH_REDIRECT_PATH` | `/oauth2/callback` | — |
| `CLICKSIGN_API_BASE_URL` | `https://4.clicksign.dev/api/v3` (staging) | flip para `https://app.clicksign.com/api/v3` em prod |
| `CLICKSIGN_API_TIMEOUT_SECONDS` | `20` | — |
| `NLU_MODEL` | `gpt-4o-mini` | — |
| `NLU_TIMEOUT_SECONDS` | `15` | — |
| `CLASSIFIER_ENABLED` | `true` | — |
| `CLASSIFIER_MODEL` | `gpt-4o-mini` | — |
| `N8N_WEBHOOK_URL` / `N8N_WEBHOOK_TOKEN` | — | opcional (proactive replies) |
| `MCP_SERVER_BASE_URL` / `MCP_ENDPOINT_PATH` | `https://mcp-api-tavola-v3-6.clicksign.dev` / `/mcp/oauth2` | só em `OAUTH_MODE=mcp` (legado, rollback) |

## Endpoints

| Método | Caminho | Auth | Descrição |
|---|---|---|---|
| `POST` | `/api/messages` | Bearer estático | recebe mensagens do n8n (texto livre ou `interactive_reply`) |
| `GET`  | `/oauth2/callback` | — | troca o `code` por tokens e renderiza a página HTML de sucesso |
| `GET`  | `/c/{token}` | — | shortlink → 302 para `authorize_url` real (expira em 5 min) |
| `GET`  | `/healthz` | — | health check |

## Contrato `POST /api/messages`

Request (mensagem normal):

```json
{
  "phone_number": "+5511999999999",
  "message": "liste meus templates",
  "attachments": [
    {"url": "https://...", "mime_type": "application/pdf", "filename": "x.pdf"}
  ],
  "message_id": "wamid.xxx"
}
```

Request (resposta a uma lista ou botão):

```json
{
  "phone_number": "+5511999999999",
  "interactive_reply": {"list_item_id": "<id-do-item>"},
  "message_id": "wamid.click.xxx"
}
```

Response (`needs_auth`):

```json
{
  "status": "needs_auth",
  "reply": "Olá! Para eu poder te ajudar… 👉 https://host/c/QKGP4BECQDPMU",
  "authorize_url": "https://oauth2.clicksign.dev/login?response_type=code&client_id=…"
}
```

Response (`ok` com lista interativa):

```json
{
  "status": "ok",
  "reply": "Você tem mais de uma conta Clicksign. Qual quer usar agora?",
  "interactive": {
    "type": "list",
    "header": "Escolha a conta",
    "items": [
      {"id": "<uuid>", "title": "Carlos Mikael", "description": "<uuid>"},
      {"id": "<uuid>", "title": "name LTDA", "description": "<uuid>"}
    ]
  },
  "flow_state": {"flow_id": "select_account", "step": "awaiting_choice"}
}
```

O shape completo (incluindo `interactive` do tipo `buttons`, `trace` e `error`) está documentado em [`docs/N8N_INTEGRATION_CONTRACT.md`](./docs/N8N_INTEGRATION_CONTRACT.md). Exemplos vivos com `curl`/Postman em [`docs/POSTMAN.md`](./docs/POSTMAN.md).

## Intents suportadas

| Intent | O que faz | Endpoint Clicksign |
|---|---|---|
| `list_templates` | Lista templates da conta selecionada | `GET /templates` |
| `list_envelopes` | Lista envelopes (com filtro opcional por status) | `GET /envelopes` |
| `envelope_status` | Detalha um envelope (id ou fuzzy match por nome) | `GET /envelopes/{id}` |
| `create_envelope_pdf` | Upload de PDF + criação em massa via `envelope_bulk_creations` | `POST /envelope_bulk_creations` |
| `create_envelope_tmpl` | Envelope a partir de template + signatários | `POST /envelope_bulk_creations` |
| `add_signer` | Adiciona signatário a envelope existente | `POST /envelopes/{id}/signers` |
| `cancel_envelope` | Exclui envelope em **draft** (Clicksign não cancela em andamento via API) | `DELETE /envelopes/{id}` |
| `select_account` | Troca a conta Clicksign ativa quando o usuário tem múltiplas | (somente local) |

Mensagens fora de domínio são bloqueadas pelo classifier (não chegam ao NLU). Saudações e perguntas "o que você faz?" caem em `meta_help` e respondem com `llm.Capabilities()` estático.

## Comandos

```bash
make run             # roda o servidor
make dev             # roda com Air (hot reload)
make build           # bin/whatsapp-mcp
make test            # testes unitários
make lint            # go vet + gofmt
make docker          # docker build
```

## Status

| Fase | Entrega |
|---|---|
| Fase 0 | Scaffolding (config, logging, chi, /healthz, Dockerfile, Makefile) ✓ |
| Fase 1 | Session store em memória + bearer estático ✓ |
| Fase 2 | OAuth2 client (PKCE + state HMAC + DCR + authorize + token + refresh) + callback + shortlink + `needs_auth` ✓ |
| Fase 3 | Pipeline Option B — NLU + flows de leitura (`list_templates`, `select_account`) ✓ |
| Fase 4 | Flows de envelope (`list_envelopes`, `envelope_status`, `create_envelope_pdf`, `create_envelope_tmpl`, `add_signer`, `cancel_envelope`) ✓ |
| Fase 5 | OAuth direto (Cognito sem MCP), remoção do pipeline legacy, erros JSON:API humanizados, Postman + README atualizados ✓ |
| Fase 6 (opcional) | Backend DynamoDB |
| Fase 7 (opcional) | Observabilidade avançada / deploy |
