// Package sqs consome uma fila SQS.
package sqs

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/jotagm/client-go/internal/porta"
)

// O SQS não deixa esconder uma mensagem por mais de 12 horas.
const visibilidadeMaxima = 12 * time.Hour

// API é o pedaço do cliente SQS que o consumidor usa.
type API interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

type Consumidor struct {
	cliente API
	url     string
	// EsperaLongPolling: até quanto o ReceiveMessage segura a conexão esperando mensagem.
	// 20s (o máximo) evita ficar pagando por recebimento vazio.
	EsperaLongPolling time.Duration
	MaxMensagens      int32
}

var _ porta.Consumidor = (*Consumidor)(nil)

func NovoConsumidor(cliente API, filaURL string) *Consumidor {
	return &Consumidor{cliente: cliente, url: filaURL, EsperaLongPolling: 20 * time.Second, MaxMensagens: 10}
}

func (c *Consumidor) Receber(ctx context.Context) ([]porta.Mensagem, error) {
	out, err := c.cliente.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(c.url),
		MaxNumberOfMessages:         c.MaxMensagens,
		WaitTimeSeconds:             int32(c.EsperaLongPolling / time.Second),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
	})
	if err != nil {
		return nil, fmt.Errorf("receber de %s: %w", c.url, err)
	}
	ms := make([]porta.Mensagem, 0, len(out.Messages))
	for _, m := range out.Messages {
		recebimento, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
		if recebimento < 1 {
			recebimento = 1
		}
		ms = append(ms, porta.Mensagem{
			ID:          aws.ToString(m.MessageId),
			Recibo:      aws.ToString(m.ReceiptHandle),
			Corpo:       []byte(aws.ToString(m.Body)),
			Recebimento: recebimento,
		})
	}
	return ms, nil
}

func (c *Consumidor) Confirmar(ctx context.Context, m porta.Mensagem) error {
	_, err := c.cliente.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(c.url), ReceiptHandle: aws.String(m.Recibo),
	})
	if err != nil {
		return fmt.Errorf("apagar mensagem %s: %w", m.ID, err)
	}
	return nil
}

// Adiar muda a visibilidade da mensagem: ela some da fila por "espera" e volta sozinha,
// contando mais um recebimento. Nenhum worker fica parado esperando.
func (c *Consumidor) Adiar(ctx context.Context, m porta.Mensagem, espera time.Duration) error {
	espera = min(espera, visibilidadeMaxima)
	segundos := int32((espera + time.Second - 1) / time.Second) // arredonda para cima
	_, err := c.cliente.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(c.url), ReceiptHandle: aws.String(m.Recibo), VisibilityTimeout: segundos,
	})
	if err != nil {
		return fmt.Errorf("adiar mensagem %s: %w", m.ID, err)
	}
	return nil
}
