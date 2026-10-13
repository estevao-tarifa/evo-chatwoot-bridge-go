# Ponte Evolution GO ⇄ Chatwoot (Go)

[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)](backend/go.mod)
[![backend CI](https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/actions/workflows/backend.yml/badge.svg)](https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/actions/workflows/backend.yml)
[![frontend CI](https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/actions/workflows/frontend.yml/badge.svg)](https://github.com/estevao-tarifa/evo-chatwoot-bridge-go/actions/workflows/frontend.yml)
[![Licença: MIT](https://img.shields.io/badge/Licen%C3%A7a-MIT-yellow.svg)](LICENSE)
[![Deploy](https://img.shields.io/badge/Deploy-Docker%20%7C%20EasyPanel-2496ED?logo=docker&logoColor=white)](deploy/README.md)

**English:** [README.md](README.md) · **Português:** este arquivo

Ponte **bidirecional** entre o **[Evolution GO](https://github.com/EvolutionAPI/evolution-go)**
(WhatsApp) e o **[Chatwoot](https://www.chatwoot.com/)**: mensagem que o cliente
manda no WhatsApp aparece na conversa certa do Chatwoot e **toda resposta do
atendente sai no WhatsApp uma única vez** — mesmo com reinícios, retentativas e
webhooks duplicados.

Servidor em **Go** (binário único, sem Node/Vite/React em runtime no backend) com
**SPA React de administração implantada de forma independente**, sobre o schema
MySQL existente. Instalação por **Docker Compose** ou **EasyPanel**.

> Procurando por: *integrar WhatsApp no Chatwoot*, *ponte Evolution API Chatwoot*,
> *Chatwoot com WhatsApp*, *atendimento WhatsApp self-hosted*, *bridge de webhook
> em Go*, *entrega idempotente de mensagens*? É isto.

**Documentação** · [Instalação](docs/INSTALL.md) ·
[Configuração](docs/CONFIGURATION.md) · [Problemas comuns](docs/TROUBLESHOOTING.md) ·
[Arquitetura](docs/ARCHITECTURE.md) · [API REST](docs/API.md) ·
[Decisões (ADRs)](docs/adr/) · [Todas as docs](docs/README.md)

---

## Índice

- [Por que esta ponte existe](#por-que-esta-ponte-existe)
- [Recursos](#recursos)
- [Arquitetura](#arquitetura)
- [Instalação rápida (Docker Compose)](#instalação-rápida-docker-compose)
- [Instalação no EasyPanel](#instalação-no-easypanel)
- [Como funciona](#como-funciona)
- [Configuração](#configuração)
- [Problemas comuns](#problemas-comuns)
- [Perguntas frequentes](#perguntas-frequentes)
- [Estado do projeto e limitações conhecidas](#estado-do-projeto-e-limitações-conhecidas)
- [Segurança](#segurança)
- [Contribuição](#contribuição)
- [Licença](#licença)

## Por que esta ponte existe

Ligar o WhatsApp a um atendimento omnichannel parece simples até as mensagens
chegarem duplicadas, a resposta do atendente se perder, ou um webhook reenviado
criar um contato duplicado. O difícil não é a chamada HTTP: é **identidade** e
**idempotência**.

- Um grupo de WhatsApp é o mesmo grupo amanhã **apenas** se você usar o **JID
  estável**, nunca o nome de exibição.
- Mensagem enviada por fora (WhatsApp Web, outra automação, o celular) deve
  **reutilizar** o contato e a conversa existentes, não criar novas entidades.
- Nome de contato nunca pode ser sobrescrito com `"Grupo"`, telefone ou JID.
- O eco de uma mensagem que a própria ponte enviou precisa ser reconhecido, para
  não voltar em laço.
- Dois workers não podem entregar o mesmo webhook.

Esta ponte foi construída em torno desses invariantes (veja
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) e os [ADRs](docs/adr/)) e os garante
com uma tabela de claims persistente — não com cache em memória.

## Recursos

| | |
|---|---|
| **Bidirecional** | WhatsApp → Chatwoot e Chatwoot → WhatsApp, texto e legenda de mídia |
| **Semântica de entrega única** | tabela de claims (`processed_messages`) com protocolo de vencedor único: webhook repetido nunca gera mensagem repetida |
| **Seguro em resultado desconhecido** | envio sem confirmação vai para **quarentena** e é registrado — nunca reenviado às cegas, nunca duplicado |
| **Integridade de identidade** | reuso de contato/conversa antes de criar; grupos pelo JID estável, escopados por conexão e conta; nomes jamais sobrescritos por placeholder |
| **Proteção de laço** | reconhece o eco dos próprios envios nos dois sentidos |
| **Mapeamento de conversa** | o id da conversa resolvido é persistido, então a resposta do atendente numa conversa criada pelo WhatsApp é roteada de volta |
| **Token de webhook por conexão** | `base64url(HMAC-SHA256(WEBHOOK_TOKEN_SECRET, "webhook:<connectionId>"))`, comparação em tempo constante, falha fechado |
| **API admin autenticada** | sessão + cabeçalho CSRF, usuários bcrypt, segredos mascarados, login sem enumeração |
| **Lista branca de eventos** | só eventos reais de mensagem são processados; presença/recibo/label/conexão são respondidos e descartados antes de qualquer escrita |
| **Migrações forward-only** | com checksum, `GET_LOCK`, aditivas; verificadas sobre schema legado em MySQL 8 e MySQL 9 |
| **Frontend separável** | o backend **não** serve assets; a SPA é um container nginx independente (ADR-0007) |

## Arquitetura

```mermaid
flowchart LR
  WA["WhatsApp"] -->|mensagens| EVO["Evolution GO"]
  EVO -->|"POST /api/webhooks/evolution/{id}/{token}"| GO["bridge (Go)<br/>claim → rota → entrega"]
  CW["Chatwoot"] -->|"POST /api/webhooks/chatwoot/{id}/{token}"| GO
  GO -->|REST do Chatwoot| CW
  GO -->|REST do Evolution| EVO
  GO <--> DB[("MySQL<br/>connections · contacts_map<br/>message_logs · processed_messages")]
  SPA["SPA React (nginx)"] -->|"/api/v1/*"| GO
```

Um binário, um MySQL, sem broker de mensagens. Pacotes internos:
`config`, `httpapi`, `webhookauth`, `bridge`, `evolution`, `chatwoot`, `dedup`,
`storage/mysql`, `worker`, `admin`, `auth`, `observability`.

## Instalação rápida (Docker Compose)

Pré-requisitos: Linux com Docker, um **MySQL 8/9**, uma instância
**[Evolution GO](https://github.com/EvolutionAPI/evolution-go)** conectada ao
WhatsApp e uma conta **Chatwoot** com uma inbox de canal API.

```sh
git clone https://github.com/estevao-tarifa/evo-chatwoot-bridge-go.git
cd evo-chatwoot-bridge-go

cp deploy/example.env deploy/.env
# edite deploy/.env: DATABASE_URL, WEBHOOK_TOKEN_SECRET, SESSION_SECRET,
# ADMIN_USER/ADMIN_PASS, BRIDGE_URL (sua URL pública)

# 1. crie/atualize o schema — SEMPRE antes do primeiro start
docker compose -f deploy/docker-compose.prod.yml run --rm bridge migrate

# 2. suba
docker compose -f deploy/docker-compose.prod.yml up -d

# 3. verifique
curl -fsS http://127.0.0.1:8100/healthz && echo " ok"
curl -fsS http://127.0.0.1:8100/readyz  && echo " banco acessível"
```

Depois crie a conexão (API admin ou SPA) e aponte os dois webhooks — o passo a
passo, incluindo a lista `subscribe` que o Evolution GO aceita, está em
**[docs/INSTALL.md](docs/INSTALL.md)**.

> Gere segredos com `openssl rand -base64 32`. Nunca versione o `deploy/.env`.

## Instalação no EasyPanel

O EasyPanel (Traefik + Docker Swarm) é o alvo de referência, validado ponta a
ponta em produção real.

```text
Projeto no EasyPanel
├── app  bridge     ← este repositório, build path /backend, porta interna 3000
│    env: DATABASE_URL, BRIDGE_URL, WEBHOOK_TOKEN_SECRET, SESSION_SECRET, ADMIN_*
└── app  bridge-ui  ← opcional, build path /frontend, porta interna 80
     env: BRIDGE_API_BASE_URL=https://<seu-dominio-da-ui>   (same-origin ⇒ sem CORS)
     domínio:<seu-dominio>/     → bridge-ui
     domínio:<seu-dominio>/api  → bridge
```

O passo a passo completo está em **[docs/INSTALL.md](docs/INSTALL.md)**, com a
variante via API do EasyPanel em [deploy/README.md](deploy/README.md).

## Como funciona

**Ingresso com claim antes do ACK.** O webhook é autenticado pelo token da
conexão, filtrado pela lista de eventos e então *reivindicado* por exatamente um
worker antes de ser confirmado; uma segunda entrega perde a claim e recebe `200`
sem efeitos.

**Roteamento por matriz congelada.** Filtros de payload primeiro (nome do evento,
tipo de mensagem, nota privada, agent bot); depois a identidade: **mapeamento
primeiro**, depois busca exata, e só então criação — condicionada a conexão ativa,
inbox válida e nome autoritativo no caso de grupo. Grupo cujo único nome
disponível é `"Grupo"` nunca é criado.

**Entrega em uma tentativa por claim.** Envio com id correlacionável conclui a
claim; recusa falha; resultado desconhecido vai para quarentena.

## Configuração

Tudo por variáveis de ambiente: referência anotada em
[deploy/example.env](deploy/example.env) e comportamento em
[docs/CONFIGURATION.md](docs/CONFIGURATION.md).

| Variável | Para que serve |
|---|---|
| `DATABASE_URL` | `mysql://user:pass@host:3306/banco` |
| `PORT` | porta HTTP (padrão `3000`) |
| `APP_ENV` | `production` \| `development` |
| `BRIDGE_URL` | URL pública desta ponte (base das URLs de webhook) |
| `WEBHOOK_TOKEN_SECRET` | segredo por trás do token de cada webhook |
| `SESSION_SECRET` | assinatura da sessão admin |
| `ADMIN_USER`, `ADMIN_PASS` | admin inicial, criado apenas se não existir nenhum |
| `CHATWOOT_WEBHOOK_SECRET` | opcional; se definido, a rota do Chatwoot também exige HMAC válido |
| `COOKIE_SECURE`, `COOKIE_SAMESITE` | política do cookie de sessão |
| `CORS_ALLOWED_ORIGINS` | só necessário se a SPA estiver em outra origem |
| `LOG_LEVEL` | `debug` \| `info` \| `warn` \| `error` |

Rotacionar o `WEBHOOK_TOKEN_SECRET` invalida as duas URLs de webhook — reaplique-as
no Chatwoot e no Evolution.

## Problemas comuns

Sintoma, causa e correção dos casos mais frequentes — `401` no webhook, instância
do Evolution que para de entregar porque a lista `subscribe` foi trocada por nomes
de evento da v2, ordem *migrate-antes-do-start*, e a SPA chamando a origem errada
da API — em **[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md)**.

## Perguntas frequentes

<details>
<summary><strong>Preciso de Redis, Kafka ou fila externa?</strong></summary>

Não. Uma fila interna com limite + a tabela de claims no MySQL dão as garantias de
entrega; a fila drena no `SIGTERM`.
</details>

<details>
<summary><strong>Posso rodar mais de uma réplica?</strong></summary>

Hoje rode **uma**. O protocolo de claim é seguro por réplica, mas o índice único
de `contacts_map` e o fallback de conflito do upsert ainda estão no roadmap.
</details>

<details>
<summary><strong>Ele mexe no schema legado?</strong></summary>

Não de forma destrutiva. As migrações são aditivas e forward-only; nenhuma tabela
existente é alterada de forma destrutiva e `__drizzle_migrations` nunca é tocado.
</details>

<details>
<summary><strong>E se o Chatwoot cair?</strong></summary>

Leituras têm retentativa com backoff exponencial limitado; mutação com resultado
desconhecido vai para quarentena (sem retentativa) para nunca entregar duas vezes.
</details>

<details>
<summary><strong>Tem suporte a grupos?</strong></summary>

Sim. Grupo é chaveado pelo JID completo, escopado por conexão e conta, o nome vem
só de `groupData.name` autoritativo, e grupos podem ser ignorados por conexão.
</details>

## Estado do projeto e limitações conhecidas

O núcleo está validado em produção: entrega bidirecional, ida e volta do Chatwoot,
quebra de laço, reuso de identidade sem criar entidades duplicadas e a lista
branca de eventos foram exercitados com tráfego real. Lacunas conhecidas, ditas
com honestidade:

- **Quarentena em vez de retentativa (B1):** envio com resultado desconhecido é
  colocado em quarentena. Troca uma mensagem raramente perdida por uma garantia de
  não duplicar.
- **A quebra de laço depende do id da mensagem criada no Chatwoot (B5).**
- **Uma única réplica** até entrarem o índice único e o fallback de conflito.
- **Métricas e performance (planejadas)** especificadas, ainda não mergeadas.
- **`instanceStatus`** do painel pode mostrar `unknown` no Evolution GO 0.7.2.
- Toggles por conexão além de `ignoreGroups` (assinar mensagens, leitura
  automática, rejeição de chamada, comportamentos de inbox) são persistidos e
  editáveis na API, mas ainda não aplicados pelo engine.
- A suíte de integração precisa de `-timeout 30m` em hosts compartilhados.

O roadmap é acompanhado nas issues e refletido no [CHANGELOG.md](CHANGELOG.md):
vêm a seguir o índice de unicidade entre réplicas, as métricas Prometheus com a
suíte de benchmarks e a aplicação dos toggles restantes por conexão.

## Segurança

- Rotas de webhook falham fechado: token ausente ou errado → `401`, nunca
  redirecionamento; `WEBHOOK_TOKEN_SECRET` ausente → `503`.
- Rotas admin exigem sessão; mutações exigem também o cabeçalho `X-CSRF: 1` e
  corpo JSON. O login não distingue usuário inexistente.
- Tokens, cookies, cabeçalhos de autenticação, base64 de mídia e corpo de mensagem
  nunca são logados; payloads de webhook são sanitizados antes de persistir (o
  payload do Evolution carrega `instanceToken` — trate como segredo).
- Vulnerabilidades: veja [SECURITY.md](SECURITY.md).

## Contribuição

Fluxo de trabalho, convenções de commit, gates obrigatórios de teste/QA/review e
requisitos de estilo em Go estão em [CONTRIBUTING.md](CONTRIBUTING.md). Os
princípios de engenharia Go aplicados aqui estão resumidos em
[docs/SKILLS.md](docs/SKILLS.md).

## Licença

[MIT](LICENSE).
