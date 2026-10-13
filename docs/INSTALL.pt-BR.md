# Instalação — evo-chatwoot-bridge (Go)

[English](INSTALL.md) · **Português** · [README (PT)](../README.pt-BR.md) ·
[Configuração](CONFIGURATION.md) · [Problemas comuns](TROUBLESHOOTING.md)

Este guia leva você do zero a uma ponte WhatsApp ⇄ Chatwoot bidirecional
funcionando. Dois caminhos de instalação:

- **A. Docker Compose** — qualquer host Linux com Docker.
- **B. EasyPanel** — a instalação de referência (Traefik + Docker Swarm),
  validada ponta a ponta.

Depois, na [§3](#3-crie-o-primeiro-admin), você cria o primeiro admin, a conexão
e liga os dois webhooks.

## 0. Antes de começar

| Você precisa de | Detalhe |
|---|---|
| **MySQL 8 ou 9** | acessível a partir do container da ponte; um banco em que você possa escrever. Tabelas existentes são preservadas; as migrações são aditivas |
| **Evolution GO** | rodando e **conectado ao WhatsApp** (instância `connected=true`). Anote a URL base, o nome da instância e a **API key global** (ou o token da instância) |
| **Chatwoot** | URL base, um `api_access_token` de um **admin da conta**, o id da conta e uma inbox do tipo **API** |
| **Uma URL pública para a ponte** | HTTPS recomendado. O Evolution GO guarda **um** webhook por instância, então essa URL precisa ser a definitiva |
| **Dois segredos** | `WEBHOOK_TOKEN_SECRET` e `SESSION_SECRET`, 32 bytes aleatórios cada (`openssl rand -base64 32`) |

> **Uma réplica.** Rode uma única instância da ponte. O índice de unicidade
> entre réplicas ainda está no [roadmap](ARCHITECTURE.md); com mais de uma
> réplica uma corrida pode criar contato duplicado.

> **Um webhook de Evolution por instância.** O Evolution GO armazena uma única
> URL de webhook por instância: esta ponte e qualquer outro consumidor não podem
> receber os mesmos eventos ao mesmo tempo. Planeje a virada (veja
> [§5](#5-migrar-de-uma-ponte-existente)).

---

## 1. Caminho A — Docker Compose

```sh
git clone https://github.com/estevao-tarifa/evo-chatwoot-bridge-go.git
cd evo-chatwoot-bridge-go

cp deploy/example.env deploy/.env
chmod 600 deploy/.env
$EDITOR deploy/.env        # DATABASE_URL, BRIDGE_URL, WEBHOOK_TOKEN_SECRET,
                           # SESSION_SECRET, ADMIN_USER, ADMIN_PASS
```

A referência anotada de cada variável é
[`deploy/example.env`](../deploy/example.env); as regras estão em
[`CONFIGURATION.md`](CONFIGURATION.md).

**Rode a migração primeiro.** O servidor não atende tráfego com schema não
migrado, e a migração é um comando separado, de execução única:

```sh
docker compose -f deploy/docker-compose.prod.yml build
docker compose -f deploy/docker-compose.prod.yml run --rm bridge migrate
docker compose -f deploy/docker-compose.prod.yml up -d
```

Verifique:

```sh
curl -fsS http://127.0.0.1:8100/healthz   # {"status":"ok"}
curl -fsS http://127.0.0.1:8100/readyz    # {"status":"ok"} — falha se o MySQL estiver inalcançável
curl -fsS http://127.0.0.1:8100/api/v1/meta
```

`/healthz` diz que o processo está vivo; `/readyz` diz que o pool do banco subiu.
Um `readyz` `unavailable` depois da migração quase sempre é `DATABASE_URL` errado
— veja [Problemas comuns](TROUBLESHOOTING.md).

Coloque um proxy reverso (ou EasyPanel/Traefik) na frente para que `BRIDGE_URL`
seja alcançável pelo Evolution GO e pelo Chatwoot.

---

## 2. Caminho B — EasyPanel

A ponte é um **app** normal do EasyPanel, construído a partir deste repositório
Git (ou de uma imagem Docker). O formato validado em produção:

```text
Projeto no EasyPanel
├── app  bridge        build path /backend   porta interna 3000
│     DATABASE_URL=mysql://usuario:senha@<servico-db>:3306/<banco>
│     BRIDGE_URL=https://<seu-dominio-da-ponte>
│     WEBHOOK_TOKEN_SECRET=<openssl rand -base64 32>
│     SESSION_SECRET=<openssl rand -base64 32>
│     APP_ENV=production
│     ADMIN_USER / ADMIN_PASS   (só no primeiro boot)
└── app  bridge-ui     (opcional) build path /frontend   porta interna 80
      BRIDGE_API_BASE_URL=https://<seu-dominio-da-ui>    ← só a origem, sem /api
```

### 2.1 Criar o app

1. **Create project** → **Create Service** → **App**.
2. **Source**: GitHub, este repositório. **Build**: Dockerfile, caminho `/backend`.
3. **Ports**: exponha o app na porta interna **3000**.
4. **Environment**: cole as variáveis do bloco acima.
5. **Domains**: anexe o domínio da ponte; o EasyPanel cuida do TLS.

### 2.2 Migrar antes do primeiro start

O comando padrão de start é o servidor. Rode a migração uma vez, **antes** de o
servidor subir, pelo terminal do app:

```sh
./bridge migrate        # idempotente; imprime a lista aplicada/já aplicada
```

Ou, mantendo o compose, rode o serviço de execução única como no Caminho A.
Somente depois disso, faça **deploy/start** do app.

### 2.3 A SPA de administração (opcional)

Implante `frontend/` como um segundo app, build path `/frontend` (nginx na porta
80). Depois anexe **dois** domínios ao host da UI:

| Caminho | Serviço |
|---|---|
| `/` | `bridge-ui` |
| `/api` | `bridge` |

e defina `BRIDGE_API_BASE_URL=https://<seu-dominio-da-ui>` (origem **sem** `/api`
no final — as rotas da SPA já incluem `/api/v1`). Mesma origem significa **sem
CORS** e cookie de sessão first-party. A SPA é opcional: parar a UI nunca afeta a
entrega de mensagens.

<details>
<summary>Prefere fazer pela API do EasyPanel?</summary>

Cada passo acima tem equivalente na API (`createAppService`, `updateAppEnv`,
`deployAppService`, `startAppService`, `createDomain`). As chamadas exatas, os
cuidados com identificadores e uma sequência copiável estão em
[`deploy/README.md`](../deploy/README.md).
</details>

---

## 3. Crie o primeiro admin

`GET /api/v1/auth/state` diz o que a ponte espera:

```sh
BRIDGE=https://bridge.example.com
curl -fsS "$BRIDGE/api/v1/auth/state"
# {"state":"needs_setup"}  → ainda não existe admin
# {"state":"ready"}        → já existe admin (ex.: semeado por ADMIN_USER/ADMIN_PASS)
```

Se responder `needs_setup`:

```sh
curl -sS -c cookies.txt -X POST "$BRIDGE/api/v1/auth/setup" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' \
  -d '{"username":"admin","password":"<uma-senha-longa>","displayName":"Admin"}'
```

Se responder `ready`, faça login:

```sh
curl -sS -c cookies.txt -X POST "$BRIDGE/api/v1/auth/login" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' \
  -d '{"username":"admin","password":"<sua-senha>"}'
```

Toda mutação abaixo exige o cookie de sessão **e** o cabeçalho `X-CSRF: 1`. Esse
é todo o modelo de autenticação: faça login, guarde o cookie, envie o cabeçalho.

---

## 4. Crie a conexão e ligue os webhooks

Uma conexão = uma instância do Evolution + uma inbox do Chatwoot em uma conta.

```sh
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' \
  -d '{
        "name":"WhatsApp Suporte",
        "evolutionApiUrl":"https://evolution.example.com",
        "evolutionApiKey":"<api key global do evolution>",
        "evolutionInstanceName":"minha-instancia",
        "chatwootApiUrl":"https://chatwoot.example.com",
        "chatwootApiToken":"<api_access_token do chatwoot>",
        "chatwootAccountId":1,
        "chatwootInboxId":1,
        "isActive":true
      }'
```

Depois, nesta ordem:

```sh
ID=<o id retornado acima>

# a) verifica se os dois upstreams respondem e as credenciais funcionam
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/test" -H 'X-CSRF: 1'

# b) persiste o token da instância do Evolution (usado para validar o ingresso)
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/resolve-token" -H 'X-CSRF: 1'

# c) configura OS DOIS webhooks em uma chamada (Evolution + inbox do Chatwoot)
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/webhook" \
  -H 'Content-Type: application/json' -H 'X-CSRF: 1' -d '{}'

# d) confirma o que está configurado (URLs mascaradas)
curl -sS -b cookies.txt "$BRIDGE/api/v1/connections/$ID/webhook-status"
# {"evolution":{"configured":true,...},"chatwoot":{"configured":true,...},"instanceStatus":"..."}
```

O passo **(c)** faz as duas ligações por você:

- **Evolution**: `POST /instance/connect` na sua instância com
  `{webhookUrl, subscribe}` e o cabeçalho `apikey`, onde `webhookUrl` é
  `https://<BRIDGE_URL>/api/webhooks/evolution/<id>/<token>`.
- **Chatwoot**: define a URL de webhook da inbox para
  `https://<BRIDGE_URL>/api/webhooks/chatwoot/<id>/<token>`.

O `<token>` é derivado, nunca guardado em texto puro em outro lugar:
`base64url(HMAC-SHA256(WEBHOOK_TOKEN_SECRET, "webhook:<id>"))`. Se precisar ligar
manualmente, as duas URLs com token são retornadas **apenas** pelos dois
endpoints dedicados (autenticados, `Cache-Control: no-store`):

```sh
curl -sS -b cookies.txt "$BRIDGE/api/v1/connections/$ID/webhook-url/evolution"
curl -sS -b cookies.txt "$BRIDGE/api/v1/connections/$ID/webhook-url/chatwoot"
```

> Rotacionar o `WEBHOOK_TOKEN_SECRET` muda as duas URLs. Refaça o passo **(c)**
> logo depois, senão as duas plataformas continuam postando numa URL que agora
> responde `401`.

### 4.1 Se preferir ligar as plataformas na mão

**Evolution GO** — a lista de eventos importa. No Evolution GO 0.7.2 os nomes
aceitos são os da v1; nomes da v2 são silenciosamente ignorados por algumas
builds, o que **para a entrega sem erro**:

```sh
curl -sS -X POST "$EVO_URL/instance/connect" \
  -H "apikey: $EVOLUTION_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
        "instanceName":"minha-instancia",
        "webhookUrl":"https://bridge.example.com/api/webhooks/evolution/1/<token>",
        "subscribe":["MESSAGE","SEND_MESSAGE","READ_RECEIPT","PRESENCE",
          "HISTORY_SYNC","CHAT_PRESENCE","CALL","CONNECTION","LABEL","CONTACT",
          "GROUP","NEWSLETTER","QRCODE","BUTTON_CLICK","PICTURE","USER_ABOUT"]
      }'
```

**Chatwoot** — Inbox settings → Configuration → *Webhook URL*:
`https://bridge.example.com/api/webhooks/chatwoot/1/<token>`. Se você definir
`CHATWOOT_WEBHOOK_SECRET`, a rota passa a exigir também um HMAC válido da conta e
falha fechado.

### 4.2 Trazer o histórico existente (opcional)

```sh
curl -sS -b cookies.txt -X POST "$BRIDGE/api/v1/connections/$ID/contacts/sync" -H 'X-CSRF: 1'
# {"total":…,"synced":…,"skipped":…,"failed":…}
```

Isso popula o `contacts_map` com os contatos já existentes no Chatwoot, para que
identidades conhecidas sejam reutilizadas em vez de recriadas.

---

## 5. Migrar de uma ponte existente

O Evolution GO guarda **um** webhook por instância. Para sair de outra ponte sem
buraco:

1. Implante esta ponte e crie a conexão (§4), mas **não** vire o webhook ainda.
2. Anote a URL de webhook atual do Evolution — é o seu valor de rollback.
3. Rode a migração e suba esta ponte.
4. Aponte o webhook do Evolution para esta ponte (passo **(c)** acima).
5. Envie uma mensagem em cada direção e confirme em `GET /api/v1/logs`.
6. Mantenha a ponte anterior rodando (ou a imagem dela) até você estar satisfeito;
   a virada é uma única chamada `POST /instance/connect` para reverter.

Enquanto o webhook aponta para cá, a ponte anterior não recebe nada. Mantenha a
janela curta.

---

## 6. Verificar o fluxo ponta a ponta

| # | Ação | Esperado |
|---|---|---|
| 1 | Cliente manda mensagem no WhatsApp | nova linha em `message_logs`, `direction=whatsapp_to_chatwoot`, `status=sent`; a mensagem aparece na conversa mapeada do Chatwoot |
| 2 | O mesmo webhook é reenviado (ou entregue duas vezes) | a segunda entrega → `duplicate`; **nenhuma** segunda mensagem no Chatwoot |
| 3 | Atendente responde no Chatwoot | linha `direction=chatwoot_to_whatsapp`, `status=sent`; a resposta chega uma única vez no WhatsApp |
| 4 | O eco do envio da própria ponte volta | classificado como mensagem própria — nunca reencaminhado (quebra de laço) |
| 5 | Mensagem enviada direto do WhatsApp Web | **reutiliza** contato/conversa existentes; nenhum contato novo, nenhum grupo renomeado |
| 6 | `/api/v1/connections/$ID/webhook-status` | os dois `configured:true` |

Inspecione tudo:

```sh
curl -sS -b cookies.txt "$BRIDGE/api/v1/logs?connectionId=$ID&limit=20"
curl -sS -b cookies.txt "$BRIDGE/api/v1/dashboard/stats"
```

Checagem de quarentena (regra: envio com resultado desconhecido nunca é
reenviado às cegas):

```sql
SELECT id, direction, message_type, status, source_message_id, dest_message_id
FROM message_logs
WHERE connection_id = <ID> ORDER BY id DESC LIMIT 20;
```

```sql
SELECT external_id, direction, status, error_code, attempt
FROM processed_messages
WHERE connection_id = <ID> ORDER BY updated_at DESC LIMIT 20;
```

`status='quarantined'` é uma parada deliberada, não um crash. Veja
[ARCHITECTURE.md §3](ARCHITECTURE.md) e
[Problemas comuns](TROUBLESHOOTING.md#mensagens-em-quarentena).

---

## 7. Atualizar e reverter

**Atualizar**

```sh
git fetch && git checkout <tag>
docker compose -f deploy/docker-compose.prod.yml run --rm bridge migrate
docker compose -f deploy/docker-compose.prod.yml up -d
```

As migrações são forward-only: rode a migração **antes** do servidor novo. É
idempotente e com lock (`GET_LOCK`), então starts concorrentes são seguros.

**Reverter**

O schema é aditivo, então um binário antigo continua funcionando sobre um schema
migrado. Reimplante a imagem/tag anterior; não existe (nem é necessário)
down-migration.

**Desinstalar**

Pare o app, remova os containers/volumes que você criou e derrube o banco se
quiser. Nada fora do seu banco e das suas duas configurações de webhook foi
tocado por esta ponte — remova ou reaponte o webhook do Evolution e o da inbox do
Chatwoot antes de desativá-la, senão eles continuarão postando numa URL morta.

## 8. Próximos passos

- [CONFIGURATION.md](CONFIGURATION.md) — todas as variáveis e campos por conexão.
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) — os modos de falha vistos na prática.
- [ARCHITECTURE.md](ARCHITECTURE.md) — por que foi construído assim.
- [deploy/README.md](../deploy/README.md) — referência do compose e o caminho pela API do EasyPanel.
