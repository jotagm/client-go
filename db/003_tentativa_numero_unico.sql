-- O número da tentativa é "a próxima da notificação" (MAX + 1). Se dois workers registrarem
-- ao mesmo tempo, os dois calculariam o mesmo número; o índice único faz um deles falhar e o
-- repositório tentar de novo, em vez de gravar duas tentativas com o mesmo número.
CREATE UNIQUE INDEX IF NOT EXISTS ux_tentativa_numero ON t_tentativa_envio (notificacao_id, numero);
