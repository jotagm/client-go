# ADR 0002: código de OTP passando pela fila

**Status:** aceita (opção 1) · out/2026 · validada no Floci em 25/out

## Contexto

No Booklit, o `SesEnviadorDeOtp` envia o código direto pelo SES depois do commit e não passa pelo
outbox de propósito: o outbox gravaria o código em claro no banco, e a garantia de entrega importa
pouco para algo que vale 10 minutos.

Mandar o código para o Client_GO pela fila reabre essa questão: o código passa a existir em claro na
mensagem SNS/SQS e, se mal cuidado, no banco e nos logs do Client_GO.

## Opções

1. **Booklit chama a API do Client_GO depois do commit** (sem outbox). O código só existe em memória,
   na requisição HTTPS e na mensagem da fila.
2. **Outbox do Booklit com o código cifrado** (envelope com chave KMS que só o Client_GO decifra).
3. **OTP continua direto no SES**; o Client_GO só cuida dos avisos sem segredo.

## Decisão

Opção 1. Como foi implementado:

- **Sem outbox no Client_GO.** A API grava a notificação (sem os valores) e publica no SNS na mesma
  requisição, com o pedido ainda em memória. Um outbox não teria o que publicar, porque o banco não
  guarda os valores. Se a publicação falhar, a API responde `503` e a notificação fica como
  `falhou/publicacao`; o Booklit trata como falha de envio e a pessoa pede outro código.
- **Envelope mínimo:** a mensagem leva só `id` e as variáveis. Destinatário, template e expiração
  o consumidor lê do banco.
- **Filas** com SSE-SQS e retenção de 15 minutos na `email-alta` (o código vale 10).
- **`expira_em`** checado antes de cada tentativa e antes de agendar a próxima: código que venceria
  durante o backoff vira `expirada` na hora.
- **Valores** fora do banco, do log, das respostas da API e das mensagens de erro, com teste para
  cada um desses lugares.

## Consequências

- Entre gravar e publicar existe uma janela: se o processo morrer ali, a notificação fica em
  `aceita` sem mensagem na fila. A reconciliação da fase 6 encontra esses casos; para OTP, a pessoa
  já terá pedido outro código.
- A mensagem na fila é o único lugar persistente com o código, por no máximo 15 minutos e
  criptografada. Quem tem permissão de ler a fila consegue ver o código: a política IAM da fila
  precisa ficar restrita à role do Client_GO.
