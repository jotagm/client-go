# Plano de implementação do Client_GO

Objetivo: tirar o envio de e-mail do Booklit e passar para o Client_GO, um serviço em Go com SNS + SQS
como transporte, sem quebrar o login com código em produção.

**O que muda de dono:** só o envio pelo SES (`SesEnviadorDeOtp`). O outbox (`OutboxRelay`) e o
`VotacaoEncerradaConsumidor` continuam no Booklit, porque são regra de domínio do clube de leitura.

**Estimativa:** de 8 a 10 sessões de estudo, contando as fases 1 a 5 (Client_GO em produção
substituindo o SES do Booklit). As fases 6 a 8 são evolução.

---

## Fase 0: Fundação ✅

- [x] Estrutura do projeto (`cmd`, `internal/notificacao`, `internal/porta`, `internal/adapter`)
- [x] Domínio: `Notificacao` e máquina de estados com testes
- [x] Portas: `Publicador`, `Consumidor`, `Provedor`
- [x] Esquema inicial (`db/001_inicial.sql`) e `docker-compose` do Postgres
- [x] ADR 0001 (transporte) e ADR 0002 (código de OTP na fila, em aberto)
- [x] `git init` (feito por você em 25/out)
- [ ] Repositório `jotagm/client-go` no GitHub, se ainda não existir

---

## Fase 1: Entrada (`POST /notificacoes`)

**Meta:** receber um pedido, validar e gravar, respondendo `202` sem chamar nenhum provedor.

- [x] Adapter Postgres com `pgx/pgxpool` (mesma lib do sync-reader)
- [x] Seed do template `otp-login@v1`, `otp-verificar-email@v1` e `otp-redefinir-pin@v1`, copiando
      o texto do `SesEnviadorDeOtp.corpo()` (assunto sem o código, corpo sem link)
- [x] Handler `POST /notificacoes`
  - [x] Valida destinatário e template existente (o canal vem do template)
  - [x] Confere se todas as variáveis obrigatórias do template vieram, e recusa as que sobram
  - [x] Idempotência por `(origem, Idempotency-Key)`: a mesma chave devolve o mesmo `id`;
        mesma chave com pedido diferente dá `409`
  - [x] Checa `t_supressao` e grava como `suprimida` quando for o caso
  - [x] Grava em `t_notificacao` só os nomes das variáveis
- [x] Autenticação entre serviços: token estático por origem (`AVISO_TOKENS=booklit=...`)
- [x] `GET /notificacoes/{id}` devolvendo estado e motivo, só para a origem dona

**Pronto quando:** um `curl` cria uma notificação, repetir com a mesma chave não duplica, e nenhum
valor de variável aparece no banco nem no log. ✅ Conferido com testes e com o binário rodando.

**Decisões tomadas:**
- A impressão da idempotência usa a versão *pedida* do template e ignora os valores das variáveis
  (um hash do código de 6 dígitos seria quebrado na hora).
- O publicador ainda é o de memória: nada é entregue até a fase 2.

---

## Fase 2: Transporte (SNS + SQS)

**Meta:** o pedido aceito chega ao consumidor pela fila certa e é retentado com backoff.

- [x] Adapters de memória para `Publicador` e `Consumidor` (usados nos testes de todas as fases)
- [x] `aws-sdk-go-v2`: publicador SNS com atributos `canal`, `prioridade` e `origem`
- [x] Consumidor SQS com long polling (`WaitTimeSeconds=20`) e pool de workers com limite
- [x] Um consumidor por fila: `email-alta` e `email` (`AVISO_FILAS`)
- [x] Backoff exponencial com jitter via `Adiar` → `ChangeMessageVisibility`
- [x] Limite de tentativas pelo `ApproximateReceiveCount`, e DLQ pela redrive policy
- [x] Desligamento gracioso: para de receber e termina o que está em andamento
- [x] Terraform no Floci: tópico `aviso-notificacoes`, filas com filter policy e DLQs
      (módulo próprio `infra/modules/mensageria`, com filter policy e SSE)
- [x] Entrega simulada, para ver o fluxo inteiro antes do SES
- [x] **Validado no Floci** (25/out, lab Fedora): 47 testes, nenhum pulado, ponta a ponta
      e desligamento OK, código fora do log e do banco (`scripts/rodar-no-lab.sh`)

**Decisão tomada:** publicar no SNS direto, sem outbox (ADR 0002 aceita). Se a publicação falhar,
a API responde `503` e a notificação fica como `falhou/publicacao`.

**Pronto quando:** com o Floci no ar, um pedido `prioridade=alta` cai só na `email-alta`, e uma
mensagem que falha sempre termina na DLQ depois do limite. ✅ Conferido contra o moto: roteamento
pelas filter policies, `Adiar` escondendo a mensagem, mensagem veneno chegando na DLQ, e o binário
consumindo 4 pedidos de ponta a ponta.

**Decisões tomadas:**
- Envelope mínimo na fila: só `id` e variáveis. O resto vem do banco, que é a fonte da verdade.
- Mensagem repetida (o SQS entrega "pelo menos uma vez") é reconhecida pelo estado no banco e só
  apagada. Duas cópias processadas *ao mesmo tempo* ainda podem gerar dois e-mails; para OTP, isso
  é aceitável.
- Duas camadas de limite: o Client_GO desiste na 5ª tentativa de erro temporário (`falhou`); a redrive
  (10 recebimentos) pega o que o Client_GO nem consegue processar, como envelope ilegível ou banco fora.
- Backoff que passaria do `expira_em` marca `expirada` na hora, em vez de reenviar código vencido.
- Se gravar o estado falhar depois de entregar, a mensagem é apagada mesmo assim: tentar de novo
  mandaria o e-mail duas vezes. A reconciliação da fase 6 encontra o que ficou preso em `aceita`.

---

## Fase 3: Entrega (provedor SES) ✅

**Meta:** o consumidor renderiza e envia de fato, registrando cada tentativa.

**Achados do teste manual no lab (25/out), corrigidos:**
- [x] Toda mudança de estado gera uma linha de log `estado alterado` com `de`, `para` e `motivo`
      (aceitar, suprimir, falhar na publicação, enviar, expirar...). Antes, dois códigos vencidos
      foram descartados sem nenhuma linha. Teste: `TestProcessarLogaCadaMudancaDeEstado`.
- [x] Horários truncados para microssegundos antes de gravar (`agoraMicro`), inclusive o `expira_em`
      que o cliente manda. Antes, a primeira resposta do `POST` mostrava nanossegundos e a repetição,
      lida do Postgres, microssegundos. Teste: `TestRepeticaoTemOsMesmosHorarios`, com um repositório
      que arredonda como o Postgres (sem a correção, ele falha).

**Entrega:**
- [x] Renderização com `text/template` e `missingkey=error` (`notificacao.Template.Renderizar`)
  - [x] `minutos` calculado a partir de `expira_em`, arredondado para cima; reservado (não pode vir no pedido)
  - [x] Valor vazio é erro, não e-mail com o código em branco
  - [x] Assunto com quebra de linha é recusado (injeção de cabeçalho)
  - [x] O aceite ensaia a renderização: o que nunca viraria e-mail volta `422` na hora, em vez de
        falhar na fila depois do `202`. Template quebrado é erro nosso (`500`), não do chamador
- [x] Golden files em `internal/notificacao/testdata/`, lendo os textos de `db/002_templates_otp.sql`
      (se o SQL mudar, o golden acusa; `go test ./internal/notificacao -atualizar` reescreve)
- [x] Provedor SES v2 (`internal/adapter/ses`), com o retryer do SDK desligado: a retentativa é da
      fila, com backoff e limite próprios, e cada chamada ao SES vira uma linha em `t_tentativa_envio`
- [x] Classificação de erro (`ses.Classificar`): throttling (`429`, `ThrottlingException`, inclusive
      quando vem como `400`), `5xx`, timeout, rede e resposta ilegível são `ErrTemporario`;
      `MessageRejected`, `BadRequest`, domínio não verificado, conta suspensa e `4xx` sem tipo são
      `ErrPermanente`. O status HTTP vem antes do erro de desserialização: um `503` de balanceador com
      corpo HTML também falha ao ser lido como JSON, e o que importa é o `503`
- [x] Antes de enviar, `Expirou()` marca `expirada` (feito na fase 2, coberto de novo pelo pipeline)
- [x] Registro de cada tentativa em `t_tentativa_envio`: provedor, status HTTP, resultado, motivo,
      latência, id no provedor. Número atribuído pelo repositório; índice único
      (`db/003_tentativa_numero_unico.sql`) + nova tentativa se dois workers disputarem o mesmo número
- [x] Transição validada por `PodeIrPara` em toda atualização: no Processador **e** dentro dos dois
      repositórios (`MudarEstado` devolve `ErrTransicaoInvalida`)
- [x] Provedor falso com `httptest` (`internal/adapter/ses`): lento, fora do ar, resposta malformada,
      `200` sem `MessageId` e `200` sem entrega, mais cada erro tipado do SES
- [x] Pipeline completo sem AWS (Processador + EntregadorEmail + SES + servidor falso): o `200` sem
      entrega fica em `enviada`, nunca `entregue`; falhas temporárias voltam para a fila com a
      tentativa registrada; rejeição vira `falhou/erro_permanente`
- [x] Segredo: o código não vai para log, para `t_tentativa_envio` nem para o erro que sobe. Se o
      provedor repetir o corpo do e-mail na mensagem de erro, `ocultar()` troca o valor por `[omitido]`
- [x] `AVISO_PROVEDOR=simulado|ses`, `AVISO_SES_REMETENTE` (obrigatório com `ses`), `AVISO_SES_CONJUNTO`

**Validação:** `scripts/validar-floci.sh` ganhou a seção 7 (instância com `AVISO_PROVEDOR=ses`): três
pedidos chegam a `enviada`, `t_tentativa_envio` tem uma linha de sucesso por pedido e o e-mail aparece no
emulador com o código e a validade renderizados. Passou no moto (25/out); falta rodar no Floci do lab.

**Achados do teste de integração (moto, 25/out):**
- O SES recusa remetente não verificado com `MessageRejected` (permanente). Em produção isso vira
  tarefa da fase 5: identidade de domínio, DKIM e saída do sandbox.
- **Deploy e long polling:** ao desligar a instância, o `ReceiveMessage` em andamento é cancelado, mas o
  servidor pode já ter entregado uma mensagem àquela chamada. Ela fica invisível até o
  `visibility_timeout` (30 s) e só então volta. Medido: a mensagem esperou exatamente 30,0 s e foi
  entregue uma vez só. É o "pelo menos uma vez" funcionando, e para um código de 10 minutos 30 s de
  atraso no deploy é aceitável. Fica registrado para a fase 5 (deploy contínuo) e para a fase 7
  (alerta de idade da mensagem mais antiga).
- Floci: o endereço de leitura dos e-mails (`/_aws/ses`) ainda não foi confirmado. Se for outro, o
  script aceita `SES_INSPECAO_URL`.

**Pronto quando:** o e-mail aparece em `GET <floci>/_aws/ses`, e os testes com o provedor falso
cobrem os quatro cenários. (Os testes: feito. O Floci do lab: pendente.)

---

## Fase 4: Integração com o Booklit ✅

**Meta:** o Booklit envia pelo Client_GO, com volta para o SES direto sem deploy de código.

- [x] `ClientGoEnviadorDeOtp` implementando `EnviadorDeOtp` (o `auth-core` não muda)
  - [x] Envio no `afterCommit`, igual ao `SesEnviadorDeOtp` (helper `ExecutaAposCommit` compartilhado)
  - [x] `Idempotency-Key` = `desafioId`
  - [x] `expira_em` = `Mensagem.expiraEm()`
  - [x] Timeout curto (2 s, `BOOKLIT_CLIENT_GO_TIMEOUT`): falha é logada só com o `desafioId` e o status; nunca o código nem o corpo da resposta
  - [x] URL e token validados no construtor (a subida falha, não o primeiro login)
- [x] Propriedade `booklit.auth.email.provedor=ses|client-go` (padrão `ses`) escolhendo o bean em
      `AuthOtpConfig`; valor desconhecido derruba a subida
- [x] Testes: `ClientGoEnviadorDeOtpTest` (servidor HTTP do JDK: payload, cabeçalhos, 401/422/429/5xx,
      conexão recusada, servidor lento, commit/rollback, sem código nos logs) e
      `OtpClientGoIntegracaoTest` (Spring, Client_GO falso: emissão, verificação, login em duas etapas,
      Client_GO recusando não quebra a requisição). Os testes do modo `ses` seguem como estavam.
- [x] `mvnw test` no Booklit: 206 testes, 0 falhas (os 12 novos e os dos dois modos antigos); o `auth-core`
      precisa estar instalado antes (`mvnw -f auth-core install`)
- [x] Teste ponta a ponta local: Booklit (desktop) → túnel SSH → Client_GO + Postgres + Floci (lab).
      `POST /auth/otp/email` → 202; notificação `enviada`, tentativa `ses/sucesso` com o `desafioId` como chave;
      e-mail no Floci com o código; `POST /auth/otp/email/verificacao` → 200.
      Roteiro: `scripts/subir-no-lab.sh` (subir | emails | estado | logs | parar)
- [ ] (opcional) repetir com `booklit.auth.login.exigir-codigo=true` para exercitar o template `otp-login` pelo Client_GO

**Observação de projeto:** a chamada ao Client_GO é síncrona na thread da requisição, depois do commit,
com no máximo 2 s de espera. Com o Client_GO fora do ar o usuário recebe 202 do mesmo jeito e pede outro código.

**Pronto quando:** o login com código funciona localmente nos dois modos, trocando só a propriedade.

---

## Fase 5: Produção

**Meta:** o Client_GO no ar ao lado do Booklit, recebendo o tráfego de OTP.

**Estado:** código, imagem, pipeline e Terraform escritos e testados no que dá para testar sem a AWS. Nada foi
aplicado. O passo a passo, as correções ao plano original e o plano de volta estão em `docs/producao.md`.

### Correções ao plano original

- O papel IAM da EC2 é compartilhado pelos dois containers: não dá para isolar o `ses:SendEmail` do Booklit sem ECS/EKS.
- O Postgres é o **RDS** do Booklit (não existe Postgres na EC2): database e usuário `aviso` dentro dele.
- A fila `notifications` do Booklit **não** conflita com o Client_GO (que tem tópico e filas `aviso-prod-*`). Renomear
  virou limpeza opcional, com a ordem corrigida em `docs/producao.md`.

### No Client_GO

- [x] Migrações embutidas no binário (`client-go migrar`, tabela `t_migracao`, trava consultiva, um script por
      transação): 6 testes, incluindo instâncias em paralelo e script com erro
- [x] `client-go sonda` (healthcheck do container, que não tem shell nem curl)
- [x] `Dockerfile` (compila em amd64 e cruza para arm64, distroless não-root) e `.dockerignore`
- [x] `deploy/compose.yaml` (rede do Booklit, 64 MB, `GOMEMLIMIT`) e `deploy/deploy.sh` (login ECR, SSM → `app.env`,
      banco, migrações, healthcheck, volta para a imagem anterior se não ficar saudável)
- [x] `.github/workflows/deploy.yml`: `go test -race` com Postgres, imagem no ECR, deploy por SSM
- [x] `scripts/criar-banco-prod.sh` (idempotente; testado num Postgres local, incluindo as migrações com o usuário limitado)
- [x] Terraform `infra/envs/prod`: tópico, filas e DLQs (SSE-SQS, `email-alta` com retenção de 15 min), ECR, parâmetros do
      SSM, política `client-go-prod` no papel da EC2, papel OIDC do GitHub, chave da virada `BOOKLIT_EMAIL_PROVEDOR`
- [ ] `scripts/validar-terraform.sh` no lab (primeiro `validate` real do Terraform de produção)

### Na AWS (feito por você, na ordem de `docs/producao.md`)

- [ ] `terraform plan` lido (só criações + uma política no papel existente) e `apply`
- [ ] Variáveis do repositório no GitHub
- [ ] Primeiro deploy do Client_GO e teste de 202 + e-mail + `enviada` por um endereço do sandbox
- [ ] SES: domínio `Verified` e acesso de produção (sem isso só os endereços de `emails_teste_sandbox` recebem)
- [ ] `free -m` / `docker stats` depois do primeiro deploy (a t4g.micro divide 1 GB entre três serviços)

### Virada

- [ ] Booklit da Fase 4 em produção com `BOOKLIT_EMAIL_PROVEDOR=ses` (nada muda para o usuário)
- [ ] `aws ssm put-parameter ... --value client-go --overwrite` + deploy do Booklit; acompanhar os primeiros logins
- [ ] Plano de volta: o mesmo comando com `ses` e um deploy
- [ ] Depois de uma semana estável, remover o `SesEnviadorDeOtp` e o `SesV2Client` do Booklit

**Pronto quando:** os logins em produção passam pelo Client_GO, com histórico e retentativa, e o Booklit não tem mais código de
envio de e-mail. (A permissão `ses:SendEmail` continua no papel compartilhado.)

---

## Fase 6: Recibos e reconciliação

> **Limitação do Floci (25/out):** ele guarda `MessageRetentionPeriod=900` na `email-alta`, mas não
> apaga as mensagens vencidas (uma mensagem sobreviveu 7 horas). Na AWS, a mensagem some e a
> notificação fica presa em `aceita`. No lab, testar a reconciliação apagando a mensagem à mão
> (`sqs purge-queue` ou `delete-message`).

**Meta:** saber o que foi entregue, não só o que foi enviado.

- [ ] Configuration set no SES com destino de eventos (Delivery, Bounce, Complaint) para um
      tópico SNS → fila `aviso-recibos`. Com o SES, não precisa de webhook HTTP.
- [ ] Consumidor de recibos casando pelo `id_no_provedor` e aplicando a transição
- [ ] Bounce e reclamação entram em `t_supressao` automaticamente
- [ ] Job de reconciliação: `enviada` sem recibo depois do prazo vira `incerta`

---

## Fase 7: Painel e métricas

- [ ] `GET /painel?de=&ate=`: contagem por estado final e motivo, via SQL
- [ ] `/metrics` no Prometheus: histogramas de espera na fila, chamada ao provedor e entrega;
      taxa de erro por provedor; profundidade das filas
- [ ] Texto no site: por que o painel conta pelo SQL e não pelo Prometheus

---

## Fase 8: Evolução

- [ ] Resend como segundo provedor de e-mail, com failover quando o SES devolve erro temporário
- [ ] Webhook da Resend com verificação de assinatura
- [ ] Fila própria do Client_GO assinando `booklit-prod-events`: `votacao.encerrada` vira e-mail para
      os membros do clube
- [ ] Rate limit por destinatário e por provedor no Redis
- [ ] Monitor de uptime como segundo cliente
- [ ] Canal push (ntfy)

---

## Riscos

| Risco | Mitigação |
|---|---|
| Client_GO fora do ar trava o login | Timeout curto no Booklit e volta para `ses` pela propriedade |
| Código de OTP vaza em log ou banco | Variáveis nunca persistidas; `String()` sem valores; teste que confere |
| Fila `notifications` disputada por dois consumidores | Renomear antes de o Client_GO assinar o tópico (Fase 5) |
| Retentativa entrega código vencido | `expira_em` checado antes de cada envio |
| SES no sandbox | O acesso de produção já é pré-requisito do Booklit; manter os e-mails de teste |
| Memória da EC2 | Binário Go pequeno e `mem_limit`; medir antes da virada |
