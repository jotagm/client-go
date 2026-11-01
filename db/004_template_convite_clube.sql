-- Template do convite de clube do Booklit: avisa por e-mail quem foi convidado.
--
-- Assunto fixo (sem variável): o nome do clube é texto livre de quem criou o clube, e uma
-- quebra de linha nele abriria espaço para cabeçalho injetado. No corpo, em texto puro, ele
-- é só texto.
--
-- O link leva à lista de clubes do Booklit (que mostra os convites pendentes); quem não está
-- logado passa pelo login e volta para lá. Sem código e sem token no link: quem aceita é a
-- conta com este e-mail, logada.

INSERT INTO t_template (nome, versao, canal, assunto, corpo, variaveis) VALUES
('convite-clube', 1, 'email', 'Você recebeu um convite para um clube de leitura no Booklit',
$$Olá!

{{.convidado_por}} convidou você para o clube "{{.clube}}" no Booklit.

Para aceitar ou recusar, entre com este e-mail (se ainda não tem conta, crie uma com ele):
{{.link}}

O convite fica disponível por tempo limitado.
Se você não conhece quem convidou, ignore este e-mail: nada acontece sem o seu aceite.

Booklit
$$, ARRAY['convidado_por', 'clube', 'link'])
ON CONFLICT (nome, versao) DO NOTHING;
