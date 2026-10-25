package notificacao

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var atualizar = flag.Bool("atualizar", false, "reescreve os golden files em testdata/")

var instante = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// templatesDoBanco lê db/002_templates_otp.sql, a fonte de verdade dos textos, em vez de
// repetir os textos aqui: se alguém mexer no SQL, o golden file acusa.
func templatesDoBanco(t *testing.T) []Template {
	t.Helper()
	bruto, err := os.ReadFile(filepath.Join("..", "..", "db", "002_templates_otp.sql"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?s)\('([^']+)', (\d+), '(\w+)', '([^']*)',\s*\$\$(.*?)\$\$, ARRAY\[([^\]]*)\]\)`)
	var out []Template
	for _, m := range re.FindAllStringSubmatch(string(bruto), -1) {
		versao, _ := strconv.Atoi(m[2])
		var vars []string
		for _, v := range strings.Split(m[6], ",") {
			if v = strings.Trim(strings.TrimSpace(v), "'"); v != "" {
				vars = append(vars, v)
			}
		}
		out = append(out, Template{Nome: m[1], Versao: versao, Canal: Canal(m[3]), Assunto: m[4], Corpo: m[5], Variaveis: vars})
	}
	if len(out) != 3 {
		t.Fatalf("esperava 3 templates em 002_templates_otp.sql, achei %d", len(out))
	}
	return out
}

func TestRenderizarGolden(t *testing.T) {
	expira := instante.Add(10 * time.Minute)
	for _, tpl := range templatesDoBanco(t) {
		t.Run(tpl.Nome, func(t *testing.T) {
			r, err := tpl.Renderizar(map[string]string{"codigo": "482913"}, &expira, instante)
			if err != nil {
				t.Fatal(err)
			}
			obtido := "Assunto: " + r.Assunto + "\n---\n" + r.Corpo
			arquivo := filepath.Join("testdata", tpl.Nome+".golden")
			if *atualizar {
				if err := os.WriteFile(arquivo, []byte(obtido), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			esperado, err := os.ReadFile(arquivo)
			if err != nil {
				t.Fatalf("%v (rode com -atualizar para criar)", err)
			}
			if obtido != string(esperado) {
				t.Errorf("diferente do golden %s\n--- obtido ---\n%s\n--- esperado ---\n%s", arquivo, obtido, esperado)
			}
		})
	}
}

// O assunto aparece na tela bloqueada do celular: nunca pode trazer o código.
func TestAssuntoDosTemplatesNaoTemOCodigo(t *testing.T) {
	expira := instante.Add(10 * time.Minute)
	for _, tpl := range templatesDoBanco(t) {
		r, err := tpl.Renderizar(map[string]string{"codigo": "482913"}, &expira, instante)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(r.Assunto, "482913") {
			t.Errorf("%s: o assunto contém o código: %q", tpl.Nome, r.Assunto)
		}
	}
}

func modelo() Template {
	return Template{
		Nome: "t", Versao: 1, Canal: CanalEmail, Assunto: "Assunto",
		Corpo: "Código {{.codigo}}, vale {{.minutos}} min.", Variaveis: []string{"codigo"},
	}
}

func TestRenderizarMinutos(t *testing.T) {
	casos := []struct {
		restante time.Duration
		quer     string
	}{
		{10 * time.Minute, "10"},
		{10*time.Minute - 2*time.Second, "10"}, // enviado 2s depois de criado: ainda "10"
		{9*time.Minute + time.Second, "10"},
		{9 * time.Minute, "9"},
		{30 * time.Second, "1"},
		{time.Nanosecond, "1"},
	}
	for _, c := range casos {
		expira := instante.Add(c.restante)
		r, err := modelo().Renderizar(map[string]string{"codigo": "1"}, &expira, instante)
		if err != nil {
			t.Fatalf("%v: %v", c.restante, err)
		}
		if !strings.Contains(r.Corpo, "vale "+c.quer+" min") {
			t.Errorf("restante %v: %q (queria %s)", c.restante, r.Corpo, c.quer)
		}
	}
}

func TestRenderizarErros(t *testing.T) {
	expira := instante.Add(10 * time.Minute)
	passado := instante.Add(-time.Second)
	casos := []struct {
		nome      string
		tpl       func(Template) Template
		variaveis map[string]string
		expira    *time.Time
		erro      error
		contem    string // trecho esperado na mensagem
	}{
		{"campo vazio", nil, map[string]string{"codigo": ""}, &expira, ErrDadosInvalidos, `"codigo" está vazia`},
		{"só espaços", nil, map[string]string{"codigo": "   "}, &expira, ErrDadosInvalidos, `"codigo" está vazia`},
		{"variável faltando", nil, map[string]string{}, &expira, ErrDadosInvalidos, `falta a variável "codigo"`},
		{"minutos enviado de fora", nil, map[string]string{"codigo": "1", "minutos": "9999"}, &expira, ErrDadosInvalidos, "calculada"},
		{"usa minutos sem expira_em", nil, map[string]string{"codigo": "1"}, nil, ErrDadosInvalidos, "expira_em"},
		{"já expirou", nil, map[string]string{"codigo": "1"}, &passado, ErrDadosInvalidos, "expirou"},
		{"template usa variável não declarada", func(t Template) Template { t.Corpo = "{{.codigo}} {{.nome}} {{.minutos}}"; return t },
			map[string]string{"codigo": "1"}, &expira, ErrTemplateInvalido, `"nome"`},
		{"template não compila", func(t Template) Template { t.Corpo = "{{.codigo"; return t },
			map[string]string{"codigo": "1"}, &expira, ErrTemplateInvalido, "não compila"},
		{"assunto com quebra de linha", func(t Template) Template { t.Assunto = "{{.codigo}}"; return t },
			map[string]string{"codigo": "1\r\nBcc: alguem@exemplo.com"}, &expira, ErrDadosInvalidos, "quebra de linha"},
		{"e-mail sem assunto", func(t Template) Template { t.Assunto = ""; return t },
			map[string]string{"codigo": "1"}, &expira, ErrTemplateInvalido, "sem assunto"},
		{"corpo vazio", func(t Template) Template { t.Corpo = "  "; return t },
			map[string]string{"codigo": "1"}, &expira, ErrTemplateInvalido, "corpo vazio"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			tpl := modelo()
			if c.tpl != nil {
				tpl = c.tpl(tpl)
			}
			_, err := tpl.Renderizar(c.variaveis, c.expira, instante)
			if !errors.Is(err, c.erro) {
				t.Fatalf("erro = %v, queria %v", err, c.erro)
			}
			if !strings.Contains(err.Error(), c.contem) {
				t.Errorf("mensagem %q não contém %q", err, c.contem)
			}
			// o erro pode citar nomes de variáveis, nunca valores
			for _, valor := range c.variaveis {
				if len(valor) > 3 && strings.Contains(err.Error(), valor) {
					t.Errorf("a mensagem de erro vazou um valor: %q", err)
				}
			}
		})
	}
}

// O valor entra no texto como texto: não é reinterpretado como template nem escapado
// como HTML (o corpo é texto puro).
func TestRenderizarValorEhLiteral(t *testing.T) {
	expira := instante.Add(10 * time.Minute)
	r, err := modelo().Renderizar(map[string]string{"codigo": "{{.minutos}}<b>&"}, &expira, instante)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Corpo, "Código {{.minutos}}<b>&,") {
		t.Errorf("corpo = %q", r.Corpo)
	}
}

func TestRenderizarNaoMudaOsDadosDeEntrada(t *testing.T) {
	expira := instante.Add(10 * time.Minute)
	entrada := map[string]string{"codigo": "1"}
	if _, err := modelo().Renderizar(entrada, &expira, instante); err != nil {
		t.Fatal(err)
	}
	if len(entrada) != 1 {
		t.Errorf("a entrada ganhou chaves: %v", entrada)
	}
}
