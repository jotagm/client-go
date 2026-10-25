// Package sns publica notificações num tópico SNS.
package sns

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

// API é o pedaço do cliente SNS que o publicador usa (facilita o teste).
type API interface {
	Publish(ctx context.Context, in *sns.PublishInput, opts ...func(*sns.Options)) (*sns.PublishOutput, error)
}

type Publicador struct {
	cliente API
	topico  string
}

var _ porta.Publicador = (*Publicador)(nil)

func NovoPublicador(cliente API, topicoARN string) *Publicador {
	return &Publicador{cliente: cliente, topico: topicoARN}
}

// Publicar manda o envelope com os atributos canal e prioridade. São esses atributos que
// as filter policies das assinaturas usam para escolher a fila.
func (p *Publicador) Publicar(ctx context.Context, n notificacao.Notificacao) error {
	corpo, err := notificacao.CodificarEnvelope(n)
	if err != nil {
		return fmt.Errorf("codificar envelope: %w", err)
	}
	_, err = p.cliente.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(p.topico),
		Message:  aws.String(string(corpo)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"canal":      texto(string(n.Canal)),
			"prioridade": texto(string(n.Prioridade)),
			"origem":     texto(n.Origem),
		},
	})
	if err != nil {
		// o erro do SDK não inclui a mensagem publicada
		return fmt.Errorf("publicar no SNS: %w", err)
	}
	return nil
}

func texto(v string) types.MessageAttributeValue {
	return types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
}
