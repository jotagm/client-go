package ses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"

	"github.com/jotagm/client-go/internal/porta"
)

// fakeSES é um servidor HTTP que imita a API do SES v2 (REST + JSON). Cada teste escolhe a
// resposta; o servidor conta as chamadas e guarda o último corpo recebido.
type fakeSES struct {
	*httptest.Server
	chamadas atomic.Int32
	ultimo   atomic.Value // []byte
}

func novoFake(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *fakeSES {
	t.Helper()
	f := &fakeSES{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.chamadas.Add(1)
		corpo, _ := io.ReadAll(r.Body)
		f.ultimo.Store(corpo)
		if r.Method != http.MethodPost || r.URL.Path != "/v2/email/outbound-emails" {
			t.Errorf("chamada inesperada: %s %s", r.Method, r.URL.Path)
		}
		h(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSES) provedor(timeout time.Duration) *Provedor {
	cfg := aws.Config{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("teste", "teste", ""),
		BaseEndpoint: aws.String(f.URL),
	}
	return Novo(NovoCliente(cfg), "Booklit <codigo@booklit.bio>", "", timeout)
}

func envio() porta.Envio {
	return porta.Envio{
		NotificacaoID: "n1", Destinatario: "jota@exemplo.com",
		Assunto: "Seu código de acesso", Corpo: "Código 482913, vale 10 min.",
	}
}

func erroSES(w http.ResponseWriter, status int, tipo, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-ErrorType", tipo)
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"message":%q}`, msg)
}

func comoErroProvedor(t *testing.T, err error) *porta.ErroProvedor {
	t.Helper()
	var ep *porta.ErroProvedor
	if !errors.As(err, &ep) {
		t.Fatalf("o erro não é *ErroProvedor: %T %v", err, err)
	}
	return ep
}

func TestEnviarSucesso(t *testing.T) {
	f := novoFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"MessageId":"0100018c-abc-000"}`)
	})

	res, err := f.provedor(time.Second).Enviar(context.Background(), envio())
	if err != nil {
		t.Fatal(err)
	}
	if res.IDNoProvedor != "0100018c-abc-000" || res.Latencia <= 0 {
		t.Errorf("resultado = %+v", res)
	}

	// o que o SES recebeu
	var corpo struct {
		FromEmailAddress string
		Destination      struct{ ToAddresses []string }
		Content          struct {
			Simple struct {
				Subject struct{ Data, Charset string }
				Body    struct {
					Text struct{ Data, Charset string }
				}
			}
		}
		ConfigurationSetName string
	}
	if err := json.Unmarshal(f.ultimo.Load().([]byte), &corpo); err != nil {
		t.Fatal(err)
	}
	s := corpo.Content.Simple
	if corpo.FromEmailAddress != "Booklit <codigo@booklit.bio>" ||
		len(corpo.Destination.ToAddresses) != 1 || corpo.Destination.ToAddresses[0] != "jota@exemplo.com" ||
		s.Subject.Data != "Seu código de acesso" || s.Subject.Charset != "UTF-8" ||
		s.Body.Text.Data != "Código 482913, vale 10 min." || s.Body.Text.Charset != "UTF-8" {
		t.Errorf("corpo da requisição = %+v", corpo)
	}
	if corpo.ConfigurationSetName != "" {
		t.Errorf("configuration set vazio não deveria ser enviado: %q", corpo.ConfigurationSetName)
	}
}

func TestEnviarComConfigurationSet(t *testing.T) {
	f := novoFake(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"MessageId":"x"}`) })
	p := f.provedor(time.Second)
	p.conjunto = "client-go-eventos"
	if _, err := p.Enviar(context.Background(), envio()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.ultimo.Load().([]byte)), `"ConfigurationSetName":"client-go-eventos"`) {
		t.Errorf("corpo = %s", f.ultimo.Load())
	}
}

// Os quatro cenários da fase 3, mais os erros que o SES tipa.
func TestEnviarClassificaAsRespostas(t *testing.T) {
	casos := []struct {
		nome   string
		h      func(w http.ResponseWriter, r *http.Request)
		classe error
		motivo string
		status int
	}{
		{"throttling", func(w http.ResponseWriter, _ *http.Request) {
			erroSES(w, 429, "TooManyRequestsException", "Rate exceeded")
		}, porta.ErrTemporario, "throttling", 429},
		{"erro interno tipado", func(w http.ResponseWriter, _ *http.Request) {
			erroSES(w, 500, "InternalServiceErrorException", "falha interna")
		}, porta.ErrTemporario, "erro_do_provedor", 500},
		{"503 sem corpo de API", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		}, porta.ErrTemporario, "erro_do_provedor", 503},
		{"429 sem tipo", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "slow down", http.StatusTooManyRequests)
		}, porta.ErrTemporario, "throttling", 429},
		{"throttling genérico como 400", func(w http.ResponseWriter, _ *http.Request) {
			erroSES(w, 400, "ThrottlingException", "Rate exceeded")
		}, porta.ErrTemporario, "throttling", 400},
		{"envio pausado", func(w http.ResponseWriter, _ *http.Request) {
			erroSES(w, 400, "SendingPausedException", "paused")
		}, porta.ErrTemporario, "envio_pausado", 400},
		{"mensagem rejeitada", func(w http.ResponseWriter, _ *http.Request) {
			erroSES(w, 400, "MessageRejected", "Email address is not verified.")
		}, porta.ErrPermanente, "mensagem_rejeitada", 400},
		{"requisição inválida", func(w http.ResponseWriter, _ *http.Request) {
			erroSES(w, 400, "BadRequestException", "Illegal address")
		}, porta.ErrPermanente, "requisicao_invalida", 400},
		{"domínio não verificado", func(w http.ResponseWriter, _ *http.Request) {
			erroSES(w, 400, "MailFromDomainNotVerifiedException", "domínio")
		}, porta.ErrPermanente, "dominio_nao_verificado", 400},
		{"conta suspensa", func(w http.ResponseWriter, _ *http.Request) {
			erroSES(w, 400, "AccountSuspendedException", "suspensa")
		}, porta.ErrPermanente, "conta_suspensa", 400},
		{"403 sem tipo conhecido", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		}, porta.ErrPermanente, "recusado", 403},
		{"resposta malformada", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `<html>isso não é JSON`)
		}, porta.ErrTemporario, "resposta_malformada", 200},
		{"200 sem MessageId", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{}`)
		}, porta.ErrTemporario, "resposta_sem_id", 200},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			f := novoFake(t, tc.h)
			_, err := f.provedor(time.Second).Enviar(context.Background(), envio())
			if !errors.Is(err, tc.classe) {
				t.Fatalf("err = %v, queria %v", err, tc.classe)
			}
			ep := comoErroProvedor(t, err)
			if ep.Motivo != tc.motivo || ep.StatusHTTP != tc.status {
				t.Errorf("motivo=%q status=%d, queria %q e %d (detalhe: %s)", ep.Motivo, ep.StatusHTTP, tc.motivo, tc.status, ep.Detalhe)
			}
			if n := f.chamadas.Load(); n != 1 {
				t.Errorf("o SES recebeu %d chamadas: o SDK está repetindo por baixo da fila", n)
			}
		})
	}
}

func TestEnviarProvedorLento(t *testing.T) {
	liberar := make(chan struct{})
	f := novoFake(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-liberar:
		case <-r.Context().Done():
		}
	})
	defer close(liberar)

	inicio := time.Now()
	_, err := f.provedor(80*time.Millisecond).Enviar(context.Background(), envio())
	if !errors.Is(err, porta.ErrTemporario) {
		t.Fatalf("err = %v", err)
	}
	if ep := comoErroProvedor(t, err); ep.Motivo != "timeout" || ep.StatusHTTP != 0 {
		t.Errorf("motivo=%q status=%d", ep.Motivo, ep.StatusHTTP)
	}
	if d := time.Since(inicio); d > 2*time.Second {
		t.Errorf("o timeout de 80ms levou %v", d)
	}
}

func TestEnviarProvedorForaDoAr(t *testing.T) {
	f := novoFake(t, func(http.ResponseWriter, *http.Request) {})
	p := f.provedor(time.Second)
	f.Close() // porta fechada: conexão recusada

	_, err := p.Enviar(context.Background(), envio())
	if !errors.Is(err, porta.ErrTemporario) {
		t.Fatalf("err = %v", err)
	}
	if ep := comoErroProvedor(t, err); ep.StatusHTTP != 0 {
		t.Errorf("sem resposta HTTP o status deveria ser 0: %+v", ep)
	}
}

func TestEnviarContextoCancelado(t *testing.T) {
	f := novoFake(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"MessageId":"x"}`) })
	ctx, cancelar := context.WithCancel(context.Background())
	cancelar()

	_, err := f.provedor(time.Second).Enviar(ctx, envio())
	if !errors.Is(err, porta.ErrTemporario) {
		t.Fatalf("err = %v", err)
	}
}

// Classificar sem passar pela rede: erros tipados embrulhados como o SDK os entrega.
func TestClassificar(t *testing.T) {
	op := func(err error) error {
		return &smithy.OperationError{ServiceID: "SESv2", OperationName: "SendEmail", Err: err}
	}
	casos := []struct {
		err    error
		classe error
		motivo string
	}{
		{op(&types.TooManyRequestsException{}), porta.ErrTemporario, "throttling"},
		{op(&types.LimitExceededException{}), porta.ErrTemporario, "limite_excedido"},
		{op(&types.MessageRejected{}), porta.ErrPermanente, "mensagem_rejeitada"},
		{op(&types.NotFoundException{}), porta.ErrPermanente, "nao_encontrado"},
		{op(context.DeadlineExceeded), porta.ErrTemporario, "timeout"},
		{errors.New("algo que ninguém previu"), porta.ErrTemporario, "desconhecido"},
	}
	for _, tc := range casos {
		ep := Classificar(tc.err)
		if !errors.Is(ep, tc.classe) || ep.Motivo != tc.motivo {
			t.Errorf("%v -> %v/%s, queria %v/%s", tc.err, ep.Classe, ep.Motivo, tc.classe, tc.motivo)
		}
	}
}

func TestDetalheTemTamanhoLimitado(t *testing.T) {
	ep := Classificar(errors.New(strings.Repeat("ação ", 200)))
	if len(ep.Detalhe) > tamanhoMaxDetalhe+len("…") {
		t.Errorf("detalhe com %d bytes", len(ep.Detalhe))
	}
	for _, r := range ep.Detalhe {
		if r == '�' {
			t.Fatal("cortou no meio de um caractere UTF-8")
		}
	}
}
