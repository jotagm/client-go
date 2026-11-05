-- Template do "esqueci minha senha" do Booklit, com o mesmo formato dos outros códigos
-- (002_templates_otp.sql): assunto sem o código, que apareceria na notificação da tela
-- bloqueada, e corpo em texto puro, sem link, que é o formato que o phishing imita.
--
-- "codigo" vem de quem pede. "minutos" é calculado pelo Client_GO a partir de expira_em.

INSERT INTO t_template (nome, versao, canal, assunto, corpo, variaveis) VALUES
('otp-redefinir-senha', 1, 'email', 'Código para redefinir sua senha no Booklit',
$$Seu código para redefinir a senha é {{.codigo}}

Ele vale por {{.minutos}} minutos e só pode ser usado uma vez.
Se não foi você que pediu, ignore este e-mail: sem o código ninguém troca a sua senha.

Booklit
$$, ARRAY['codigo'])
ON CONFLICT (nome, versao) DO NOTHING;
