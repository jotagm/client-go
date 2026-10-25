package ses

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jotagm/client-go/internal/adapter/memoria"
	"github.com/jotagm/client-go/internal/aplicacao"
	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

// Pipeline completo, sem AWS: Processador -> EntregadorEmail -> ses.Provedor -> servidor HTTP
// falso, com o repositório em memória. É a prova de que cada resposta do provedor leva a
// notificação ao estado certo e deixa a tentativa certa registrada.

type pipeline struct {
	repo *memoria.Repositorio
	proc *aplicacao.Processador
	n    notificacao.Notificacao
	logs *bytes.Buffer
	vars map[string]string
	fake *fakeSES
	ses  *Provedor
}

func novoPipeline(t *testing.T, timeout time.Duration, h func(w http.ResponseWriter, r *http.Request)) *pipeline {
	t.Helper()
	p := &pipeline{repo: memoria.NovoRepositorio(), logs: &bytes.Buffer{}, vars: map[string]string{"codigo": "482913"}}
	p.repo.AdicionarTemplate(notificacao.Template{
		Nome: "otp-login", Versao: 1, Canal: notificacao.CanalEmail, Assunto: "Seu código de acesso",
		Corpo: "Código {{.codigo}}, vale {{.minutos}} min.", Variaveis: []string{"codigo"},
	})
	p.fake = novoFake(t, h)
	p.ses = p.fake.provedor(timeout)
	log := slog.New(slog.NewTextHandler(p.logs, nil))
	entregador := aplicacao.NovoEntregadorEmail(p.repo, p.repo, p.ses, log)
	p.proc = aplicacao.NovoProcessador(p.repo, entregador, aplicacao.RetentativaPadrao(), log)

	agora := time.Now().UTC().Truncate(time.Microsecond)
	expira := agora.Add(10 * time.Minute)
	p.n = notificacao.Notificacao{
		ID: notificacao.NovoID(), Origem: "booklit", ChaveIdempot: notificacao.NovoID(),
		Canal: notificacao.CanalEmail, Prioridade: notificacao.PrioridadeAlta, Destinatario: "jota@exemplo.com",
		Template: "otp-login", VersaoTemplate: 1, NomesVariaveis: []string{"codigo"},
		Estado: notificacao.EstadoAceita, ExpiraEm: &expira, CriadoEm: agora, AtualizadoEm: agora,
	}
	if _, _, err := p.repo.Criar(context.Background(), p.n); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *pipeline) processar(t *testing.T, recebimento int) aplicacao.Decisao {
	t.Helper()
	n := p.n
	n.Variaveis = p.vars
	corpo, err := notificacao.CodificarEnvelope(n)
	if err != nil {
		t.Fatal(err)
	}
	return p.proc.Processar(context.Background(), porta.Mensagem{ID: "m", Recibo: "r", Corpo: corpo, Recebimento: recebimento})
}

func (p *pipeline) estado(t *testing.T) (notificacao.Estado, string) {
	t.Helper()
	n, err := p.repo.Buscar(context.Background(), p.n.ID)
	if err != nil {
		t.Fatal(err)
	}
	return n.Estado, n.Motivo
}

func (p *pipeline) tentativas(t *testing.T) []porta.Tentativa {
	t.Helper()
	ts, err := p.repo.Tentativas(context.Background(), p.n.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func (p *pipeline) naoVazou(t *testing.T) {
	t.Helper()
	if strings.Contains(p.logs.String(), "482913") {
		t.Errorf("o log vazou o código:\n%s", p.logs.String())
	}
	for _, tt := range p.tentativas(t) {
		if strings.Contains(tt.Resposta, "482913") {
			t.Errorf("a tabela de tentativas guardou o código: %+v", tt)
		}
	}
}

func respondeOK(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"MessageId":"0100018c-pipeline-001"}`)
}

// Cenário "200 sem entrega": o SES aceita e o e-mail nunca chega. Até o recibo (fase 6), a
// notificação fica em "enviada": nunca "entregue".
func TestPipeline200SemEntregaFicaEnviada(t *testing.T) {
	p := novoPipeline(t, time.Second, respondeOK)

	if d := p.processar(t, 1); !d.Confirmar {
		t.Fatalf("%+v", d)
	}
	if e, _ := p.estado(t); e != notificacao.EstadoEnviada {
		t.Errorf("estado = %s", e)
	}
	ts := p.tentativas(t)
	if len(ts) != 1 || ts[0].Resultado != porta.ResultadoSucesso || ts[0].IDNoProvedor != "0100018c-pipeline-001" || ts[0].Provedor != "ses" {
		t.Fatalf("tentativas = %+v", ts)
	}
	if !strings.Contains(string(p.fake.ultimo.Load().([]byte)), "Código 482913, vale 10 min.") {
		t.Errorf("o SES não recebeu o texto renderizado: %s", p.fake.ultimo.Load())
	}
	p.naoVazou(t)
}

// Cenários "lento", "fora do ar" e "resposta malformada": a notificação continua "aceita", a
// mensagem volta para a fila com espera e a tentativa fica registrada com o motivo.
func TestPipelineFalhasTemporariasVoltamParaAFila(t *testing.T) {
	casos := []struct {
		nome    string
		timeout time.Duration
		h       func(w http.ResponseWriter, r *http.Request)
		fechar  bool
		motivo  string
	}{
		{"provedor lento", 60 * time.Millisecond, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, false, "timeout"},
		{"provedor fora do ar", time.Second, respondeOK, true, ""}, // motivo depende do SO: rede
		{"resposta malformada", time.Second, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `<html>isso não é JSON`)
		}, false, "resposta_malformada"},
		{"throttling", time.Second, func(w http.ResponseWriter, _ *http.Request) {
			erroSES(w, 429, "TooManyRequestsException", "Rate exceeded")
		}, false, "throttling"},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			p := novoPipeline(t, tc.timeout, tc.h)
			if tc.fechar {
				p.fake.Close()
			}

			d := p.processar(t, 1)
			if d.Confirmar || d.Espera <= 0 || d.Espera > 2*time.Second {
				t.Fatalf("decisão = %+v (queria voltar para a fila em até 2s)", d)
			}
			if e, _ := p.estado(t); e != notificacao.EstadoAceita {
				t.Errorf("estado = %s, deveria continuar aceita", e)
			}
			ts := p.tentativas(t)
			if len(ts) != 1 || ts[0].Resultado != porta.ResultadoTemporario {
				t.Fatalf("tentativas = %+v", ts)
			}
			if tc.motivo != "" && ts[0].Motivo != tc.motivo {
				t.Errorf("motivo = %q, queria %q", ts[0].Motivo, tc.motivo)
			}
			p.naoVazou(t)
		})
	}
}

func TestPipelineRecuperaDepoisDoThrottling(t *testing.T) {
	var chamadas atomic.Int32
	p := novoPipeline(t, time.Second, func(w http.ResponseWriter, r *http.Request) {
		if chamadas.Add(1) == 1 {
			erroSES(w, 429, "TooManyRequestsException", "Rate exceeded")
			return
		}
		respondeOK(w, r)
	})

	if d := p.processar(t, 1); d.Confirmar {
		t.Fatalf("1ª: %+v", d)
	}
	if d := p.processar(t, 2); !d.Confirmar {
		t.Fatalf("2ª: %+v", d)
	}
	if e, _ := p.estado(t); e != notificacao.EstadoEnviada {
		t.Errorf("estado = %s", e)
	}
	ts := p.tentativas(t)
	if len(ts) != 2 || ts[0].Resultado != porta.ResultadoTemporario || ts[1].Resultado != porta.ResultadoSucesso {
		t.Fatalf("tentativas = %+v", ts)
	}
}

func TestPipelineRejeicaoEhPermanente(t *testing.T) {
	// o SES ecoa o texto do e-mail na mensagem de erro: o código não pode ir para a tabela
	p := novoPipeline(t, time.Second, func(w http.ResponseWriter, _ *http.Request) {
		erroSES(w, 400, "MessageRejected", "rejeitado: Código 482913, vale 10 min.")
	})

	if d := p.processar(t, 1); !d.Confirmar {
		t.Fatalf("%+v", d)
	}
	if e, motivo := p.estado(t); e != notificacao.EstadoFalhou || motivo != "erro_permanente" {
		t.Errorf("estado=%s motivo=%s", e, motivo)
	}
	ts := p.tentativas(t)
	if len(ts) != 1 || ts[0].Resultado != porta.ResultadoPermanente || ts[0].Motivo != "mensagem_rejeitada" || ts[0].StatusHTTP != 400 {
		t.Fatalf("tentativas = %+v", ts)
	}
	if !strings.Contains(ts[0].Resposta, "[omitido]") {
		t.Errorf("resposta = %q", ts[0].Resposta)
	}
	p.naoVazou(t)
}
