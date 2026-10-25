-- Templates do código por e-mail do Booklit, com o mesmo texto do SesEnviadorDeOtp:
-- assunto sem o código (apareceria na notificação da tela bloqueada) e corpo em texto puro,
-- sem link (e-mail de código com link é o formato que o phishing imita).
--
-- "codigo" vem de quem pede. "minutos" é calculado pelo Client_GO na hora de renderizar, a
-- partir de expira_em, por isso não está na lista de variáveis obrigatórias.

INSERT INTO t_template (nome, versao, canal, assunto, corpo, variaveis) VALUES
('otp-login', 1, 'email', 'Seu código de acesso ao Booklit',
$$Seu código é {{.codigo}}

Ele vale por {{.minutos}} minutos e só pode ser usado uma vez.
Se não foi você que pediu, ignore este e-mail: sem o código ninguém entra na sua conta.

Booklit
$$, ARRAY['codigo']),

('otp-verificar-email', 1, 'email', 'Confirme seu e-mail no Booklit',
$$Seu código é {{.codigo}}

Ele vale por {{.minutos}} minutos e só pode ser usado uma vez.
Se não foi você que pediu, ignore este e-mail: sem o código ninguém entra na sua conta.

Booklit
$$, ARRAY['codigo']),

('otp-redefinir-pin', 1, 'email', 'Código para redefinir seu PIN no Booklit',
$$Seu código é {{.codigo}}

Ele vale por {{.minutos}} minutos e só pode ser usado uma vez.
Se não foi você que pediu, ignore este e-mail: sem o código ninguém entra na sua conta.

Booklit
$$, ARRAY['codigo'])
ON CONFLICT (nome, versao) DO NOTHING;
