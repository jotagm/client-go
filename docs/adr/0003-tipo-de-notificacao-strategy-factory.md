# ADR 0003: tipo de notificação por Factory + Strategy

**Status:** aceita · nov/2026

## Contexto

Cada template de e-mail tem regras próprias: o OTP exige expiração e vale poucos minutos, o convite não tem pressa. Com `if` por nome de template na camada de aplicação, cada template novo mexeria no fluxo de aceite.

## Decisão

`notificacao.TipoDe(nomeDoTemplate)` (factory) devolve um `Tipo` (strategy) com:
- `PrioridadePadrao()`: usada quando o pedido não informa prioridade;
- `ExigeExpiracao()`: se verdadeiro, o pedido sem `expira_em` é recusado;
- `Validar(...)`: regras extras do tipo.

Um template sem tipo registrado cai no tipo padrão. O fluxo de aceite (`aplicacao/aceitar.go`) só consulta o `Tipo`, sem conhecer nomes de template.

## Como adicionar um tipo

1. Migração `db/00N_template_<nome>.sql` com o template (aplicada no deploy).
2. Registrar o tipo em `internal/notificacao/tipo.go` com teste em `tipo_test.go`.
3. Se o tipo mudar as regras de aceite, cobrir em `aceitar_tipo_test.go`.

Exemplos: `db/004_template_convite_clube.sql` (template `convite-clube`) e `db/005_template_lembrete_votacao.sql` (template `lembrete-votacao`).
