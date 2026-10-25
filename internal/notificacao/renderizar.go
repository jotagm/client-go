package notificacao

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
)

var (
	// ErrDadosInvalidos: o template está certo, mas os dados do pedido não servem para ele
	// (variável vazia, faltando, validade ausente). Culpa de quem pediu: no aceite vira 422.
	ErrDadosInvalidos = errors.New("dados inválidos para o template")
	// ErrTemplateInvalido: o próprio template está quebrado (não compila, usa uma variável
	// que ninguém declarou). Culpa nossa: no aceite vira 500, na entrega vira falha permanente.
	ErrTemplateInvalido = errors.New("template inválido")
)

// VariavelMinutos é calculada na hora de renderizar, a partir de ExpiraEm. Quem pede o aviso
// não pode mandá-la: um valor vindo de fora poderia contradizer a validade real do código.
const VariavelMinutos = "minutos"

// Renderizado é o texto pronto para ir ao provedor.
type Renderizado struct {
	Assunto string
	Corpo   string
}

var campoAusente = regexp.MustCompile(`map has no entry for key "([^"]+)"`)

// Renderizar monta assunto e corpo com text/template e missingkey=error: qualquer variável
// que o template use e ninguém tenha fornecido é erro, nunca um texto com "<no value>" ou
// um código em branco indo para a caixa de entrada de alguém.
//
// Os erros só citam NOMES de variáveis, nunca valores: o código do OTP não pode chegar ao
// log nem à resposta da API por esta via.
func (t Template) Renderizar(variaveis map[string]string, expiraEm *time.Time, agora time.Time) (Renderizado, error) {
	dados, err := t.dados(variaveis, expiraEm, agora)
	if err != nil {
		return Renderizado{}, err
	}

	assunto, err := t.executar("assunto", t.Assunto, dados)
	if err != nil {
		return Renderizado{}, err
	}
	corpo, err := t.executar("corpo", t.Corpo, dados)
	if err != nil {
		return Renderizado{}, err
	}

	// Quebra de linha no assunto abriria espaço para injetar cabeçalhos no e-mail.
	if strings.ContainsAny(assunto, "\r\n") {
		return Renderizado{}, fmt.Errorf("%w: o assunto tem quebra de linha", ErrDadosInvalidos)
	}
	if t.Canal == CanalEmail && strings.TrimSpace(assunto) == "" {
		return Renderizado{}, fmt.Errorf("%w: e-mail sem assunto", ErrTemplateInvalido)
	}
	if strings.TrimSpace(corpo) == "" {
		return Renderizado{}, fmt.Errorf("%w: corpo vazio", ErrTemplateInvalido)
	}
	return Renderizado{Assunto: assunto, Corpo: corpo}, nil
}

func (t Template) dados(variaveis map[string]string, expiraEm *time.Time, agora time.Time) (map[string]string, error) {
	if _, usada := variaveis[VariavelMinutos]; usada {
		return nil, fmt.Errorf("%w: %q é calculada pelo Client_GO e não pode ser enviada", ErrDadosInvalidos, VariavelMinutos)
	}
	dados := make(map[string]string, len(variaveis)+1)
	for _, nome := range t.Variaveis {
		valor, ok := variaveis[nome]
		switch {
		case !ok:
			return nil, fmt.Errorf("%w: falta a variável %q", ErrDadosInvalidos, nome)
		case strings.TrimSpace(valor) == "":
			// "Seu código é " numa caixa de entrada é pior do que e-mail nenhum.
			return nil, fmt.Errorf("%w: a variável %q está vazia", ErrDadosInvalidos, nome)
		}
		dados[nome] = valor
	}
	// Variável a mais é ignorada aqui de propósito: o aceite já as recusa, e a renderização
	// não pode depender de dados que o template não declarou.

	if expiraEm != nil {
		restante := expiraEm.Sub(agora)
		if restante <= 0 {
			return nil, fmt.Errorf("%w: o aviso já expirou", ErrDadosInvalidos)
		}
		// Arredonda para cima: um código de 10 minutos, enviado 2 segundos depois de criado,
		// tem que dizer "10" e não "9". Nunca sai 0: o restante é sempre positivo aqui.
		dados[VariavelMinutos] = strconv.Itoa(int((restante + time.Minute - 1) / time.Minute))
	}
	return dados, nil
}

func (t Template) executar(parte, texto string, dados map[string]string) (string, error) {
	nome := fmt.Sprintf("%s@v%d/%s", t.Nome, t.Versao, parte)
	tpl, err := template.New(nome).Option("missingkey=error").Parse(texto)
	if err != nil {
		return "", fmt.Errorf("%w: %s não compila: %v", ErrTemplateInvalido, nome, err)
	}
	var saida strings.Builder
	if err := tpl.Execute(&saida, dados); err != nil {
		if m := campoAusente.FindStringSubmatch(err.Error()); m != nil {
			if m[1] == VariavelMinutos {
				return "", fmt.Errorf("%w: este template usa %q, que depende de expira_em", ErrDadosInvalidos, VariavelMinutos)
			}
			return "", fmt.Errorf("%w: %s usa a variável %q, que o template não declarou", ErrTemplateInvalido, nome, m[1])
		}
		return "", fmt.Errorf("%w: %s: %v", ErrTemplateInvalido, nome, err)
	}
	return saida.String(), nil
}
