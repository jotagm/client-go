package notificacao

import (
	"net/url"
	"strings"
)

// Tipo é o que muda de um tipo de aviso para outro: a prioridade quando quem pediu não
// disse, se a validade é obrigatória e as regras extras sobre as variáveis. É o padrão
// Strategy; TipoDe é a fábrica que escolhe a estratégia pelo nome do template.
//
// O texto do e-mail continua no banco (t_template), versionado. O Tipo não o substitui: ele
// guarda as regras que o texto sozinho não expressa (um link precisa ser http/https).
// Template sem tipo registrado cai em Generico, que não acrescenta regra nenhuma.
type Tipo interface {
	// Nome identifica o tipo nos logs.
	Nome() string
	// PrioridadePadrao vale quando o pedido não traz prioridade.
	PrioridadePadrao() Prioridade
	// ExigeExpiracao diz se o pedido precisa de expira_em (o OTP precisa: vale por minutos).
	ExigeExpiracao() bool
	// Validar confere as variáveis. Devolve campo -> motivo, sem nunca citar valores.
	Validar(variaveis map[string]string) map[string]string
}

// TipoDe é a fábrica: devolve o tipo do template, ou Generico se ele não tem tipo próprio.
func TipoDe(template string) Tipo {
	if t, ok := tipos[template]; ok {
		return t
	}
	return Generico{}
}

var tipos = map[string]Tipo{
	"otp-login":           CodigoPorEmail{},
	"otp-verificar-email": CodigoPorEmail{},
	"otp-redefinir-pin":   CodigoPorEmail{},
	"otp-redefinir-senha": CodigoPorEmail{},
	"convite-clube":       ConviteDeClube{},
	"lembrete-votacao":    LembreteDeVotacao{},
}

// Generico é o tipo de quem não tem regra própria: prioridade normal e mais nada.
type Generico struct{}

func (Generico) Nome() string                                { return "generico" }
func (Generico) PrioridadePadrao() Prioridade                { return PrioridadeNormal }
func (Generico) ExigeExpiracao() bool                        { return false }
func (Generico) Validar(map[string]string) map[string]string { return nil }

// CodigoPorEmail cobre os OTPs do Booklit: o usuário está esperando na tela, então a
// prioridade é alta, e o código só vale por um tempo, então expira_em é obrigatório.
type CodigoPorEmail struct{}

func (CodigoPorEmail) Nome() string                 { return "codigo-por-email" }
func (CodigoPorEmail) PrioridadePadrao() Prioridade { return PrioridadeAlta }
func (CodigoPorEmail) ExigeExpiracao() bool         { return true }

func (CodigoPorEmail) Validar(variaveis map[string]string) map[string]string {
	// Código com espaço ou quebra de linha nunca é um código: é erro de quem chamou.
	if codigo := variaveis["codigo"]; strings.ContainsAny(codigo, " \t\r\n") {
		return map[string]string{"variaveis.codigo": "não pode ter espaço nem quebra de linha"}
	}
	return nil
}

// ConviteDeClube é o aviso de convite: não é urgente (prioridade normal), não vence em
// minutos, e o link vai para a caixa de entrada de um terceiro, então só aceita http(s).
type ConviteDeClube struct{}

func (ConviteDeClube) Nome() string                 { return "convite-clube" }
func (ConviteDeClube) PrioridadePadrao() Prioridade { return PrioridadeNormal }
func (ConviteDeClube) ExigeExpiracao() bool         { return false }

func (ConviteDeClube) Validar(variaveis map[string]string) map[string]string {
	campos := map[string]string{}
	u, err := url.Parse(variaveis["link"])
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		campos["variaveis.link"] = "precisa ser um endereço http(s)"
	}
	for _, nome := range []string{"convidado_por", "clube"} {
		if strings.ContainsAny(variaveis[nome], "\r\n") {
			campos["variaveis."+nome] = "não pode ter quebra de linha"
		}
	}
	if len(campos) == 0 {
		return nil
	}
	return campos
}

// LembreteDeVotacao é o "falta o seu voto" que o líder dispara: como o convite, não é urgente
// e não vence em minutos. O link vai para a caixa de um terceiro (só http/https) e o nome do
// clube e o prazo entram no corpo, então não podem trazer quebra de linha.
type LembreteDeVotacao struct{}

func (LembreteDeVotacao) Nome() string                 { return "lembrete-votacao" }
func (LembreteDeVotacao) PrioridadePadrao() Prioridade { return PrioridadeNormal }
func (LembreteDeVotacao) ExigeExpiracao() bool         { return false }

func (LembreteDeVotacao) Validar(variaveis map[string]string) map[string]string {
	campos := map[string]string{}
	if !linkHTTP(variaveis["link"]) {
		campos["variaveis.link"] = "precisa ser um endereço http(s)"
	}
	for _, nome := range []string{"clube", "prazo"} {
		if strings.ContainsAny(variaveis[nome], "\r\n") {
			campos["variaveis."+nome] = "não pode ter quebra de linha"
		}
	}
	if len(campos) == 0 {
		return nil
	}
	return campos
}

func linkHTTP(link string) bool {
	u, err := url.Parse(link)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}
