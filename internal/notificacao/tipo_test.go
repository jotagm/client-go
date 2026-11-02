package notificacao

import "testing"

func TestTipoDeEscolhePeloNomeDoTemplate(t *testing.T) {
	casos := map[string]string{
		"otp-login":           "codigo-por-email",
		"otp-verificar-email": "codigo-por-email",
		"otp-redefinir-pin":   "codigo-por-email",
		"convite-clube":       "convite-clube",
		"lembrete-votacao":    "lembrete-votacao",
		"qualquer-outro":      "generico",
		"":                    "generico",
	}
	for template, esperado := range casos {
		if got := TipoDe(template).Nome(); got != esperado {
			t.Errorf("TipoDe(%q) = %q, esperava %q", template, got, esperado)
		}
	}
}

func TestPrioridadeEValidadePorTipo(t *testing.T) {
	if p := TipoDe("otp-login").PrioridadePadrao(); p != PrioridadeAlta {
		t.Errorf("OTP deveria ser prioridade alta, veio %q", p)
	}
	if !TipoDe("otp-login").ExigeExpiracao() {
		t.Error("OTP deveria exigir expira_em")
	}
	for _, template := range []string{"convite-clube", "lembrete-votacao", "outro"} {
		if p := TipoDe(template).PrioridadePadrao(); p != PrioridadeNormal {
			t.Errorf("%s: prioridade = %q", template, p)
		}
		if TipoDe(template).ExigeExpiracao() {
			t.Errorf("%s não deveria exigir expira_em", template)
		}
	}
}

func TestValidarCodigo(t *testing.T) {
	tipo := TipoDe("otp-login")
	if c := tipo.Validar(map[string]string{"codigo": "482913"}); c != nil {
		t.Errorf("código bom recusado: %v", c)
	}
	for _, ruim := range []string{"482 913", "4829\n13", " 482913"} {
		if c := tipo.Validar(map[string]string{"codigo": ruim}); c["variaveis.codigo"] == "" {
			t.Errorf("código %q deveria ser recusado", ruim)
		}
	}
}

func TestValidarConvite(t *testing.T) {
	tipo := TipoDe("convite-clube")
	bom := map[string]string{"convidado_por": "Ana", "clube": "Leitores", "link": "https://booklit.bio/clubes"}
	if c := tipo.Validar(bom); c != nil {
		t.Fatalf("convite bom recusado: %v", c)
	}
	ruins := map[string]map[string]string{
		"variaveis.link":          {"convidado_por": "Ana", "clube": "Leitores", "link": "javascript:alert(1)"},
		"variaveis.clube":         {"convidado_por": "Ana", "clube": "a\nb", "link": "https://booklit.bio/clubes"},
		"variaveis.convidado_por": {"convidado_por": "a\rb", "clube": "Leitores", "link": "https://booklit.bio/clubes"},
	}
	for campo, variaveis := range ruins {
		if c := tipo.Validar(variaveis); c[campo] == "" {
			t.Errorf("esperava recusar %s, veio %v", campo, c)
		}
	}
}

func TestValidarLembreteDeVotacao(t *testing.T) {
	tipo := TipoDe("lembrete-votacao")
	bom := map[string]string{"clube": "Leitores", "prazo": "quarta-feira, 5 de novembro, às 20:00",
		"link": "https://booklit.bio/votacoes/42"}
	if c := tipo.Validar(bom); c != nil {
		t.Fatalf("lembrete bom recusado: %v", c)
	}
	ruins := map[string]map[string]string{
		"variaveis.link":  {"clube": "Leitores", "prazo": "quarta", "link": "javascript:alert(1)"},
		"variaveis.clube": {"clube": "a\nb", "prazo": "quarta", "link": "https://booklit.bio/votacoes/42"},
		"variaveis.prazo": {"clube": "Leitores", "prazo": "quarta\r\nBcc: x", "link": "https://booklit.bio/votacoes/42"},
	}
	for campo, variaveis := range ruins {
		if c := tipo.Validar(variaveis); c[campo] == "" {
			t.Errorf("esperava recusar %s, veio %v", campo, c)
		}
	}
}

func TestGenericoNaoAcrescentaRegra(t *testing.T) {
	if c := TipoDe("novo").Validar(map[string]string{"x": "y z\n"}); c != nil {
		t.Errorf("genérico não valida nada, veio %v", c)
	}
}
