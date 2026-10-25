package aplicacao

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jotagm/client-go/internal/adapter/memoria"
	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

type cenarioEntrega struct {
	repo      *memoria.Repositorio
	provedor  *memoria.Provedor
	entregue  *EntregadorEmail
	logs      *bytes.Buffer
	n         notificacao.Notificacao
	variaveis map[string]string
}

func novoCenarioEntrega(t *testing.T, erros ...error) *cenarioEntrega {
	t.Helper()
	repo := memoria.NovoRepositorio()
	repo.AdicionarTemplate(notificacao.Template{
		Nome: "otp-login", Versao: 1, Canal: notificacao.CanalEmail,
		Assunto: "Seu código de acesso", Corpo: "Código {{.codigo}}, vale {{.minutos}} min.",
		Variaveis: []string{"codigo"},
	})
	c := &cenarioEntrega{
		repo: repo, provedor: memoria.NovoProvedor(erros...), logs: &bytes.Buffer{},
		variaveis: map[string]string{"codigo": "482913"},
	}
	c.entregue = NovoEntregadorEmail(repo, repo, c.provedor, slog.New(slog.NewTextHandler(c.logs, nil)))
	c.entregue.agora = func() time.Time { return agoraFixo }

	expira := agoraFixo.Add(10 * time.Minute)
	c.n = notificacao.Notificacao{
		ID: notificacao.NovoID(), Origem: "booklit", ChaveIdempot: notificacao.NovoID(),
		Canal: notificacao.CanalEmail, Prioridade: notificacao.PrioridadeAlta,
		Destinatario: "jota@exemplo.com", Template: "otp-login", VersaoTemplate: 1,
		NomesVariaveis: []string{"codigo"}, Estado: notificacao.EstadoAceita,
		ExpiraEm: &expira, CriadoEm: agoraFixo, AtualizadoEm: agoraFixo,
	}
	if _, _, err := repo.Criar(context.Background(), c.n); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *cenarioEntrega) tentativas(t *testing.T) []porta.Tentativa {
	t.Helper()
	ts, err := c.repo.Tentativas(context.Background(), c.n.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestEntregarSucesso(t *testing.T) {
	c := novoCenarioEntrega(t)

	if err := c.entregue.Entregar(context.Background(), c.n, c.variaveis); err != nil {
		t.Fatal(err)
	}

	envios := c.provedor.Envios()
	if len(envios) != 1 {
		t.Fatalf("envios = %d", len(envios))
	}
	e := envios[0]
	if e.Destinatario != "jota@exemplo.com" || e.Assunto != "Seu código de acesso" ||
		e.Corpo != "Código 482913, vale 10 min." || e.NotificacaoID != c.n.ID {
		t.Errorf("envio = %+v", e)
	}

	ts := c.tentativas(t)
	if len(ts) != 1 || ts[0].Resultado != porta.ResultadoSucesso || ts[0].Numero != 1 ||
		ts[0].Provedor != "memoria" || ts[0].IDNoProvedor == "" {
		t.Fatalf("tentativas = %+v", ts)
	}
}

func TestEntregarClassificaOErro(t *testing.T) {
	casos := []struct {
		nome      string
		erro      error
		classe    error
		resultado string
		motivo    string
		status    int
	}{
		{"temporário tipado", &porta.ErroProvedor{Classe: porta.ErrTemporario, Motivo: "throttling", StatusHTTP: 429},
			porta.ErrTemporario, porta.ResultadoTemporario, "throttling", 429},
		{"permanente tipado", &porta.ErroProvedor{Classe: porta.ErrPermanente, Motivo: "mensagem_rejeitada", StatusHTTP: 400},
			porta.ErrPermanente, porta.ResultadoPermanente, "mensagem_rejeitada", 400},
		{"erro cru é temporário", errors.New("connection reset"),
			porta.ErrTemporario, porta.ResultadoTemporario, "desconhecido", 0},
		{"classe estranha é temporária", &porta.ErroProvedor{Classe: errors.New("?"), Motivo: "x"},
			porta.ErrTemporario, porta.ResultadoTemporario, "x", 0},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			c := novoCenarioEntrega(t, tc.erro)
			err := c.entregue.Entregar(context.Background(), c.n, c.variaveis)
			if !errors.Is(err, tc.classe) {
				t.Fatalf("err = %v, queria %v", err, tc.classe)
			}
			ts := c.tentativas(t)
			if len(ts) != 1 || ts[0].Resultado != tc.resultado || ts[0].Motivo != tc.motivo || ts[0].StatusHTTP != tc.status {
				t.Fatalf("tentativas = %+v", ts)
			}
		})
	}
}

func TestEntregarNumeraAsTentativas(t *testing.T) {
	c := novoCenarioEntrega(t,
		&porta.ErroProvedor{Classe: porta.ErrTemporario, Motivo: "throttling"},
		&porta.ErroProvedor{Classe: porta.ErrTemporario, Motivo: "timeout"})
	for range 3 {
		_ = c.entregue.Entregar(context.Background(), c.n, c.variaveis)
	}
	ts := c.tentativas(t)
	if len(ts) != 3 {
		t.Fatalf("tentativas = %d", len(ts))
	}
	for i, want := range []string{porta.ResultadoTemporario, porta.ResultadoTemporario, porta.ResultadoSucesso} {
		if ts[i].Numero != i+1 || ts[i].Resultado != want {
			t.Errorf("tentativa %d = %+v", i+1, ts[i])
		}
	}
}

// Se o provedor repetir o corpo do e-mail no erro, o código não pode ir para a tabela,
// para o log, nem para o erro que sobe até o Processador.
func TestEntregarNaoVazaOCodigoQuandoOProvedorOEcoa(t *testing.T) {
	eco := &porta.ErroProvedor{Classe: porta.ErrPermanente, Motivo: "mensagem_rejeitada", StatusHTTP: 400,
		Detalhe: "rejeitado: Código 482913, vale 10 min."}
	c := novoCenarioEntrega(t, eco)

	err := c.entregue.Entregar(context.Background(), c.n, c.variaveis)
	if err == nil || strings.Contains(err.Error(), "482913") {
		t.Fatalf("o erro vazou o código: %v", err)
	}
	for _, tt := range c.tentativas(t) {
		if strings.Contains(tt.Resposta, "482913") || !strings.Contains(tt.Resposta, "[omitido]") {
			t.Errorf("resposta gravada = %q", tt.Resposta)
		}
	}
	if strings.Contains(c.logs.String(), "482913") {
		t.Errorf("log vazou o código:\n%s", c.logs.String())
	}
	if eco.Detalhe != "rejeitado: Código 482913, vale 10 min." {
		t.Error("o erro original do provedor foi modificado")
	}
}

func TestEntregarNaoChamaOProvedorSeNaoRenderiza(t *testing.T) {
	casos := []struct {
		nome      string
		variaveis map[string]string
		mudar     func(*cenarioEntrega)
		motivo    string
	}{
		{"código vazio", map[string]string{"codigo": ""}, nil, "dados_invalidos"},
		{"sem a variável", map[string]string{}, nil, "dados_invalidos"},
		{"template inexistente", map[string]string{"codigo": "482913"},
			func(c *cenarioEntrega) { c.n.Template = "nao-existe" }, "template_inexistente"},
		{"canal sem entregador", map[string]string{"codigo": "482913"},
			func(c *cenarioEntrega) { c.n.Canal = notificacao.CanalSMS }, "canal_nao_suportado"},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			c := novoCenarioEntrega(t)
			if tc.mudar != nil {
				tc.mudar(c)
			}
			err := c.entregue.Entregar(context.Background(), c.n, tc.variaveis)
			if !errors.Is(err, porta.ErrPermanente) {
				t.Fatalf("err = %v", err)
			}
			if len(c.provedor.Envios()) != 0 {
				t.Error("chamou o provedor com um e-mail que não renderiza")
			}
			ts := c.tentativas(t)
			if len(ts) != 1 || ts[0].Motivo != tc.motivo || ts[0].Resultado != porta.ResultadoPermanente {
				t.Errorf("tentativas = %+v", ts)
			}
		})
	}
}

type registroQueFalha struct{ porta.RegistroTentativas }

func (registroQueFalha) RegistrarTentativa(context.Context, porta.Tentativa) error {
	return errors.New("banco fora do ar")
}

// Perder uma linha de histórico não pode virar erro de entrega: o e-mail já saiu, e
// reportar falha faria a fila mandar o mesmo código outra vez.
func TestEntregarSucessoMesmoSeORegistroFalha(t *testing.T) {
	c := novoCenarioEntrega(t)
	c.entregue.tentativas = registroQueFalha{}

	if err := c.entregue.Entregar(context.Background(), c.n, c.variaveis); err != nil {
		t.Fatalf("falha de registro derrubou a entrega: %v", err)
	}
	if len(c.provedor.Envios()) != 1 {
		t.Error("não enviou")
	}
	if !strings.Contains(c.logs.String(), "não foi possível registrar a tentativa") {
		t.Errorf("a falha de registro precisa aparecer no log:\n%s", c.logs.String())
	}
}

func TestEntregarRespeitaOTempoDoProvedor(t *testing.T) {
	c := novoCenarioEntrega(t)
	c.provedor.Atraso = time.Hour
	ctx, cancelar := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelar()

	err := c.entregue.Entregar(ctx, c.n, c.variaveis)
	if !errors.Is(err, porta.ErrTemporario) {
		t.Fatalf("err = %v", err)
	}
	// o contexto da mensagem já venceu, mas o registro da tentativa ainda é gravado
	if ts := c.tentativas(t); len(ts) != 1 || ts[0].Motivo != "timeout" {
		t.Errorf("tentativas = %+v", ts)
	}
}

// --- o caminho completo: Processador + EntregadorEmail + repositório ---

func novoProcessadorReal(c *cenarioEntrega) *Processador {
	p := NovoProcessador(c.repo, c.entregue, retentativaFixa(), slog.New(slog.NewTextHandler(c.logs, nil)))
	p.agora = func() time.Time { return agoraFixo }
	return p
}

func (c *cenarioEntrega) mensagem(t *testing.T, recebimento int) porta.Mensagem {
	t.Helper()
	n := c.n
	n.Variaveis = c.variaveis
	corpo, err := notificacao.CodificarEnvelope(n)
	if err != nil {
		t.Fatal(err)
	}
	return porta.Mensagem{ID: "m1", Recibo: "r1", Corpo: corpo, Recebimento: recebimento}
}

func (c *cenarioEntrega) estado(t *testing.T) (notificacao.Estado, string) {
	t.Helper()
	n, err := c.repo.Buscar(context.Background(), c.n.ID)
	if err != nil {
		t.Fatal(err)
	}
	return n.Estado, n.Motivo
}

// O provedor respondeu 200 e o e-mail nunca chegou: a notificação para em "enviada".
// "entregue" só vem do recibo (fase 6), nunca da resposta do envio.
func TestPipelineProvedorAceitaMasNaoEntregaFicaEnviada(t *testing.T) {
	c := novoCenarioEntrega(t)
	d := novoProcessadorReal(c).Processar(context.Background(), c.mensagem(t, 1))

	if !d.Confirmar {
		t.Fatalf("%+v", d)
	}
	if e, _ := c.estado(t); e != notificacao.EstadoEnviada {
		t.Errorf("estado = %s, queria enviada", e)
	}
	if ts := c.tentativas(t); len(ts) != 1 || ts[0].Resultado != porta.ResultadoSucesso {
		t.Errorf("tentativas = %+v", ts)
	}
}

func TestPipelineTemporarioDepoisSucesso(t *testing.T) {
	c := novoCenarioEntrega(t, &porta.ErroProvedor{Classe: porta.ErrTemporario, Motivo: "throttling", StatusHTTP: 429})
	p := novoProcessadorReal(c)

	d := p.Processar(context.Background(), c.mensagem(t, 1))
	if d.Confirmar || d.Espera != 2*time.Second {
		t.Fatalf("1ª tentativa: %+v", d)
	}
	if e, _ := c.estado(t); e != notificacao.EstadoAceita {
		t.Errorf("estado após erro temporário = %s", e)
	}

	d = p.Processar(context.Background(), c.mensagem(t, 2))
	if !d.Confirmar {
		t.Fatalf("2ª tentativa: %+v", d)
	}
	if e, _ := c.estado(t); e != notificacao.EstadoEnviada {
		t.Errorf("estado = %s", e)
	}
	ts := c.tentativas(t)
	if len(ts) != 2 || ts[0].Resultado != porta.ResultadoTemporario || ts[1].Resultado != porta.ResultadoSucesso {
		t.Errorf("tentativas = %+v", ts)
	}
}

func TestPipelinePermanenteFalha(t *testing.T) {
	c := novoCenarioEntrega(t, &porta.ErroProvedor{Classe: porta.ErrPermanente, Motivo: "mensagem_rejeitada", StatusHTTP: 400})
	d := novoProcessadorReal(c).Processar(context.Background(), c.mensagem(t, 1))

	if !d.Confirmar {
		t.Fatalf("%+v", d)
	}
	if e, motivo := c.estado(t); e != notificacao.EstadoFalhou || motivo != "erro_permanente" {
		t.Errorf("estado=%s motivo=%s", e, motivo)
	}
}

func TestPipelineExpiradoNaoChegaAoProvedor(t *testing.T) {
	c := novoCenarioEntrega(t)
	p := novoProcessadorReal(c)
	p.agora = func() time.Time { return agoraFixo.Add(11 * time.Minute) }

	d := p.Processar(context.Background(), c.mensagem(t, 1))
	if !d.Confirmar {
		t.Fatalf("%+v", d)
	}
	if e, motivo := c.estado(t); e != notificacao.EstadoExpirada || motivo != "expirou_antes_do_envio" {
		t.Errorf("estado=%s motivo=%s", e, motivo)
	}
	if len(c.provedor.Envios()) != 0 || len(c.tentativas(t)) != 0 {
		t.Error("um código vencido chegou ao provedor")
	}
}

func TestPipelineNaoVazaOCodigo(t *testing.T) {
	c := novoCenarioEntrega(t, &porta.ErroProvedor{Classe: porta.ErrTemporario, Motivo: "x", Detalhe: "eco 482913"})
	novoProcessadorReal(c).Processar(context.Background(), c.mensagem(t, 1))
	if strings.Contains(c.logs.String(), "482913") {
		t.Errorf("log vazou o código:\n%s", c.logs.String())
	}
}
