package aplicacao

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

const (
	// Valores menores que isso não são escondidos do texto de erro: "1" ou "ab" apareceriam
	// em qualquer lugar e estragariam a mensagem. Um código de OTP tem 6 dígitos.
	tamanhoMinimoParaOcultar = 4
	tamanhoMaxResposta       = 500
)

// EntregadorEmail é a entrega de verdade: busca o template, renderiza, chama o provedor e
// registra a tentativa. Implementa porta.Entregador.
//
// Devolve nil, um erro que satisfaz porta.ErrPermanente, ou um que satisfaz
// porta.ErrTemporario. Quem decide o que fazer com cada um é o Processador.
type EntregadorEmail struct {
	repo       porta.Repositorio
	tentativas porta.RegistroTentativas
	provedor   porta.Provedor
	agora      func() time.Time
	log        *slog.Logger
}

var _ porta.Entregador = (*EntregadorEmail)(nil)

func NovoEntregadorEmail(repo porta.Repositorio, tentativas porta.RegistroTentativas,
	provedor porta.Provedor, log *slog.Logger) *EntregadorEmail {
	return &EntregadorEmail{repo: repo, tentativas: tentativas, provedor: provedor, agora: time.Now, log: log}
}

func (e *EntregadorEmail) Entregar(ctx context.Context, n notificacao.Notificacao, variaveis map[string]string) error {
	log := e.log.With("id", n.ID)
	inicio := e.agora()

	if n.Canal != notificacao.CanalEmail {
		// Chegou aqui por engano (fila errada ou canal novo sem entregador). Não adianta repetir.
		return e.recusar(ctx, log, n, inicio, "canal_nao_suportado", fmt.Sprintf("canal %q", n.Canal))
	}

	tpl, err := e.repo.BuscarTemplate(ctx, n.Template, n.VersaoTemplate)
	if errors.Is(err, porta.ErrNaoEncontrado) {
		return e.recusar(ctx, log, n, inicio, "template_inexistente", n.Template)
	}
	if err != nil {
		// Banco fora do ar: nada foi tentado, então nada é registrado. O Processador trata
		// erro desconhecido como temporário.
		return fmt.Errorf("buscar template: %w", err)
	}

	texto, err := tpl.Renderizar(variaveis, n.ExpiraEm, inicio)
	if err != nil {
		// Não adianta repetir: o texto sai igual. As mensagens de erro só citam nomes de
		// variáveis, mas a limpeza abaixo vale mesmo assim.
		motivo := "dados_invalidos"
		if errors.Is(err, notificacao.ErrTemplateInvalido) {
			motivo = "template_invalido"
		}
		return e.recusar(ctx, log, n, inicio, motivo, ocultar(err.Error(), variaveis))
	}

	resultado, err := e.provedor.Enviar(ctx, porta.Envio{
		NotificacaoID: n.ID,
		Destinatario:  n.Destinatario,
		Assunto:       texto.Assunto,
		Corpo:         texto.Corpo,
	})
	latencia := e.agora().Sub(inicio)
	if resultado.Latencia > 0 {
		latencia = resultado.Latencia
	}

	if err == nil {
		e.registrar(ctx, log, porta.Tentativa{
			NotificacaoID: n.ID, Provedor: e.provedor.Nome(), IDNoProvedor: resultado.IDNoProvedor,
			Resultado: porta.ResultadoSucesso, Latencia: latencia, TentadoEm: inicio,
		})
		return nil
	}

	// O erro do provedor vira um ErroProvedor mesmo quando o adapter devolveu um erro cru.
	// Classe desconhecida conta como temporária: na dúvida, tenta de novo.
	var recebido *porta.ErroProvedor
	ep := porta.ErroProvedor{Classe: porta.ErrTemporario, Motivo: "desconhecido", Detalhe: err.Error()}
	if errors.As(err, &recebido) {
		ep = *recebido
	}
	ep.Detalhe = ocultar(ep.Detalhe, variaveis)
	resultadoTexto := porta.ResultadoTemporario
	if errors.Is(ep.Classe, porta.ErrPermanente) {
		resultadoTexto = porta.ResultadoPermanente
	} else {
		ep.Classe = porta.ErrTemporario
	}
	e.registrar(ctx, log, porta.Tentativa{
		NotificacaoID: n.ID, Provedor: e.provedor.Nome(), StatusHTTP: ep.StatusHTTP,
		Resultado: resultadoTexto, Motivo: ep.Motivo, Resposta: ep.Detalhe,
		Latencia: latencia, TentadoEm: inicio,
	})
	return &ep
}

// recusar registra uma tentativa que nem chegou ao provedor e devolve erro permanente.
func (e *EntregadorEmail) recusar(ctx context.Context, log *slog.Logger, n notificacao.Notificacao,
	inicio time.Time, motivo, detalhe string) error {
	e.registrar(ctx, log, porta.Tentativa{
		NotificacaoID: n.ID, Provedor: e.provedor.Nome(), Resultado: porta.ResultadoPermanente,
		Motivo: motivo, Resposta: detalhe, TentadoEm: inicio,
	})
	return &porta.ErroProvedor{Classe: porta.ErrPermanente, Motivo: motivo, Detalhe: detalhe}
}

// registrar grava a tentativa. Se não conseguir, só loga: o e-mail já saiu (ou já falhou),
// e perder uma linha de histórico não pode virar um segundo envio.
func (e *EntregadorEmail) registrar(ctx context.Context, log *slog.Logger, t porta.Tentativa) {
	t.TentadoEm = t.TentadoEm.UTC().Truncate(time.Microsecond)
	t.Resposta = truncar(t.Resposta, tamanhoMaxResposta)
	// O contexto da mensagem pode já ter sido cancelado (desligamento): o registro vale mais.
	if err := e.tentativas.RegistrarTentativa(context.WithoutCancel(ctx), t); err != nil {
		log.Error("não foi possível registrar a tentativa", "erro", err,
			"resultado", t.Resultado, "motivo", t.Motivo)
	}
}

// ocultar troca por "[omitido]" qualquer valor de variável que apareça no texto. É a
// segunda barreira: o provedor não deveria repetir o corpo do e-mail num erro, mas se
// repetir, o código do OTP não vai parar na tabela nem no log.
func ocultar(texto string, variaveis map[string]string) string {
	for _, valor := range variaveis {
		if len(valor) >= tamanhoMinimoParaOcultar {
			texto = strings.ReplaceAll(texto, valor, "[omitido]")
		}
	}
	return texto
}

func truncar(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// não corta no meio de um caractere UTF-8
	corte := max
	for corte > 0 && s[corte]&0xC0 == 0x80 {
		corte--
	}
	return s[:corte] + "…"
}
