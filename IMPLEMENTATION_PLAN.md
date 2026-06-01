# Plano de Implementação — `whatsapp-mcp` (MCP Client + LLM Bridge)

> Serviço HTTP em Go que recebe mensagens do WhatsApp via n8n, autentica o usuário no MCP server da Clicksign (OAuth2 + DCR) e responde com uma LLM (OpenAI) usando as tools do MCP server.

---

## 1. Objetivo

Construir um endpoint HTTP que:

1. Recebe mensagens encaminhadas pelo n8n (que está integrado ao WhatsApp) autenticadas por um Bearer token estático.
2. Identifica o usuário pelo `phone_number`.
3. Verifica se o usuário já fez login no MCP server da Clicksign via OAuth2:
   - **Não logado** → devolve a `authorize_url` para o n8n responder no WhatsApp.
   - **Logado** → atua como **MCP client**, conecta a OpenAI ao MCP server e responde com tool-calling.
4. Expõe `/oauth2/callback` para receber a redireção do MCP server, trocar `code` por tokens, salvar por `phone_number`, mostrar página de sucesso e (opcional) avisar o n8n via webhook.

---

## 2. Arquitetura

```text
WhatsApp user
   │
   ▼
n8n (WhatsApp trigger)
   │  POST /api/messages
   │  Authorization: Bearer <API_STATIC_TOKEN>
   │  { phone_number, message, attachments[] }
   ▼
whatsapp-mcp ──► session.Store.Get(phone_number)
   │
   ├── NÃO autenticado ───► gera authorize_url (PKCE + state HMAC)
   │                       persiste pkce_verifier por state
   │                       responde { status: "needs_auth", authorize_url }
   │
   └── autenticado ──► MCPClient (streamable HTTP + Bearer)
                        │  tools/list (cacheado por TTL)
                        ▼
                       OpenAI Chat Completions com tools=[…]
                        │  loop:
                        │   - model devolve tool_call
                        │   - executa via MCP tools/call
                        │   - 401 → refresh_token → retry uma vez
                        │   - feed result → model
                        ▼
                       responde { status: "ok", reply, tool_calls[] }


GET /oauth2/callback?code=...&state=...
   ▼
valida state HMAC → recupera phone_number + code_verifier
   ▼
POST /oauth2/token (authorization_code + PKCE)
   ▼
salva { access_token, refresh_token, expires_at } por phone_number
   ▼
renderiza success.html  +  POST webhook do n8n ("conectado")
```

---

## 3. Estrutura de pastas

```text
whatsapp-mcp/
├── cmd/server/
│   └── main.go                    # bootstrap, DI, HTTP server
├── internal/
│   ├── api/
│   │   ├── middleware.go          # bearer estático + request logging + recover
│   │   ├── messages_handler.go    # POST /api/messages
│   │   ├── oauth_handler.go       # GET /oauth2/callback
│   │   ├── shortlink_handler.go   # GET /c/{link_token} (302 → authorize_url)
│   │   ├── health_handler.go      # GET /healthz
│   │   └── errors.go              # response helpers
│   ├── oauth/
│   │   ├── client.go              # discovery, DCR, authorize URL, token, refresh
│   │   ├── pkce.go                # code_verifier/code_challenge S256
│   │   ├── state.go               # state = base64(HMAC(phone|nonce|exp))
│   │   └── types.go               # TokenResponse, ClientRegistration etc.
│   ├── session/
│   │   ├── store.go               # interface Store (Get/Put/Delete por phone)
│   │   ├── memory.go              # impl em memória (MVP)
│   │   ├── dynamodb.go            # impl DynamoDB (produção)
│   │   └── pending.go             # store temporário para PKCE/state
│   ├── mcpclient/
│   │   ├── client.go              # wraps mark3labs/mcp-go client
│   │   ├── auth.go                # injeta Bearer + refresh on 401
│   │   └── tools.go               # converte mcp.Tool → openai.Tool
│   ├── llm/
│   │   ├── openai.go              # cliente OpenAI
│   │   ├── loop.go                # loop tool-calling
│   │   ├── prompts.go             # system prompt
│   │   └── replies.go             # textos amigáveis para o usuário (templates estáticos)
│   ├── n8n/
│   │   └── webhook.go             # POST de volta ao n8n
│   ├── config/
│   │   └── config.go              # Viper + envs
│   └── logging/
│       └── logger.go              # slog setup
├── web/
│   └── success.html               # página pós-OAuth
├── test/
│   └── integration/               # e2e simulado
├── scripts/
│   └── dev.sh
├── infra/
│   └── (terraform futuro)
├── .env.example
├── .gitignore
├── .editorconfig
├── .air.server.toml
├── Dockerfile
├── Dockerfile.dev
├── docker-compose.yml
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

---

## 4. Stack & dependências

| Camada | Lib |
|---|---|
| HTTP router | `github.com/go-chi/chi/v5` |
| MCP client | `github.com/mark3labs/mcp-go/client` |
| OpenAI | `github.com/openai/openai-go` |
| Config | `github.com/spf13/viper` + `github.com/joho/godotenv` |
| Logging | `log/slog` (stdlib) |
| Storage | `github.com/aws/aws-sdk-go-v2/service/dynamodb` |
| Tests | `github.com/stretchr/testify` |
| Hot reload (dev) | `github.com/air-verse/air` |

Go 1.23+ (alinhar com a versão do Dockerfile).

---

## 5. Contratos de API

### 5.1 `POST /api/messages`

> **Princípio**: o n8n é um repassador "burro". O campo `reply` deve sempre vir **pronto para ser entregue no WhatsApp**, em pt-BR, com tom natural. O `status` e demais campos são apenas metadados para o n8n decidir o que fazer além de mandar o texto (ex.: registrar log, abrir flow alternativo). Em qualquer cenário (autenticação pendente, sucesso, erro) o n8n deve poder pegar `reply` cegamente e enviar.

Headers:

```http
Authorization: Bearer <API_STATIC_TOKEN>
Content-Type: application/json
```

Request body:

```json
{
  "phone_number": "+5511999999999",
  "message": "Quero enviar um envelope para joao@x.com",
  "attachments": [
    {
      "url": "https://...",
      "mime_type": "application/pdf",
      "filename": "contrato.pdf"
    }
  ],
  "conversation_id": "opcional",
  "message_id": "wamid.xxx"
}
```

#### 5.1.1 Schema da resposta

Todas as respostas compartilham a forma:

```json
{
  "status": "needs_auth | ok | error",
  "reply": "texto amigável em pt-BR, pronto para enviar no WhatsApp",
  "authorize_url": "string (apenas quando status=needs_auth)",
  "tool_calls": [],
  "error": {"code": "string", "details": "string"}
}
```

#### 5.1.2 Resposta — usuário **não autenticado** (`needs_auth`)

A `reply` deve ser uma mensagem completa e cordial, contendo um **link curto no nosso próprio domínio** (ver §5.1.6 — short link). O `authorize_url` separado contém a URL longa de fato do `/oauth2/authorize` do MCP server (caso o n8n queira montar botão, template HSM, log, etc.). Ambos resolvem para o mesmo fluxo.

```json
{
  "status": "needs_auth",
  "reply": "Olá! Para eu poder te ajudar com seus envelopes da Clicksign, primeiro preciso que você conecte sua conta. É rápido:\n\n1. Toque no link abaixo\n2. Faça login na Clicksign\n3. Volte aqui no WhatsApp\n\n👉 https://seu-host/c/AB12CD34EF\n\nO link expira em 5 minutos e é só seu — evite compartilhar.",
  "authorize_url": "https://mcp-api-tavola-v3-6.clicksign.dev/oauth2/authorize?client_id=...&state=...&code_challenge=...&redirect_uri=https%3A%2F%2Fseu-host%2Foauth2%2Fcallback&scope=openid+email+phone"
}
```

> **Por que o link da `reply` é diferente do `authorize_url`?**
> O `authorize_url` é a URL real, longa, no domínio do MCP server da Clicksign. Para o WhatsApp, criamos um **short link no nosso domínio** (`/c/{link_token}`) que faz 302 para esse `authorize_url`. Isso deixa a mensagem mais curta, mais bonita e dá mais confiança (domínio do nosso serviço). Detalhes em §5.1.6.

> **Sobre "expira em 5 minutos"**: o que expira é o `Pending` (state + PKCE verifier) que mantemos com TTL=`PKCE_TTL_SECONDS`. Mesmo que o usuário logue depois disso, o callback rejeita por não encontrar o registro. Então "expira em 5 min" é factualmente verdadeiro.

> **Sobre "é só seu / não compartilhar"**: é framing de segurança honesto — o `state` é HMAC-bound ao `phone_number`, mas o link em si não autentica quem clica. Se outra pessoa receber e logar com a conta Clicksign dela, essa conta fica associada ao telefone original. O TTL curto é a defesa real; o aviso no texto reforça a boa prática.

Variações da `reply` (escolha por contexto, ver §5.1.5):

| Situação | Texto sugerido |
|---|---|
| Primeira interação | "Olá! Para eu te ajudar… conecte sua conta: <short_url>" |
| Sessão expirou | "Sua sessão com a Clicksign expirou. Conecte novamente em: <short_url>" |
| Refresh falhou | "Precisei desconectar sua conta por segurança. Para continuar: <short_url>" |

#### 5.1.3 Resposta — **sucesso** (`ok`)

```json
{
  "status": "ok",
  "reply": "Pronto! Enviei o envelope para joao@x.com. ID: env_123. Assim que ele assinar, eu te aviso.",
  "tool_calls": [
    {"name": "quick_send_envelope", "ok": true}
  ]
}
```

#### 5.1.4 Resposta — **erro** (`error`)

`reply` ainda precisa ser amigável (sem stacktrace, sem nomes de tool). `error` é para o n8n logar.

```json
{
  "status": "error",
  "reply": "Tive um problema para falar com a Clicksign agora. Você pode tentar de novo em alguns segundos?",
  "error": {"code": "UPSTREAM_TIMEOUT", "details": "mcp tools/call timeout after 30s"}
}
```

Códigos de erro previstos:

| `error.code` | Quando | `reply` sugerida |
|---|---|---|
| `UPSTREAM_TIMEOUT` | Timeout no MCP server ou OpenAI | "Tive um problema temporário, pode tentar de novo?" |
| `LLM_FAILURE` | OpenAI retornou erro | "Não consegui interpretar agora, tente reformular." |
| `TOOL_FAILURE` | Tool MCP retornou erro de negócio | mensagem específica do erro (ex.: "Esse template não foi encontrado.") |
| `MAX_ITERATIONS` | Loop tool-calling estourou | "Acho que estamos andando em círculos, pode resumir o que precisa?" |
| `INVALID_INPUT` | Payload inválido do n8n | "Não recebi o número corretamente, tente de novo." |

#### 5.1.5 Geração da `reply`

- **Caminhos sem LLM** (`needs_auth`, validação, `MAX_ITERATIONS`, `UPSTREAM_TIMEOUT`): texto vem de **templates estáticos** em `internal/llm/replies.go` (ou similar). Sem chamada à OpenAI — barato e previsível.
- **Caminho `ok`**: texto vem da própria resposta final do LLM (o system prompt instrui linguagem amigável em pt-BR).
- **Caminho `error` de tool**: se a tool MCP retornar mensagem útil, repassar; senão, template estático.

#### 5.1.6 Short link de autorização — `GET /c/{link_token}`

Para evitar despejar uma URL gigante (300+ chars) no WhatsApp, criamos um **redirect curto no nosso domínio** que aponta para a URL real do `/oauth2/authorize` do MCP server.

Fluxo:

1. Ao gerar `needs_auth`, criamos:
   - `state` = HMAC(phone|nonce|exp) — usado pelo OAuth
   - `link_token` = 10–16 chars base32 aleatórios — usado só na URL curta
   - `Pending` no store contendo `{state, link_token, phone, code_verifier, exp}`
2. `authorize_url` = URL real, longa, com `state`/`client_id`/`code_challenge`/`redirect_uri`/`scope`.
3. `reply` contém `https://{PUBLIC_BASE_URL}/c/{link_token}`.
4. `GET /c/{link_token}`:
   - busca `Pending` por `link_token`
   - se não achar / expirado: HTML amigável "Link expirado, peça um novo no WhatsApp"
   - se achar: `302 Found` com `Location: {authorize_url}` e header `Cache-Control: no-store`
   - **não invalida** o `Pending` aqui (o usuário pode recarregar a página de login do Cognito); a invalidação acontece no `/oauth2/callback` após troca bem-sucedida do `code`

Índice secundário no store: `pk = "linktoken#<link_token>"` → mesmo `Pending`. Pode ser uma segunda escrita (duplicada) ou um GSI no DynamoDB.

**Alternativa simples (Opção A)**: pular o short link e fazer `reply` conter diretamente o `authorize_url` longo. Funciona, mas a UX no WhatsApp fica ruim. Use só se não houver tempo de implementar o redirect.

### 5.2 `GET /oauth2/callback`

Query: `code`, `state` (obrigatórios). Retorna `text/html` com a página de sucesso, status 200. Em erro, página HTML amigável com status 400/500.

**Efeito colateral**: ao concluir com sucesso, dispara `n8n.Notify(...)` (ver §5.4) para o WhatsApp do usuário receber confirmação. Assim o usuário não precisa nem voltar manualmente — recebe a mensagem no chat dizendo que está pronto.

### 5.3 Webhook de saída para o n8n — `POST {N8N_WEBHOOK_URL}`

Sempre que o nosso serviço precisar **iniciar** uma mensagem ao usuário (fora do ciclo request/response do `/api/messages`), chamamos esse webhook. O n8n pega `reply` e envia direto no WhatsApp para `phone_number`.

Headers:

```http
Authorization: Bearer <N8N_WEBHOOK_TOKEN>
Content-Type: application/json
```

Body:

```json
{
  "event": "oauth_success | oauth_failed | session_expired",
  "phone_number": "+5511999999999",
  "reply": "Pronto! Sua conta Clicksign está conectada. É só me dizer o que precisa: enviar um envelope, listar templates, etc.",
  "metadata": {"account_key": "opcional"}
}
```

Mensagens padrão por evento:

| `event` | `reply` |
|---|---|
| `oauth_success` | "Pronto! Sua conta Clicksign está conectada ✅\n\nÉ só me dizer o que precisa — por exemplo: \"liste meus templates\" ou \"envie um envelope para fulano@x.com\"." |
| `oauth_failed` | "Não consegui concluir sua conexão com a Clicksign. Pode tentar novamente? Se persistir, me avise." |
| `session_expired` | "Sua sessão com a Clicksign expirou. Mande qualquer mensagem aqui e eu te envio um novo link de conexão." |

Falhas no webhook do n8n não devem quebrar o callback HTTP (timeout curto, log de warning, página de sucesso renderiza normalmente).

### 5.4 `GET /healthz`

```json
{"status": "ok", "version": "0.1.0"}
```

---

## 6. Modelo de dados (session store)

```go
type Session struct {
    PhoneNumber  string    // chave
    AccessToken  string
    RefreshToken string
    ExpiresAt    time.Time
    AccountKey   string    // após select_account, se aplicável
    UpdatedAt    time.Time
}

type Pending struct {
    State         string    // chave primária
    LinkToken     string    // chave secundária (usada pelo short link /c/{token})
    AuthorizeURL  string    // URL real do /oauth2/authorize já montada
    PhoneNumber   string
    CodeVerifier  string
    Nonce         string
    ExpiresAt     time.Time // TTL ~5min
}
```

Chaves no DynamoDB (tabela única, GSI opcional):
- `pk = "session#<phone>"` → Session
- `pk = "pending#<state>"` → Pending (com TTL)
- `pk = "linktoken#<link_token>"` → mesmo Pending (espelho ou GSI) — usado pelo short link
- `pk = "oauth_client"` → ClientRegistration (singleton do DCR)

---

## 7. Variáveis de ambiente

```bash
# Endpoint público
API_STATIC_TOKEN=                  # bearer que o n8n envia
PUBLIC_BASE_URL=https://seu-host   # usado para redirect_uri

# MCP Server (Clicksign)
MCP_SERVER_BASE_URL=https://mcp-api-tavola-v3-6.clicksign.dev
MCP_ENDPOINT_PATH=/mcp/oauth2
MCP_OAUTH_SCOPES=openid email phone

# OAuth2 / DCR
OAUTH_REDIRECT_PATH=/oauth2/callback
STATE_HMAC_SECRET=                 # >=32 bytes aleatórios
PKCE_TTL_SECONDS=300

# OpenAI
OPENAI_API_KEY=
OPENAI_MODEL=gpt-4o-mini
OPENAI_MAX_TOOL_ITERATIONS=8
OPENAI_TIMEOUT_SECONDS=60

# n8n callback (opcional)
N8N_WEBHOOK_URL=
N8N_WEBHOOK_TOKEN=

# Storage
SESSION_BACKEND=memory             # memory | dynamodb
DYNAMODB_TABLE_NAME=whatsapp_mcp_sessions
DYNAMODB_ENDPOINT=                 # vazio = AWS real
AWS_REGION=us-east-1

# HTTP
PORT=8080
LOG_LEVEL=info
```

---

## 8. Fases de implementação

### Fase 0 — Scaffolding (0.5 dia)

- [ ] `go mod init github.com/<owner>/whatsapp-mcp`
- [ ] estrutura de pastas
- [ ] `Makefile` (run, test, lint, build, docker)
- [ ] `.air.server.toml`, `Dockerfile`, `Dockerfile.dev`, `docker-compose.yml`
- [ ] `internal/config/config.go` com Viper
- [ ] `internal/logging/logger.go` com slog
- [ ] `cmd/server/main.go` levantando chi + `/healthz`
- [ ] `.env.example`, `.gitignore`, `.editorconfig`
- [ ] README inicial

**Critério**: `go run ./cmd/server` sobe na porta 8080 e `GET /healthz` responde 200.

### Fase 1 — Sessions & bearer estático (0.5 dia)

- [ ] `internal/session/store.go` interface (`Get/Put/Delete`, `PutPending/GetPending/GetPendingByLinkToken/DeletePending`)
- [ ] `internal/session/memory.go` com mutex (indexação dupla por `state` e `link_token`)
- [ ] `internal/api/middleware.go` bearer estático com `subtle.ConstantTimeCompare`
- [ ] `internal/api/messages_handler.go` stub que só ecoa request validada
- [ ] testes unitários do middleware e do memory store

**Critério**: `POST /api/messages` rejeita sem bearer, aceita com bearer e ecoa o payload.

### Fase 2 — OAuth2 client + DCR (1 dia)

- [ ] `internal/oauth/pkce.go` (`code_verifier` 32 bytes, `code_challenge` S256)
- [ ] `internal/oauth/state.go` HMAC-SHA256 com `phone|nonce|exp`
- [ ] `internal/oauth/client.go`:
  - `DiscoverAuthorizationServer(ctx) (*Metadata, error)` — `GET /.well-known/oauth-authorization-server`
  - `RegisterDynamic(ctx, redirectURIs) (*ClientRegistration, error)` — `POST /oauth2/register` (com `token_endpoint_auth_method=none`)
  - `BuildAuthorizeURL(state, codeChallenge, scopes) string`
  - `ExchangeCode(ctx, code, codeVerifier) (*TokenResponse, error)`
  - `RefreshToken(ctx, refreshToken) (*TokenResponse, error)`
- [ ] bootstrap em `main.go`: discovery + DCR uma vez, persiste `client_id` em session store (pk `oauth_client`)
- [ ] `internal/api/oauth_handler.go` `GET /oauth2/callback`:
  - valida state HMAC e expiração
  - recupera `Pending` (phone + verifier)
  - troca code → tokens
  - persiste `Session` por phone
  - apaga `Pending` (invalida também o `link_token`)
  - renderiza `web/success.html`
  - opcional: dispara `n8n.Webhook(phone, "oauth_success")`
- [ ] `internal/api/shortlink_handler.go` `GET /c/{link_token}`:
  - busca `Pending` por `link_token`
  - se inexistente / expirado → HTML "link expirado"
  - senão → `302 Found` para `Pending.AuthorizeURL` com `Cache-Control: no-store`
- [ ] handler de `/api/messages` agora:
  - se `Session` ausente: gera `state` + `link_token`, monta `authorize_url`, persiste `Pending{state, link_token, authorize_url, ...}`, responde `needs_auth` com `reply` apontando para `{PUBLIC_BASE_URL}/c/{link_token}` e `authorize_url` com a URL longa real
- [ ] `web/success.html` + `web/link_expired.html`
- [ ] testes unitários de PKCE, state e short link

**Critério**: usuário desconhecido recebe `authorize_url`, login no Cognito funciona, callback persiste tokens, segunda chamada não pede mais auth.

### Fase 3 — MCP Client (1 dia)

- [ ] `internal/mcpclient/client.go`:
  - construtor com `baseURL`, `endpointPath`, `Session` provider
  - usa `mcp-go/client` com transporte streamable HTTP
  - injeta `Authorization: Bearer <access_token>` por request
- [ ] `internal/mcpclient/auth.go`: middleware que em 401 chama `oauth.RefreshToken`, atualiza session e re-tenta uma vez
- [ ] `internal/mcpclient/tools.go`:
  - `ListTools(ctx) ([]Tool, error)` com cache TTL (~5 min)
  - `CallTool(ctx, name, args) (mcp.CallToolResult, error)`
  - `ToOpenAITools(mcpTools) []openai.Tool` (conversão de schema)
- [ ] testes com mock HTTP server simulando MCP

**Critério**: chamada explícita `mcpclient.ListTools` retorna as tools reais; `CallTool` executa uma tool simples (`list_templates`) e retorna resultado.

### Fase 4 — LLM loop com tool-calling (1 dia)

- [ ] `internal/llm/prompts.go`: system prompt com regras (idioma pt-BR, contexto Clicksign, comportamento conciso)
- [ ] `internal/llm/openai.go`: cliente
- [ ] `internal/llm/loop.go`:
  - recebe `phone, message, attachments[], tools[]`
  - chama Chat Completions com `tools=[...]` e `tool_choice=auto`
  - enquanto resposta vier com `tool_calls`:
    - executa via `MCPClient.CallTool`
    - retorna resultado como `role=tool` message
    - decrementa contador de iterações (`OPENAI_MAX_TOOL_ITERATIONS`)
  - encerra quando vier `finish_reason=stop` ou estourar limite
- [ ] tratamento de attachments (passar URLs/metadata no prompt; opcional: anexar como `image_url` se for imagem)
- [ ] integração no `messages_handler.go` para o caminho autenticado

**Critério**: enviar "lista os templates" pelo endpoint resulta em chamada real à tool `list_templates` e resposta natural no `reply`.

### Fase 5 — n8n webhook + polimento UX (0.5 dia)

- [ ] `internal/llm/replies.go`: catálogo de textos amigáveis em pt-BR (`AuthRequired(url)`, `OAuthSuccess()`, `OAuthFailed()`, `SessionExpired()`, `UpstreamTimeout()`, `MaxIterations()`, `InvalidInput()`) — mesmas frases referenciadas em §5.1.2 / §5.1.4 / §5.3.
- [ ] `internal/api/messages_handler.go` usa `replies.AuthRequired(authorizeURL)` no caminho `needs_auth` (sempre devolvendo `reply` + `authorize_url` juntos).
- [ ] `internal/n8n/webhook.go`: `Notify(ctx, phone, event, reply, metadata)` POSTs com bearer e timeout curto (3-5s), retry simples, falha silenciosa (warn no log) — payload conforme §5.3.
- [ ] disparo no callback OAuth de sucesso com `event="oauth_success"` e `reply=replies.OAuthSuccess()`.
- [ ] disparo no refresh com falha permanente: marca sessão como expirada e dispara `event="session_expired"`.
- [ ] mensagens amigáveis para erros comuns (refresh falhou, tool erro, timeout LLM) vindas do `replies.go`.
- [ ] idempotência: cache em memória de `message_id` por 60s.

**Critério**: ao concluir OAuth, n8n recebe POST `{event:"oauth_success", phone_number, reply:"Pronto! Sua conta Clicksign está conectada ✅..."}` e o usuário recebe essa mensagem no WhatsApp.

### Fase 6 — DynamoDB backend (0.5 dia, opcional para o hackathon)

- [ ] `internal/session/dynamodb.go` implementando `Store`
- [ ] tabela com TTL em `Pending`
- [ ] feature flag `SESSION_BACKEND`
- [ ] testes de integração com DynamoDB Local (docker-compose)

**Critério**: rodar com `SESSION_BACKEND=dynamodb` e dynamodb-local; fluxo end-to-end persiste através de restart.

### Fase 7 — Observabilidade & deploy (0.5 dia)

- [ ] structured logging com `phone_number` redatado (hash) e `trace_id`
- [ ] métricas básicas (request count, latency, tool_call duration) — pode ser stdout no MVP
- [ ] Dockerfile multi-stage final
- [ ] README com instruções de uso
- [ ] (opcional) Terraform mínimo

---

## 9. Loop OpenAI ↔ MCP (pseudo-código)

```go
func (l *Loop) Run(ctx context.Context, req Request) (Response, error) {
    tools, err := l.mcp.ListTools(ctx, req.Phone)
    if err != nil { return Response{}, err }
    oaTools := mcpclient.ToOpenAITools(tools)

    messages := []openai.Message{
        {Role: "system", Content: l.systemPrompt},
        {Role: "user",   Content: buildUserContent(req)},
    }
    var toolCalls []ToolCallTrace

    for i := 0; i < l.maxIterations; i++ {
        resp, err := l.oai.Chat(ctx, openai.ChatRequest{
            Model: l.model, Messages: messages, Tools: oaTools, ToolChoice: "auto",
        })
        if err != nil { return Response{}, err }

        choice := resp.Choices[0]
        messages = append(messages, choice.Message)

        if len(choice.Message.ToolCalls) == 0 {
            return Response{Reply: choice.Message.Content, ToolCalls: toolCalls}, nil
        }

        for _, tc := range choice.Message.ToolCalls {
            result, err := l.mcp.CallTool(ctx, req.Phone, tc.Function.Name, tc.Function.Arguments)
            toolCalls = append(toolCalls, ToolCallTrace{Name: tc.Function.Name, OK: err == nil})
            messages = append(messages, openai.Message{
                Role: "tool", ToolCallID: tc.ID,
                Content: stringifyToolResult(result, err),
            })
        }
    }
    return Response{}, ErrMaxIterations
}
```

---

## 10. OAuth2 — detalhes do handshake

> Mapa rápido das URLs envolvidas:
> - `authorize_url` (longa, MCP server): `https://mcp-api-tavola-v3-6.clicksign.dev/oauth2/authorize?...`
> - **short link (curto, nosso)**: `https://{PUBLIC_BASE_URL}/c/{link_token}` → 302 → `authorize_url`
> - **redirect_uri (callback, nosso)**: `https://{PUBLIC_BASE_URL}/oauth2/callback`
> O usuário no WhatsApp clica no short link; o MCP server volta para o redirect_uri.

1. **Bootstrap (1x na vida do serviço)**:
   - `GET {MCP_SERVER_BASE_URL}/.well-known/oauth-authorization-server`
   - `POST {issuer}/oauth2/register`
     ```json
     {
       "redirect_uris": ["https://<PUBLIC_BASE_URL>/oauth2/callback"],
       "token_endpoint_auth_method": "none",
       "grant_types": ["authorization_code", "refresh_token"],
       "response_types": ["code"],
       "scope": "openid email phone"
     }
     ```
   - persiste `client_id` em `oauth_client` no session store.

2. **Início do login** (handler `/api/messages` quando não há sessão):
   - gera `code_verifier` (32 bytes b64url) + `code_challenge` (SHA256)
   - gera `nonce` (16 bytes) e `state = HMAC(phone|nonce|exp)`
   - gera `link_token` (10–16 chars base32 aleatórios)
   - monta `authorize_url`:
     ```
     {issuer}/oauth2/authorize?
       response_type=code&
       client_id={dcr_client_id}&
       redirect_uri={PUBLIC_BASE_URL}/oauth2/callback&
       state={state}&
       code_challenge={code_challenge}&
       code_challenge_method=S256&
       scope=openid email phone
     ```
   - `PutPending{state, link_token, authorize_url, phone, code_verifier, nonce, exp=now+5min}`
   - responde `needs_auth` com:
     - `reply` contendo `https://{PUBLIC_BASE_URL}/c/{link_token}` (curto, pro WhatsApp)
     - `authorize_url` com a URL longa acima (para uso programático pelo n8n)

3. **Callback** (`GET /oauth2/callback?code&state`):
   - valida HMAC + expiração do state
   - `pending = GetPending(state)` → recupera phone e code_verifier
   - `POST {issuer}/oauth2/token` com `grant_type=authorization_code`, `code`, `redirect_uri`, `client_id`, `code_verifier`
   - `PutSession(phone, access_token, refresh_token, expires_at)`
   - `DeletePending(state)`
   - render success page + webhook n8n

4. **Refresh** (no `mcpclient.auth.go` em 401):
   - `POST {issuer}/oauth2/token` com `grant_type=refresh_token`, `refresh_token`, `client_id`
   - atualiza session e re-tenta a request original uma vez
   - se refresh falhar → marca sessão expirada, devolve `needs_auth` na próxima chamada

---

## 11. Estratégia de testes

- **Unitários**: PKCE, state HMAC, conversão `mcp.Tool ↔ openai.Tool`, middleware bearer, session memory store.
- **Integração (mock)**: servidor HTTP fake simulando endpoints OAuth2 e MCP para validar o fluxo completo sem depender da Clicksign.
- **E2E manual** (hackathon): rodar contra `https://mcp-api-tavola-v3-6.clicksign.dev` em staging, usando ngrok ou similar para o callback público.

Comandos:

```bash
make test          # unitários
make test-int      # com DynamoDB Local
make run           # dev com Air
make build         # binário
make docker        # build da imagem
```

---

## 12. Riscos & mitigações

| Risco | Mitigação |
|---|---|
| DCR rejeitado / rate-limited | Persistir o `client_id` no primeiro boot; não re-registrar. Logar resposta completa em caso de erro. |
| `redirect_uri` divergente entre `/authorize` e `/token` | Centralizar em `config.RedirectURI()` e usar em todos os pontos. |
| Refresh token expira / invalida | Apagar sessão e voltar a `needs_auth` na próxima mensagem. |
| Tool-calling loop infinito | `OPENAI_MAX_TOOL_ITERATIONS` + timeout global do request. |
| Anexos privados do WhatsApp (Graph API) | Combinar com o n8n para devolver URLs já públicas ou base64 dataURL. |
| Múltiplas contas Clicksign (`select_account`) | Deixar a tool exposta ao LLM nos primeiros turnos; cachear `account_key` por phone. |
| Mensagens duplicadas pelo n8n | Cache curto (60s) de `message_id` para dedup. |
| Latência > 10s | Resposta assíncrona: ack imediato + segundo webhook ao n8n com a reply final (fase 2). |

---

## 13. Cronograma sugerido (hackathon)

| Dia | Entregas |
|---|---|
| D1 manhã | Fase 0 + Fase 1 |
| D1 tarde | Fase 2 (OAuth + DCR + callback + success page) |
| D2 manhã | Fase 3 (MCP client) |
| D2 tarde | Fase 4 (LLM loop) — primeira ponta a ponta funcionando |
| D3 manhã | Fase 5 (n8n webhook + UX) |
| D3 tarde | Fase 6/7 (DynamoDB se sobrar tempo + deploy + demo) |

---

## 14. Definição de pronto (MVP demo)

1. n8n manda mensagem de usuário novo → resposta com `status="needs_auth"`, `reply` amigável já contendo o link e `authorize_url` separado → n8n repassa o `reply` no WhatsApp e o usuário vê o convite com o link clicável.
2. Usuário clica, faz login no Cognito → vê página de sucesso → recebe mensagem no WhatsApp via webhook do n8n ("Pronto! Sua conta Clicksign está conectada ✅...").
3. Usuário pede "lista meus templates" → endpoint chama OpenAI → OpenAI invoca `list_templates` via MCP → `reply` natural em pt-BR com a lista formatada.
4. Usuário pede "envia o template X para joao@x.com" → endpoint executa `quick_send_envelope` via MCP → `reply` confirma com tom natural.
5. Após 1h (ou simulando expiração), refresh transparente mantém a sessão; se refresh falhar, próxima mensagem volta a `needs_auth` com `reply` amigável diferente ("Sua sessão expirou...").

---

## 15. Próximos passos (pós-hackathon)

- Persistência DynamoDB de produção + Terraform.
- Suporte a múltiplos MCP servers (registry de tools por usuário).
- Streaming de resposta para o n8n (chunks via SSE) reduzindo percepção de latência.
- Rate limiting por `phone_number`.
- Observabilidade: Datadog/OTel traces no loop tool-calling.
- Política de retenção / LGPD do conteúdo de mensagens em log.
