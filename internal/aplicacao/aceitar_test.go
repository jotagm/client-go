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

var agoraFixo = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

type cenario struct {
	servico    *Servico
	repo       *memoria.Repositorio
	publicador *memoria.Publicador
	logs       *bytes.Buffer
}

func novoCenario(t *testing.T) cenario {
	t.Helper()
	repo := memoria.NovoRepositorio()
	repo.AdicionarTemplate(notificacao.Template{
		Nome: "otp-login", Versao: 1, Canal: notificacao.CanalEmail,
		Assunto: "Seu código", Corpo: "{{.codigo}}", Variaveis: []string{"codigo"},
	})
	pub := memoria.NovoPublicador()
	logs := &bytes.Buffer{}
	s := NovoServico(repo, pub, slog.New(slog.NewTextHandler(logs, nil)))
	s.agora = func() time.Time { return agoraFixo }
	return cenario{servico: s, repo: repo, publicador: pub, logs: logs}
}

func pedidoOTP() Pedido {
	expira := agoraFixo.Add(10 * time.Minute)
	return Pedido{
		Origem:       "booklit",
		ChaveIdempot: "desafio-1",
		Template:     "otp-login",
		Destinatario: "jota@exemplo.com",
		Prioridade:   notificacao.PrioridadeAlta,
		Variaveis:    map[string]string{"codigo": "482913"},
		ExpiraEm:     &expira,
	}
}

func TestAceitarPublicaEGrava(t *testing.T) {
	c := novoCenario(t)

	n, nova, err := c.servico.Aceitar(context.Background(), pedidoOTP())
	if err != nil {
		t.Fatal(err)
	}
	if !nova || n.Estado != notificacao.EstadoAceita {
		t.Fatalf("nova=%v estado=%s", nova, n.Estado)
	}
	if n.Canal != notificacao.CanalEmail || n.VersaoTemplate != 1 {
		t.Errorf("canal/versão não vieram do template: %s v%d", n.Canal, n.VersaoTemplate)
	}

	publicadas := c.publicador.Publicadas()
	if len(publicadas) != 1 || publicadas[0].Variaveis["codigo"] != "482913" {
		t.Fatalf("o publicador precisa receber o código para entregar: %+v", publicadas)
	}

	salva, err := c.repo.Buscar(context.Background(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if salva.Variaveis != nil {
		t.Errorf("repositório guardou valores das variáveis: %v", salva.Variaveis)
	}
	if strings.Join(salva.NomesVariaveis, ",") != "codigo" {
		t.Errorf("NomesVariaveis = %v", salva.NomesVariaveis)
	}
	if n.Variaveis != nil {
		t.Error("Aceitar devolveu os valores das variáveis para fora")
	}
}

func TestAceitarRepetidoNaoDuplica(t *testing.T) {
	c := novoCenario(t)
	primeira, _, err := c.servico.Aceitar(context.Background(), pedidoOTP())
	if err != nil {
		t.Fatal(err)
	}

	segunda, nova, err := c.servico.Aceitar(context.Background(), pedidoOTP())
	if err != nil {
		t.Fatal(err)
	}
	if nova || segunda.ID != primeira.ID {
		t.Fatalf("repetição criou outra notificação: nova=%v %s != %s", nova, segunda.ID, primeira.ID)
	}
	if len(c.publicador.Publicadas()) != 1 {
		t.Errorf("repetição publicou de novo: %d publicações", len(c.publicador.Publicadas()))
	}
}

func TestAceitarMesmaChavePedidoDiferente(t *testing.T) {
	c := novoCenario(t)
	if _, _, err := c.servico.Aceitar(context.Background(), pedidoOTP()); err != nil {
		t.Fatal(err)
	}
	outro := pedidoOTP()
	outro.Destinatario = "outra@exemplo.com"

	_, _, err := c.servico.Aceitar(context.Background(), outro)
	if !errors.Is(err, ErrConflitoIdempotencia) {
		t.Fatalf("esperava ErrConflitoIdempotencia, veio %v", err)
	}
}

func TestAceitarCodigoDiferenteMesmaChaveEhRepeticao(t *testing.T) {
	// A impressão ignora os valores; um código diferente com a mesma chave é tratado
	// como repetição. No Booklit a chave é o desafioId, que tem um código só.
	c := novoCenario(t)
	if _, _, err := c.servico.Aceitar(context.Background(), pedidoOTP()); err != nil {
		t.Fatal(err)
	}
	outro := pedidoOTP()
	outro.Variaveis = map[string]string{"codigo": "000000"}
	if _, nova, err := c.servico.Aceitar(context.Background(), outro); err != nil || nova {
		t.Fatalf("nova=%v err=%v", nova, err)
	}
}

func TestAceitarValidacao(t *testing.T) {
	casos := map[string]struct {
		mudar func(*Pedido)
		campo string
	}{
		"sem chave":           {func(p *Pedido) { p.ChaveIdempot = "" }, "chave_idempotencia"},
		"chave longa":         {func(p *Pedido) { p.ChaveIdempot = strings.Repeat("x", 201) }, "chave_idempotencia"},
		"sem destinatário":    {func(p *Pedido) { p.Destinatario = "" }, "destinatario"},
		"e-mail inválido":     {func(p *Pedido) { p.Destinatario = "nao-e-email" }, "destinatario"},
		"e-mail com nome":     {func(p *Pedido) { p.Destinatario = "Jota <jota@exemplo.com>" }, "destinatario"},
		"prioridade estranha": {func(p *Pedido) { p.Prioridade = "urgentissima" }, "prioridade"},
		"já expirado": {func(p *Pedido) {
			passado := agoraFixo.Add(-time.Second)
			p.ExpiraEm = &passado
		}, "expira_em"},
		"variável faltando": {func(p *Pedido) { p.Variaveis = map[string]string{} }, "variaveis"},
		"variável a mais": {func(p *Pedido) {
			p.Variaveis = map[string]string{"codigo": "1", "senha": "x"}
		}, "variaveis_extras"},
		"valor gigante": {func(p *Pedido) {
			p.Variaveis = map[string]string{"codigo": strings.Repeat("9", 2001)}
		}, "variaveis.codigo"},
		"template inexistente": {func(p *Pedido) { p.Template = "nao-existe" }, "template"},
		"versão inexistente":   {func(p *Pedido) { p.VersaoTemplate = 7 }, "template"},
	}
	for nome, caso := range casos {
		t.Run(nome, func(t *testing.T) {
			c := novoCenario(t)
			p := pedidoOTP()
			caso.mudar(&p)

			_, _, err := c.servico.Aceitar(context.Background(), p)
			var ev *ErrValidacao
			if !errors.As(err, &ev) {
				t.Fatalf("esperava ErrValidacao, veio %v", err)
			}
			if _, ok := ev.Campos[caso.campo]; !ok {
				t.Errorf("esperava erro no campo %q, veio %v", caso.campo, ev.Campos)
			}
			if len(c.publicador.Publicadas()) != 0 {
				t.Error("pedido inválido foi publicado")
			}
		})
	}
}

func TestAceitarSuprimido(t *testing.T) {
	c := novoCenario(t)
	c.repo.Suprimir("jota@exemplo.com", notificacao.CanalEmail, "bounce")

	n, nova, err := c.servico.Aceitar(context.Background(), pedidoOTP())
	if err != nil {
		t.Fatal(err)
	}
	if !nova || n.Estado != notificacao.EstadoSuprimida || n.Motivo != "bounce" {
		t.Fatalf("nova=%v estado=%s motivo=%s", nova, n.Estado, n.Motivo)
	}
	if len(c.publicador.Publicadas()) != 0 {
		t.Error("notificação suprimida foi publicada")
	}
}

func TestAceitarFalhaNaPublicacao(t *testing.T) {
	c := novoCenario(t)
	c.publicador.Falhar(errors.New("SNS fora do ar"))

	_, _, err := c.servico.Aceitar(context.Background(), pedidoOTP())
	if !errors.Is(err, ErrPublicacao) {
		t.Fatalf("esperava ErrPublicacao, veio %v", err)
	}

	// a repetição devolve o registro como falhou, sem publicar de novo
	c.publicador.Falhar(nil)
	n, nova, err := c.servico.Aceitar(context.Background(), pedidoOTP())
	if err != nil {
		t.Fatal(err)
	}
	if nova || n.Estado != notificacao.EstadoFalhou || n.Motivo != "publicacao" {
		t.Fatalf("nova=%v estado=%s motivo=%s", nova, n.Estado, n.Motivo)
	}
}

func TestAceitarVersaoMaisRecente(t *testing.T) {
	c := novoCenario(t)
	c.repo.AdicionarTemplate(notificacao.Template{
		Nome: "otp-login", Versao: 2, Canal: notificacao.CanalEmail, Variaveis: []string{"codigo"},
	})
	n, _, err := c.servico.Aceitar(context.Background(), pedidoOTP())
	if err != nil {
		t.Fatal(err)
	}
	if n.VersaoTemplate != 2 {
		t.Errorf("versão 0 deveria resolver para a mais recente, veio v%d", n.VersaoTemplate)
	}

	// v3 publicada depois: a repetição com versao=0 ainda é a mesma notificação
	c.repo.AdicionarTemplate(notificacao.Template{
		Nome: "otp-login", Versao: 3, Canal: notificacao.CanalEmail, Variaveis: []string{"codigo"},
	})
	repetida, nova, err := c.servico.Aceitar(context.Background(), pedidoOTP())
	if err != nil || nova || repetida.ID != n.ID {
		t.Fatalf("repetição após nova versão: nova=%v err=%v", nova, err)
	}
}

func TestBuscarDeOutraOrigem(t *testing.T) {
	c := novoCenario(t)
	n, _, err := c.servico.Aceitar(context.Background(), pedidoOTP())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.servico.Buscar(context.Background(), "uptime", n.ID); !errors.Is(err, porta.ErrNaoEncontrado) {
		t.Fatalf("outra origem enxergou a notificação: %v", err)
	}
	if _, err := c.servico.Buscar(context.Background(), "booklit", n.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCodigoNaoApareceNoLog(t *testing.T) {
	c := novoCenario(t)
	if _, _, err := c.servico.Aceitar(context.Background(), pedidoOTP()); err != nil {
		t.Fatal(err)
	}
	c.publicador.Falhar(errors.New("SNS fora do ar"))
	p := pedidoOTP()
	p.ChaveIdempot = "desafio-2"
	_, _, _ = c.servico.Aceitar(context.Background(), p)

	if strings.Contains(c.logs.String(), "482913") {
		t.Fatalf("o código apareceu no log:\n%s", c.logs.String())
	}
}
