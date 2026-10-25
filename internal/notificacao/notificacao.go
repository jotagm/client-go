// Package notificacao é o domínio do Client_GO: o que é um pedido de aviso e por quais
// estados ele passa. Não conhece SNS, SQS, Resend nem Postgres.
package notificacao

import (
	"errors"
	"fmt"
	"time"
)

type Canal string

const (
	CanalEmail Canal = "email"
	CanalPush  Canal = "push"
	CanalSMS   Canal = "sms"
)

// Prioridade decide a fila. PIN e código de login vão em Alta para não esperar atrás de
// lembrete de clube de leitura.
type Prioridade string

const (
	PrioridadeAlta   Prioridade = "alta"
	PrioridadeNormal Prioridade = "normal"
)

// Notificacao é um pedido de aviso vindo de outro sistema (o Booklit, o monitor de uptime).
type Notificacao struct {
	ID             string
	Origem         string // sistema que pediu: "booklit", "uptime"
	ChaveIdempot   string // a mesma chave duas vezes vira um aviso só
	Canal          Canal
	Prioridade     Prioridade
	Destinatario   string
	Template       string // nome do template, ex.: "otp-login"
	VersaoTemplate int
	// Variaveis do template. Podem ter segredo (o código do OTP): nunca vão para log
	// nem para o banco. Só existem no pedido em trânsito; lida do banco, vem nil.
	Variaveis map[string]string
	// NomesVariaveis é o que o banco guarda no lugar dos valores.
	NomesVariaveis []string
	// Impressao resume o pedido para a idempotência (ver aplicacao.impressao). Não inclui
	// os valores das variáveis.
	Impressao    string
	Estado       Estado
	Motivo       string     // por que falhou, foi suprimida etc.; vazio quando não se aplica
	ExpiraEm     *time.Time // depois disso não adianta mais entregar (OTP vencido)
	CriadoEm     time.Time
	AtualizadoEm time.Time
}

// Expirou diz se o aviso perdeu o sentido. Retentativa de OTP vencido é descartada.
func (n Notificacao) Expirou(agora time.Time) bool {
	return n.ExpiraEm != nil && !agora.Before(*n.ExpiraEm)
}

// String sem as variáveis, pelo mesmo motivo do toString da Mensagem no auth-core.
func (n Notificacao) String() string {
	return fmt.Sprintf("Notificacao[id=%s origem=%s canal=%s template=%s@v%d estado=%s]",
		n.ID, n.Origem, n.Canal, n.Template, n.VersaoTemplate, n.Estado)
}

// Estado do aviso. O 200 do provedor só leva até Enviada; Entregue depende do recibo
// (webhook). É aqui que mora o "provedor respondeu 200 e não entregou".
type Estado string

const (
	EstadoAceita    Estado = "aceita"    // gravada e publicada, ainda não saiu
	EstadoEnviada   Estado = "enviada"   // provedor aceitou (2xx)
	EstadoEntregue  Estado = "entregue"  // recibo de entrega chegou
	EstadoDevolvida Estado = "devolvida" // bounce ou reclamação
	EstadoFalhou    Estado = "falhou"    // erro permanente ou tentativas esgotadas
	EstadoSuprimida Estado = "suprimida" // destinatário pediu para não receber
	EstadoExpirada  Estado = "expirada"  // perdeu a validade antes de sair
	EstadoIncerta   Estado = "incerta"   // enviada, mas sem recibo dentro do prazo
)

var transicoes = map[Estado][]Estado{
	EstadoAceita:  {EstadoEnviada, EstadoFalhou, EstadoSuprimida, EstadoExpirada},
	EstadoEnviada: {EstadoEntregue, EstadoDevolvida, EstadoIncerta},
	// recibo atrasado ainda corrige um "incerta"
	EstadoIncerta: {EstadoEntregue, EstadoDevolvida},
}

var ErrTransicaoInvalida = errors.New("transição de estado inválida")

// Final diz se o estado não muda mais. O painel conta por estado final, não por tentativa.
func (e Estado) Final() bool {
	_, temSaida := transicoes[e]
	return !temSaida
}

// PodeIrPara valida a transição; é a regra que o consumidor e o webhook respeitam.
func (e Estado) PodeIrPara(destino Estado) error {
	for _, permitido := range transicoes[e] {
		if permitido == destino {
			return nil
		}
	}
	return fmt.Errorf("%w: %s -> %s", ErrTransicaoInvalida, e, destino)
}
