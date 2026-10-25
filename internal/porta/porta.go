// Package porta define as portas do Client_GO. Os adapters (SNS, SQS, Resend, Postgres e
// as versões em memória para teste) implementam estas interfaces; o domínio só fala com elas.
package porta

import (
	"context"
	"errors"
	"time"

	"github.com/jotagm/client-go/internal/notificacao"
)

var (
	ErrNaoEncontrado = errors.New("não encontrado")
	// ErrConcorrencia: o estado mudou entre a leitura e a escrita (outro worker ou o
	// webhook chegou antes). Quem chamou relê e decide.
	ErrConcorrencia = errors.New("estado alterado por outro processo")
)

// Repositorio guarda notificações, templates e a lista de supressão.
type Repositorio interface {
	// BuscarTemplate com versao 0 devolve a versão mais recente.
	BuscarTemplate(ctx context.Context, nome string, versao int) (notificacao.Template, error)
	// MotivoSupressao devolve "" quando o destinatário pode receber naquele canal.
	MotivoSupressao(ctx context.Context, destinatario string, canal notificacao.Canal) (string, error)
	// Criar grava a notificação. Se já existir uma com a mesma (Origem, ChaveIdempot),
	// não grava e devolve a existente com criada=false.
	Criar(ctx context.Context, n notificacao.Notificacao) (salva notificacao.Notificacao, criada bool, err error)
	Buscar(ctx context.Context, id string) (notificacao.Notificacao, error)
	// MudarEstado só altera se o estado atual ainda for "de" (trava otimista);
	// senão devolve ErrConcorrencia.
	MudarEstado(ctx context.Context, id string, de, para notificacao.Estado, motivo string, em time.Time) error
}

// Publicador coloca o pedido no transporte. Implementação real: SNS com atributos
// canal/prioridade para a filter policy escolher a fila.
type Publicador interface {
	Publicar(ctx context.Context, n notificacao.Notificacao) error
}

// Mensagem é o que o consumidor recebe da fila.
type Mensagem struct {
	ID          string // id da mensagem no transporte
	Recibo      string // receipt handle do SQS: é ele que apaga ou adia a mensagem
	Corpo       []byte
	Recebimento int // ApproximateReceiveCount: base do backoff e do limite de tentativas
}

// Consumidor lê de uma fila. Implementação real: SQS com long polling.
type Consumidor interface {
	Receber(ctx context.Context) ([]Mensagem, error)
	// Confirmar apaga a mensagem: só depois de registrar o resultado.
	Confirmar(ctx context.Context, m Mensagem) error
	// Adiar devolve a mensagem para a fila depois de "espera". No SQS é
	// ChangeMessageVisibility, sem sleep dentro do worker.
	Adiar(ctx context.Context, m Mensagem, espera time.Duration) error
}

// Entregador é o passo de entrega visto pelo consumidor: recebe a notificação (do banco)
// e as variáveis (do envelope) e devolve nil, ErrTemporario ou ErrPermanente.
// A implementação real (aplicacao.EntregadorEmail) renderiza o template, chama o Provedor
// e registra a tentativa; a simulada só loga.
type Entregador interface {
	Entregar(ctx context.Context, n notificacao.Notificacao, variaveis map[string]string) error
}

// Provedor entrega de fato (Resend, ntfy, um SMS falso).
type Provedor interface {
	Nome() string
	Enviar(ctx context.Context, envio Envio) (Resultado, error)
}

type Envio struct {
	NotificacaoID string
	Destinatario  string
	Assunto       string
	Corpo         string
}

type Resultado struct {
	IDNoProvedor string // usado para casar o webhook de recibo
	Latencia     time.Duration
}

// ErroProvedor é o erro que um Provedor devolve: diz se vale tentar de novo (Classe) e
// carrega o que a tabela de tentativas guarda. O texto de Detalhe nunca pode conter o
// corpo do e-mail nem valores de variáveis; quem registra ainda faz uma limpeza extra.
type ErroProvedor struct {
	Classe     error  // ErrTemporario ou ErrPermanente
	Motivo     string // curto e estável, vai para o painel: "throttling", "mensagem_rejeitada"...
	StatusHTTP int    // 0 quando não houve resposta HTTP (timeout, conexão recusada)
	Detalhe    string
}

func (e *ErroProvedor) Error() string {
	if e.Detalhe == "" {
		return e.Classe.Error() + ": " + e.Motivo
	}
	return e.Classe.Error() + ": " + e.Motivo + ": " + e.Detalhe
}

func (e *ErroProvedor) Unwrap() error { return e.Classe }

// Resultado de uma tentativa, como fica em t_tentativa_envio.
const (
	ResultadoSucesso    = "sucesso"
	ResultadoTemporario = "temporario"
	ResultadoPermanente = "permanente"
)

// Tentativa é uma linha de t_tentativa_envio: só inserção, uma por chamada ao provedor.
// É daqui que sai a latência por provedor. O número da tentativa é atribuído pelo
// repositório (a próxima da notificação), não por quem registra.
type Tentativa struct {
	NotificacaoID string
	Numero        int // preenchido na leitura
	Provedor      string
	IDNoProvedor  string
	StatusHTTP    int
	Resultado     string
	Motivo        string
	Resposta      string // texto curto do erro, sem valores de variáveis
	Latencia      time.Duration
	TentadoEm     time.Time
}

// RegistroTentativas guarda o histórico de envios de cada notificação.
type RegistroTentativas interface {
	RegistrarTentativa(ctx context.Context, t Tentativa) error
	// Tentativas devolve em ordem de número.
	Tentativas(ctx context.Context, notificacaoID string) ([]Tentativa, error)
}

// Classificação do erro do provedor. Decide se tenta de novo.
var (
	// ErrTemporario: timeout, 5xx, 429. Volta para a fila com backoff.
	ErrTemporario = errors.New("erro temporário do provedor")
	// ErrPermanente: 4xx de payload, destinatário inválido. Não adianta tentar de novo.
	ErrPermanente = errors.New("erro permanente do provedor")
)
