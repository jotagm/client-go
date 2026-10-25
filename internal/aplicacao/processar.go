package aplicacao

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

// Decisao é o que fazer com a mensagem depois de processar.
type Decisao struct {
	Confirmar bool          // apagar da fila
	Espera    time.Duration // quando não confirma: devolver para a fila depois disso
}

func confirmar() Decisao                 { return Decisao{Confirmar: true} }
func adiar(espera time.Duration) Decisao { return Decisao{Espera: espera} }

// Processador é o caso de uso do consumidor: uma mensagem entra, uma decisão sai.
// Não sabe de SQS; quem aplica a decisão na fila é o Consumir.
type Processador struct {
	repo        porta.Repositorio
	entregador  porta.Entregador
	retentativa Retentativa
	agora       func() time.Time
	log         *slog.Logger
}

func NovoProcessador(repo porta.Repositorio, entregador porta.Entregador, r Retentativa, log *slog.Logger) *Processador {
	return &Processador{repo: repo, entregador: entregador, retentativa: r, agora: time.Now, log: log}
}

// Processar decide o destino de uma mensagem.
//
// A fila entrega "pelo menos uma vez": a mesma mensagem pode chegar duas vezes. Por isso
// o estado no banco manda: só uma notificação em "aceita" é entregue; qualquer outro
// estado quer dizer que alguém já cuidou dela, e a mensagem é só apagada.
func (p *Processador) Processar(ctx context.Context, m porta.Mensagem) Decisao {
	log := p.log.With("mensagem", m.ID, "tentativa", m.Recebimento)

	env, err := notificacao.DecodificarEnvelope(m.Corpo)
	if err != nil {
		// Nunca vai dar certo, mas não apaga: deixa a redrive policy levar para a DLQ,
		// onde dá para inspecionar.
		log.Error("envelope inválido", "erro", err)
		return adiar(p.retentativa.Espera(m.Recebimento))
	}
	log = log.With("id", env.ID)

	n, err := p.repo.Buscar(ctx, env.ID)
	if err != nil {
		// Não encontrado também cai aqui: a publicação só acontece depois do commit, então
		// é mensagem estranha. Banco fora do ar é temporário. Nos dois casos, a DLQ decide.
		log.Error("não foi possível carregar a notificação", "erro", err)
		return adiar(p.retentativa.Espera(m.Recebimento))
	}

	if n.Estado != notificacao.EstadoAceita {
		log.Info("mensagem repetida: notificação já processada", "estado", n.Estado)
		return confirmar()
	}

	agora := p.agora()
	if n.Expirou(agora) {
		return p.encerrar(ctx, log, n, notificacao.EstadoExpirada, "expirou_antes_do_envio")
	}

	inicio := time.Now()
	err = p.entregador.Entregar(ctx, n, env.Variaveis)
	duracao := time.Since(inicio)

	switch {
	case err == nil:
		log.Info("entregue ao provedor", "duracao_ms", duracao.Milliseconds())
		return p.encerrar(ctx, log, n, notificacao.EstadoEnviada, "")

	case errors.Is(err, porta.ErrPermanente):
		log.Warn("erro permanente na entrega", "erro", err)
		return p.encerrar(ctx, log, n, notificacao.EstadoFalhou, "erro_permanente")
	}

	// Temporário (ou erro desconhecido, tratado como temporário).
	if p.retentativa.Esgotou(m.Recebimento) {
		log.Warn("tentativas esgotadas", "erro", err)
		return p.encerrar(ctx, log, n, notificacao.EstadoFalhou, "tentativas_esgotadas")
	}
	espera := p.retentativa.Espera(m.Recebimento)
	if n.ExpiraEm != nil && !agora.Add(espera).Before(*n.ExpiraEm) {
		// Não adianta tentar de novo um código que vai estar vencido quando a mensagem voltar.
		log.Warn("expiraria antes da próxima tentativa", "erro", err, "espera", espera)
		return p.encerrar(ctx, log, n, notificacao.EstadoExpirada, "expiraria_antes_da_retentativa")
	}
	log.Warn("erro temporário, tentando de novo", "erro", err, "espera", espera)
	return adiar(espera)
}

// encerrar grava o estado final da tentativa e confirma a mensagem.
//
// Se a gravação falhar DEPOIS de entregar, a mensagem é confirmada mesmo assim: tentar de
// novo mandaria o e-mail duas vezes. Fica um aviso no log e a notificação presa em
// "aceita", que a reconciliação da fase 6 vai encontrar.
func (p *Processador) encerrar(ctx context.Context, log *slog.Logger, n notificacao.Notificacao,
	para notificacao.Estado, motivo string) Decisao {
	if err := n.Estado.PodeIrPara(para); err != nil {
		log.Error("transição inválida", "erro", err)
		return confirmar()
	}
	err := p.repo.MudarEstado(ctx, n.ID, n.Estado, para, motivo, p.agora().UTC())
	switch {
	case err == nil:
	case errors.Is(err, porta.ErrConcorrencia):
		log.Warn("outro processo mudou o estado antes", "para", para)
	default:
		log.Error("não foi possível gravar o estado", "para", para, "erro", err)
	}
	return confirmar()
}
