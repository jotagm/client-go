# ADR 0001: SNS + SQS como transporte, Go como orquestrador

**Status:** aceita · out/2026

## Contexto

O Client_GO recebe pedidos de aviso de outros sistemas (Booklit primeiro, monitor de uptime depois)
e entrega por e-mail, push ou SMS. A entrega depende de provedores externos que ficam lentos,
caem e devolvem erro temporário. Quem pede o aviso não pode ficar esperando o provedor.

## Decisão

- A API responde `202 Accepted` com o `id` da notificação assim que grava e publica.
- Publicação num tópico SNS com os atributos `canal` e `prioridade`. Filter policies distribuem
  para filas SQS separadas (`email-alta`, `email`, `push`), cada uma com sua DLQ.
- SNS e SQS são só transporte. Template, opt-out, classificação de erro, retentativa e histórico
  ficam no Go.
- O transporte fica atrás das portas `Publicador` e `Consumidor`, com uma implementação em memória
  para teste. Trocar por RabbitMQ não mexe no domínio.
- Retentativa por `ChangeMessageVisibility` com backoff exponencial e jitter; depois do limite,
  a mensagem vai para a DLQ.
- O e-mail nativo do SNS não é usado como canal: quem entrega é o provedor (Resend).

## Consequências

- O cadastro do Booklit não depende da disponibilidade da Resend.
- "Enviada" e "entregue" passam a ser estados diferentes; a entrega só é confirmada pelo webhook.
- Mais peças para operar (tópico, filas, DLQs), já descritas no módulo `messaging` do Terraform do
  Booklit, que pode ser reaproveitado.
