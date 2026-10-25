# Client_GO

Serviço de notificações em Go. Outros sistemas pedem um aviso; o Client_GO decide como entregar
(e-mail, push ou SMS), tenta de novo quando o provedor falha de forma temporária e registra o que
aconteceu com cada envio.

Primeiro cliente: o código de login e verificação de e-mail do [Booklit](https://github.com/jotagm/booklit-server).

> O projeto começou com o nome **Aviso**. Ficaram com esse nome, de propósito, as variáveis de
> ambiente `AVISO_*`, o prefixo `aviso-` do tópico e das filas na AWS e o banco/usuário `aviso` do
> Postgres: trocar esses nomes recriaria a infraestrutura e quebraria configurações já montadas.

## Fluxo

```
Booklit (após o commit do desafio OTP)
  → POST /notificacoes            ← 202 Accepted + id
Client_GO API
  → valida template e idempotência, checa supressão, grava em t_notificacao
  → SNS  aviso-notificacoes  (atributos: canal, prioridade, origem)
       ├─ canal=email, prioridade=alta   → SQS aviso-email-alta  (retenção 15 min)
       └─ canal=email, prioridade=normal → SQS aviso-email       (retenção 4 dias)
       cada fila com DLQ (redrive depois de 10 recebimentos)
Consumidores Go (um laço por fila, N workers)
  → lê a notificação do banco; as variáveis vêm do envelope
  → já processada?           → apaga a mensagem (a fila entrega "pelo menos uma vez")
  → expirou?                 → expirada
  → entrega (fase 3: template + SES; por enquanto simulada)
       ok                    → enviada
       erro permanente       → falhou
       erro temporário       → ChangeMessageVisibility com backoff (2s, 4s, 8s, 16s)
                               → falhou na 5ª tentativa, ou expirada se o código vencer antes
Recibo do SES → entregue | devolvida      (fase 6)
Reconciliação: enviada sem recibo → incerta (fase 6)
```

As filter policies são disjuntas de propósito: uma notificação que caísse em duas filas seria
processada duas vezes.

## Estados

`aceita → enviada → entregue | devolvida | incerta`, e `aceita → falhou | suprimida | expirada`.
O 200 do provedor só leva até `enviada`. O painel conta pelo estado final, não pelas tentativas.

## Estrutura

```
cmd/client-go/              ponto de entrada: API e consumidores no mesmo processo
internal/notificacao/   domínio: Notificacao, Template, Envelope, Estado e transições
internal/porta/         portas: Repositorio, Publicador, Consumidor, Entregador, Provedor
internal/aplicacao/     casos de uso: aceitar, buscar, processar mensagem, laço de consumo, backoff
internal/api/           HTTP e autenticação por origem
internal/adapter/       postgres, sns, sqs e memoria (para teste e para rodar sem AWS)
infra/                  Terraform: módulo de mensageria e ambiente do Floci
db/                     scripts SQL
docs/                   ADRs e plano de implementação
```

## Rodando

Precisa de Go 1.24 ou mais novo (o SDK da AWS pede).

```bash
go mod tidy                   # primeira vez: baixa as dependências e gera o go.sum
docker compose up -d          # Postgres do Client_GO na porta 5433, já com os scripts de db/
```

### Validação completa no lab (um comando)

Do WSL, com o Floci no ar no lab:

```bash
bash /mnt/d/Projetos/aviso/scripts/rodar-no-lab.sh
```

Copia o projeto para `~/client-go-validacao` no lab, roda `scripts/validar-floci.sh` lá
(Terraform, Postgres, `go mod tidy`, todos os testes com Postgres e Floci, ponta a ponta pela API,
desligamento e checagem de vazamento do código) e traz de volta `relatorio-floci.txt` e o `go.sum`.
Go e Terraform no lab são opcionais: sem eles, o script usa as imagens oficiais pelo Docker.

### Tópico e filas no Floci

Com o Floci do Booklit no ar em `localhost:4566`:

```bash
cd infra/envs/floci
terraform init
terraform apply
terraform output -raw topico_arn     # vai para AVISO_TOPICO_ARN
terraform output -raw aviso_filas    # vai para AVISO_FILAS
```

### Testes

```bash
# só unidade (Postgres e AWS são pulados)
go test ./...

# tudo, com o Postgres do compose e o Floci
AVISO_TESTE_DB_URL=postgres://aviso:aviso@localhost:5433/aviso \
AVISO_TESTE_AWS_ENDPOINT=http://localhost:4566 \
go test ./...
```

Os testes de AWS criam tópico e filas próprios (com sufixo único), então não mexem no que o
Terraform criou.

### Subindo

```bash
AVISO_DB_URL=postgres://aviso:aviso@localhost:5433/aviso \
AVISO_TOKENS=booklit=<token de 32+ caracteres> \
AVISO_TOPICO_ARN=<terraform output -raw topico_arn> \
AVISO_FILAS=<terraform output -raw aviso_filas> \
AWS_ENDPOINT_URL=http://localhost:4566 \
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
go run ./cmd/client-go
```

| Variável | Para quê |
|---|---|
| `AVISO_ENDERECO` | Endereço da API (padrão `:8081`; `8080` é do Booklit) |
| `AVISO_DB_URL` | Postgres do Client_GO |
| `AVISO_TOKENS` | `origem=token`, separados por vírgula; tokens com 32+ caracteres |
| `AVISO_TOPICO_ARN` | Tópico SNS. Vazio = publicador em memória (nada sai do processo) |
| `AVISO_FILAS` | `nome=url`, separados por vírgula. Vazio = sem consumidores |
| `AVISO_WORKERS` | Workers por fila (padrão 4) |
| `AWS_ENDPOINT_URL` | Aponta o SDK para o Floci; em produção, não defina |

No PowerShell, troque o `VAR=valor comando` por `$env:VAR="valor"` antes do comando.

**Fase 2:** a entrega ainda é simulada. A notificação vira `enviada` e o log registra
"entrega simulada", mas nenhum e-mail sai. O SES entra na fase 3.

## API

Todas as rotas, menos `/saude`, pedem `Authorization: Bearer <token>`. A origem do pedido
vem do token, nunca do corpo.

### `POST /notificacoes`

Cabeçalho `Idempotency-Key` obrigatório (até 200 caracteres). No Booklit, use o `desafioId`.

```json
{
  "template": "otp-login",
  "versao": 1,
  "destinatario": "jota@exemplo.com",
  "prioridade": "alta",
  "variaveis": { "codigo": "482913" },
  "expira_em": "2026-10-25T15:10:00Z"
}
```

`versao` é opcional (sem ela, vale a mais recente). `prioridade` é `alta` ou `normal` (padrão).
O canal vem do template.

| Status | Quando |
|---|---|
| `202` | Aceita. Com `Idempotent-Replayed: true`, é a repetição de um pedido já feito |
| `400` | JSON inválido, campo desconhecido ou corpo acima de 16 KiB |
| `401` | Token ausente ou inválido |
| `409` | Mesma chave com um pedido diferente |
| `422` | Pedido inválido; `campos` diz o que corrigir |
| `503` | Gravou, mas não conseguiu publicar; a notificação fica como `falhou/publicacao` |

Os valores das variáveis não são gravados no banco, não aparecem no log e não voltam na resposta.
Eles só existem na requisição e na mensagem da fila (criptografada em repouso).

### `GET /notificacoes/{id}`

Estado atual. Só a origem que pediu consegue consultar; para as outras, `404`.

## Próximos passos

Plano completo em [`docs/plano-de-implementacao.md`](docs/plano-de-implementacao.md).

1. ~~`POST /notificacoes` com idempotência e o adapter Postgres.~~ ✅
2. ~~Publicador SNS e consumidor SQS + versões em memória.~~ ✅
3. Entrega de verdade: templates com `missingkey=error`, provedor SES, histórico de tentativas.
4. Integração com o Booklit.
5. Produção, recibos, reconciliação e painel.
# client-go
