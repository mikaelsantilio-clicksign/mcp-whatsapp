# Simulando uma conversa de WhatsApp — cURL + Postman

Este documento mostra como simular o fluxo completo do `whatsapp-mcp` como se você fosse o n8n encaminhando mensagens do WhatsApp. Cobre o pipeline **NLU + Guided Flow (Option B)**, que é o único pipeline ativo desde a Fase 5.

Material disponível:

- comandos `curl` prontos para copiar/colar (abaixo)
- collection do Postman: [`whatsapp-mcp.postman_collection.json`](./whatsapp-mcp.postman_collection.json)
- environment do Postman: [`whatsapp-mcp.postman_environment.json`](./whatsapp-mcp.postman_environment.json)
- contrato com o time de n8n: [`N8N_INTEGRATION_CONTRACT.md`](./N8N_INTEGRATION_CONTRACT.md)

---

## Setup

### 1. Variáveis usadas

Edite o environment do Postman (ou exporte no shell para usar `curl`):

```bash
export BASE_URL="http://localhost:8080"
export API_TOKEN="<o-mesmo-valor-de-API_STATIC_TOKEN-no-.env>"
export PHONE="+5511999999999"
```

### 2. Importando no Postman

1. **Importar a collection**: Postman → Import → arraste `docs/whatsapp-mcp.postman_collection.json`
2. **Importar o environment**: Postman → Import → arraste `docs/whatsapp-mcp.postman_environment.json`
3. No canto superior direito, selecione o environment **whatsapp-mcp (local)** e preencha `api_token` com o valor do seu `.env`
4. Rode as requests na ordem dos folders (0 → 4)

A collection já tem **scripts de teste** que capturam o `authorize_url` e o `short_link` automaticamente do primeiro `needs_auth` e salvam no environment, então as requests seguintes não precisam de copy/paste manual.

---

## Resposta padrão

Todas as respostas de `/api/messages` têm o shape unificado abaixo (campos opcionais omitidos quando vazios):

```jsonc
{
  "status": "ok | needs_auth | error",
  "reply": "texto a renderizar no WhatsApp",
  "authorize_url": "https://… (só em needs_auth)",
  "interactive": {
    "type": "list | buttons",
    "header": "…", "body": "…", "footer": "…",
    "items": [{ "id": "…", "title": "…", "description": "…" }]
  },
  "flow_state": { "flow_id": "create_envelope_pdf", "step": "awaiting_confirm" },
  "trace": [{ "step": "…", "ok": true, "info": "…" }],
  "error": { "code": "INTERNAL_ERROR", "details": "…" }
}
```

Veja `docs/N8N_INTEGRATION_CONTRACT.md` para o detalhamento exato de cada campo e como o n8n deve renderizar listas e botões.

---

## Fluxo da conversa

### 0. Sanity check — `/healthz`

```bash
curl -s "$BASE_URL/healthz"
```

```json
{"status":"ok","version":"0.1.0"}
```

### 1. Primeira mensagem (não autenticado) → `needs_auth`

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "'"$PHONE"'",
    "message": "olá",
    "message_id": "wamid.0001"
  }'
```

```json
{
  "status": "needs_auth",
  "reply": "Olá! Para eu poder te ajudar com seus envelopes da Clicksign…\n\n👉 http://localhost:8080/c/QKGP4BECQDPMU\n\nO link expira em 5 minutos e é só seu — evite compartilhar.",
  "authorize_url": "https://oauth2.clicksign.dev/login?response_type=code&client_id=…&redirect_uri=…&code_challenge=…&scope=openid+email+phone"
}
```

O `authorize_url` agora aponta direto para o Cognito da Clicksign (modo `OAUTH_MODE=direct`). O n8n manda o `reply` no WhatsApp.

### 2. Usuário "clica" no shortlink

```bash
SHORTLINK="http://localhost:8080/c/QKGP4BECQDPMU"
curl -i "$SHORTLINK"
```

```
HTTP/1.1 302 Found
Location: https://oauth2.clicksign.dev/login?…
Cache-Control: no-store
```

### 3. Login no Cognito (manual, no browser)

1. Abra o `authorize_url` (ou o shortlink) em uma aba anônima
2. Faça login com sua conta Clicksign
3. Você será redirecionado para `{PUBLIC_BASE_URL}/oauth2/callback?code=…&state=…`
4. Verá a página HTML de sucesso ✅
5. Se `N8N_WEBHOOK_URL` estiver configurado, o n8n recebe um POST com `event: "oauth_success"`

### 4. Pedido autenticado — listar templates

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "'"$PHONE"'",
    "message": "liste meus templates",
    "message_id": "wamid.0002"
  }' | jq
```

Se o usuário tem **uma conta só**, vem direto a lista de templates. Se tem múltiplas contas, a resposta carrega uma **lista interativa** com as contas:

```json
{
  "status": "ok",
  "reply": "Você tem mais de uma conta Clicksign. Qual quer usar agora?",
  "interactive": {
    "type": "list",
    "header": "Escolha a conta",
    "items": [
      {"id": "1b34…293", "title": "Carlos Mikael", "description": "1b34…293"},
      {"id": "5b8a…2ea", "title": "name LTDA", "description": "5b8a…2ea"}
    ]
  },
  "flow_state": {"flow_id": "select_account", "step": "awaiting_choice"}
}
```

### 5. Resposta a uma lista interativa

Quando o usuário clica num item, o n8n envia o `list_item_id` correspondente:

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "'"$PHONE"'",
    "interactive_reply": {"list_item_id": "1b34…293"},
    "message_id": "wamid.0002.click"
  }' | jq
```

O flow `select_account` salva a `PreferredAccount` na sessão e **transfere** automaticamente para o flow original (`list_templates`).

### 6. Criar envelope a partir de PDF (URL)

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "'"$PHONE"'",
    "message": "envia esse PDF https://www.w3.org/WAI/ER/tests/xhtml/testfiles/resources/pdf/dummy.pdf, nome do envelope Contrato Stg 1, signatário Mikael Nunes mikael@example.com como parte",
    "message_id": "wamid.0006"
  }' | jq
```

Resposta típica (`awaiting_confirm`):

```json
{
  "status": "ok",
  "reply": "Quer que eu envie o envelope abaixo? *Contrato Stg 1* (1 documento, 1 signatário)",
  "interactive": {
    "type": "buttons",
    "body": "*Contrato Stg 1*\n• Mikael Nunes (mikael@example.com) — parte",
    "items": [
      {"id": "confirm_yes", "title": "Sim, enviar"},
      {"id": "confirm_no", "title": "Não"}
    ]
  },
  "flow_state": {"flow_id": "create_envelope_pdf", "step": "awaiting_confirm"}
}
```

E para confirmar:

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "'"$PHONE"'",
    "interactive_reply": {"button_id": "confirm_yes"},
    "message_id": "wamid.0008"
  }' | jq
```

### 7. Criar envelope com PDF vindo do WhatsApp

O n8n deve **re-hospedar** a mídia do WhatsApp numa URL pública (Estratégia A — ver `N8N_INTEGRATION_CONTRACT.md`) e passar o link em `attachments[0].url`:

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "'"$PHONE"'",
    "message": "manda esse contrato pra Maria Souza maria@empresa.com como parte",
    "attachments": [{
      "url": "https://www.w3.org/WAI/ER/tests/xhtml/testfiles/resources/pdf/dummy.pdf",
      "mime_type": "application/pdf",
      "filename": "contrato.pdf"
    }],
    "message_id": "wamid.0007"
  }' | jq
```

O flow usa `attachments[0].url` como `pdf_url` automaticamente (sem precisar repetir no texto).

### 8. Listar / consultar status / cancelar / adicionar signatário

| O que o usuário diz | Intent | Comportamento |
|---|---|---|
| "liste meus envelopes em andamento" | `list_envelopes` (filter_status=running) | Lista interativa; clique → status |
| "qual o status do envelope Contrato 1?" | `envelope_status` | 1 match → detalhes; N matches → lista; 0 → mensagem amigável |
| "adicione Maria Souza maria@empresa.com como parte no envelope Contrato 1" | `add_signer` | Valida nome/e-mail/papel → confirmação → POST `/envelopes/{id}/signers` |
| "cancela o envelope Contrato 1" | `cancel_envelope` | Pré-check: só envelopes em `draft` podem ser excluídos via API; se sim → botão destrutivo |

A collection do Postman tem uma request para cada cenário (folder *2. Flows (Option B)*).

### 9. Saudação / off-topic

```bash
# saudação → resposta estática llm.Capabilities()
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" -H "Content-Type: application/json" \
  -d '{"phone_number":"'"$PHONE"'","message":"bom dia! O que você pode fazer?","message_id":"wamid.x1"}'

# off-topic → resposta estática llm.OffTopic() (NLU e flows nem são chamados)
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" -H "Content-Type: application/json" \
  -d '{"phone_number":"'"$PHONE"'","message":"quanto é 2+2?","message_id":"wamid.x2"}'
```

---

## Casos de erro

### Sem Bearer → 401

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE_URL/api/messages" \
  -H "Content-Type: application/json" \
  -d '{"phone_number":"+5511","message":"x"}'
# 401
```

### Bearer errado → 401

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer wrong-token" \
  -H "Content-Type: application/json" \
  -d '{"phone_number":"+5511","message":"x"}' | jq
```

### Payload inválido → 400

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}' | jq
```

```json
{
  "status": "error",
  "reply": "Não recebi seus dados corretamente. Tente enviar novamente?",
  "error": {"code": "INVALID_INPUT", "details": "phone_number is required"}
}
```

### Idempotência (mesma `message_id` em < 60s)

```bash
PAYLOAD='{"phone_number":"'"$PHONE"'","message":"oi","message_id":"wamid.dup-001"}'

curl -s -X POST "$BASE_URL/api/messages" -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" -d "$PAYLOAD" | jq

curl -s -X POST "$BASE_URL/api/messages" -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" -d "$PAYLOAD" | jq
# segunda chamada: {"status":"ok","reply":""}
```

### Shortlink inexistente → 410

```bash
curl -s -o /dev/null -w "%{http_code}\n" "$BASE_URL/c/INEXISTENTE"
# 410
```

---

## Ordem sugerida para a demo

1. `GET /healthz` — confirma que está no ar
2. `POST /api/messages` "olá" → recebe `needs_auth`
3. Abre o `authorize_url` no browser → login → callback → vê página de sucesso (e o n8n recebe webhook se configurado)
4. `POST /api/messages` "liste meus templates" → seleciona conta (interactive) → recebe a lista
5. `POST /api/messages` "envia esse PDF …" → recebe card de confirmação → clica `confirm_yes` → envelope criado
6. `POST /api/messages` "adicione Maria …" → confirma → signer adicionado
7. (opcional) "cancela o envelope X" → mostra que só `draft` pode ser excluído
8. (opcional) Mostra idempotência e erros
