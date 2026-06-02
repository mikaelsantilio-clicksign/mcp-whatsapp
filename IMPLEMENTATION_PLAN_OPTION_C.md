# Plano de Implementação — Opção C: Login via MCP Prod + REST Direto

> Pivot mínimo e cirúrgico para destravar o hackathon. **Mantém** todo o fluxo OAuth2/DCR/PKCE existente apontando para o MCP server de **produção**, e **substitui** apenas a camada de execução de tools (`internal/mcpclient`) por chamadas REST diretas à API Clicksign (`https://app.clicksign.com/api/v3`). O `access_token` que o MCP facade já retorna É um JWT do Cognito válido, então não precisamos de `COGNITO_CLIENT_ID/SECRET`.

---

## 1. Motivação

### 1.1 Restrições do hackathon

- **Staging do MCP não tem integração WhatsApp** — bloqueia o demo end-to-end.
- **Produção do MCP tem um bug** que só está corrigido em uma feature branch que não pode ser deployada antes do fim do evento.
- **Não temos `COGNITO_CLIENT_ID`/`COGNITO_CLIENT_SECRET` de produção** — esses são secrets de servidor (lado MCP), não de cliente.

### 1.2 Insight central

Olhando o `cmd/server/oauth_facade.go` do `clicksign/mcp-api-tavola-v3`:

```go
// handleAuthorizationCodeGrant
w.Header().Set("Content-Type", issuedCode.ContentType)
_, _ = io.Copy(w, strings.NewReader(issuedCode.TokenResponse))
```

A `TokenResponse` armazenada em `issuedCode.TokenResponse` é **literalmente o body que o Cognito devolveu** ao MCP facade no `proxyTokenForm`. Ou seja, o `access_token` que o nosso projeto recebe quando chama `/oauth2/token` no MCP **É um JWT do Cognito Clicksign**, e ele é aceito direto pela API REST `https://app.clicksign.com/api/v3` como `Authorization: Bearer <jwt>` (vide `internal/clicksign/client.go` no MCP server, função `newOAuth2Req`).

### 1.3 Decisão

Pular o MCP na camada de execução, manter o MCP só como provedor de OAuth (autenticação). Resultado: imune ao bug em prod, sem precisar de secret nenhum, mantemos UX e código existente quase intactos.

### 1.4 Escopo de tools no MVP

Internalizamos **6 tools** (o suficiente para o demo do hackathon):

| Tool | Endpoint REST | Justificativa |
|---|---|---|
| `list_envelopes` | `GET /envelopes` | Mostrar lista de documentos do usuário no chat |
| `list_templates` | `GET /templates` | Pré-requisito para `get_template_fields` e `create_envelope_with_template` |
| `get_template_fields` | `GET /templates/{id}/template_fields` | Listar variáveis do template para o LLM pedir os valores certos ao usuário |
| `create_envelope_with_template` | `POST /envelope_bulk_creations` | Criar+enviar envelope a partir de template (caso comum) |
| `create_envelope_with_file_url` | `POST /envelope_bulk_creations` | Criar+enviar envelope a partir de URL pública de PDF/imagem |
| `select_account` | (local) | Permitir o LLM trocar conta quando o usuário tem múltiplas |

As 11 demais tools que o MCP server expõe (`quick_send_envelope`, `create_template`, `edit_template`, `get_envelope_details`, `list_envelope_documents`, `get_document_details`, `configure_api_token`, etc.) ficam **fora** do MVP — podem ser adicionadas pós-hackathon com baixo custo (cada uma é ~20-50 linhas de Go reaproveitando o cliente REST).

---

## 2. Decisões arquiteturais

| Decisão | Escolha | Justificativa |
|---|---|---|
| OAuth/DCR/PKCE | Mantém via MCP facade de prod | Já funciona; obtém JWT Cognito sem secret |
| Onde executam as "tools" | Cliente REST `internal/clicksign` direto contra `app.clicksign.com/api/v3` | Imune ao bug em `tools/call` do MCP prod |
| Catálogo de tools p/ OpenAI | Estático (Go) em `internal/tools` | Sem dependência do `tools/list` do MCP; total controle de schema e descrição |
| Tools no catálogo (MVP) | `list_envelopes`, `list_templates`, `get_template_fields`, `create_envelope_with_template`, `create_envelope_with_file_url`, `select_account` | Cobre o demo do hackathon (criar envelope + notificar via WhatsApp) |
| Account selection (`X-Account-Key`) | Auto-seleciona a 1ª conta no callback **+** expõe `select_account` como tool | Auto-seleção resolve o caso comum (1 conta); a tool é o fallback se o usuário tem N contas |
| Refresh-on-401 | Reusar a lógica de `internal/mcpclient.Manager.refresh` | Mesmo padrão, só muda a chamada que dispara o 401 |
| Loop de tool-calling LLM | **Mantém** (`internal/llm/openai.go`) | Não é uma reescrita de Opção B; é só swap da engine de tools |
| `internal/mcpclient` | Aposentar ao fim | Não mais necessário; mantemos enquanto migramos para reduzir risco |
| Classifier / MetaHelp | Mantém | Nada muda |
| Session store | Mantém | `Session.AccountKey` já existe (`internal/session/store.go:20`) |

### 2.1 Sobre `select_account` — sim, é necessária

A pergunta foi se `select_account` poderia ser dispensada. Resposta curta: **expor como tool é necessário**, mesmo com auto-seleção. Razões:

1. **Auto-seleção cobre só o caso de 1 conta.** Se o usuário tiver 2+ contas, a primeira pode ser a errada (ex.: conta pessoal vs. corporativa). O LLM precisa de uma forma de trocar.
2. **A notificação WhatsApp é feature de conta específica.** Se a feature só está habilitada numa das contas do usuário, o LLM precisa poder migrar.
3. **Custo de implementação é trivial.** A tool só atualiza `Session.AccountKey` — não bate em endpoint externo. ~20 linhas de Go.

Estratégia:
- **Callback OAuth** auto-seleciona a 1ª conta (resolve 80% dos casos sem o LLM saber que existe seleção).
- **Tool `select_account`** continua disponível: se a 1ª conta falhar com erro de permissão, o LLM tem a saída.
- **Tool `list_envelopes`/`list_templates`** retornam um erro estruturado quando o Clicksign devolver "múltiplas contas" sem `X-Account-Key`, indicando ao LLM que ele precisa chamar `select_account`. Isso emula o comportamento do MCP server hoje.

---

## 3. Arquitetura alvo

```text
WhatsApp → n8n → POST /api/messages (Bearer estático)
   │
   ▼
┌────────────────────────────────────────────────────────────────┐
│ whatsapp-mcp                                                   │
│                                                                │
│ [auth path — INALTERADO]                                       │
│   sem sessão  → DCR (cache) → /oauth2/authorize MCP prod       │
│                → user faz login Clicksign no Cognito           │
│                → /oauth2/callback                              │
│                → troca code via /oauth2/token MCP prod         │
│                → recebe JWT Cognito como access_token          │
│                → NOVO: chama GET /api/v3/oauth2/accounts e     │
│                       persiste account_key na Session          │
│                → success.html + webhook n8n                    │
│                                                                │
│ [tool exec path — REESCRITO]                                   │
│   com sessão  → classifier (igual)                             │
│                → OpenAI tool-calling loop (igual)              │
│                → tools.Runner.Call(name, args)                 │
│                          │                                     │
│                          ▼                                     │
│                   internal/clicksign HTTPClient                │
│                   Authorization: Bearer <Session.AccessToken>  │
│                   X-Account-Key: <Session.AccountKey>          │
│                          │                                     │
│                          ▼                                     │
│                  https://app.clicksign.com/api/v3/<endpoint>   │
│                                                                │
│                   401 → oauth.RefreshToken → retry             │
└────────────────────────────────────────────────────────────────┘
```

---

## 4. Fase 0 — Validação prévia (1h, antes de escrever código)

Antes de mexer no código, validar duas premissas críticas. Se qualquer uma falhar, o plano precisa de plano B.

### 4.1 DCR aberto em produção?

```bash
# Esperado: HTTP 200 com client_id no body
curl -i -X POST \
  https://mcp-api-tavola-v3.clicksign.com/oauth2/register \
  -H 'Content-Type: application/json' \
  -d '{
    "redirect_uris": ["https://<seu-ngrok>.ngrok.io/oauth2/callback"],
    "token_endpoint_auth_method": "none",
    "grant_types": ["authorization_code", "refresh_token"],
    "response_types": ["code"],
    "scope": "openid email phone",
    "client_name": "whatsapp-mcp-hackathon"
  }'
```

**Se 403/404:** DCR está restrito em prod. Fallback: pedir à infra para registrar um client estático no MCP prod e usá-lo via env var (já temos o handler `f.clientRegistry.Resolve` que aceita ambos).

### 4.2 JWT do Cognito é aceito pela API REST com `oauth2/accounts`?

Após fazer um OAuth completo manual contra prod e pegar o `access_token`:

```bash
# Esperado: HTTP 200 com data[]
curl -i \
  https://app.clicksign.com/api/v3/oauth2/accounts \
  -H "Authorization: Bearer <jwt-cognito>"
```

**Se 401:** verificar se faltam scopes. Pode ser preciso ajustar `MCP_OAUTH_SCOPES`.

### 4.3 `/.well-known/oauth-authorization-server` de prod

```bash
curl -s https://mcp-api-tavola-v3.clicksign.com/.well-known/oauth-authorization-server | jq
```

Validar que `authorization_endpoint`, `token_endpoint` e `registration_endpoint` estão presentes.

**Critério de aceite da Fase 0:** os três comandos retornam 200 com payloads válidos.

---

## 5. Fase 1 — Config (15 min)

### 5.1 Arquivos: `.env`, `.env.example`, `internal/config/config.go`

**Adicionar:**

| Env | Default | Descrição |
|---|---|---|
| `CLICKSIGN_API_BASE_URL` | `https://app.clicksign.com/api/v3` | Base URL da API REST |
| `CLICKSIGN_HTTP_TIMEOUT_SECONDS` | `30` | Timeout por request REST |
| `MCP_SERVER_BASE_URL` | (existente) | **Mudar para prod**: `https://mcp-api-tavola-v3.clicksign.com` |

**Em `internal/config/config.go`:**

```go
type Config struct {
    // ... existentes ...
    ClicksignAPIBaseURL       string `mapstructure:"clicksign_api_base_url"`
    ClicksignHTTPTimeoutSeconds int  `mapstructure:"clicksign_http_timeout_seconds"`
}

func (c *Config) ClicksignHTTPTimeout() time.Duration {
    return time.Duration(c.ClicksignHTTPTimeoutSeconds) * time.Second
}
```

E `v.SetDefault` + `v.BindEnv` correspondentes.

### 5.2 Critério de aceite

`go run ./cmd/server` boota sem erro, log inicial mostra `mcp_endpoint=https://mcp-api-tavola-v3.clicksign.com/mcp/oauth2`.

---

## 6. Fase 2 — Cliente REST Clicksign (3-4h)

### 6.1 Novo pacote: `internal/clicksign/`

```text
internal/clicksign/
├── client.go        # HTTPClient + do() com refresh-on-401
├── types.go         # structs (Envelope, Template, Document, Signer, Account, …)
├── envelopes.go     # CreateEnvelope, ListEnvelopes, GetEnvelopeDetails, AddDocument, AddSigner, MakeAvailable, NotifyAll, NotifySigner
├── templates.go     # ListTemplates, GetTemplateFields, CreateTemplate, UpdateTemplate, DeleteTemplate
├── accounts.go      # ListOAuth2Accounts
└── client_test.go   # testes com httptest.Server
```

### 6.2 Padrão de cliente

```go
// internal/clicksign/client.go
package clicksign

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/clicksign/whatsapp-mcp/internal/logging"
    "github.com/clicksign/whatsapp-mcp/internal/oauth"
    "github.com/clicksign/whatsapp-mcp/internal/session"
)

var (
    ErrUnauthorized = errors.New("clicksign: unauthorized")
    ErrAuthExpired  = errors.New("clicksign: auth expired") // depois de refresh falhar
    ErrServer       = errors.New("clicksign: server error")
)

type HTTPClient struct {
    baseURL string
    http    *http.Client
    logger  *slog.Logger
    store   session.Store
    oauth   *oauth.Client
}

func NewHTTPClient(
    baseURL string,
    timeout time.Duration,
    logger *slog.Logger,
    store session.Store,
    oauthClient *oauth.Client,
) *HTTPClient {
    return &HTTPClient{
        baseURL: strings.TrimRight(baseURL, "/"),
        http:    &http.Client{Timeout: timeout},
        logger:  logger,
        store:   store,
        oauth:   oauthClient,
    }
}

// doForPhone executa um request com Bearer + X-Account-Key da sessão.
// Em caso de 401, tenta refresh do access_token uma vez e re-executa.
func (c *HTTPClient) doForPhone(
    ctx context.Context,
    phone, method, path string,
    body io.Reader,
    out any,
) (int, error) {
    sess, err := c.store.GetSession(ctx, phone)
    if err != nil {
        return 0, ErrAuthExpired
    }

    status, raw, err := c.doOnce(ctx, sess, method, path, body)
    if err != nil {
        return status, err
    }
    if status == http.StatusUnauthorized {
        if err := c.refresh(ctx, phone); err != nil {
            return status, ErrAuthExpired
        }
        sess, _ = c.store.GetSession(ctx, phone)
        // re-executa o request original; cuidado: se body for io.Reader não-rebobinável,
        // o caller deve passar []byte e converter aqui — vide assinatura helper.
        status, raw, err = c.doOnce(ctx, sess, method, path, body)
        if err != nil {
            return status, err
        }
    }

    if status >= 400 {
        return status, fmt.Errorf("%w: %d %s", ErrServer, status, string(raw))
    }
    if out != nil && len(raw) > 0 {
        if err := json.Unmarshal(raw, out); err != nil {
            return status, fmt.Errorf("decode: %w", err)
        }
    }
    return status, nil
}

func (c *HTTPClient) doOnce(
    ctx context.Context,
    sess *session.Session,
    method, path string,
    body io.Reader,
) (int, []byte, error) {
    url := c.baseURL + path
    req, err := http.NewRequestWithContext(ctx, method, url, body)
    if err != nil {
        return 0, nil, err
    }
    req.Header.Set("Authorization", "Bearer "+sess.AccessToken)
    if sess.AccountKey != "" {
        req.Header.Set("X-Account-Key", sess.AccountKey)
    }
    req.Header.Set("Accept", "application/json")
    if body != nil {
        // o caller que define Content-Type
    }

    resp, err := c.http.Do(req)
    if err != nil {
        return 0, nil, fmt.Errorf("http: %w", err)
    }
    defer resp.Body.Close()
    raw, _ := io.ReadAll(resp.Body)
    c.logger.Debug("clicksign_api",
        slog.String("phone_hash", logging.HashPhone(sess.PhoneNumber)),
        slog.String("method", method),
        slog.String("path", path),
        slog.Int("status", resp.StatusCode),
    )
    return resp.StatusCode, raw, nil
}

func (c *HTTPClient) refresh(ctx context.Context, phone string) error {
    sess, err := c.store.GetSession(ctx, phone)
    if err != nil || sess.RefreshToken == "" {
        return ErrAuthExpired
    }
    reg, err := c.store.GetClientRegistration(ctx)
    if err != nil {
        return ErrAuthExpired
    }
    token, err := c.oauth.RefreshToken(ctx, reg.ClientID, sess.RefreshToken)
    if err != nil {
        _ = c.store.DeleteSession(ctx, phone)
        return ErrAuthExpired
    }
    sess.AccessToken = token.AccessToken
    if token.RefreshToken != "" {
        sess.RefreshToken = token.RefreshToken
    }
    sess.ExpiresAt = token.ExpiresAt()
    sess.UpdatedAt = time.Now().UTC()
    return c.store.PutSession(ctx, sess)
}
```

**Detalhe importante (body re-execução):** quando o body é um `io.Reader` consumido na primeira tentativa, o retry após refresh quebra. Solução: helpers tipados que serializam para `[]byte` e criam um novo `bytes.NewReader` em cada tentativa:

```go
func (c *HTTPClient) postJSON(ctx context.Context, phone, path string, in, out any) (int, error) {
    payload, err := json.Marshal(in)
    if err != nil { return 0, err }

    do := func() (int, []byte, error) {
        sess, err := c.store.GetSession(ctx, phone)
        if err != nil { return 0, nil, ErrAuthExpired }
        req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
        req.Header.Set("Authorization", "Bearer "+sess.AccessToken)
        req.Header.Set("X-Account-Key", sess.AccountKey)
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("Accept", "application/json")
        resp, err := c.http.Do(req)
        if err != nil { return 0, nil, err }
        defer resp.Body.Close()
        raw, _ := io.ReadAll(resp.Body)
        return resp.StatusCode, raw, nil
    }

    status, raw, err := do()
    if err != nil { return 0, err }
    if status == http.StatusUnauthorized {
        if err := c.refresh(ctx, phone); err != nil { return status, ErrAuthExpired }
        status, raw, err = do()
        if err != nil { return 0, err }
    }
    // ... decode em out ...
}
```

Aplicar o mesmo pattern (closure que monta o request do zero) para todos os verbos.

### 6.3 Endpoints a implementar (escopo MVP — só o que as 6 tools demandam)

| Método cliente | HTTP | Path | Content-Type | Usado por |
|---|---|---|---|---|
| `ListOAuth2Accounts(ctx, accessToken)` | GET | `/oauth2/accounts` | — | callback OAuth, tool `select_account` |
| `ListEnvelopes(ctx, phone, status?, limit?)` | GET | `/envelopes?status=...&limit=...` | — | tool `list_envelopes` |
| `ListTemplates(ctx, phone)` | GET | `/templates` | — | tool `list_templates` |
| `GetTemplateFields(ctx, phone, templateID)` | GET | `/templates/{id}/template_fields` | — | tool `get_template_fields` |
| `CreateEnvelopeBulkCreation(ctx, phone, req)` | POST | `/envelope_bulk_creations` | `application/json` | tools `create_envelope_with_template` **e** `create_envelope_with_file_url` |

**Importante:** as duas tools `create_envelope_with_*` batem no **mesmo endpoint** (`POST /envelope_bulk_creations`). A diferença é só **como** o `document` é montado no payload (`template` vs. `content_base64`). Isso simplifica muito o cliente REST.

### 6.4 Estruturas do payload (extraídas de `internal/clicksign/types.go` do MCP)

Replicar em `internal/clicksign/types.go`:

```go
type EnvelopeBulkCreationRequest struct {
    Data EnvelopeBulkCreationData `json:"data"`
}

type EnvelopeBulkCreationData struct {
    Type       string                         `json:"type"` // "envelope_bulk_creations"
    Attributes EnvelopeBulkCreationAttributes `json:"attributes"`
}

type EnvelopeBulkCreationAttributes struct {
    Envelope      BulkEnvelope     `json:"envelope"`
    Document      BulkDocument     `json:"document"`
    Signers       []BulkSigner     `json:"signers"`
    Notifications BulkNotification `json:"notifications"`
}

type BulkEnvelope struct {
    Name              string                 `json:"name"`
    DefaultSubject    string                 `json:"default_subject,omitempty"`
    Locale            string                 `json:"locale,omitempty"`
    AutoClose         bool                   `json:"auto_close"`
    RemindInterval    int                    `json:"remind_interval"`
    BlockAfterRefusal bool                   `json:"block_after_refusal"`
    Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

// Template OU ContentBase64 (mutuamente exclusivos)
type BulkDocument struct {
    Template      *BulkTemplate          `json:"template,omitempty"`
    ContentBase64 string                 `json:"content_base64,omitempty"`
    Filename      string                 `json:"filename"`
    Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

type BulkTemplate struct {
    Key  string                 `json:"key"`
    Data map[string]interface{} `json:"data"`
}

type BulkSigner struct {
    Name              string                 `json:"name"`
    Email             string                 `json:"email,omitempty"`
    PhoneNumber       string                 `json:"phone_number,omitempty"`
    HasDocumentation  bool                   `json:"has_documentation"`
    Refusable         bool                   `json:"refusable"`
    Requirements      []BulkRequirement      `json:"requirements,omitempty"`
}

// Action: "agree" → role; "provide_evidence" → auth (email|sms|whatsapp|...)
type BulkRequirement struct {
    Action string `json:"action"`
    Role   string `json:"role,omitempty"`
    Auth   string `json:"auth,omitempty"`
}

type BulkNotification struct {
    Message string `json:"message"`
}

type EnvelopeBulkCreationResponse struct {
    Data struct {
        ID         string `json:"id"`
        Type       string `json:"type"`
        Attributes struct {
            EnvelopeID string `json:"envelope_id"`
            Status     string `json:"status"`
        } `json:"attributes"`
    } `json:"data"`
}
```

Para `Envelope`, `Template`, `TemplateField` e `OAuth2Account`, replicar os tipos em formato JSON:API (vide `internal/clicksign/types.go` do MCP server). O `TemplateField` traz `id`, `attributes.name`, `attributes.kind` e timestamps — o LLM usa `attributes.name` como chave para preencher `document.template.data` em `create_envelope_with_template`.

### 6.5 FileFetcher (para `create_envelope_with_file_url`)

A tool `create_envelope_with_file_url` recebe uma URL HTTPS pública e precisa:
1. Baixar o arquivo server-side (não pode passar pelo LLM).
2. Validar MIME type (apenas `.pdf`, `.jpg`, `.jpeg`, `.png`, `.txt`, `.doc`, `.docx`).
3. Validar tamanho máximo (default: 20 MB).
4. **Proteger contra SSRF** rejeitando ranges privados (`10.0.0.0/8`, `127.0.0.0/8`, `169.254.0.0/16` etc.).
5. Converter para base64 e atribuir a `BulkDocument.ContentBase64`.

Criar `internal/clicksign/file_fetcher.go` espelhando o do MCP server (vide `clicksign/mcp-api-tavola-v3/internal/clicksign/file_fetcher.go`). Pode-se literalmente **copiar o arquivo** ajustando o package — a lógica é genérica e não depende do MCP.

Adicionar ao config:

| Env | Default | Descrição |
|---|---|---|
| `FILE_FETCHER_MAX_BYTES` | `20971520` (20 MB) | Tamanho máximo do arquivo baixado |
| `FILE_FETCHER_ALLOW_HTTP` | `false` | Se `true`, permite `http://` (apenas dev) |

### 6.6 Critério de aceite

- Testes em `client_test.go` rodando com `httptest.Server`: GET lista, POST cria, 401 → refresh → retry.
- Teste do `FileFetcher`: aceita HTTPS de PDF mock, rejeita HTTP, rejeita `127.0.0.1`, rejeita arquivo > maxBytes.
- `make test` verde.

---

## 7. Fase 3 — Catálogo estático de tools (`internal/tools`) (3-4h)

### 7.1 Novo pacote: `internal/tools/`

```text
internal/tools/
├── runner.go                          # interface Runner + Tool + StaticRunner
├── catalog.go                         # função Catalog(deps) []Tool — agrega as 6 tools
├── list_envelopes.go                  # tool list_envelopes
├── list_templates.go                  # tool list_templates
├── get_template_fields.go             # tool get_template_fields
├── create_envelope_with_template.go   # tool create_envelope_with_template
├── create_envelope_with_file_url.go   # tool create_envelope_with_file_url
├── select_account.go                  # tool select_account
└── runner_test.go
```

### 7.1.1 Tools no MVP (6 ao todo)

| Tool | Endpoint REST chamado | Notas |
|---|---|---|
| `list_envelopes` | `GET /envelopes` | filtros opcionais: `status`, `limit` |
| `list_templates` | `GET /templates` | sem args obrigatórios |
| `get_template_fields` | `GET /templates/{id}/template_fields` | requer `template_id` (UUID) |
| `create_envelope_with_template` | `POST /envelope_bulk_creations` | `BulkDocument.Template{Key, Data}` preenchido |
| `create_envelope_with_file_url` | `POST /envelope_bulk_creations` | `FileFetcher` baixa, valida e converte → `BulkDocument.ContentBase64` |
| `select_account` | nenhum (só toca `Session.AccountKey`) | atualiza store local |

### 7.2 Interface

```go
// internal/tools/runner.go
package tools

import "context"

type Tool struct {
    Name        string
    Description string
    Parameters  map[string]any           // JSON Schema
    Run         func(ctx context.Context, phone string, args map[string]any) (string, error)
}

type Runner interface {
    List(ctx context.Context, phone string) ([]Tool, error)
    Call(ctx context.Context, phone, name string, args map[string]any) (string, error)
}

type StaticRunner struct {
    catalog []Tool
    byName  map[string]Tool
}

func NewStaticRunner(catalog []Tool) *StaticRunner {
    m := make(map[string]Tool, len(catalog))
    for _, t := range catalog { m[t.Name] = t }
    return &StaticRunner{catalog: catalog, byName: m}
}

func (s *StaticRunner) List(_ context.Context, _ string) ([]Tool, error) {
    return s.catalog, nil
}

func (s *StaticRunner) Call(ctx context.Context, phone, name string, args map[string]any) (string, error) {
    t, ok := s.byName[name]
    if !ok { return "", fmt.Errorf("tool %q not found", name) }
    return t.Run(ctx, phone, args)
}
```

### 7.3 Dependências do catálogo

```go
// internal/tools/runner.go
type CatalogDeps struct {
    Clicksign   *clicksign.HTTPClient
    Store       session.Store
    FileFetcher clicksign.FileFetcher
}

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
```

### 7.4 Exemplos das tools (schemas copiados literalmente do MCP server)

**`list_envelopes`** — `internal/tools/list_envelopes.go`:

```go
func listEnvelopesTool(d CatalogDeps) Tool {
    return Tool{
        Name: "list_envelopes",
        Description: "List Clicksign envelopes for the currently selected account. Optional filters: status and limit.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "status": map[string]any{
                    "type":        "string",
                    "description": "Optional status filter (e.g. running, closed, canceled). Omit to list all statuses.",
                },
                "limit": map[string]any{
                    "type":        "integer",
                    "minimum":     1,
                    "maximum":     100,
                    "description": "Optional maximum number of envelopes to return (1-100).",
                },
            },
        },
        Run: func(ctx context.Context, phone string, args map[string]any) (string, error) {
            status, _ := args["status"].(string)
            limit := 0
            if v, ok := args["limit"].(float64); ok { limit = int(v) }
            envs, err := d.Clicksign.ListEnvelopes(ctx, phone, status, limit)
            if err != nil { return "", err }
            b, _ := json.Marshal(envs)
            return string(b), nil
        },
    }
}
```

**`list_templates`** — `internal/tools/list_templates.go`:

```go
func listTemplatesTool(d CatalogDeps) Tool {
    return Tool{
        Name: "list_templates",
        Description: "List Clicksign templates available for the currently selected account.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{},
        },
        Run: func(ctx context.Context, phone string, _ map[string]any) (string, error) {
            tpls, err := d.Clicksign.ListTemplates(ctx, phone)
            if err != nil { return "", err }
            b, _ := json.Marshal(tpls)
            return string(b), nil
        },
    }
}
```

**`get_template_fields`** — `internal/tools/get_template_fields.go`:

```go
func getTemplateFieldsTool(d CatalogDeps) Tool {
    return Tool{
        Name: "get_template_fields",
        Description: "List the variable fields defined in a Clicksign template. Required input: template_id " +
            "(UUID from list_templates). Use this before create_envelope_with_template so the user can fill in " +
            "the right variables (template.data) — each returned field's name is a key expected in template.data.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "template_id": map[string]any{
                    "type":        "string",
                    "format":      "uuid",
                    "description": "Template UUID returned by list_templates.",
                },
            },
            "required": []string{"template_id"},
        },
        Run: func(ctx context.Context, phone string, args map[string]any) (string, error) {
            id, _ := args["template_id"].(string)
            if strings.TrimSpace(id) == "" {
                return "", errors.New("template_id is required")
            }
            fields, err := d.Clicksign.GetTemplateFields(ctx, phone, id)
            if err != nil { return "", err }
            b, _ := json.Marshal(fields)
            return string(b), nil
        },
    }
}
```

**`create_envelope_with_template`** — `internal/tools/create_envelope_with_template.go`:

```go
func createEnvelopeWithTemplateTool(d CatalogDeps) Tool {
    return Tool{
        Name: "create_envelope_with_template",
        Description: "Create and send an envelope using a Clicksign template. Required inputs: envelope.name, " +
            "document.template.key + document.template.data, document.filename (.doc/.docx) and at least one " +
            "signer with name + requirements (one qualification action=agree and one authentication " +
            "action=provide_evidence). When auth=email the signer needs an email; when auth=sms or " +
            "auth=whatsapp the signer needs phone_number. notifications.message is optional.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "envelope": envelopeSchemaWithRemind(),
                "document": map[string]any{
                    "type": "object",
                    "properties": map[string]any{
                        "filename": map[string]any{
                            "type":        "string",
                            "description": "File name ending in .doc or .docx (e.g. \"Contract.docx\").",
                        },
                        "template": map[string]any{
                            "type": "object",
                            "properties": map[string]any{
                                "key":  map[string]any{"type": "string", "format": "uuid"},
                                "data": map[string]any{"type": "object"},
                            },
                            "required": []string{"key", "data"},
                        },
                    },
                    "required": []string{"filename", "template"},
                },
                "signers":       signersSchema(),
                "notifications": notificationsSchema(),
            },
            "required": []string{"envelope", "document", "signers"},
        },
        Run: func(ctx context.Context, phone string, args map[string]any) (string, error) {
            req, err := buildBulkRequestFromTemplate(args)
            if err != nil { return "", err }
            resp, err := d.Clicksign.CreateEnvelopeBulkCreation(ctx, phone, req)
            if err != nil { return "", err }
            b, _ := json.Marshal(resp)
            return string(b), nil
        },
    }
}
```

**`create_envelope_with_file_url`** — `internal/tools/create_envelope_with_file_url.go`:

```go
func createEnvelopeWithFileURLTool(d CatalogDeps) Tool {
    return Tool{
        Name: "create_envelope_with_file_url",
        Description: "Create and send an envelope from a public HTTPS file URL. The server downloads, validates and " +
            "base64-encodes the file (pdf, jpg, jpeg, png, txt, doc, docx). Required inputs: envelope.name, " +
            "document.file_url and at least one signer with name + requirements.",
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "envelope": envelopeSchemaWithRemind(),
                "document": map[string]any{
                    "type": "object",
                    "properties": map[string]any{
                        "file_url": map[string]any{
                            "type":   "string",
                            "format": "uri",
                            "description": "Public HTTPS URL of the file.",
                        },
                        "filename": map[string]any{
                            "type":        "string",
                            "description": "Optional. File name with an accepted extension. When omitted, derived from URL path.",
                        },
                        "metadata": map[string]any{"type": "object"},
                    },
                    "required": []string{"file_url"},
                },
                "signers":       signersSchema(),
                "notifications": notificationsSchema(),
            },
            "required": []string{"envelope", "document", "signers"},
        },
        Run: func(ctx context.Context, phone string, args map[string]any) (string, error) {
            doc, _ := args["document"].(map[string]any)
            fileURL, _ := doc["file_url"].(string)

            data, mime, err := d.FileFetcher.Fetch(ctx, fileURL)
            if err != nil { return "", fmt.Errorf("fetch file: %w", err) }

            req, err := buildBulkRequestFromFile(args, data, mime)
            if err != nil { return "", err }
            resp, err := d.Clicksign.CreateEnvelopeBulkCreation(ctx, phone, req)
            if err != nil { return "", err }
            b, _ := json.Marshal(resp)
            return string(b), nil
        },
    }
}
```

**`select_account`** — `internal/tools/select_account.go`:

```go
func selectAccountTool(d CatalogDeps) Tool {
    return Tool{
        Name: "select_account",
        Description: "Choose which Clicksign account to use in the current session. " +
            "Use the account_key returned by list_envelopes or list_templates when more than one account is available. " +
            "The selection persists for the entire session.",
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
            key, _ := args["account_key"].(string)
            if strings.TrimSpace(key) == "" {
                return "", errors.New("account_key is required")
            }
            sess, err := d.Store.GetSession(ctx, phone)
            if err != nil { return "", clicksign.ErrAuthExpired }
            sess.AccountKey = strings.TrimSpace(key)
            sess.UpdatedAt = time.Now().UTC()
            if err := d.Store.PutSession(ctx, sess); err != nil {
                return "", fmt.Errorf("persist session: %w", err)
            }
            return fmt.Sprintf(`{"ok":true,"account_key":%q}`, sess.AccountKey), nil
        },
    }
}
```

### 7.5 Helpers compartilhados

Em `internal/tools/schemas.go`, extrair os trechos comuns:

```go
func envelopeSchemaWithRemind() map[string]any { /* ... */ }
func signersSchema() map[string]any            { /* ... */ }
func notificationsSchema() map[string]any      { /* ... */ }
```

Em `internal/tools/bulk_builder.go`, extrair a montagem do `EnvelopeBulkCreationRequest`:

```go
// buildBulkRequestFromTemplate decodifica args (map[string]any vindo do LLM) em um EnvelopeBulkCreationRequest
// preenchendo Document.Template{Key,Data}. Aplica defaults (auto_close=false, remind_interval=3 etc.).
func buildBulkRequestFromTemplate(args map[string]any) (clicksign.EnvelopeBulkCreationRequest, error)

// buildBulkRequestFromFile faz o mesmo, mas com Document.ContentBase64 = base64(data) e Document.Filename
// derivado do mime e/ou args["document"]["filename"].
func buildBulkRequestFromFile(args map[string]any, data []byte, mime string) (clicksign.EnvelopeBulkCreationRequest, error)
```

A maior parte do código aqui é serialização defensiva (`map[string]any` → struct tipado). Pode espelhar a validação do MCP server em `internal/mcp/validation.go` se quiser fidelidade total, mas no hackathon pode-se confiar mais na LLM e validar só o mínimo (campos obrigatórios).

### 7.6 Critério de aceite

- `StaticRunner.List` retorna 6 tools.
- `Call("list_envelopes", {})` para sessão válida devolve JSON com envelopes.
- `Call("list_templates", {})` retorna JSON com templates.
- `Call("get_template_fields", {template_id: "<uuid>"})` retorna JSON com a lista de variáveis do template.
- `Call("create_envelope_with_template", {...})` cria envelope e retorna o `envelope_id`.
- `Call("create_envelope_with_file_url", {file_url: "https://...pdf"})` baixa, monta payload e cria envelope.
- `Call("select_account", {account_key: "abc"})` atualiza `Session.AccountKey` no store.

---

## 8. Fase 4 — Account selection no callback OAuth (1-2h)

### 8.1 Arquivo: `internal/api/oauth_handler.go`

Hoje o `Callback` faz: trocar code → tokens → persistir `Session` → webhook n8n.

**Adicionar** entre "tokens" e "persistir":

```go
// Pseudocódigo dentro do handler de callback
accounts, err := clicksignClient.ListOAuth2Accounts(ctx, token.AccessToken)
if err != nil {
    logger.Warn("oauth_callback_list_accounts_failed",
        slog.String("phone_hash", logging.HashPhone(phone)),
        slog.String("err", err.Error()),
    )
    // não bloqueia o login; segue sem AccountKey, deixa o LLM lidar
}

session.AccountKey = ""
if len(accounts) > 0 {
    session.AccountKey = accounts[0].Key   // ou .ID, conforme o tipo
    logger.Info("oauth_callback_account_selected",
        slog.String("phone_hash", logging.HashPhone(phone)),
        slog.String("account_key_hash", logging.HashShort(session.AccountKey)),
        slog.Int("accounts_count", len(accounts)),
    )
}
```

**Importante:** `ListOAuth2Accounts` recebe `accessToken` direto (não a sessão), porque a `Session` ainda não foi persistida nesse ponto.

### 8.2 Crítica de assinatura

`internal/clicksign/accounts.go` deve ter um construtor que aceita o token explícito, separado do fluxo "sessão+phone":

```go
func (c *HTTPClient) ListOAuth2AccountsWithToken(ctx context.Context, accessToken string) ([]Account, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/oauth2/accounts", nil)
    req.Header.Set("Authorization", "Bearer "+accessToken)
    req.Header.Set("Accept", "application/json")
    // ... resto idêntico ...
}
```

### 8.3 Critério de aceite

Após login OAuth completo, log mostra `oauth_callback_account_selected accounts_count=N` e a `Session` persistida tem `AccountKey` preenchido.

---

## 9. Fase 5 — Substituir `mcpclient.Manager` por `tools.Runner` no LLM (1-2h)

### 9.1 Arquivo: `internal/llm/openai.go`

Trocar a dependência:

```go
// antes
type Conversation struct {
    // ...
    mgr        *mcpclient.Manager
    // ...
}

// depois
type Conversation struct {
    // ...
    tools      tools.Runner
    // ...
}
```

E adaptar `Run`:

```go
// antes
conn, err := c.mgr.Open(ctx, in.Phone)
if err != nil { /* ErrAuthExpired */ }
defer conn.Close()

mcpTools, err := c.mgr.ListTools(ctx, conn)
// ... ToOpenAITools ...

// depois
catalog, err := c.tools.List(ctx, in.Phone)
if err != nil { /* ErrAuthExpired se vier */ }

tools := make([]openai.ChatCompletionToolParam, 0, len(catalog))
for _, t := range catalog {
    tools = append(tools, openai.ChatCompletionToolParam{
        Function: shared.FunctionDefinitionParam{
            Name:        t.Name,
            Description: openai.String(t.Description),
            Parameters:  shared.FunctionParameters(t.Parameters),
        },
    })
}
```

E na execução de cada `tool_call`:

```go
// antes
result, callErr := c.mgr.CallTool(ctx, conn, name, args)
toolPayload = mcpclient.ExtractText(result)

// depois
toolPayload, callErr := c.tools.Call(ctx, in.Phone, name, args)
// callErr: clicksign.ErrAuthExpired → conv.ErrSessionExpired
```

### 9.2 Tradução de erros

Hoje detecta `errors.Is(err, mcpclient.ErrAuthExpired)` em três pontos. Trocar para `errors.Is(err, clicksign.ErrAuthExpired)`. O `tools.Runner.Call` deve **repassar** o erro do cliente Clicksign sem encapsular.

### 9.3 `internal/llm/meta.go`

Hoje usa `mgr.ListToolsCached()` para listar capacidades dinâmicas. Substituir por chamada ao `tools.Runner.List` (sempre estático e barato, sem cache necessário).

### 9.4 Critério de aceite

- Removidos todos os imports de `internal/mcpclient` em `internal/llm`.
- Testes `internal/llm/*_test.go` continuam passando (pode ser preciso ajustar mocks).
- Fluxo end-to-end (curl simulando n8n) responde corretamente.

---

## 10. Fase 6 — Wire-up no `cmd/server/main.go` (30 min)

### 10.1 Mudanças

```go
// substituir
mcpManager := mcpclient.NewManager(cfg, logger, store, oauthClient)
// ...
metaResponder = llm.NewMetaHelpResponder(cfg, logger, mcpManager)
// ...
conversation := llm.NewConversation(cfg, logger, store, mcpManager, intentClassifier, metaResponder)

// por
clicksignClient := clicksign.NewHTTPClient(
    cfg.ClicksignAPIBaseURL,
    cfg.ClicksignHTTPTimeout(),
    logger,
    store,
    oauthClient,
)
toolRunner := tools.NewStaticRunner(tools.Catalog(clicksignClient))
// ...
metaResponder = llm.NewMetaHelpResponder(cfg, logger, toolRunner)
// ...
conversation := llm.NewConversation(cfg, logger, store, toolRunner, intentClassifier, metaResponder)
```

E passar `clicksignClient` para o `oauth_handler` para a Fase 4.

### 10.2 Critério de aceite

`go build ./...` sem erros; `go run ./cmd/server` boota; healthcheck verde.

---

## 11. Fase 7 — Cleanup (15 min)

### 11.1 Aposentar `internal/mcpclient`

Depois que tudo está verde:

- `git rm -r internal/mcpclient/`
- `go mod tidy` para remover `github.com/mark3labs/mcp-go` do `go.mod`/`go.sum`
- Atualizar `README.md` (seção "Componentes") para refletir os novos pacotes
- Remover do `IMPLEMENTATION_PLAN.md` referências a "MCP client / `tools/call`" se quiser; ou deixar e adicionar nota no topo.

### 11.2 Critério de aceite

`grep -r mcpclient ./internal ./cmd` retorna vazio. `go build ./...` verde.

---

## 12. Plano de teste end-to-end

### 12.1 Sequência de smoke test

1. `make run` (com `.env` apontando para prod).
2. `ngrok http 8080` → atualizar `PUBLIC_BASE_URL`.
3. `curl -i -X POST http://localhost:8080/api/messages -H 'Authorization: Bearer $API_STATIC_TOKEN' -H 'Content-Type: application/json' -d '{"phone_number":"+5511999999999","message":"oi","message_id":"test-001"}'`
   → esperado: `status: needs_auth` com `authorize_url` apontando para `mcp-api-tavola-v3.clicksign.com`.
4. Abrir o `authorize_url` no browser, completar login Clicksign.
5. Browser cai em `/oauth2/callback` → `success.html`. Verificar nos logs: `oauth_callback_account_selected`.
6. `curl -i -X POST ... -d '{"phone_number":"+5511999999999","message":"liste meus envelopes","message_id":"test-002"}'`
   → esperado: `status: ok` com `reply` listando envelopes reais e `tool_calls: [{name:"list_envelopes", ok:true}]`.

### 12.2 Teste de refresh

Forçar `Session.AccessToken` para um valor inválido em runtime (ou esperar expirar) e repetir o passo 6. Esperado: log `oauth_refreshed` aparece e a resposta chega normalmente.

### 12.3 Validar acionamento da feature de WhatsApp da Clicksign

O motivador original do pivot. Após executar uma ação que dispare notificação (ex.: `notify_all_signers` em um envelope com signatário cujo método de notificação seja WhatsApp), confirmar o disparo.

---

## 13. Riscos e mitigações

| Risco | Probabilidade | Impacto | Mitigação |
|---|---|---|---|
| DCR não habilitado em prod (Fase 0.1 falha) | Média | Alto | Pedir client estático à infra; cair para `client_id` configurado por env |
| Scope insuficiente no JWT (Fase 0.2 falha com 403) | Baixa | Médio | Adicionar scopes faltantes em `MCP_OAUTH_SCOPES` (ex.: `signatures/read`, `templates/write`) |
| Usuário com múltiplas contas e a primeira é a errada | Média | Médio | MVP: primeira conta. Pós-MVP: tool `select_account` |
| `Content-Type` errado em endpoints `vnd.api+json` | Alta | Médio | Replicar exatamente o que o MCP server usa (vide tabela 6.3); cobrir com teste unitário |
| Bug no refresh (re-execução de body já consumido) | Alta | Alto | Sempre serializar `[]byte` e criar novo `bytes.NewReader` no closure de retry (vide 6.2) |
| Latência da API REST > timeout configurado | Baixa | Médio | Default 30s; bump pra 60s se necessário |
| `tools` estáticas com schema divergente do que a Clicksign aceita | Média | Médio | Validar cada tool no smoke test antes do demo |

---

## 14. Stretch goals (se sobrar tempo)

- **Tool `select_account`**: paridade com o MCP server quando o usuário tem múltiplas contas.
- **Caching de `ListOAuth2Accounts`**: salvar contas em memória atreladas ao phone, para permitir trocar conta depois.
- **Streaming/SSE para tools longas**: nada disso era suportado via MCP `tools/call` anyway, então não é regressão.
- **Métricas Prometheus**: `clicksign_api_request_duration_seconds{path,method,status}`.

---

## 15. Checklist consolidado

- [ ] **Fase 0.** Validar DCR em prod com curl.
- [ ] **Fase 0.** Validar JWT em `/oauth2/accounts` com curl.
- [ ] **Fase 0.** Validar `/.well-known/...` em prod.
- [ ] **Fase 1.** `internal/config/config.go` + `.env.example` + `.env` com `CLICKSIGN_API_BASE_URL` e `MCP_SERVER_BASE_URL=prod`.
- [ ] **Fase 2.** `internal/clicksign/client.go` com `doOnce` + refresh.
- [ ] **Fase 2.** `internal/clicksign/types.go` com structs principais (incluindo `EnvelopeBulkCreationRequest` e tipos `Bulk*`).
- [ ] **Fase 2.** Endpoints MVP: `ListOAuth2Accounts`, `ListEnvelopes`, `ListTemplates`, `GetTemplateFields`, `CreateEnvelopeBulkCreation`.
- [ ] **Fase 2.** `internal/clicksign/file_fetcher.go` com download HTTPS + validação MIME + proteção SSRF.
- [ ] **Fase 2.** Testes com `httptest.Server` (incluindo cenários do FileFetcher).
- [ ] **Fase 3.** `internal/tools/runner.go` + `StaticRunner`.
- [ ] **Fase 3.** `internal/tools/catalog.go` com as 6 tools: `list_envelopes`, `list_templates`, `get_template_fields`, `create_envelope_with_template`, `create_envelope_with_file_url`, `select_account`.
- [ ] **Fase 3.** `internal/tools/schemas.go` + `internal/tools/bulk_builder.go` com helpers compartilhados pelas duas tools `create_envelope_with_*`.
- [ ] **Fase 4.** `internal/api/oauth_handler.go` chama `ListOAuth2AccountsWithToken` no callback e popula `Session.AccountKey` com a 1ª conta.
- [ ] **Fase 5.** `internal/llm/openai.go` usa `tools.Runner` em vez de `mcpclient.Manager`.
- [ ] **Fase 5.** `internal/llm/meta.go` usa `tools.Runner`.
- [ ] **Fase 6.** `cmd/server/main.go` instancia `clicksignClient` + `toolRunner`, passa para `oauth_handler` e `Conversation`.
- [ ] **Fase 7.** `internal/mcpclient/` removido. `go mod tidy`.
- [ ] **E2E.** Smoke test 12.1 passa em ambiente com ngrok.
- [ ] **E2E.** Notificação WhatsApp confirmada disparando no celular.

---

## 16. Estimativa de esforço

| Fase | Tempo otimista | Tempo realista |
|---|---|---|
| 0. Validação | 1h | 2h |
| 1. Config | 15min | 30min |
| 2. Cliente REST + FileFetcher | 4h | 6h |
| 3. Catálogo tools (6 tools) | 3h30 | 5h30 |
| 4. Account selection | 1h | 2h |
| 5. Refactor LLM | 1h | 3h |
| 6. Wire-up | 30min | 1h |
| 7. Cleanup | 15min | 30min |
| E2E + ajustes | 2h | 4h |
| **Total** | **~13h30** | **~24h30** |

Cabe em 2 dias de hackathon razoavelmente bem, com 1 dia de buffer. **Atalho possível:** se o demo não precisa de `create_envelope_with_file_url`, pula-se o `FileFetcher` (-2h) e fica em ~11h otimista.

---

## 17. Por que não a Opção B?

A `IMPLEMENTATION_PLAN_OPTION_B.md` propõe uma reescrita arquitetural (NLU + state machines + interactive WhatsApp messages). Ela é melhor estrategicamente, mas:

- Estimativa muito maior (~3-5 dias).
- Reescreve `internal/llm` inteiro, perdendo o investimento atual no loop de tool-calling que já funciona.
- Não é o problema crítico do hackathon: o problema é **alcançar a feature de notificação WhatsApp que só existe em prod**. A Opção C resolve isso em ~1 dia.

**Recomendação:** Opção C agora; Opção B pós-hackathon.
