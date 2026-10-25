// Package aplicacao junta domínio e portas nos casos de uso do Client_GO.
package aplicacao

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

const (
	tamanhoMaxChave        = 200
	tamanhoMaxValor        = 2000
	maxVariaveis           = 30
	tamanhoMaxDestinatario = 320
)

// Pedido é o que outro sistema manda para pedir um aviso.
type Pedido struct {
	Origem         string // vem da autenticação, nunca do corpo da requisição
	ChaveIdempot   string
	Template       string
	VersaoTemplate int // 0 = mais recente
	Destinatario   string
	Prioridade     notificacao.Prioridade
	Variaveis      map[string]string
	ExpiraEm       *time.Time
}

// ErrValidacao lista o que está errado no pedido, campo por campo.
type ErrValidacao struct {
	Campos map[string]string
}

func (e *ErrValidacao) Error() string {
	nomes := make([]string, 0, len(e.Campos))
	for campo := range e.Campos {
		nomes = append(nomes, campo)
	}
	sort.Strings(nomes)
	return "pedido inválido: " + strings.Join(nomes, ", ")
}

var (
	// ErrConflitoIdempotencia: mesma chave, pedido diferente. Quase sempre é bug de quem
	// chamou (reaproveitou a chave); aceitar em silêncio esconderia o bug.
	ErrConflitoIdempotencia = errors.New("chave de idempotência já usada com outro pedido")
	// ErrPublicacao: gravou, mas não conseguiu colocar no transporte. A notificação fica
	// como falhou/publicacao e quem pediu decide se pede de novo (com outra chave).
	ErrPublicacao = errors.New("não foi possível publicar a notificação")
)

type Servico struct {
	repo       porta.Repositorio
	publicador porta.Publicador
	agora      func() time.Time
	log        *slog.Logger
}

func NovoServico(repo porta.Repositorio, publicador porta.Publicador, log *slog.Logger) *Servico {
	return &Servico{repo: repo, publicador: publicador, agora: time.Now, log: log}
}

// Aceitar valida, grava e publica. Devolve a notificação e se ela é nova (false quando
// é a repetição de um pedido com a mesma chave).
//
// A publicação acontece aqui, com o pedido ainda em memória, e não a partir do banco:
// o banco não tem os valores das variáveis (o código do OTP), então um outbox não teria
// o que publicar. Ver docs/adr/0002.
func (s *Servico) Aceitar(ctx context.Context, p Pedido) (notificacao.Notificacao, bool, error) {
	agora := s.agora().UTC()
	if err := validar(p, agora); err != nil {
		return notificacao.Notificacao{}, false, err
	}

	tpl, err := s.repo.BuscarTemplate(ctx, p.Template, p.VersaoTemplate)
	if errors.Is(err, porta.ErrNaoEncontrado) {
		return notificacao.Notificacao{}, false, &ErrValidacao{Campos: map[string]string{
			"template": "template não encontrado: " + nomeComVersao(p.Template, p.VersaoTemplate)}}
	}
	if err != nil {
		return notificacao.Notificacao{}, false, fmt.Errorf("buscar template: %w", err)
	}

	if err := validarParaTemplate(p, tpl); err != nil {
		return notificacao.Notificacao{}, false, err
	}

	prioridade := p.Prioridade
	if prioridade == "" {
		prioridade = notificacao.PrioridadeNormal
	}

	n := notificacao.Notificacao{
		ID:             notificacao.NovoID(),
		Origem:         p.Origem,
		ChaveIdempot:   p.ChaveIdempot,
		Canal:          tpl.Canal,
		Prioridade:     prioridade,
		Destinatario:   p.Destinatario,
		Template:       tpl.Nome,
		VersaoTemplate: tpl.Versao,
		Variaveis:      p.Variaveis,
		NomesVariaveis: notificacao.Nomes(p.Variaveis),
		Estado:         notificacao.EstadoAceita,
		ExpiraEm:       utc(p.ExpiraEm),
		CriadoEm:       agora,
		AtualizadoEm:   agora,
	}
	n.Impressao = impressao(p, prioridade)

	motivo, err := s.repo.MotivoSupressao(ctx, n.Destinatario, n.Canal)
	if err != nil {
		return notificacao.Notificacao{}, false, fmt.Errorf("consultar supressão: %w", err)
	}
	if motivo != "" {
		n.Estado = notificacao.EstadoSuprimida
		n.Motivo = motivo
	}

	salva, criada, err := s.repo.Criar(ctx, n)
	if err != nil {
		return notificacao.Notificacao{}, false, fmt.Errorf("gravar notificação: %w", err)
	}
	if !criada {
		if salva.Impressao != n.Impressao {
			return notificacao.Notificacao{}, false, ErrConflitoIdempotencia
		}
		s.log.Info("pedido repetido", "id", salva.ID, "origem", salva.Origem, "estado", salva.Estado)
		return salva, false, nil
	}

	if n.Estado != notificacao.EstadoAceita {
		s.log.Info("notificação suprimida", "id", n.ID, "origem", n.Origem, "motivo", n.Motivo)
		return n, true, nil
	}

	if err := s.publicador.Publicar(ctx, n); err != nil {
		s.log.Error("falha ao publicar", "id", n.ID, "erro", err)
		if errMudar := s.repo.MudarEstado(ctx, n.ID, notificacao.EstadoAceita,
			notificacao.EstadoFalhou, "publicacao", s.agora().UTC()); errMudar != nil {
			s.log.Error("falha ao marcar como falhou", "id", n.ID, "erro", errMudar)
		}
		return notificacao.Notificacao{}, false, ErrPublicacao
	}

	s.log.Info("notificação aceita", "id", n.ID, "origem", n.Origem, "template",
		nomeComVersao(n.Template, n.VersaoTemplate), "prioridade", n.Prioridade)
	n.Variaveis = nil // daqui para fora ninguém precisa dos valores
	return n, true, nil
}

// Buscar devolve a notificação só para a origem que pediu.
func (s *Servico) Buscar(ctx context.Context, origem, id string) (notificacao.Notificacao, error) {
	n, err := s.repo.Buscar(ctx, id)
	if err != nil {
		return notificacao.Notificacao{}, err
	}
	if n.Origem != origem {
		// mesmo erro de "não existe": não confirma para uma origem que o id é de outra
		return notificacao.Notificacao{}, porta.ErrNaoEncontrado
	}
	return n, nil
}

func validar(p Pedido, agora time.Time) error {
	campos := map[string]string{}
	if p.Origem == "" {
		campos["origem"] = "obrigatória"
	}
	switch {
	case p.ChaveIdempot == "":
		campos["chave_idempotencia"] = "obrigatória (cabeçalho Idempotency-Key)"
	case len(p.ChaveIdempot) > tamanhoMaxChave:
		campos["chave_idempotencia"] = fmt.Sprintf("no máximo %d caracteres", tamanhoMaxChave)
	}
	if p.Template == "" {
		campos["template"] = "obrigatório"
	}
	if p.VersaoTemplate < 0 {
		campos["versao"] = "não pode ser negativa"
	}
	switch {
	case p.Destinatario == "":
		campos["destinatario"] = "obrigatório"
	case len(p.Destinatario) > tamanhoMaxDestinatario:
		campos["destinatario"] = fmt.Sprintf("no máximo %d caracteres", tamanhoMaxDestinatario)
	}
	switch p.Prioridade {
	case "", notificacao.PrioridadeAlta, notificacao.PrioridadeNormal:
	default:
		campos["prioridade"] = "use alta ou normal"
	}
	if len(p.Variaveis) > maxVariaveis {
		campos["variaveis"] = fmt.Sprintf("no máximo %d", maxVariaveis)
	}
	for nome, valor := range p.Variaveis {
		if len(valor) > tamanhoMaxValor {
			campos["variaveis."+nome] = fmt.Sprintf("no máximo %d caracteres", tamanhoMaxValor)
		}
	}
	if p.ExpiraEm != nil && !p.ExpiraEm.After(agora) {
		campos["expira_em"] = "precisa estar no futuro"
	}
	if len(campos) > 0 {
		return &ErrValidacao{Campos: campos}
	}
	return nil
}

func validarParaTemplate(p Pedido, tpl notificacao.Template) error {
	campos := map[string]string{}
	faltando, sobrando := tpl.ConferirVariaveis(p.Variaveis)
	if len(faltando) > 0 {
		campos["variaveis"] = "faltando: " + strings.Join(faltando, ", ")
	}
	if len(sobrando) > 0 {
		campos["variaveis_extras"] = "não esperadas pelo template: " + strings.Join(sobrando, ", ")
	}
	if tpl.Canal == notificacao.CanalEmail && !emailValido(p.Destinatario) {
		campos["destinatario"] = "e-mail inválido"
	}
	if len(campos) > 0 {
		return &ErrValidacao{Campos: campos}
	}
	return nil
}

// emailValido aceita só o endereço puro: "Jota <a@b.com>" é recusado, porque o nome
// de exibição viraria conteúdo controlado por quem chama dentro do cabeçalho do e-mail.
func emailValido(s string) bool {
	endereco, err := mail.ParseAddress(s)
	return err == nil && endereco.Name == "" && endereco.Address == s
}

// impressao resume o pedido para comparar duas chamadas com a mesma chave.
//
// Usa a versão PEDIDA (0 = mais recente), não a resolvida: se uma versão nova do template
// for publicada entre a primeira chamada e a repetição, a repetição continua legítima.
//
// Os VALORES das variáveis ficam de fora de propósito: o código do OTP tem só 10^6
// possibilidades, e um hash dele no banco seria quebrado em milissegundos.
func impressao(p Pedido, prioridade notificacao.Prioridade) string {
	expira := ""
	if p.ExpiraEm != nil {
		expira = p.ExpiraEm.UTC().Format(time.RFC3339Nano)
	}
	partes := []string{
		p.Origem, p.Template, strconv.Itoa(p.VersaoTemplate), p.Destinatario,
		string(prioridade), strings.Join(notificacao.Nomes(p.Variaveis), ","), expira,
	}
	soma := sha256.Sum256([]byte(strings.Join(partes, "\x00")))
	return hex.EncodeToString(soma[:])
}

func nomeComVersao(nome string, versao int) string {
	if versao == 0 {
		return nome
	}
	return nome + "@v" + strconv.Itoa(versao)
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
