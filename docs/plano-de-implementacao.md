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
- [ ] `git init` e repositório `jotagm/client-go` no GitHub

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

## Fase 3: Entrega (provedor SES)

**Meta:** o consumidor renderiza e envia de fato, registrando cada tentativa.

**Achados do teste manual no lab (25/out), corrigir aqui:**
- [ ] Toda mudança de estado gera uma linha de log com estado e motivo. Hoje o caminho de
      expiração não loga nada: dois códigos vencidos foram descartados sem nenhuma linha no log.
- [ ] Truncar os horários para microssegundos antes de gravar. A primeira resposta do `POST`
      mostra nanossegundos e a repetição (lida do Postgres) mostra microssegundos, para o mesmo campo.

- [ ] Renderização com `text/template` e `missingkey=error`
- [ ] Testes de renderização com golden files, incluindo o caso de campo vazio
- [ ] Provedor SES v2 (`aws-sdk-go-v2/service/sesv2`)
- [ ] Classificação de erro: throttling e 5xx como `ErrTemporario`; destinatário inválido e
      mensagem rejeitada como `ErrPermanente`
- [ ] Antes de enviar: checar `Expirou()` e marcar `expirada` em vez de mandar código vencido
- [ ] Registro de cada tentativa em `t_tentativa_envio`, com latência e motivo
- [ ] Transição de estado validada por `PodeIrPara` em toda atualização
- [ ] Provedor falso com `httptest` para os testes: lento, fora do ar, resposta malformada e
      200 sem entrega

**Pronto quando:** o e-mail aparece em `GET <floci>/_aws/ses`, e os testes com o provedor falso
cobrem os quatro cenários.

---

## Fase 4: Integração com o Booklit

**Meta:** o Booklit envia pelo Client_GO, com volta para o SES direto sem deploy de código.

- [ ] `ClientGoEnviadorDeOtp` implementando `EnviadorDeOtp` (o `auth-core` não muda)
  - [ ] Envio no `afterCommit`, igual ao `SesEnviadorDeOtp`
  - [ ] `Idempotency-Key` = `desafioId`
  - [ ] `expira_em` = `Mensagem.expiraEm()`
  - [ ] Timeout curto (2 s): falha é logada só com o `desafioId`
- [ ] Propriedade `booklit.email.provedor=ses|aviso` escolhendo o bean em `AuthOtpConfig`
- [ ] Ajustar `OtpEmailIntegracaoTest` e `LoginComCodigoIntegracaoTest` para os dois modos
      (no modo `aviso`, servidor HTTP falso)
- [ ] Teste ponta a ponta local: Booklit + Client_GO + Floci, login com código funcionando

**Pronto quando:** o login com código funciona localmente nos dois modos, trocando só a propriedade.

---

## Fase 5: Produção

**Meta:** o Client_GO no ar ao lado do Booklit, recebendo o tráfego de OTP.

### Renomear a fila `notifications` do Booklit

Ela é lida pelo `VotacaoEncerradaConsumidor` e não tem nada de notificação. Se o Client_GO assinar a
mesma fila, os dois disputam as mensagens e as votações encerradas se perdem. A ordem importa:

1. [ ] Criar a fila `leituras` (com DLQ) assinando o tópico `booklit-prod-events`, sem remover a antiga
2. [ ] Deploy do Booklit com `BOOKLIT_EVENTOS_FILA_URL` apontando para `leituras`
3. [ ] Esperar a fila `notifications` esvaziar (`ApproximateNumberOfMessages` = 0)
4. [ ] Remover a fila `notifications` e a DLQ dela

### Infra do Client_GO

- [ ] Terraform prod: tópico, filas, DLQs, filter policies e criptografia SSE-SQS
- [ ] Retenção curta na `email-alta` (por exemplo 15 minutos: OTP vale 10)
- [ ] Banco do Client_GO: database separado no mesmo Postgres da EC2, com usuário próprio
- [ ] Role do Client_GO com `ses:SendEmail`, `sns:Publish` no próprio tópico e permissões SQS nas
      próprias filas
- [ ] Segredos no SSM (`AVISO_TOKEN_BOOKLIT`, URL do banco)
- [ ] Imagem no ECR e terceiro serviço no `compose.yaml.tftpl`, com `mem_limit` de 64 MB
- [ ] Conferir se a instância aguenta os três serviços
- [ ] Pipeline no GitHub Actions: `go test`, build da imagem e deploy

### Virada

- [ ] Deploy com `booklit.email.provedor=ses` (nada muda para o usuário)
- [ ] Trocar para `aviso` e acompanhar os primeiros logins
- [ ] Plano de volta: trocar a propriedade para `ses`
- [ ] Depois de uma semana estável, remover a `ses:SendEmail` da role do Booklit e apagar o
      `SesEnviadorDeOtp`

**Pronto quando:** os logins em produção passam pelo Client_GO e a role do Booklit não envia mais e-mail.

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
