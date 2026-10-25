package sqs_test

// Testes contra uma AWS emulada (Floci, LocalStack, moto). Sem a variável, são pulados:
//
//	AVISO_TESTE_AWS_ENDPOINT=http://localhost:4566 go test ./internal/adapter/...
//
// Cada execução cria tópico e filas com nome próprio, então não briga com o que o
// Terraform criou.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssns "github.com/aws/aws-sdk-go-v2/service/sns"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/jotagm/client-go/internal/adapter/memoria"
	"github.com/jotagm/client-go/internal/adapter/sns"
	"github.com/jotagm/client-go/internal/adapter/sqs"
	"github.com/jotagm/client-go/internal/aplicacao"
	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

type ambiente struct {
	sns    *awssns.Client
	sqs    *awssqs.Client
	topico string
	filas  map[string]string // prioridade -> url
}

func novoAmbiente(t *testing.T) ambiente {
	t.Helper()
	endpoint := os.Getenv("AVISO_TESTE_AWS_ENDPOINT")
	if endpoint == "" {
		t.Skip("AVISO_TESTE_AWS_ENDPOINT não definida")
	}
	ctx := context.Background()
	cfg, err := awscfg.LoadDefaultConfig(ctx,
		awscfg.WithRegion("us-east-1"),
		awscfg.WithBaseEndpoint(endpoint),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	a := ambiente{sns: awssns.NewFromConfig(cfg), sqs: awssqs.NewFromConfig(cfg), filas: map[string]string{}}

	sufixo := fmt.Sprintf("%d", time.Now().UnixNano())
	topico, err := a.sns.CreateTopic(ctx, &awssns.CreateTopicInput{Name: aws.String("teste-" + sufixo)})
	if err != nil {
		t.Fatal(err)
	}
	a.topico = aws.ToString(topico.TopicArn)
	t.Cleanup(func() { _, _ = a.sns.DeleteTopic(ctx, &awssns.DeleteTopicInput{TopicArn: topico.TopicArn}) })

	for _, prioridade := range []string{"alta", "normal"} {
		fila, err := a.sqs.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String("teste-" + prioridade + "-" + sufixo)})
		if err != nil {
			t.Fatal(err)
		}
		url := aws.ToString(fila.QueueUrl)
		a.filas[prioridade] = url
		t.Cleanup(func() { _, _ = a.sqs.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(url)}) })

		attrs, err := a.sqs.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
			QueueUrl: fila.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
		})
		if err != nil {
			t.Fatal(err)
		}
		filtro, _ := json.Marshal(map[string][]string{"canal": {"email"}, "prioridade": {prioridade}})
		_, err = a.sns.Subscribe(ctx, &awssns.SubscribeInput{
			TopicArn: topico.TopicArn, Protocol: aws.String("sqs"),
			Endpoint: aws.String(attrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]),
			Attributes: map[string]string{
				"RawMessageDelivery": "true",
				"FilterPolicy":       string(filtro),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func (a ambiente) consumidor(prioridade string) *sqs.Consumidor {
	c := sqs.NovoConsumidor(a.sqs, a.filas[prioridade])
	c.EsperaLongPolling = 1 * time.Second
	return c
}

func receberUma(t *testing.T, c *sqs.Consumidor, limite time.Duration) (porta.Mensagem, bool) {
	t.Helper()
	prazo := time.Now().Add(limite)
	for time.Now().Before(prazo) {
		ms, err := c.Receber(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(ms) > 0 {
			if len(ms) > 1 {
				t.Fatalf("esperava 1 mensagem, vieram %d", len(ms))
			}
			return ms[0], true
		}
	}
	return porta.Mensagem{}, false
}

func notificacaoDeTeste(prioridade notificacao.Prioridade) notificacao.Notificacao {
	return notificacao.Notificacao{
		ID: notificacao.NovoID(), Origem: "booklit", Canal: notificacao.CanalEmail, Prioridade: prioridade,
		Variaveis: map[string]string{"codigo": "482913"},
	}
}

func TestPublicarRoteiaPelaPrioridade(t *testing.T) {
	a := novoAmbiente(t)
	pub := sns.NovoPublicador(a.sns, a.topico)
	ctx := context.Background()

	alta := notificacaoDeTeste(notificacao.PrioridadeAlta)
	normal := notificacaoDeTeste(notificacao.PrioridadeNormal)
	for _, n := range []notificacao.Notificacao{alta, normal} {
		if err := pub.Publicar(ctx, n); err != nil {
			t.Fatal(err)
		}
	}

	for prioridade, esperada := range map[string]notificacao.Notificacao{"alta": alta, "normal": normal} {
		c := a.consumidor(prioridade)
		m, ok := receberUma(t, c, 5*time.Second)
		if !ok {
			t.Fatalf("fila %s: nada chegou", prioridade)
		}
		env, err := notificacao.DecodificarEnvelope(m.Corpo)
		if err != nil {
			t.Fatalf("fila %s: corpo não é o envelope cru (raw delivery desligado?): %v", prioridade, err)
		}
		if env.ID != esperada.ID || env.Variaveis["codigo"] != "482913" {
			t.Errorf("fila %s recebeu %s, esperava %s", prioridade, env, esperada.ID)
		}
		if m.Recebimento != 1 {
			t.Errorf("fila %s: Recebimento = %d", prioridade, m.Recebimento)
		}
		if err := c.Confirmar(ctx, m); err != nil {
			t.Fatal(err)
		}
		// filtros disjuntos: nada mais nesta fila
		if m, ok := receberUma(t, c, 2*time.Second); ok {
			t.Errorf("fila %s recebeu uma segunda mensagem: %s", prioridade, m.Corpo)
		}
	}
}

// Mensagem que o Client_GO não consegue processar (envelope ilegível) não é apagada: volta com
// backoff até a redrive policy levar para a DLQ. Laço de consumo de verdade + SQS de verdade.
func TestMensagemVenenoChegaNaDLQ(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	sufixo := fmt.Sprintf("%d", time.Now().UnixNano())

	dlq, err := a.sqs.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String("teste-dlq-" + sufixo)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.sqs.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl}) })
	dlqAttrs, err := a.sqs.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl: dlq.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatal(err)
	}
	redrive, _ := json.Marshal(map[string]any{
		"deadLetterTargetArn": dlqAttrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)],
		"maxReceiveCount":     2,
	})
	fila, err := a.sqs.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName:  aws.String("teste-veneno-" + sufixo),
		Attributes: map[string]string{"RedrivePolicy": string(redrive)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.sqs.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: fila.QueueUrl}) })

	if _, err := a.sqs.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl: fila.QueueUrl, MessageBody: aws.String(`isto não é um envelope`),
	}); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := aplicacao.RetentativaPadrao()
	r.Base = 500 * time.Millisecond
	processador := aplicacao.NovoProcessador(memoria.NovoRepositorio(), memoria.NovoEntregador(), r, log)
	consumidor := sqs.NovoConsumidor(a.sqs, aws.ToString(fila.QueueUrl))
	consumidor.EsperaLongPolling = time.Second

	ctxConsumo, parar := context.WithCancel(ctx)
	feito := make(chan struct{})
	go func() {
		aplicacao.Consumir(ctxConsumo, "veneno", consumidor, processador, 1, log)
		close(feito)
	}()
	defer func() { parar(); <-feito }()

	daDLQ := sqs.NovoConsumidor(a.sqs, aws.ToString(dlq.QueueUrl))
	daDLQ.EsperaLongPolling = time.Second
	m, ok := receberUma(t, daDLQ, 20*time.Second)
	if !ok {
		t.Fatal("a mensagem veneno não chegou na DLQ")
	}
	if string(m.Corpo) != `isto não é um envelope` {
		t.Errorf("chegou outra coisa na DLQ: %s", m.Corpo)
	}
}

func TestAdiarEsconde(t *testing.T) {
	a := novoAmbiente(t)
	pub := sns.NovoPublicador(a.sns, a.topico)
	ctx := context.Background()
	if err := pub.Publicar(ctx, notificacaoDeTeste(notificacao.PrioridadeAlta)); err != nil {
		t.Fatal(err)
	}

	c := a.consumidor("alta")
	m, ok := receberUma(t, c, 5*time.Second)
	if !ok {
		t.Fatal("nada chegou")
	}
	if err := c.Adiar(ctx, m, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, ok := receberUma(t, c, 1*time.Second); ok {
		t.Fatal("a mensagem adiada voltou antes da hora")
	}

	de, ok := receberUma(t, c, 5*time.Second)
	if !ok {
		t.Fatal("a mensagem adiada não voltou")
	}
	if de.Recebimento != 2 {
		t.Errorf("Recebimento = %d, esperava 2", de.Recebimento)
	}
	if err := c.Confirmar(ctx, de); err != nil {
		t.Fatal(err)
	}
	if _, ok := receberUma(t, c, 2*time.Second); ok {
		t.Error("a mensagem confirmada continuou na fila")
	}
}
