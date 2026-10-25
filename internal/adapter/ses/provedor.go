// Package ses implementa porta.Provedor com o Amazon SES v2.
//
// O papel deste pacote é um só: mandar o e-mail e dizer, com a classificação certa, se
// vale tentar de novo. Quem decide quando e quantas vezes é o Processador.
package ses

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"

	"github.com/jotagm/client-go/internal/porta"
)

// Cliente é o pedaço do SDK que o provedor usa; existe para o teste poder trocar.
type Cliente interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, opts ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

const (
	nome              = "ses"
	charset           = "UTF-8"
	TimeoutPadrao     = 5 * time.Second
	tamanhoMaxDetalhe = 300
)

type Provedor struct {
	cliente   Cliente
	remetente string
	conjunto  string
	timeout   time.Duration
}

var _ porta.Provedor = (*Provedor)(nil)

// Novo cria o provedor. remetente é o endereço de origem (ex.: "Booklit <codigo@booklit.bio>");
// conjunto é o configuration set do SES (para os eventos de entrega da fase 6) e pode ser
// vazio; timeout vale para cada chamada.
func Novo(cliente Cliente, remetente, conjunto string, timeout time.Duration) *Provedor {
	if timeout <= 0 {
		timeout = TimeoutPadrao
	}
	return &Provedor{cliente: cliente, remetente: remetente, conjunto: conjunto, timeout: timeout}
}

// NovoCliente devolve o cliente do SDK sem as retentativas embutidas. O Client_GO já tenta de
// novo pela fila, com backoff e limite próprios; o SDK repetindo por baixo esconderia
// tentativas da tabela t_tentativa_envio e atrasaria o ChangeMessageVisibility.
func NovoCliente(cfg aws.Config) *sesv2.Client {
	return sesv2.NewFromConfig(cfg, func(o *sesv2.Options) { o.Retryer = aws.NopRetryer{} })
}

func (p *Provedor) Nome() string { return nome }

func (p *Provedor) Enviar(ctx context.Context, e porta.Envio) (porta.Resultado, error) {
	ctx, cancelar := context.WithTimeout(ctx, p.timeout)
	defer cancelar()

	entrada := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(p.remetente),
		Destination:      &types.Destination{ToAddresses: []string{e.Destinatario}},
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(e.Assunto), Charset: aws.String(charset)},
			Body:    &types.Body{Text: &types.Content{Data: aws.String(e.Corpo), Charset: aws.String(charset)}},
		}},
	}
	if p.conjunto != "" {
		entrada.ConfigurationSetName = aws.String(p.conjunto)
	}

	inicio := time.Now()
	saida, err := p.cliente.SendEmail(ctx, entrada)
	latencia := time.Since(inicio)
	if err != nil {
		return porta.Resultado{Latencia: latencia}, Classificar(err)
	}

	// 200 sem identificador: não dá para casar o recibo depois. O e-mail provavelmente saiu, mas
	// não sabemos; tratar como temporário (o OTP repetido é inofensivo, e o contrário não é).
	if saida == nil || aws.ToString(saida.MessageId) == "" {
		return porta.Resultado{Latencia: latencia}, &porta.ErroProvedor{
			Classe: porta.ErrTemporario, Motivo: "resposta_sem_id", StatusHTTP: 200,
			Detalhe: "o SES respondeu sem MessageId"}
	}
	return porta.Resultado{IDNoProvedor: aws.ToString(saida.MessageId), Latencia: latencia}, nil
}

// Classificar traduz o erro do SDK em *porta.ErroProvedor.
//
//   - permanente: o SES entendeu e recusou (mensagem rejeitada, requisição inválida, domínio
//     não verificado, conta suspensa). Repetir produz a mesma recusa.
//   - temporário: limite de taxa, 5xx, timeout, rede, resposta ilegível. Na dúvida, temporário:
//     o limite de 5 tentativas e a validade do código impedem que isso vire laço infinito.
func Classificar(err error) *porta.ErroProvedor {
	status := 0
	var resp *awshttp.ResponseError
	if errors.As(err, &resp) {
		status = resp.HTTPStatusCode()
	}
	novo := func(classe error, motivo string) *porta.ErroProvedor {
		return &porta.ErroProvedor{Classe: classe, Motivo: motivo, StatusHTTP: status, Detalhe: detalhe(err)}
	}

	var (
		tooMany      *types.TooManyRequestsException
		limite       *types.LimitExceededException
		pausado      *types.SendingPausedException
		interno      *types.InternalServiceErrorException
		rejeitada    *types.MessageRejected
		invalida     *types.BadRequestException
		dominio      *types.MailFromDomainNotVerifiedException
		suspensa     *types.AccountSuspendedException
		naoEncontrou *types.NotFoundException
		deserializa  *smithy.DeserializationError
		rede         net.Error
	)
	switch {
	case errors.As(err, &tooMany):
		return novo(porta.ErrTemporario, "throttling")
	case errors.As(err, &limite):
		return novo(porta.ErrTemporario, "limite_excedido")
	case errors.As(err, &pausado):
		return novo(porta.ErrTemporario, "envio_pausado")
	case errors.As(err, &interno):
		return novo(porta.ErrTemporario, "erro_do_provedor")

	case errors.As(err, &rejeitada):
		return novo(porta.ErrPermanente, "mensagem_rejeitada")
	case errors.As(err, &invalida):
		return novo(porta.ErrPermanente, "requisicao_invalida")
	case errors.As(err, &dominio):
		return novo(porta.ErrPermanente, "dominio_nao_verificado")
	case errors.As(err, &suspensa):
		return novo(porta.ErrPermanente, "conta_suspensa")
	case errors.As(err, &naoEncontrou):
		return novo(porta.ErrPermanente, "nao_encontrado")

	// Throttling genérico da AWS às vezes vem como 400 com um destes códigos: status sozinho
	// o chamaria de permanente.
	case codigoDeThrottling(err):
		return novo(porta.ErrTemporario, "throttling")

	// Erro que o SDK não tipou: o status HTTP decide. Vem antes do erro de desserialização
	// porque um 503 de balanceador com corpo HTML também falha ao ser lido como JSON, e o que
	// importa ali é o 503, não o corpo.
	case status == 429:
		return novo(porta.ErrTemporario, "throttling")
	case status >= 500:
		return novo(porta.ErrTemporario, "erro_do_provedor")
	case status >= 400:
		return novo(porta.ErrPermanente, "recusado")

	case errors.Is(err, context.DeadlineExceeded):
		return novo(porta.ErrTemporario, "timeout")
	case errors.Is(err, context.Canceled):
		return novo(porta.ErrTemporario, "cancelado")
	case errors.As(err, &deserializa):
		// Status 2xx com corpo ilegível: não sabemos se o e-mail saiu.
		return novo(porta.ErrTemporario, "resposta_malformada")
	}

	if errors.As(err, &rede) {
		if rede.Timeout() {
			return novo(porta.ErrTemporario, "timeout")
		}
		return novo(porta.ErrTemporario, "rede")
	}
	return novo(porta.ErrTemporario, "desconhecido")
}

func codigoDeThrottling(err error) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.ErrorCode() {
	case "ThrottlingException", "Throttling", "RequestLimitExceeded", "ServiceUnavailable", "RequestTimeout":
		return true
	}
	return false
}

// detalhe tira da mensagem do SDK o que dá para guardar: uma linha, tamanho limitado. O SDK
// não inclui o corpo do e-mail nas mensagens de erro; o EntregadorEmail ainda esconde os
// valores das variáveis antes de gravar.
func detalhe(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > tamanhoMaxDetalhe {
		corte := tamanhoMaxDetalhe
		for corte > 0 && s[corte]&0xC0 == 0x80 { // não corta no meio de um caractere UTF-8
			corte--
		}
		s = s[:corte] + "…"
	}
	return s
}
