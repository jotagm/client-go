-- Template do lembrete de votação do Booklit: o líder do clube lembra, por e-mail, quem ainda
-- não votou no próximo livro. O Booklit limita a um lembrete a cada 24 horas por votação.
--
-- Assunto fixo (sem variável), pelo mesmo motivo do convite: o nome do clube é texto livre de
-- quem criou o clube. No corpo, em texto puro, ele é só texto.
--
-- O link leva direto à votação; quem não está logado passa pelo login e volta para lá.

INSERT INTO t_template (nome, versao, canal, assunto, corpo, variaveis) VALUES
('lembrete-votacao', 1, 'email', 'Falta o seu voto no próximo livro do clube',
$$Olá!

A votação do próximo livro do clube "{{.clube}}" no Booklit fecha {{.prazo}}, e o seu voto ainda não entrou.

Para ver as opções e votar, entre com este e-mail:
{{.link}}

Booklit
$$, ARRAY['clube', 'prazo', 'link'])
ON CONFLICT (nome, versao) DO NOTHING;
