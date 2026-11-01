package aplicacao

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jotagm/client-go/internal/notificacao"
)

func cenarioComConvite(t *testing.T) cenario {
	t.Helper()
	c := novoCenario(t)
	c.repo.AdicionarTemplate(notificacao.Template{
		Nome: "convite-clube", Versao: 1, Canal: notificacao.CanalEmail,
		Assunto: "Convite", Corpo: `{{.convidado_por}} convidou você para "{{.clube}}": {{.link}}`,
		Variaveis: []string{"convidado_por", "clube", "link"},
	})
	return c
}

func pedidoConvite() Pedido {
	return Pedido{
		Origem:       "booklit",
		ChaveIdempot: "convite-1",
		Template:     "convite-clube",
		Destinatario: "novo@exemplo.com",
		Variaveis: map[string]string{
			"convidado_por": "Ana", "clube": "Leitores", "link": "https://booklit.bio/clubes",
		},
	}
}

func TestConviteSemExpiracaoNemPrioridadeSaiComoNormal(t *testing.T) {
	c := cenarioComConvite(t)

	n, nova, err := c.servico.Aceitar(context.Background(), pedidoConvite())
	if err != nil {
		t.Fatal(err)
	}
	if !nova || n.Prioridade != notificacao.PrioridadeNormal {
		t.Fatalf("nova=%v prioridade=%q", nova, n.Prioridade)
	}
	if n.ExpiraEm != nil {
		t.Errorf("convite não tem validade curta, veio %v", n.ExpiraEm)
	}
}

func TestOTPSemPrioridadeSaiComoAlta(t *testing.T) {
	c := novoCenario(t)
	p := pedidoOTP()
	p.Prioridade = ""

	n, _, err := c.servico.Aceitar(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if n.Prioridade != notificacao.PrioridadeAlta {
		t.Errorf("prioridade = %q, o tipo do OTP manda alta", n.Prioridade)
	}
}

func TestOTPSemExpiracaoEhRecusadoPeloTipo(t *testing.T) {
	c := novoCenario(t)
	p := pedidoOTP()
	p.ExpiraEm = nil

	_, _, err := c.servico.Aceitar(context.Background(), p)
	var ev *ErrValidacao
	if !errors.As(err, &ev) || !strings.Contains(ev.Campos["expira_em"], "obrigatório") {
		t.Fatalf("esperava ErrValidacao de expira_em obrigatório, veio %v", err)
	}
}

func TestConviteComLinkPerigosoEhRecusadoPeloTipo(t *testing.T) {
	c := cenarioComConvite(t)
	p := pedidoConvite()
	p.Variaveis["link"] = "javascript:alert(1)"

	_, _, err := c.servico.Aceitar(context.Background(), p)
	var ev *ErrValidacao
	if !errors.As(err, &ev) || ev.Campos["variaveis.link"] == "" {
		t.Fatalf("esperava ErrValidacao em variaveis.link, veio %v", err)
	}
	if len(c.publicador.Publicadas()) != 0 {
		t.Error("publicou um convite com link recusado")
	}
}
