package notificacao

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTransicoes(t *testing.T) {
	casos := []struct {
		de, para Estado
		ok       bool
	}{
		{EstadoAceita, EstadoEnviada, true},
		{EstadoAceita, EstadoExpirada, true},
		{EstadoEnviada, EstadoEntregue, true},
		{EstadoEnviada, EstadoIncerta, true},
		{EstadoIncerta, EstadoEntregue, true},
		// 200 do provedor não pula direto para entregue
		{EstadoAceita, EstadoEntregue, false},
		{EstadoEntregue, EstadoDevolvida, false},
		{EstadoFalhou, EstadoEnviada, false},
	}
	for _, c := range casos {
		err := c.de.PodeIrPara(c.para)
		if c.ok && err != nil {
			t.Errorf("%s -> %s: esperava permitido, veio %v", c.de, c.para, err)
		}
		if !c.ok && !errors.Is(err, ErrTransicaoInvalida) {
			t.Errorf("%s -> %s: esperava ErrTransicaoInvalida, veio %v", c.de, c.para, err)
		}
	}
}

func TestFinal(t *testing.T) {
	for _, e := range []Estado{EstadoEntregue, EstadoDevolvida, EstadoFalhou, EstadoSuprimida, EstadoExpirada} {
		if !e.Final() {
			t.Errorf("%s deveria ser final", e)
		}
	}
	for _, e := range []Estado{EstadoAceita, EstadoEnviada, EstadoIncerta} {
		if e.Final() {
			t.Errorf("%s não deveria ser final", e)
		}
	}
}

func TestExpirou(t *testing.T) {
	agora := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	vence := agora.Add(10 * time.Minute)
	n := Notificacao{ExpiraEm: &vence}

	if n.Expirou(agora) {
		t.Error("não deveria ter expirado antes do prazo")
	}
	if !n.Expirou(vence) {
		t.Error("deveria expirar no instante exato do prazo")
	}
	if (Notificacao{}).Expirou(agora) {
		t.Error("sem ExpiraEm nunca expira")
	}
}

func TestStringNaoVazaVariaveis(t *testing.T) {
	n := Notificacao{ID: "1", Template: "otp-login", Variaveis: map[string]string{"codigo": "482913"}}
	if strings.Contains(n.String(), "482913") {
		t.Fatalf("String() vazou o código: %s", n)
	}
}
