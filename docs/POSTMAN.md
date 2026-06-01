# Simulando uma conversa de WhatsApp — cURL + Postman

Este documento mostra como simular o fluxo completo do `whatsapp-mcp` como se você fosse o n8n encaminhando mensagens do WhatsApp. Inclui:

- comandos `curl` prontos para copiar/colar
- collection do Postman: [`whatsapp-mcp.postman_collection.json`](./whatsapp-mcp.postman_collection.json)
- environment do Postman: [`whatsapp-mcp.postman_environment.json`](./whatsapp-mcp.postman_environment.json)

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
4. Rode as requests na ordem dos folders (1 → 5)

A collection já tem **scripts de teste** que capturam o `authorize_url` e o `short_link` automaticamente do primeiro `needs_auth` e salvam no environment, então as requests seguintes não precisam de copy/paste manual.

---

## Fluxo da conversa

### 0. Sanity check — `/healthz`

```bash
curl -s "$BASE_URL/healthz"
```

Resposta:

```json
{"status":"ok","version":"0.1.0"}
```

### 1. Primeira mensagem do usuário (não autenticado) → `needs_auth`

Simula o n8n encaminhando "olá" pela primeira vez:

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

Resposta esperada:

```json
{
  "status": "needs_auth",
  "reply": "Olá! Para eu poder te ajudar com seus envelopes da Clicksign, primeiro preciso que você conecte sua conta…\n\n👉 http://localhost:8080/c/QKGP4BECQDPMU\n\nO link expira em 5 minutos e é só seu — evite compartilhar.",
  "authorize_url": "https://mcp-api-tavola-v3-6.clicksign.dev/oauth2/authorize?…"
}
```

**O n8n enviaria o conteúdo de `reply` direto pro WhatsApp.**

Guarde o `authorize_url` (ou o shortlink) — você vai usar no próximo passo.

### 2. Usuário "clica" no shortlink

Isso é normalmente o browser do usuário, mas você pode reproduzir no terminal:

```bash
SHORTLINK="http://localhost:8080/c/QKGP4BECQDPMU"   # cole o link recebido
curl -i "$SHORTLINK"
```

Resposta esperada (302):

```http
HTTP/1.1 302 Found
Location: https://mcp-api-tavola-v3-6.clicksign.dev/oauth2/authorize?client_id=…
Cache-Control: no-store
```

### 3. Login no Cognito (manual, no browser)

Esse passo **só pode ser feito num browser real** porque envolve telas de login do Cognito/Clicksign:

1. Abra o `authorize_url` (ou o shortlink) em uma aba anônima
2. Faça login com sua conta Clicksign
3. Você será redirecionado para `http://localhost:8080/oauth2/callback?code=…&state=…`
4. Verá a página HTML de sucesso ✅
5. Se `N8N_WEBHOOK_URL` estiver configurado no `.env`, o n8n também recebe um POST com `event: "oauth_success"`

### 4. Mensagem após autenticação → `ok` com tool-calling

Agora a mesma rota com o usuário já autenticado:

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

Resposta:

```json
{
  "status": "ok",
  "reply": "Você tem 3 templates: …",
  "tool_calls": [{"name": "list_templates", "ok": true}]
}
```

### 5. Pedido com ação (envio de envelope)

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "'"$PHONE"'",
    "message": "envia o template Contrato Padrão para joao@example.com",
    "message_id": "wamid.0003"
  }' | jq
```

O LLM pode pedir confirmação antes (depende do system prompt). Se pedir, mande:

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "'"$PHONE"'",
    "message": "sim, pode enviar",
    "message_id": "wamid.0004"
  }' | jq
```

### 6. Mensagem com anexo

```bash
curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "'"$PHONE"'",
    "message": "envia esse contrato pra maria@example.com",
    "attachments": [
      {
        "url": "https://www.w3.org/WAI/ER/tests/xhtml/testfiles/resources/pdf/dummy.pdf",
        "mime_type": "application/pdf",
        "filename": "contrato.pdf"
      }
    ],
    "message_id": "wamid.0005"
  }' | jq
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

Resposta:

```json
{
  "status": "error",
  "reply": "Não recebi seus dados corretamente. Tente enviar novamente?",
  "error": {"code": "INVALID_INPUT", "details": "phone_number and message are required"}
}
```

### Idempotência (mesma `message_id` em < 60s)

Mande a mesma requisição duas vezes seguidas com o mesmo `message_id`. A segunda volta com `reply` vazio (não chama o LLM):

```bash
PAYLOAD='{"phone_number":"'"$PHONE"'","message":"oi","message_id":"wamid.dup-001"}'

curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d "$PAYLOAD" | jq

curl -s -X POST "$BASE_URL/api/messages" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d "$PAYLOAD" | jq
# segunda chamada: {"status":"ok","reply":""}
```

### Shortlink inexistente → 410

```bash
curl -s -o /dev/null -w "%{http_code}\n" "$BASE_URL/c/INEXISTENTE"
# 410
```

---

## Ordem sugerida para a demo

1. `GET /healthz` → confirma que está no ar
2. `POST /api/messages` "olá" → recebe `needs_auth`
3. Abre o `authorize_url` no browser → login → redireciona para `/oauth2/callback` → vê página de sucesso (e o n8n recebe webhook se configurado)
4. `POST /api/messages` "liste meus templates" → resposta natural com tool-calling
5. `POST /api/messages` com pedido de envio → confirmação → envio
6. (opcional) Mostra idempotência e erros
