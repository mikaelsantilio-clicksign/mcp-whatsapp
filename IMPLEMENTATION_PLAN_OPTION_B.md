# Plano de Implementação — Opção B: NLU + Fluxo Guiado + API REST

> Reescrita arquitetural do `whatsapp-mcp` para uma abordagem híbrida onde a LLM atua apenas como NLU (extrator de intenção/entidades), e o fluxo é controlado por máquinas de estado tipadas que chamam diretamente a **API REST da Clicksign**. Substitui o pipeline atual (LLM + MCP com tool-calling) por algo mais determinístico, mais rápido e com UX rica no WhatsApp (interactive messages).

---

## 1. Motivação

### 1.1 Problemas do design atual (Opção A — LLM + MCP)

- **Latência alta**: ~10s por interação em fluxos com 2 tool calls. Cada round-trip do LLM custa 1-2s; multi-conta facilmente roda 3-4 rounds.
- **UX pobre no WhatsApp**: tudo vira texto longo com markdown que não renderiza. Usuário precisa digitar respostas que poderiam ser cliques.
- **Risco de alucinação**: o LLM esquece `account_key`, mapeia opção numérica errada, formata markdown ignorando system prompt.
- **Custo OpenAI proporcional ao texto gerado**: respostas longas consomem tokens; histórico cresce.
- **Acoplamento ao loop de tool-calling**: erros de MCP (multi-conta, timeout) chegam ao LLM em texto bruto e ele tenta interpretar/parsear de novo.

### 1.2 O que a Opção B muda

- **LLM perde o protagonismo**: vira um extrator de `{intent, entities}` chamado **uma vez por mensagem**. Sem loop, sem tool-calling.
- **Código volta a comandar o fluxo**: cada intent vira uma **máquina de estados** em Go, com validações, pré-condições e mensagens humanizadas codificadas.
- **API REST direta**: troca o MCP server por um cliente HTTP típico contra a API da Clicksign. Menos um hop, mais controle.
- **Resposta estruturada ao n8n**: novo campo `interactive` no `MessageResponse` permite ao n8n renderizar **list messages** e **quick replies** nativos do WhatsApp.

---

## 2. Decisões arquiteturais

| Decisão | Escolha | Justificativa |
|---|---|---|
| Onde decide o próximo passo? | Código (state machine) | Determinismo, testabilidade, validações fortes |
| Quem fala com Clicksign? | Cliente REST próprio (`internal/clicksign`) | Menos um hop; controle de cache/retry/timeout |
| Função da LLM? | Extrair intent + entities (JSON schema) | Resolve só a parte caótica (linguagem natural) |
| Onde mora o estado da conversa? | `session.Session.ActiveFlow` (FlowState) | Persistente, sobrevive a reconexão, fácil de testar |
| Como o WhatsApp recebe escolhas? | n8n interpreta `interactive` e manda list/buttons | UX nativa, zero ambiguidade |
| Histórico de mensagens? | Mantém últimas 2-4 turns no NLU (não no fluxo) | NLU se beneficia de contexto leve; fluxo é stateful por outro caminho |
| OAuth/DCR/PKCE? | Mantém | Já funciona; não dependente de MCP |
| Classifier on/off-topic? | Mantém | Gate de custo continua valioso |
| MetaHelp responder? | Mantém | Saudações continuam sendo bem servidas por LLM cheap |

---

## 3. Arquitetura

```text
WhatsApp user
   │
   ▼
n8n (WhatsApp trigger; n8n decide se manda texto, list ou botões)
   │  POST /api/messages
   │  Authorization: Bearer <API_STATIC_TOKEN>
   │  { phone_number, message?, interactive_reply?, attachments?, message_id }
   ▼
┌─────────────────────────────────────────────────────────────────┐
│ whatsapp-mcp (renomeado conceitualmente: whatsapp-clicksign-bot)│
│                                                                 │
│  [1] middleware: idempotência + bearer + log + req_id           │
│  [2] sessão: existe?                                            │
│       ├── não → needs_auth (PKCE + shortlink — igual hoje)      │
│       └── sim → continua                                        │
│  [3] gate: classifier (meta_help / off_topic / on_topic)        │
│       ├── meta_help → MetaHelpResponder (mantém)                │
│       ├── off_topic → OffTopic() estático (mantém)              │
│       └── on_topic → continua                                   │
│  [4] short-circuit: interactive_reply presente?                 │
│       ├── sim → carrega FlowState e injeta entity direto        │
│       └── não → NLU LLM extrai {intent, entities}               │
│  [5] Router → flow := flows[intent]                             │
│  [6] flow.Handle(ctx, input) → Result                           │
│  [7] Persiste FlowState atualizado em session                   │
│  [8] Serializa Result em MessageResponse                        │
└─────────────────────────────────────────────────────────────────┘
   │
   ▼
n8n recebe { reply, interactive?, flow_state? }
   ├── se interactive.type == "list"   → WhatsApp list message
   ├── se interactive.type == "buttons" → WhatsApp quick reply buttons
   └── senão                            → mensagem de texto simples
```

---

## 4. Estrutura de pastas

```text
internal/
├── api/
│   ├── messages_handler.go      [REESCRITO]   pipeline novo
│   ├── oauth_handler.go         [MANTÉM]
│   ├── shortlink_handler.go     [MANTÉM]
│   ├── health_handler.go        [MANTÉM]
│   ├── middleware.go            [MANTÉM]
│   ├── idempotency.go           [MANTÉM]
│   ├── embed.go                 [MANTÉM]
│   └── errors.go                [REESCRITO]   MessageResponse com Interactive/FlowState/Trace
├── classifier/                  [MANTÉM]      gate meta_help/off_topic continua útil
├── llm/
│   ├── meta.go                  [MANTÉM]      MetaHelpResponder
│   ├── nlu.go                   [NOVO]        extrator de intent + entities
│   ├── replies.go               [SIMPLIFICA]  só strings curtas (OffTopic, AuthRequired)
│   ├── prompts.go               [AJUSTA]      embeds: system.md (removido), classifier.md, meta_help.md, nlu.md
│   └── prompts/
│       ├── classifier.md        [MANTÉM]
│       ├── meta_help.md         [MANTÉM]
│       ├── nlu.md               [NOVO]
│       └── system.md            [REMOVIDO]    o LLM principal sumiu
├── clicksign/                   [NOVO]
│   ├── client.go                cliente HTTP com bearer + refresh
│   ├── accounts.go              GET /accounts, POST /select_account
│   ├── templates.go             GET /templates, GET /templates/{id}
│   ├── envelopes.go             GET /envelopes, POST /envelopes, GET /envelopes/{id}, POST /cancel
│   ├── signers.go               POST /signers, GET /signers
│   └── types.go                 Account, Template, Envelope, Signer, etc.
├── flow/                        [NOVO]
│   ├── flow.go                  interface Flow + tipos Result/Kind
│   ├── router.go                Router{flows map[string]Flow}.Handle()
│   ├── state.go                 FlowState (persistido)
│   ├── list_templates.go        flow simples (uma escolha de conta + listagem)
│   ├── list_envelopes.go
│   ├── envelope_status.go
│   ├── select_account.go        flow explícito; também usado por outros flows
│   ├── create_envelope_tmpl.go  multi-step a partir de template
│   ├── create_envelope_pdf.go   multi-step a partir de PDF
│   ├── add_signer.go
│   └── cancel_envelope.go
├── session/
│   ├── store.go                 [AJUSTA]      Session ganha ActiveFlow + PreferredAccount
│   └── memory.go                [AJUSTA]      deep-copy dos novos campos
├── oauth/                       [MANTÉM]      reusado pra autenticar API REST
├── mcpclient/                   [REMOVIDO]    ou atrás de feature flag durante migração
├── n8n/                         [MANTÉM]
├── logging/                     [MANTÉM]
└── config/                      [AJUSTA]      novas envs CLICKSIGN_API_*

cmd/
└── server/
    └── main.go                  [REESCRITO]   wiring novo
```

---

## 5. Contratos de API

### 5.1 `POST /api/messages` — Request

```json
{
  "phone_number": "5511999998888",
  "message": "liste meus templates",
  "interactive_reply": {
    "list_item_id": "942c35a4-0acb-45c6-ad06-0ef89e9bdaeb"
  },
  "attachments": [
    { "url": "https://...", "mime_type": "application/pdf", "filename": "contrato.pdf" }
  ],
  "message_id": "wamid.0042",
  "conversation_id": "..."
}
```

Regras:
- `message` ou `interactive_reply` deve estar presente (não os dois simultaneamente em produção, mas tolerar).
- `interactive_reply.list_item_id` (escolha em list message) ou `interactive_reply.button_id` (quick reply).

### 5.2 `POST /api/messages` — Response (4 formatos)

#### 5.2.1 `needs_auth` (igual hoje)

```json
{
  "status": "needs_auth",
  "reply": "Pra continuar, faz login aqui: https://.../c/<token>\nO link é pessoal e expira em 5 minutos.",
  "authorize_url": "https://mcp-api-tavola-v3-6.clicksign.dev/oauth2/authorize?..."
}
```

#### 5.2.2 `ok` — texto puro (ex.: meta_help, off_topic, status simples)

```json
{
  "status": "ok",
  "reply": "Boa tarde! Posso te ajudar com... [resposta natural]",
  "trace": [
    { "kind": "classifier", "name": "intent_gate", "ok": true, "duration": "320ms" }
  ]
}
```

#### 5.2.3 `ok` — list message

```json
{
  "status": "ok",
  "reply": "Você tem múltiplas contas Clicksign. Escolha uma:",
  "interactive": {
    "type": "list",
    "header": "Escolha sua conta",
    "body": "Toque pra continuar",
    "items": [
      { "id": "1b34...", "title": "Carlos Mikael Nunes Santilio", "description": "Conta pessoal" },
      { "id": "5b8a...", "title": "name LTDA", "description": "" },
      { "id": "942c...", "title": "Integration LTDA", "description": "" }
    ]
  },
  "flow_state": { "flow_id": "list_templates", "step": "awaiting_account" },
  "trace": [
    { "kind": "nlu", "name": "extract_intent", "ok": true, "duration": "420ms" },
    { "kind": "api_call", "name": "GET /api/v3/accounts", "ok": true, "duration": "320ms" }
  ]
}
```

#### 5.2.4 `ok` — quick reply buttons (até 3)

```json
{
  "status": "ok",
  "reply": "Confirma criar o envelope \"Contrato STG 1\" com 1 signatário?",
  "interactive": {
    "type": "buttons",
    "body": "Confirma criar o envelope?",
    "items": [
      { "id": "confirm_yes", "title": "Sim, enviar" },
      { "id": "confirm_no",  "title": "Cancelar" }
    ]
  },
  "flow_state": { "flow_id": "create_envelope_tmpl", "step": "awaiting_confirm" }
}
```

### 5.3 Tipos Go

```go
type MessageRequest struct {
    PhoneNumber      string             `json:"phone_number"`
    Message          string             `json:"message,omitempty"`
    InteractiveReply *InteractiveReply  `json:"interactive_reply,omitempty"`
    Attachments      []Attachment       `json:"attachments,omitempty"`
    MessageID        string             `json:"message_id,omitempty"`
    ConversationID   string             `json:"conversation_id,omitempty"`
}

type InteractiveReply struct {
    ListItemID string `json:"list_item_id,omitempty"`
    ButtonID   string `json:"button_id,omitempty"`
}

type MessageResponse struct {
    Status       string              `json:"status"`
    Reply        string              `json:"reply"`
    AuthorizeURL string              `json:"authorize_url,omitempty"`
    Interactive  *InteractivePayload `json:"interactive,omitempty"`
    FlowState    *FlowStateDigest    `json:"flow_state,omitempty"`
    Trace        []TraceStep         `json:"trace,omitempty"`
    Error        *ErrorBody          `json:"error,omitempty"`
}

type InteractivePayload struct {
    Type   string            `json:"type"`  // "list" | "buttons"
    Header string            `json:"header,omitempty"`
    Body   string            `json:"body,omitempty"`
    Footer string            `json:"footer,omitempty"`
    Items  []InteractiveItem `json:"items"`
}

type InteractiveItem struct {
    ID          string `json:"id"`
    Title       string `json:"title"`
    Description string `json:"description,omitempty"`
}

type FlowStateDigest struct {
    FlowID  string    `json:"flow_id"`
    Step    string    `json:"step"`
    AskedAt time.Time `json:"asked_at"`
}

type TraceStep struct {
    Kind     string `json:"kind"`     // "classifier" | "nlu" | "api_call" | "flow_decision"
    Name     string `json:"name"`
    OK       bool   `json:"ok"`
    Duration string `json:"duration,omitempty"`
    Err      string `json:"err,omitempty"`
}
```

---

## 6. NLU LLM — extrator de intenção

### 6.1 Prompt (`internal/llm/prompts/nlu.md`, esboço)

```
Você é um extrator de intenções para o assistente Clicksign no WhatsApp.

Dada a MENSAGEM ATUAL e o CONTEXTO RECENTE (últimas 2-4 turns), produza UM JSON
estritamente seguindo o schema fornecido.

INTENTS suportados:
- list_templates        ()
- list_envelopes        (filter_status?)
- envelope_status       (envelope_id?, envelope_name?)
- create_envelope_tmpl  (template_id?, template_name?, envelope_name?, signers?)
- create_envelope_pdf   (pdf_url?, envelope_name?, signers?)
- add_signer            (envelope_id?, signer_name?, signer_email?, role?)
- select_account        (account_key?, account_name?, account_index?)
- cancel_envelope       (envelope_id?)
- unknown

`signers`: array de objetos { name, email, role } onde role ∈ {"sign","witness","party","approve"}.

REGRAS:
- Extraia APENAS o que está EXPLICITAMENTE na mensagem. Nunca invente IDs, emails ou nomes.
- Se a mensagem cita um índice numérico ("use a conta 3"), preencha `account_index` e deixe os outros nulos.
- Se a mensagem é uma resposta muito curta a uma pergunta do bot (sim/não), use intent=unknown e o caller resolverá pelo flow_state.
- Confidence: "high" quando intent e entities óbvios; "low" quando ambíguo.
```

### 6.2 Chamada OpenAI

- Modelo: `gpt-4o-mini`.
- `response_format: json_schema` com strict schema.
- `temperature: 0` (determinístico).
- `max_completion_tokens: 200`.

### 6.3 Schema JSON do output

```json
{
  "type": "object",
  "properties": {
    "intent": { "type": "string", "enum": ["list_templates", "list_envelopes", "envelope_status", "create_envelope_tmpl", "create_envelope_pdf", "add_signer", "select_account", "cancel_envelope", "unknown"] },
    "entities": {
      "type": "object",
      "properties": {
        "account_key":    { "type": ["string", "null"] },
        "account_index":  { "type": ["integer", "null"] },
        "envelope_id":    { "type": ["string", "null"] },
        "envelope_name":  { "type": ["string", "null"] },
        "template_id":    { "type": ["string", "null"] },
        "template_name":  { "type": ["string", "null"] },
        "pdf_url":        { "type": ["string", "null"] },
        "filter_status":  { "type": ["string", "null"] },
        "signers":        { "type": ["array", "null"], "items": { "type": "object" } }
      }
    },
    "confidence": { "type": "string", "enum": ["high", "medium", "low"] }
  },
  "required": ["intent", "entities", "confidence"]
}
```

---

## 7. Camada de Flows — state machines

### 7.1 Interface

```go
// internal/flow/flow.go
package flow

type Input struct {
    Phone     string
    Session   *session.Session
    Intent    string
    Entities  map[string]any
    State     *FlowState         // nil se for o início do flow
    Interact  *InteractiveReply  // se a turn veio de um clique
    Attaches  []Attachment
}

type Kind int

const (
    KindAsk      Kind = iota // perguntar texto livre (ex.: "qual o nome do envelope?")
    KindChoose                // lista de opções (list message)
    KindConfirm               // sim/não (buttons)
    KindDone                  // operação concluída
    KindError                 // erro irrecuperável
    KindTransfer              // transfere controle pra outro flow (ex.: dispara select_account)
)

type Result struct {
    Kind        Kind
    Reply       string
    Interactive *InteractivePayload
    NextState   *FlowState
    NextIntent  string             // só pra Kind=KindTransfer
    Trace       []TraceStep
}

type Flow interface {
    ID() string
    Handle(ctx context.Context, in Input) (Result, error)
}
```

### 7.2 Router

```go
// internal/flow/router.go
type Router struct {
    flows map[string]Flow
}

func (r *Router) Handle(ctx context.Context, in Input) (Result, error) {
    flowID := in.Intent
    if in.State != nil {
        flowID = in.State.FlowID  // se um flow está aberto, ele tem prioridade
    }
    f, ok := r.flows[flowID]
    if !ok {
        return Result{Kind: KindError, Reply: "Desculpe, não entendi. Tente perguntar de outra forma."}, nil
    }
    return f.Handle(ctx, in)
}
```

### 7.3 FlowState

```go
// internal/flow/state.go
type FlowState struct {
    FlowID    string         `json:"flow_id"`
    Step      string         `json:"step"`
    Data      map[string]any `json:"data,omitempty"`   // dados parciais acumulados
    StartedAt time.Time      `json:"started_at"`
    UpdatedAt time.Time      `json:"updated_at"`
}
```

### 7.4 Exemplo: `ListTemplatesFlow`

```go
// internal/flow/list_templates.go
type ListTemplatesFlow struct {
    cs *clicksign.Client
}

func (f *ListTemplatesFlow) ID() string { return "list_templates" }

func (f *ListTemplatesFlow) Handle(ctx context.Context, in Input) (Result, error) {
    // 1) Já temos conta selecionada (cached) ou interactive_reply trouxe?
    accountKey := in.Session.PreferredAccount
    if in.Interact != nil && in.Interact.ListItemID != "" && in.State != nil && in.State.Step == "awaiting_account" {
        accountKey = in.Interact.ListItemID
    }

    if accountKey == "" {
        // Transfer pro flow de seleção de conta, dizendo que depois deve voltar.
        return Result{
            Kind:       KindTransfer,
            NextIntent: "select_account",
            NextState: &FlowState{
                FlowID: "select_account",
                Step:   "starting",
                Data:   map[string]any{"return_to": "list_templates"},
            },
        }, nil
    }

    // 2) Conta selecionada — lista templates direto.
    templates, err := f.cs.ListTemplates(ctx, in.Phone, accountKey)
    if err != nil {
        return Result{Kind: KindError, Reply: "Não consegui listar os templates agora. Tenta de novo em alguns segundos."}, err
    }

    // 3) Persiste a conta como preferida.
    in.Session.PreferredAccount = accountKey

    items := make([]InteractiveItem, 0, len(templates))
    for _, t := range templates {
        items = append(items, InteractiveItem{
            ID: t.ID, Title: t.Name, Description: t.CreatedAt.Format("02/01/2006"),
        })
    }
    return Result{
        Kind:  KindChoose,
        Reply: "Templates da sua conta:",
        Interactive: &InteractivePayload{
            Type: "list",
            Body: "Toque em um template para começar um envelope, ou diga \"voltar\" para sair.",
            Items: items,
        },
        NextState: nil, // fluxo concluído
    }, nil
}
```

### 7.5 Tabela de flows MVP

| Flow | Steps | Complexidade |
|---|---|---|
| `select_account` | `starting → awaiting_choice → done` | Baixa |
| `list_templates` | `starting → (transfer select_account) → done` | Baixa |
| `list_envelopes` | `starting → (filtros opcionais) → done` | Baixa |
| `envelope_status` | `starting → awaiting_envelope_id → done` | Média |
| `create_envelope_tmpl` | `starting → pick_template → pick_signers → confirm → done` | Alta |
| `create_envelope_pdf` | `starting → upload_pdf → pick_signers → confirm → done` | Alta |
| `add_signer` | `starting → pick_envelope → collect_data → confirm → done` | Média |
| `cancel_envelope` | `starting → pick_envelope → confirm → done` | Baixa |

---

## 8. Cliente Clicksign REST — endpoints e contratos REAIS

> **Fonte autoritativa**: clonei o repo `clicksign/mcp-api-tavola-v3` (o MCP server é um proxy fino sobre a API REST da Clicksign) e extraí endpoints, headers, content-types e payloads diretamente do código. Os tipos e o cliente HTTP podem ser **portados quase sem mudanças** para acelerar a implementação.

### 8.1 Configuração base

- **Base URL (produção)**: `https://app.clicksign.com/api/v3`
- **Base URL (staging)**: subdomínio equivalente (mesmo path)
- **Formato de resposta**: JSON:API spec (`{"data": [...]}` em listas, `{"data": {...}}` em singletons)
- **Headers em toda chamada autenticada**:
  - `Authorization: Bearer <access_token>`
  - `X-Account-Key: <account_key>` ← **CRÍTICO**: usuários multi-conta precisam disso em **toda** chamada. Sem ele, a API rejeita.
- **Content-Type — varia por endpoint**:
  - `application/json` → envelopes legados, signers, envelope bulk creations
  - `application/vnd.api+json` → templates (criar/editar) e notificações
  - `Accept: application/json` em todas

### 8.2 Catálogo de endpoints (extraído de `internal/clicksign/client.go` do MCP)

| Operação | Método | Path | Content-Type | Observação |
|---|---|---|---|---|
| Validar token | GET | `/users/me` | — | Checagem rápida pós-OAuth. |
| **Listar contas OAuth2** | GET | `/oauth2/accounts` | — | Retorna `[OAuth2Account{id, attributes:{name, key}}]`. Não exige `X-Account-Key`. |
| Listar templates | GET | `/templates` | — | JSON:API list. |
| Campos de um template | GET | `/templates/{id}/template_fields` | — | Variáveis para interpolar no envelope. |
| Criar template | POST | `/templates` | `application/vnd.api+json` | Body `{"data":{"type":"templates","attributes":{...}}}`. |
| Editar template | PATCH | `/templates/{id}` | `application/vnd.api+json` | |
| Deletar template | DELETE | `/templates/{id}` | — | |
| Listar envelopes | GET | `/envelopes` | — | |
| Detalhe envelope | GET | `/envelopes/{id}` | — | |
| Listar documentos do envelope | GET | `/envelopes/{id}/documents` | — | |
| Detalhe documento | GET | `/envelopes/{id}/documents/{did}` | — | |
| Criar envelope (modo split, legado) | POST | `/envelopes` | `application/json` | Body `{"name":"..."}`. Preferir bulk_creations. |
| Adicionar documento ao envelope | POST | `/envelopes/{id}/documents` | `application/json` | |
| Adicionar signatário | POST | `/envelopes/{id}/signers` | `application/json` | Body `{"email":"..."}` no fluxo split. |
| Disponibilizar para assinatura | POST | `/envelopes/{id}/available` | — | Coloca envelope em `running`. |
| Notificar todos signatários | POST | `/envelopes/{id}/notifications` | `application/vnd.api+json` | |
| Notificar signatário individual | POST | `/envelopes/{id}/signers/{sid}/notifications` | `application/vnd.api+json` | |
| **Criar envelope completo (RECOMENDADO)** | POST | `/envelope_bulk_creations` | `application/json` | Envelope + documento (template **ou** PDF base64) + signers + notifications em **uma única chamada**. |

> **Pendência detectada**: endpoint específico de **cancelar envelope** não está no cliente do MCP. Validar com docs públicas (`developers.clicksign.com`) antes da Fase 4.

### 8.3 Padrão `envelope_bulk_creations` (caminho feliz pro `create_envelope_*`)

O MCP usa este endpoint como caminho principal das tools `create_envelope_with_template` e `create_envelope_with_file_url`. É bulk porque encapsula tudo num payload só.

Esqueleto do body (referência: `EnvelopeBulkCreationRequest` em `internal/clicksign/types.go` do MCP):

```json
{
  "data": {
    "type": "envelope_bulk_creations",
    "attributes": {
      "envelope": {
        "name": "Contrato Stg WhatsApp 1",
        "default_subject": "Assinatura do Contrato",
        "locale": "pt-BR",
        "auto_close": true,
        "remind_interval": 3,
        "block_after_refusal": false,
        "metadata": { "source": "whatsapp-bot" }
      },
      "document": {
        "filename": "Contrato.docx",
        "metadata": { "source": "whatsapp-bot" },
        "template": {
          "key": "d782aa2e-146f-44a8-b31c-60c107896bd1",
          "data": { "nome_contratado": "João Silva", "valor": "1.000" }
        }
        /* OU, no caso de PDF a partir de URL: substituir o bloco "template" por:
           "content_base64": "data:application/pdf;base64,JVBERi0xLjQK...",
           "filename": "Contrato.pdf"
           (formato data URI completo, com prefixo "data:<mime>;base64," — NÃO base64 puro)
        */
      },
      "signers": [
        {
          "name": "Mikael Nunes",
          "email": "mikael@x.com",
          "location_required_enabled": false,
          "has_documentation": true,
          "refusable": false,
          "requirements": [
            { "action": "agree", "role": "party" },
            { "action": "provide_evidence", "auth": "email" }
          ]
        }
      ],
      "notifications": { "message": "Por favor, assine o contrato." }
    }
  }
}
```

**Regras críticas de signatário** (resumo de `docs/PAYLOAD_SIGNERS.md`):

- `name` obrigatório, com nome **e** sobrenome, sem números.
- Cada signer precisa de **dois `requirements`**: um `{action:"agree", role:"<papel>"}` (qualificação) e um `{action:"provide_evidence", auth:"<método>"}` (autenticação).
- `email` obrigatório se `auth=email`; `phone_number` (10–11 dígitos) se `auth=sms` ou `auth=whatsapp`.
- Roles canônicos mais comuns: `party`, `sign`, `witness`, `approve`. Existe um glossário PT→EN com 50+ termos em `internal/mcp/validation.go` do MCP — vale **portar tal qual**.
- `auth=presential` **não** é aceito pela API.
- Se `has_documentation=false`, não enviar `documentation` nem `birthday`.

**Erros frequentes da API** (extraídos de `docs/PAYLOAD_ENVELOPE_DOCUMENT.md`):

| Erro retornado | Causa | Mitigação no flow |
|---|---|---|
| `remind_interval deve ser maior que 0` | `remind_interval=0` ou negativo | Default `3`; omitir se zero. |
| `source campo é obrigatório` (envelope/document) | metadata sem `source` | Sempre setar `metadata.source = "whatsapp-bot"`. |
| `filename deve ter formato válido (.doc ou .docx)` | extensão errada | Validar antes (template aceita só doc/docx; file_url aceita pdf/jpg/png/txt/doc/docx). |
| `data deve ser um objeto` | `document.template.data` ausente ou não-objeto | Sempre enviar objeto (mesmo vazio: `{}`). |

### 8.4 Multi-conta — detecção e tratamento determinístico

No MCP server, quando o `X-Account-Key` não foi enviado e o usuário tem múltiplas contas, a Clicksign devolve erro e o MCP transforma em mensagem human-readable. **No Option B fazemos isso sem LLM**:

1. Flow tenta a operação real (ex.: `GET /templates`) sem `X-Account-Key`.
2. Cliente HTTP detecta erro tipo `ErrMultiAccount` (status 4xx + payload específico).
3. Flow chama `GET /oauth2/accounts` para obter a lista.
4. Devolve `Result{Kind: KindChoose, Interactive: ListPayload(contas)}`.
5. Usuário clica → `interactive_reply.list_item_id = "<account_key>"` → persiste `sess.PreferredAccount` → re-executa a operação original.

A partir daí, **toda chamada** do cliente HTTP envia `X-Account-Key: sess.PreferredAccount`.

### 8.5 Esqueleto do cliente Go (adaptado direto do MCP)

```go
// internal/clicksign/client.go
type Client struct {
    httpc   *http.Client
    baseURL string
    logger  *slog.Logger
    store   session.Store
    oauth   *oauth.Client
}

func (c *Client) do(ctx context.Context, phone, method, path, contentType string, body io.Reader) ([]byte, error) {
    sess, err := c.store.GetSession(ctx, phone)
    if err != nil { return nil, conv.ErrSessionExpired }

    req, _ := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
    req.Header.Set("Authorization", "Bearer "+sess.AccessToken)
    req.Header.Set("Accept", "application/json")
    if sess.PreferredAccount != "" {
        req.Header.Set("X-Account-Key", sess.PreferredAccount)
    }
    if contentType != "" { req.Header.Set("Content-Type", contentType) }

    resp, err := c.httpc.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()
    raw, _ := io.ReadAll(resp.Body)

    if resp.StatusCode == 401 {
        if err := c.refresh(ctx, phone); err != nil { return nil, conv.ErrSessionExpired }
        return c.do(ctx, phone, method, path, contentType, body) // retry uma vez
    }
    if isMultiAccountErr(resp.StatusCode, raw) {
        return nil, ErrMultiAccount{Body: raw}
    }
    if resp.StatusCode >= 400 {
        return nil, decodeError(resp.StatusCode, raw)
    }
    return raw, nil
}

// Métodos tipados: ListAccounts, ListTemplates, GetTemplateFields,
// CreateEnvelopeBulk, ListEnvelopes, GetEnvelope, NotifyEnvelope, etc.
```

### 8.6 Reaproveitamento direto do mcp-api-tavola-v3

A maior alavanca de produtividade: **portar arquivos quase como estão** do MCP server.

| Arquivo do MCP (`clicksign/mcp-api-tavola-v3`) | Destino no nosso projeto | Mudanças necessárias |
|---|---|---|
| `internal/clicksign/types.go` | `internal/clicksign/types.go` | **Zero**. Copiar tal qual — DTOs JSON:API (Envelope, Document, Signer, Template, OAuth2Account, EnvelopeBulkCreation*, etc.). |
| `internal/clicksign/client.go` | `internal/clicksign/client.go` | **Pequenas**: remover `OAuth2Credentials` encode/parse (usaremos `session.Session` direto); adicionar `refresh` chamando nosso `oauth.Client`. |
| `internal/clicksign/file_fetcher.go` | `internal/clicksign/file_fetcher.go` | **Zero ou pequenas**. Implementa download de URL com SSRF protection — útil pro `create_envelope_with_file_url`. |
| `internal/mcp/validation.go` (regras de signer + glossário PT→EN de roles) | `internal/clicksign/validation.go` | **Médias**: remover dependências MCP, manter `NormalizeRole`, validação de CPF/email/phone, regras de requirements. |
| `docs/PAYLOAD_SIGNERS.md` + `PAYLOAD_ENVELOPE_DOCUMENT*.md` | `docs/` ou `internal/clicksign/prompts/` | **Zero**. Referência viva pro NLU prompt e pra testes. |

> Atalho prático: criar uma pasta `internal/clicksign/` no nosso projeto e fazer `cp` dos quatro arquivos do MCP, ajustando imports.

### 8.7 Upload de PDF/imagem via URL (caminho B do bulk creation)

> **Quando usar**: usuário do WhatsApp envia uma URL pública (ex.: `https://storage.empresa.com/contrato.pdf`) em vez de escolher um template. O backend baixa o arquivo, valida e converte em **data URI base64** antes de enviar para a Clicksign.

**O LLM nunca vê o binário.** Ele só recebe a URL como string no payload da mensagem. O download e a codificação acontecem no Flow `create_envelope_pdf`, depois do NLU.

#### Pipeline

```
[WhatsApp]  →  URL como string
   ↓
[NLU LLM]   →  extrai { intent: "create_envelope", document.file_url: "..." }
   ↓
[Flow create_envelope_pdf]
   ├─ 1. FileFetcher.Fetch(url) — baixa server-side
   ├─ 2. Validações: HTTPS, SSRF guard, MIME allowlist, tamanho ≤ 20MB
   ├─ 3. data URI: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(bytes)
   ├─ 4. Monta BulkDocument { ContentBase64: dataURI, Filename: derivado da URL }
   └─ 5. POST /envelope_bulk_creations
```

#### Regras (resumo de `docs/PAYLOAD_ENVELOPE_DOCUMENT_FILE.md`)

| Regra | Detalhe | Comportamento |
|---|---|---|
| **Formato** | `data:<mime>;base64,<bytes>` — data URI completo | Sem o prefixo `data:...;base64,` a API rejeita. |
| **MIME allowlist** | `application/pdf`, `image/jpeg`, `image/png`, `text/plain`, `application/msword`, `application/vnd.openxmlformats-officedocument.wordprocessingml.document` | Extensões aceitas: `.pdf`, `.jpg`, `.jpeg`, `.png`, `.txt`, `.doc`, `.docx`. |
| **Tamanho** | Default 20 MB | Configurável via `FILE_FETCHER_MAX_BYTES`. Note que o base64 expande em ~33% (`20 MB → ~27 MB no JSON`). |
| **HTTPS** | Obrigatório | Flag dev `FILE_FETCHER_ALLOW_HTTP=true` libera HTTP. |
| **SSRF guard** | Bloqueia IPs privados (`10/8`, `172.16/12`, `192.168/16`, `127/8`, `169.254/16` AWS metadata, `::1`, `fc00::/7`, `fe80::/10`) | Checagem feita também em **redirects**. |
| **filename opcional** | Quando omitido, derivado do basename da URL (decodifica `%20`, ignora query) | Se URL não tem basename útil (`/files/abc123`), erro pedindo filename explícito. |
| **MIME detection** | Lê `Content-Type` da resposta; se `application/octet-stream` ou vazio, faz sniffing nos primeiros 512 bytes | Mitiga servidores mal configurados. |

#### Custo de memória

O arquivo inteiro fica em RAM durante o encode + envio. Picos:
- Bytes originais: até 20 MB.
- Bytes base64 em memória: ~27 MB.
- Body JSON do request: ~27 MB de payload.

Aceitável pro escopo. Se quisermos otimizar no futuro, vale considerar streaming com `base64.NewEncoder`, mas requer mudar o build do JSON body também.

#### Erros frequentes que precisamos mapear pro usuário (humanizar no Flow)

| Erro técnico | Mensagem natural no WhatsApp |
|---|---|
| `scheme nao e permitido` | "Só consigo baixar arquivos com URL **https://**. Pode me mandar de novo?" |
| `SSRF bloqueado` (IP privado) | "Essa URL aponta pra um endereço interno e eu não consigo acessar. Tem um link público?" |
| `extensao nao suportada` | "Aceito apenas PDF, JPG, PNG, TXT, DOC e DOCX. Pode me mandar nesse formato?" |
| `excede o tamanho maximo` | "Esse arquivo passa do limite de 20 MB. Consegue reduzir ou compactar?" |
| `tipo de arquivo nao e suportado` (MIME sniff falhou) | "Não consegui identificar o formato do arquivo. Pode confirmar que é um PDF/JPG/PNG válido?" |
| Status HTTP != 200 ao baixar | "Não consegui baixar o arquivo dessa URL (ela voltou erro). Confere se o link continua acessível?" |

> **Atalho de implementação**: copiar `internal/clicksign/file_fetcher.go` do MCP server tal qual. São 239 LoC já testados, com SSRF guard completo. Junto, copiar a constante `allowedMimeTypes` e o `init()` que carrega os CIDRs privados.

### 8.8 Reaproveitamento de OAuth

- `internal/oauth/client.go` já tem `RefreshToken`.
- A função `refresh` no novo cliente é praticamente idêntica a `mcpclient.Manager.refresh`. Vale extrair para `internal/oauth` (`oauth.RefreshAndStore(ctx, store, phone)`) — Fase 1.

---

## 9. O que muda em `session.Session`

```go
type Session struct {
    PhoneNumber      string
    AccessToken      string
    RefreshToken     string
    ExpiresAt        time.Time
    PreferredAccount string         // NOVO: cache da conta preferida
    ActiveFlow       *FlowState     // NOVO: flow aberto (se houver)
    History          []ChatTurn     // MANTÉM: usado pelo NLU como contexto leve
    UpdatedAt        time.Time
}
```

Migração: `MemoryStore.copySession` precisa deep-copiar `ActiveFlow` (especialmente `Data map[string]any`).

---

## 10. Pipeline novo no `messages_handler.go`

Pseudo-código:

```go
func (h *MessagesHandler) Post(w, r) {
    req := decodeRequest(r)
    if h.idempotency.Seen(req.MessageID) { return }

    sess, err := h.store.GetSession(ctx, req.PhoneNumber)
    if errors.Is(err, session.ErrNotFound) {
        return h.respondNeedsAuth(...)
    }

    // [3] Gate
    if req.InteractiveReply == nil { // só classifica texto livre
        verdict, _ := h.classifier.Classify(ctx, req.Message, recent)
        if verdict.Intent == "meta_help"  { return h.metaHelp(...) }
        if verdict.Intent == "off_topic"  { return h.offTopic(...) }
    }

    // [4] Short-circuit ou NLU
    var intent string
    var entities map[string]any
    if req.InteractiveReply != nil {
        // O flow corrente já sabe o que fazer com isso
        intent = sess.ActiveFlow.FlowID
        entities = nil
    } else {
        // [4a] NLU
        nlu, err := h.nlu.Extract(ctx, req.Message, recent)
        if err != nil { return h.respondInternalError(err) }
        intent = nlu.Intent
        entities = nlu.Entities
    }

    // [5] Router
    result, err := h.router.Handle(ctx, flow.Input{
        Phone:    req.PhoneNumber,
        Session:  sess,
        Intent:   intent,
        Entities: entities,
        State:    sess.ActiveFlow,
        Interact: req.InteractiveReply,
        Attaches: req.Attachments,
    })

    // [6] Aplica estado e persiste
    sess.ActiveFlow = result.NextState
    h.store.PutSession(ctx, sess)

    // [7] Serializa
    writeJSON(w, 200, toMessageResponse(result))
}
```

---

## 11. Plano de migração faseado

Cada fase entrega valor mensurável e é mergeable. Feature flag `PIPELINE` em `cfg` controla o roteamento entre o pipeline atual (A) e o novo (B) durante a transição.

### Fase 0 — Foundation (0.5 dia)

- Adiciona campos novos em `MessageResponse` e `MessageRequest` mantendo retrocompat.
- Adiciona `PIPELINE=legacy` (default) | `flow` em config.
- Adiciona `Session.PreferredAccount` e `Session.ActiveFlow` com deep-copy.
- Build/testes: tudo continua passando em `legacy`.

### Fase 1 — Primeiro fluxo (0.5–1 dia)

- **Portar** `internal/clicksign/types.go` e `client.go` do MCP `mcp-api-tavola-v3` (ver §8.6).
- Adicionar `refresh` + métodos `ListAccounts`, `ListTemplates` (uns 30-40 LoC, o resto vem pronto).
- `internal/flow/{flow.go, router.go, state.go, list_templates.go, select_account.go}`.
- `internal/llm/nlu.go` + `prompts/nlu.md`.
- Quando `PIPELINE=flow`, `messages_handler.go` usa o pipeline novo só pros intents `list_templates` e `select_account`. Demais intents caem em `unknown` → mensagem de fallback ("estamos migrando...").

**Critério**: "liste meus templates" → list message com contas → escolhe → list de templates. Latência < 3s.

### Fase 2 — Mais consultas (0.5–1 dia)

- `internal/flow/{list_envelopes.go, envelope_status.go}`.
- Já reusa `GetEnvelopeDetails` e `ListEnvelopes` portados na Fase 1.

**Critério**: "quais envelopes estou esperando?" e "status do envelope X" funcionam.

### Fase 3 — Criação de envelope (1.5–2 dias)

- `internal/flow/{create_envelope_tmpl.go, create_envelope_pdf.go}` usando `POST /envelope_bulk_creations` (§8.3).
- Multi-step com confirm buttons.
- Portar `internal/clicksign/file_fetcher.go` (download + SSRF guard) e `validation.go` (regras de signer + glossário PT→EN de roles) do MCP.

**Critério**: criar envelope a partir de template ou PDF, com signatários, com confirmação antes de enviar.

### Fase 4 — Ações destrutivas (0.5 dia)

- `add_signer.go`, `cancel_envelope.go` (endpoint de cancel pendente — validar contra docs públicas antes).

**Critério**: cancelar envelope com confirmação obrigatória; adicionar signer a envelope existente.

### Fase 5 — Polimento + remoção do legado (0.5 dia)

- Mensagens de erro humanizadas por path (multi-account, validação de signer, etc.).
- Testes E2E (Postman).
- Remove `internal/mcpclient` (ou mantém atrás de flag para rollback rápido).
- Atualiza `README.md`, `IMPLEMENTATION_PLAN.md`.

### Total revisado: 4–5 dias

> O ganho vem do **reaproveitamento direto** do `mcp-api-tavola-v3`: types.go, client.go, file_fetcher.go e validation.go portam quase sem mudanças. A Fase 1 cai de 1 dia pra 0.5 dia, e a Fase 3 de 2 dias pra 1.5–2.

---

## 12. O que se aproveita do código atual

| Componente | Status |
|---|---|
| `internal/oauth/*` | ✅ Mantém 100% |
| `internal/session/store.go` | ✅ Mantém + 2 campos novos |
| `internal/session/memory.go` | ✅ Mantém + deep-copy dos novos campos |
| `internal/api/oauth_handler.go` | ✅ Mantém |
| `internal/api/shortlink_handler.go` | ✅ Mantém |
| `internal/api/health_handler.go` | ✅ Mantém |
| `internal/api/middleware.go` | ✅ Mantém |
| `internal/api/idempotency.go` | ✅ Mantém |
| `internal/api/embed.go` + HTML | ✅ Mantém |
| `internal/api/errors.go` | 🔄 Reescreve `MessageResponse`/`MessageRequest` |
| `internal/api/messages_handler.go` | 🔄 Reescreve pipeline |
| `internal/classifier/*` | ✅ Mantém |
| `internal/llm/meta.go` + prompts | ✅ Mantém |
| `internal/llm/openai.go` (Conversation) | ❌ Substitui por `nlu.go` |
| `internal/llm/history.go` | 🔄 Simplifica (NLU só precisa de 2-4 turns) |
| `internal/llm/prompts/system.md` | ❌ Remove (não tem mais LLM principal) |
| `internal/llm/replies.go` | 🔄 Mantém só `OffTopic`, `AuthRequired`, `InternalError`, `OAuthSuccess` |
| `internal/llm/prompts/nlu.md` | ✨ Novo |
| `internal/mcpclient/*` | ❌ Remove (ou feature-flag durante transição) |
| `internal/clicksign/*` | ✨ Novo |
| `internal/flow/*` | ✨ Novo |
| `internal/n8n/*` | ✅ Mantém |
| `internal/logging/*` | ✅ Mantém |
| `internal/config/*` | 🔄 Ajusta (envs novas; remove envs MCP) |
| `cmd/server/main.go` | 🔄 Wiring novo |

---

## 13. Variáveis de ambiente (delta)

### Adicionar

```bash
# Pipeline switcher (durante migração)
PIPELINE=flow                      # "legacy" | "flow"; default "legacy" no boot

# Clicksign REST API
CLICKSIGN_API_BASE_URL=https://api.clicksign.dev    # ou prod
CLICKSIGN_API_TIMEOUT_SECONDS=20

# NLU LLM (substitui o LLM principal antigo)
NLU_MODEL=gpt-4o-mini
NLU_TIMEOUT_SECONDS=15
```

### Remover (quando completar Fase 5)

```bash
MCP_SERVER_BASE_URL=...
MCP_ENDPOINT_PATH=...
MCP_OAUTH_SCOPES=...
OPENAI_MAX_TOOL_ITERATIONS=...
```

---

## 14. Riscos & mitigações

| Risco | Probabilidade | Impacto | Mitigação |
|---|---|---|---|
| n8n + WhatsApp não suportam interactive | Média | Alto | Confirmar **ANTES** da Fase 1. Se não, degradar pra "Responda com o número (1/2/3)" no `reply`, sem `interactive`. |
| Catálogo de operações explode | Baixa | Médio | Aceitar como custo. Fallback "texto livre" pode chamar um intent `unknown` que devolve `MetaHelp` ou mensagem genérica. |
| NLU extrai entity errada | Média | Médio | Validação em cada Flow (ex.: `account_key` precisa existir no `GET /accounts`); confirm steps em ações destrutivas. |
| Token refresh race condition | Baixa | Alto | Lock por phone (mesmo padrão do `mcpclient`). |
| Time gasta mais que estimado | Média | Baixo | Cortar Fase 3-4 se o demo está próximo; entregar com Fase 1-2 estável. |
| Pra demo: PIPELINE=flow ainda incompleto | Baixa | Médio | PIPELINE=legacy continua funcional como fallback até remoção definitiva. |
| Bug que só aparece com `interactive_reply` | Média | Médio | Testar manualmente; postman collection com ambos formatos. |

---

## 15. Estratégia de testes

- **Unit tests** por flow:
  - `ListTemplatesFlow`: caso "sem conta selecionada" → `KindTransfer`; caso "interactive_reply válido" → `KindChoose` com templates.
  - `CreateEnvelopeTmplFlow`: cada step.
- **Unit tests** do NLU: mock OpenAI, valida que o parser não engasga com fences markdown, intent unknown, etc.
- **Integration tests** com Clicksign REST: usar httptest server fake retornando JSONs canônicos.
- **E2E manual**: Postman collection cobrindo:
  - "liste meus templates" (sem conta selecionada).
  - "use a 3" (resposta numérica).
  - `interactive_reply` direto.
  - "obrigado!" (meta_help continua).
  - "quanto é 2+2?" (off_topic continua).

---

## 16. Perguntas pro PO antes de codificar

> Perguntas resolvidas via leitura do código `clicksign/mcp-api-tavola-v3` estão marcadas com ✅. Restam as decisões de produto.

1. **n8n + WhatsApp Business API suportam `interactive` (list/buttons) no setup atual do hackathon?** _(bloqueante pra Fase 1)_
2. **Catálogo MVP de operações é exatamente os 8 listados na seção 7.5? Falta alguma?**
3. ✅ ~~Doc oficial da API REST da Clicksign disponível? Validar paths exatos da seção 8.2.~~ — extraídos diretamente de `mcp-api-tavola-v3/internal/clicksign/client.go`. Pendente apenas o endpoint de **cancelar envelope**.
4. **Mantemos a pipeline atual (Opção A) atrás de feature flag em produção, ou jogamos fora ao final?**
5. **SLA de latência esperado:** < 2s? < 5s? (Define se vale ir até Fase 3 ou parar antes.)
6. **Tela final do envelope criado**: só confirma com texto? link clicável? mostra status atualizado? (Influencia design do `Done`.)
7. **Multi-idioma**: pt-BR only ou inglês também? (NLU prompt teria de cobrir.)
8. **Confirmação destrutiva (cancelar envelope, criar envelope) sempre obrigatória?** Recomendo sim.
9. **Qual ambiente da API Clicksign vamos usar no demo** — `https://app.clicksign.com/api/v3` (prod) ou base equivalente de staging? (Definir `CLICKSIGN_API_BASE_URL`.)
10. **`metadata.source` no envelope/document**: usamos `"whatsapp-bot"` ou um valor padronizado pelo time?

---

## 17. Definição de pronto (MVP demo)

- [ ] Usuário não autenticado vê short-link de OAuth → autentica → recebe webhook "conectado" no WhatsApp.
- [ ] "liste meus templates" → list message do WhatsApp com contas → escolhe → list message com templates. Latência < 3s.
- [ ] "qual o status do envelope X?" → busca pelo nome se houver ambiguidade → traz status humanizado.
- [ ] "envia esse PDF, nome do envelope Contrato 1, signatário Mikael, mikael@x.com, parte" → cria envelope, confirma, envia.
- [ ] "cancela o envelope Y" → buttons "Sim/Não" → ação executada após confirm.
- [ ] "oi", "obrigado", "o que você faz?" → respostas naturais (meta_help mantém).
- [ ] "quanto é 2+2?" → recusa cordial (off_topic mantém).
- [ ] Sessão expira → flag automática + webhook n8n + próxima mensagem dispara re-auth.

---

## 18. Próximos passos imediatos

1. Validar este plano com o PO (perguntas da §16).
2. Adicionar `PIPELINE=flow` flag e ajustar `Session` (Fase 0).
3. Implementar Fase 1 (`list_templates` + `select_account` + `clicksign.Client` mínimo + `nlu.go`).
4. Testar o fluxo end-to-end com o WhatsApp real.
5. Se OK: avançar fases 2-5. Se UX da Fase 1 não rende (por limitação do n8n), iterar no design antes de seguir.
