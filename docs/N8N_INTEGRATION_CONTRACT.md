# Contrato de Integração WhatsApp ↔ n8n ↔ Backend (Clicksign WhatsApp Bot)

> **Audiência**: time responsável por configurar o workflow no n8n.
> **Última atualização**: 2026-06-01.
> **Status**: rascunho — alinhar com o backend antes da implementação final do workflow.

Este documento descreve o contrato HTTP entre o n8n e o backend Go (`whatsapp-mcp`). O backend recebe mensagens vindas do WhatsApp Business API (intermediadas pelo n8n) e devolve a resposta que deve ser enviada ao usuário. O backend também envia mensagens proativas ao n8n (ex.: confirmação de login OAuth).

---

## 1. Visão geral do fluxo

```
[Usuário WhatsApp] ─────► [WhatsApp Business API / Meta Cloud]
                                      │
                                      ▼ webhook
                                  [n8n workflow]
                                      │
        (1) request síncrono ─────────┴──────────► POST /api/messages (backend)
                                                          │
                                                          ▼ resposta JSON
        (2) n8n converte em mensagem WhatsApp  ◄──────────┘
                                      │
                                      ▼
[Usuário WhatsApp] ◄──────  [WhatsApp Business API]


Eventos proativos (sem usuário ter mandado msg):

[Backend]  ──────► POST {N8N_WEBHOOK_URL}  ──────► [n8n] ──────► [WhatsApp]
   ex.: usuário acabou de autenticar via OAuth
```

---

## 2. Autenticação

### Inbound (n8n → backend)

Todas as chamadas em `POST /api/messages` exigem header estático:

```
Authorization: Bearer <STATIC_INBOUND_TOKEN>
```

O valor é combinado fora-de-banda. Sem header ou com header inválido o backend retorna **401**.

### Outbound (backend → n8n)

O backend envia para `N8N_WEBHOOK_URL` com header:

```
Authorization: Bearer <N8N_WEBHOOK_TOKEN>
```

Se o webhook do n8n não exigir auth, defina `N8N_WEBHOOK_TOKEN=""` no backend e ignore.

---

## 3. `POST /api/messages` — Request (n8n → backend)

### 3.1 Esquema geral

```json
{
  "phone_number": "5511999998888",
  "message": "texto livre digitado pelo usuário (opcional)",
  "interactive_reply": {
    "list_item_id": "...",
    "button_id": "..."
  },
  "attachments": [
    {
      "url": "https://...",
      "mime_type": "application/pdf",
      "filename": "Contrato.pdf"
    }
  ],
  "message_id": "wamid.HBgN...",
  "conversation_id": "opcional"
}
```

### 3.2 Regras

- **`phone_number`**: obrigatório. Apenas dígitos, com DDI (ex.: `"5511999998888"`). Sem `+` nem espaços.
- **`message_id`**: obrigatório. Usar o `wamid` original do WhatsApp — o backend usa para **idempotência** (dedup de retentativas).
- **`message` OU `interactive_reply`** deve estar presente. O n8n não deve mandar os dois ao mesmo tempo.
- **`attachments`** é opcional e pode ser omitido. Quando presente, ver §4.
- **`conversation_id`**: opcional, apenas para tracing.

### 3.3 Quatro cenários possíveis

#### Cenário A — Texto puro (sem anexo)

Usuário digita "liste meus templates":

```json
{
  "phone_number": "5511999998888",
  "message": "liste meus templates",
  "message_id": "wamid.HBgN..."
}
```

#### Cenário B — Texto + anexo (PDF/imagem)

Usuário anexa um PDF e escreve uma caption: "envia esse pra Mikael":

```json
{
  "phone_number": "5511999998888",
  "message": "envia esse pra Mikael",
  "attachments": [
    {
      "url": "https://uploads.example-bucket.s3.amazonaws.com/wa-media/8f3a2b1c.pdf?X-Amz-Algorithm=...",
      "mime_type": "application/pdf",
      "filename": "Contrato.pdf"
    }
  ],
  "message_id": "wamid.HBgN..."
}
```

> **Atenção**: a URL deve ser **pública** e **acessível por HTTPS sem autenticação**. Ver §4.

#### Cenário C — Anexo sem caption (usuário só mandou o arquivo)

```json
{
  "phone_number": "5511999998888",
  "message": "",
  "attachments": [
    { "url": "https://...", "mime_type": "application/pdf", "filename": "Contrato.pdf" }
  ],
  "message_id": "wamid.HBgN..."
}
```

O backend trata `message: ""` como "usuário só anexou" e pede informações faltantes (nome do envelope, signatário etc.) via mensagens interativas.

#### Cenário D — Clique em mensagem interativa

Quando o backend responde com `interactive` (list ou buttons), o n8n recebe um evento de clique do WhatsApp e converte em:

**Clique em list:**
```json
{
  "phone_number": "5511999998888",
  "interactive_reply": { "list_item_id": "942c35a4-0acb-45c6-ad06-0ef89e9bdaeb" },
  "message_id": "wamid.HBgN..."
}
```

**Clique em button:**
```json
{
  "phone_number": "5511999998888",
  "interactive_reply": { "button_id": "confirm_yes" },
  "message_id": "wamid.HBgN..."
}
```

> Nesses cliques, **não envie `message`**. O backend recupera o contexto da sessão.

---

## 4. Anexos — Estratégia A (re-hospedagem)

> **Decisão arquitetural**: o n8n é responsável por baixar o arquivo do WhatsApp Cloud API e re-hospedar em um storage público antes de mandar ao backend. O backend **não tem** o token do Meta nem acesso direto à API do WhatsApp.

### 4.1 Por que essa estratégia?

- O WhatsApp Cloud API entrega só um `media_id` no webhook.
- A URL real do Meta (`lookaside.fbsbx.com/...`):
  - Requer header `Authorization: Bearer <META_TOKEN>` para baixar.
  - Expira em poucos minutos.
  - Conteúdo só vive 30 dias.
- Repassar essa URL crua ao backend forçaria o backend a:
  - Conhecer o token Meta (mais um segredo).
  - Implementar adapter HTTP autenticado.
  - Ficar acoplado ao WhatsApp Cloud.

Com a Estratégia A, o backend recebe uma URL pública e **trata anexo e link colado-no-texto exatamente igual**.

### 4.2 Passo a passo no n8n

Para cada mensagem com mídia (`type: image | document | audio | video` no webhook do WhatsApp):

1. **Resolver media_id → URL Meta**

   ```http
   GET https://graph.facebook.com/v18.0/{media_id}
   Authorization: Bearer {META_PERMANENT_TOKEN}
   ```

   Resposta:
   ```json
   {
     "url": "https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid=...",
     "mime_type": "application/pdf",
     "sha256": "...",
     "file_size": 215430,
     "id": "1234567890"
   }
   ```

2. **Baixar o arquivo da URL Meta**

   ```http
   GET https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid=...
   Authorization: Bearer {META_PERMANENT_TOKEN}
   ```

   Salvar bytes em variável binária do node.

3. **Upload para storage público** (S3, Cloudflare R2, GCS, Supabase Storage etc.)

   - Bucket dedicado: ex.: `whatsapp-uploads`.
   - Path sugerido: `wa-media/{YYYY-MM-DD}/{uuid}.{ext}`.
   - Política recomendada: **URL assinada com TTL ≥ 15 minutos** (cobre fluxos com confirmação humana). Não precisa ser "for-eternity-public".
   - Lifecycle rule: apagar objetos > 7 dias para economizar.

4. **Montar `attachments[]`** e enviar para o backend:

   ```json
   {
     "url": "<URL assinada gerada no passo 3>",
     "mime_type": "<mime do passo 1>",
     "filename": "<nome original vindo do webhook do WhatsApp, se houver>"
   }
   ```

### 4.3 Campos de `attachments[]` — contrato detalhado

| Campo | Tipo | Obrigatório | Detalhes |
|---|---|---|---|
| `url` | string | **Sim** | URL HTTPS pública. Deve aceitar `GET` sem header de auth. TTL ≥ 15 min. |
| `mime_type` | string | **Sim** | MIME real do arquivo. Use o que veio do passo 1, não invente. |
| `filename` | string | Recomendado | Nome original do arquivo (vem no webhook do WhatsApp em `document.filename` quando o usuário anexa um documento; nem sempre vem para imagens). |

### 4.4 Restrições que o backend aplica (informativo)

O backend rejeita o anexo se:

- URL não for `https://` (exceto em dev local com flag específica).
- URL apontar para IP privado/loopback/link-local (proteção SSRF).
- `GET` retornar status diferente de 200.
- MIME real (após sniffing) não estiver em: `application/pdf`, `image/jpeg`, `image/png`, `text/plain`, `application/msword`, `application/vnd.openxmlformats-officedocument.wordprocessingml.document`.
- Tamanho > 20 MB.

Se algum desses falhar, o backend responde com mensagem natural ao usuário (ex.: "Não consegui baixar o arquivo dessa URL"). O n8n só precisa transmitir a `reply`.

### 4.5 Múltiplos anexos

Se o usuário mandar 2+ arquivos, envie todos no array. **Hoje o backend usa apenas `attachments[0]`** e, se houver mais, pergunta ao usuário qual usar (resposta vem via list message — §5.3).

---

## 5. `POST /api/messages` — Response (backend → n8n)

A response sempre é JSON. Existem **4 formatos** que o n8n precisa saber renderizar.

### 5.1 `needs_auth` — usuário não autenticado

```json
{
  "status": "needs_auth",
  "reply": "Pra continuar, faz login aqui: https://whatsapp-mcp.example.com/c/abc123\nO link é pessoal e expira em 5 minutos.",
  "authorize_url": "https://mcp-api-tavola-v3-6.clicksign.dev/oauth2/authorize?..."
}
```

**Ação no n8n**: enviar `reply` como mensagem de texto WhatsApp. O `authorize_url` é informativo (já está dentro do `reply` como short-link).

### 5.2 `ok` — texto puro

```json
{
  "status": "ok",
  "reply": "Boa tarde! Posso te ajudar com envelopes, templates e assinaturas da Clicksign.",
  "trace": [...]
}
```

**Ação no n8n**: enviar `reply` como mensagem de texto WhatsApp. Ignorar `trace` (uso interno para debug).

### 5.3 `ok` — list message (escolha entre opções)

```json
{
  "status": "ok",
  "reply": "Você tem múltiplas contas Clicksign. Escolha uma:",
  "interactive": {
    "type": "list",
    "header": "Escolha sua conta",
    "body": "Toque pra continuar",
    "footer": "Clicksign Assistant",
    "items": [
      { "id": "1b345b40-...", "title": "Carlos Mikael Nunes Santilio", "description": "Conta pessoal" },
      { "id": "5b8a92a1-...", "title": "name LTDA" },
      { "id": "942c35a4-...", "title": "Integration LTDA" }
    ]
  },
  "flow_state": { "flow_id": "list_templates", "step": "awaiting_account" }
}
```

**Ação no n8n**: enviar mensagem WhatsApp do tipo **`interactive` com `type: "list"`**, mapeando:

- `interactive.header` → `header.text`
- `interactive.body` → `body.text`
- `interactive.footer` → `footer.text` (opcional)
- `interactive.items[].id` → `id` da row
- `interactive.items[].title` → `title` (máx 24 chars)
- `interactive.items[].description` → `description` (máx 72 chars)

Quando o usuário tocar em uma row, o webhook do WhatsApp retorna o `id` selecionado. O n8n deve mandar pro backend como `interactive_reply.list_item_id` (Cenário D, §3.3).

**Limites do WhatsApp** que o backend respeita:

- Máx **10 items** por list.
- Máx **1 section** (o backend não usa sections múltiplas no MVP).

### 5.4 `ok` — quick reply buttons (até 3)

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
  "flow_state": { "flow_id": "create_envelope_pdf", "step": "awaiting_confirm" }
}
```

**Ação no n8n**: enviar mensagem WhatsApp do tipo **`interactive` com `type: "button"`**:

- `interactive.body` → `body.text`
- `interactive.items[].id` → `reply.id` do botão (máx 256 chars)
- `interactive.items[].title` → `reply.title` (máx 20 chars)

Quando o usuário clicar, o `id` volta no webhook. Mandar pro backend como `interactive_reply.button_id` (Cenário D, §3.3).

**Limite do WhatsApp**: máx **3 buttons**.

### 5.5 Resposta com erro (raro)

```json
{
  "status": "error",
  "reply": "Tive um problema interno. Pode tentar de novo daqui a pouco?",
  "error": {
    "code": "internal_error",
    "message": "details for debugging"
  }
}
```

**Ação no n8n**: enviar `reply` como texto. O bloco `error` é para logs (não mostrar ao usuário).

### 5.6 Resumo — decisão de envio do n8n

```
if response.status == "needs_auth" || response.status == "error":
    send_text(response.reply)
elif response.interactive == null:
    send_text(response.reply)
elif response.interactive.type == "list":
    send_interactive_list(response.reply, response.interactive)
elif response.interactive.type == "buttons":
    send_interactive_buttons(response.reply, response.interactive)
```

---

## 6. Webhook outbound (backend → n8n) — mensagens proativas

Quando algo acontece no backend **fora de uma conversa ativa** (ex.: usuário acabou de autenticar via OAuth na URL de login), o backend dispara um POST para `N8N_WEBHOOK_URL` para que o n8n envie uma mensagem WhatsApp proativa.

### 6.1 Payload

```json
{
  "event": "oauth_success",
  "phone_number": "5511999998888",
  "reply": "Conexão com a Clicksign concluída. Já pode me chamar aqui no WhatsApp quando precisar.",
  "metadata": { "account_key": "942c35a4-..." }
}
```

### 6.2 Eventos atuais

| `event` | Quando dispara | `reply` típico |
|---|---|---|
| `oauth_success` | Usuário concluiu OAuth com sucesso na página de login. | "Conexão com a Clicksign concluída. ..." |
| `oauth_failed` | Falha durante callback OAuth (raro). | "Não consegui concluir o login. Pode tentar de novo?" |
| `session_expired` | Token expirou e não foi possível dar refresh (usuário precisa relogar). | "Sua sessão expirou. Mande qualquer mensagem aqui para receber um novo link." |

### 6.3 Ação no n8n

- Aceitar `POST` no webhook configurado em `N8N_WEBHOOK_URL`.
- Validar `Authorization: Bearer {N8N_WEBHOOK_TOKEN}` se token definido.
- Enviar `reply` como mensagem de texto para `phone_number`.
- `metadata` pode ser ignorado (uso futuro).

---

## 7. Idempotência

- O `message_id` enviado no request é **chave de idempotência**.
- O backend cacheia respostas por `message_id` durante alguns minutos.
- Se o n8n reenviar o mesmo `message_id` por causa de retry/timeout, o backend devolve a mesma resposta sem reprocessar (sem cobrar OpenAI duas vezes, sem criar dois envelopes etc.).
- **Sempre repasse o `wamid` original do WhatsApp** como `message_id`.

---

## 8. Erros que o n8n pode ver

| Status HTTP | Significa | Ação |
|---|---|---|
| **200** | OK, render conforme §5. | Enviar mensagem. |
| **400** | Request malformado (campos faltando, JSON inválido). | Logar e alertar — bug no workflow. |
| **401** | Header `Authorization` faltando/incorreto. | Revisar configuração do token estático. |
| **413** | Payload muito grande (não deve acontecer no fluxo normal). | Logar. |
| **429** | Rate limit (futuro, hoje não está ativo). | Backoff exponencial. |
| **500** | Erro interno do backend. | Logar; opcional enviar mensagem genérica ao usuário ("Tive um problema, tente em alguns minutos"). |
| **timeout** | Backend não respondeu em 30s. | Mensagem genérica ao usuário. |

---

## 9. Boas práticas para o workflow n8n

1. **Sempre repassar `wamid` como `message_id`** (idempotência).
2. **Não tentar reinterpretar** a `reply` — passar como está. Toda lógica de fraseado, multi-conta, perguntas faltantes, etc. está no backend.
3. **TTL da URL assinada** ≥ 15 minutos para sobreviver a confirmações humanas via buttons (usuário pode demorar para tocar em "Sim, enviar").
4. **Não logar** o body completo da mídia (PDFs podem ter dados sensíveis).
5. **Storage**: usar bucket dedicado a uploads do WhatsApp com lifecycle rule de 7 dias para limpar.
6. **Failover**: se o upload pro storage falhar, responder ao usuário no WhatsApp ("Não consegui salvar seu arquivo. Pode tentar de novo?") em vez de chamar o backend sem `attachments`.
7. **Limites do WhatsApp em mensagens interativas**:
   - List: máx 10 items, title ≤ 24 chars, description ≤ 72 chars.
   - Buttons: máx 3 buttons, title ≤ 20 chars.
   - O backend já respeita esses limites; o n8n só precisa repassar.
8. **Não enviar mensagens duplicadas**: se o backend devolveu `interactive`, o WhatsApp envia uma mensagem só (não mandar `reply` como texto separado antes).

---

## 10. Checklist de configuração mínima do n8n

- [ ] Trigger configurado no webhook do WhatsApp Business API.
- [ ] Credencial Meta com token permanente.
- [ ] Credencial do storage público (S3/R2/etc.) com permissão de upload + URL assinada.
- [ ] Variável `BACKEND_BASE_URL` apontando para o backend (ex.: `https://whatsapp-mcp.example.com`).
- [ ] Variável `BACKEND_TOKEN` com o `STATIC_INBOUND_TOKEN` combinado com o time de backend.
- [ ] Workflow inbound: WhatsApp → resolve mídia (se houver) → upload no storage → POST `/api/messages` → render resposta (texto/list/buttons) → enviar via WhatsApp API.
- [ ] Workflow outbound (proativo): webhook recebido do backend → enviar texto via WhatsApp API.

---

## 11. FAQ rápido

**Q: O backend baixa o PDF diretamente do WhatsApp?**
A: Não. O n8n baixa, sobe pra um storage público, e manda a URL pro backend. O backend não tem token Meta.

**Q: A URL precisa ser permanente?**
A: Não. TTL ≥ 15 minutos é suficiente. Cobre fluxos com confirmação humana via buttons.

**Q: O backend redimensiona/otimiza o arquivo?**
A: Não. Ele baixa e converte em base64 para enviar à Clicksign tal qual. Limite de 20 MB.

**Q: O backend chama a API do Meta diretamente?**
A: Não. Toda comunicação WhatsApp é mediada pelo n8n.

**Q: O usuário pode mandar áudio/vídeo?**
A: O backend hoje aceita só PDF, JPG, PNG, TXT, DOC, DOCX. Se vier áudio/vídeo, o n8n pode descartar o anexo (mandar request sem `attachments`) e o backend responde com mensagem explicando os formatos aceitos.

**Q: Como simular o fluxo sem WhatsApp real?**
A: Tem uma Postman collection em `docs/whatsapp-mcp.postman_collection.json` que cobre os 4 cenários do §3.3. Importar e usar o env de exemplo.

---

## 12. Exemplos completos (copiar-e-colar)

### Exemplo 1 — Mensagem de texto

```bash
curl -X POST "$BACKEND_BASE_URL/api/messages" \
  -H "Authorization: Bearer $BACKEND_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "5511999998888",
    "message": "liste meus templates",
    "message_id": "wamid.HBgNNTUxMTk5OTk5ODg4OBUCABIYIDM2QTk..."
  }'
```

### Exemplo 2 — Mensagem com anexo (após upload no S3)

```bash
curl -X POST "$BACKEND_BASE_URL/api/messages" \
  -H "Authorization: Bearer $BACKEND_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "5511999998888",
    "message": "envia esse pra Mikael mikael@x.com como parte, nome do envelope Contrato 1",
    "attachments": [
      {
        "url": "https://uploads.example-bucket.s3.amazonaws.com/wa-media/2026-06-01/8f3a2b1c.pdf?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Expires=900&X-Amz-Signature=...",
        "mime_type": "application/pdf",
        "filename": "Contrato.pdf"
      }
    ],
    "message_id": "wamid.HBgN..."
  }'
```

### Exemplo 3 — Clique em row de list

```bash
curl -X POST "$BACKEND_BASE_URL/api/messages" \
  -H "Authorization: Bearer $BACKEND_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "5511999998888",
    "interactive_reply": { "list_item_id": "942c35a4-0acb-45c6-ad06-0ef89e9bdaeb" },
    "message_id": "wamid.HBgN..."
  }'
```

### Exemplo 4 — Clique em quick reply button

```bash
curl -X POST "$BACKEND_BASE_URL/api/messages" \
  -H "Authorization: Bearer $BACKEND_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "5511999998888",
    "interactive_reply": { "button_id": "confirm_yes" },
    "message_id": "wamid.HBgN..."
  }'
```

---

## 13. Contato

Dúvidas sobre o contrato ou sobre comportamento esperado do backend: time backend (`whatsapp-mcp`).

Para alinhar mudanças no contrato, abrir PR atualizando este documento e o `IMPLEMENTATION_PLAN_OPTION_B.md`.
