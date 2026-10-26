# Client_GO em produção

O Client_GO roda como **terceiro container da EC2 do Booklit** (ao lado do Spring e do Caddy), usa um
**database próprio no RDS do Booklit** e fala com o SNS/SQS/SES da mesma conta. Não cria máquina nem
banco novos, então o custo extra é praticamente zero.

```
GitHub (push na main de jotagm/client-go)
  └─ Actions: go test ─► imagem arm64 ─► ECR ─► SSM Run Command ─► EC2 (/opt/client-go/deploy.sh)

 EC2 t4g.micro (rede Docker "booklit_default")
 ┌──────────────────────────────────────────────────────────────────────────┐
 │ caddy :443 ─► api (Spring :8080) ──POST /notificacoes──► client-go :8081   │
 │                                      (só dentro da rede)     │  │  │       │
 └──────────────────────────────────────────────────────────────┼──┼──┼───────┘
                                       RDS: database "aviso" ◄───┘  │  │
                              SNS aviso-prod-notificacoes ◄──────────┘  │
                                  └─► SQS aviso-prod-email-alta / -email ─┘
                                        (+ DLQs)        consumidor ──► SES (booklit.bio)
```

## O que mudou em relação ao plano original

1. **O papel IAM é um só.** Os dois containers rodam na mesma instância e assumem o mesmo papel
   (`booklit-prod-api`). O Client_GO ganha uma política própria (`client-go-prod`, em `infra/envs/prod/iam.tf`)
   anexada a esse papel, mas **não dá para tirar o `ses:SendEmail` do Booklit de forma que o container
   dele não consiga mais enviar**: separar exigiria ECS/EKS. O ganho real é de código (o Booklit deixa
   de ter o `SesEnviadorDeOtp`) e de histórico/retentativa, não de isolamento de permissão.
2. **O banco é o RDS, não o Postgres da EC2.** Não existe Postgres na EC2. O Client_GO usa o database
   `aviso` com o usuário `aviso` dentro do RDS do Booklit (`scripts/criar-banco-prod.sh`, chamado pelo deploy).
3. **A fila `notifications` do Booklit não precisa ser renomeada.** O Client_GO tem tópico e filas
   próprios (`aviso-prod-*`) e nunca lê `booklit-prod-notifications`. O motivo do plano ("os dois disputam
   as mensagens") não existe. Fica como limpeza opcional, com a ordem corrigida ao final deste documento.

## Pré-requisitos

- A infra do Booklit (`booklit-server/infra/envs/prod`) já aplicada. Ela é a dona da EC2, do RDS, do domínio
  `booklit.bio` no SES (com DKIM) e do provedor OIDC do GitHub.
- O código da Fase 4 do Booklit (`ClientGoEnviadorDeOtp`) deployado em produção. Com
  `BOOKLIT_EMAIL_PROVEDOR` ausente, o Booklit continua enviando pelo SES como hoje.
- Domínio `booklit.bio` **Verified** no SES. Conta ainda no sandbox só envia para os endereços de
  `emails_teste_sandbox`; para o Client_GO atender qualquer usuário é preciso o acesso de produção do SES.
- AWS CLI com permissão de administrador e Terraform ≥ 1.6 no seu computador.

## Passo a passo

### 0. Validar o Terraform sem tocar na AWS

No lab (precisa de Docker e do registry do Terraform):

```bash
cd ~/client-go-validacao && bash scripts/validar-terraform.sh
```

Isso roda `fmt -check` e `validate` em `infra/envs/prod` e `infra/envs/floci`. Foi escrito sem poder rodar
o Terraform onde o código foi gerado, então este é o primeiro lugar onde ele é de fato validado.

### 1. Aplicar a infra do Client_GO

```bash
# valores do Terraform do Booklit
cd booklit-server/infra/envs/prod && terraform output EC2_INSTANCE_ID banco_endpoint

cd Client_GO/infra/envs/prod
cp terraform.tfvars.example terraform.tfvars   # preencha ec2_instance_id e rds_endpoint
terraform init
terraform plan -out=prod.tfplan
```

Leia o plano antes de aplicar. Esperado: **só criações** (tópico, 2 filas e 2 DLQs, assinaturas, repositório ECR,
papel do GitHub, parâmetros do SSM, senhas) e **uma política nova** (`client-go-prod`) no papel
existente `booklit-prod-api`. Nenhum recurso do Booklit deve ser alterado ou destruído; se aparecer, pare.

```bash
terraform apply prod.tfplan
```

**Faça backup do `terraform.tfstate`**: ele guarda a senha do banco e o token.

### 2. Variáveis do repositório no GitHub

Em `jotagm/client-go` → Settings → Secrets and variables → Actions → Variables, crie com as saídas de
`terraform output`: `AWS_ROLE_ARN`, `AWS_REGION`, `ECR_REPOSITORY`, `EC2_INSTANCE_ID`, `DOCKER_PLATFORM`.
Nenhuma é segredo.

### 3. Primeiro deploy

Push na `main` do Client_GO (ou *Actions → CI/CD → Run workflow*). O pipeline roda os testes, builda a imagem
arm64, envia ao ECR e dispara `/opt/client-go/deploy.sh` na EC2 por SSM. O script garante o database `aviso`,
aplica as migrações (`client-go migrar`), sobe o container e espera o healthcheck; se não ficar saudável em
2 minutos, volta a imagem anterior.

Se o deploy falhar com "Rede booklit_default não existe", o Booklit ainda não foi deployado nesta instância.

### 4. Conferir sem tráfego real

Numa sessão da EC2 (`aws ssm start-session --target <EC2_INSTANCE_ID>`), mandando um e-mail para um endereço
verificado no sandbox:

```bash
TOKEN=$(aws ssm get-parameter --name /booklit/prod/BOOKLIT_CLIENT_GO_TOKEN --with-decryption \
  --query Parameter.Value --output text)
BODY='{"template":"otp-login","destinatario":"devjotagm@gmail.com","prioridade":"alta","variaveis":{"codigo":"123456"},"expira_em":"'$(date -u -d '+10 min' +%Y-%m-%dT%H:%M:%SZ)'"}'

sudo docker run --rm --network booklit_default -e TOKEN -e BODY --entrypoint sh curlimages/curl:8.10.1 -c \
  'curl -s -i -X POST http://client-go:8081/notificacoes -H "Authorization: Bearer $TOKEN" -H "Idempotency-Key: teste-prod-1" -d "$BODY"'
```

Esperado: `HTTP/1.1 202`, e o e-mail na caixa em poucos segundos. Para ver o estado e a tentativa:

```bash
URL=$(aws ssm get-parameter --name /client-go/prod/AVISO_DB_URL --with-decryption --query Parameter.Value --output text)
sudo docker run --rm postgres:16-alpine psql "$URL" -c \
  "select n.estado, n.motivo, t.provedor, t.resultado, t.latencia_ms from t_notificacao n left join t_tentativa_envio t on t.notificacao_id = n.id order by n.criado_em desc limit 5"
```

Esperado: `enviada | | ses | sucesso | <ms>`. (A URL com a senha fica na linha de comando só durante este
comando, numa sessão que só o root da instância vê.) Os logs do container estão no CloudWatch, grupo
`/booklit/prod`, stream `client-go`.

### 5. A virada

```bash
aws ssm put-parameter --name /booklit/prod/BOOKLIT_EMAIL_PROVEDOR --type String --value client-go --overwrite
```

Depois rode o workflow do Booklit (*Run workflow*) ou `sudo /opt/booklit/deploy.sh <imagem>` na EC2: o parâmetro só
vira variável do container no deploy. Acompanhe os primeiros logins:

- Booklit (stream `api`): `OTP entregue ao Client_GO desafio=...`. Qualquer `Client_GO recusou ... status=` ou
  `Falha ao enviar OTP ao Client_GO` é um código que não saiu.
- Client_GO (stream `client-go`): uma linha `estado alterado` por mudança, terminando em `enviada`.

O Terraform ignora o valor desse parâmetro (`ignore_changes`), então um `apply` futuro não desfaz a virada.

### 6. Plano de volta

Mesmo comando com `--value ses` e um deploy do Booklit (~2 minutos). O Client_GO pode continuar de pé;
nada mais depende dele. Enquanto estiver no `client-go` e o Client_GO cair, **o usuário recebe 202 mas
nenhum e-mail sai**: ele pede outro código (decisão do projeto: o código vale 10 minutos, não vale um outbox
que gravaria o código em claro). Por isso o primeiro dia merece olhar para os logs.

### 7. Depois de uma semana estável

Remover o `SesEnviadorDeOtp` do Booklit, o provedor `ses` do `AuthOtpConfig` e o `SesV2Client`. A permissão
`ses:SendEmail` do Booklit pode sair do Terraform dele, mas lembre da ressalva 1 acima: o papel é compartilhado
e o do Client_GO a mantém.

## Operação

| Situação | O que fazer |
|---|---|
| Ver mensagens que esgotaram as tentativas | DLQs `aviso-prod-email-alta-dlq` e `aviso-prod-email-dlq` (retenção de 14 dias) |
| Trocar o token do Booklit | `terraform apply -replace=random_password.token_booklit` e depois um deploy do Client_GO **e** um do Booklit (os dois leem o token no deploy) |
| Mudar remetente ou número de workers | Variável do Terraform, `apply` e deploy do Client_GO |
| Voltar uma versão do Client_GO | `sudo /opt/client-go/deploy.sh <ecr>/client-go:<sha anterior>`; as migrações só adicionam, a imagem antiga funciona em cima do esquema novo |
| Reiniciar a instância | Uma mensagem pode ficar até 30 s invisível (visibility timeout) porque o long polling cancelado pode já ter pegado uma; ela volta sozinha. Medido na Fase 3 |

## Custo e memória

- **Sem recursos novos de custo fixo**: sem EC2, RDS ou NAT. SNS, SQS e SSM (parâmetros padrão) cabem nos
  níveis gratuitos; o Client_GO faz ~130 mil `ReceiveMessage` por mês com 2 workers por fila (o nível gratuito
  do SQS é 1 milhão). ECR guarda 10 imagens de ~20 MB. O SES cobra US$ 0,10 por mil e-mails.
- **Memória é o ponto de atenção**: a t4g.micro tem 1 GB para o Spring (700 MB), o Caddy (64 MB), o Client_GO
  (64 MB, `GOMEMLIMIT=48MiB`), o Docker e o agente do SSM, com 1 GB de swap como rede de segurança. Depois do primeiro
  deploy rode `free -m` e `sudo docker stats --no-stream` na EC2. Se o swap estiver sendo usado de forma contínua,
  reduza `mem_limit` do Spring (por exemplo 640m) ou suba para t4g.small (cerca de US$ 6 a mais por mês).

## Limpeza opcional: renomear a fila `notifications` do Booklit

Só vale pelo nome (`notifications` sugere o Client_GO, mas é a fila do `VotacaoEncerradaConsumidor`). Como o tópico
entrega a **todas** as filas assinadas, a ordem segura é:

1. No Terraform do Booklit, `consumers = ["notifications", "leituras"]` e `BOOKLIT_EVENTOS_FILA_URL` apontando
   para `queue_urls["leituras"]`; `apply`.
2. Deploy do Booklit. A fila nova já recebe todos os eventos desde o `apply`, então não perde nenhum.
3. Remover `notifications` de `consumers` e `apply`. As mensagens que sobrarem lá são cópias das que a fila nova já
   entregou; **não é preciso esperar a fila esvaziar** (ela nunca esvazia: continua assinada e recebendo até
   o passo 3).
