package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jotagm/client-go/internal/adapter/memoria"
	"github.com/jotagm/client-go/internal/aplicacao"
	"github.com/jotagm/client-go/internal/notificacao"
)

const (
	tokenBooklit = "booklit-token-de-teste-com-32-caracteres!"
	tokenUptime  = "uptime-token-de-teste-com-32-caracteres!!"
)

func novoServidor(t *testing.T) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	repo := memoria.NovoRepositorio()
	repo.AdicionarTemplate(notificacao.Template{
		Nome: "otp-login", Versao: 1, Canal: notificacao.CanalEmail,
		Assunto: "Seu código", Corpo: "Código {{.codigo}}, vale {{.minutos}} min.", Variaveis: []string{"codigo"},
	})
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	tokens := Tokens{tokenBooklit: "booklit", tokenUptime: "uptime"}
	srv := httptest.NewServer(Rotas(aplicacao.NovoServico(repo, memoria.NovoPublicador(), log), tokens, log))
	t.Cleanup(srv.Close)
	return srv, logs
}

func requisitar(t *testing.T, metodo, url, token, chave, corpo string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(metodo, url, strings.NewReader(corpo))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if chave != "" {
		req.Header.Set("Idempotency-Key", chave)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	bruto, _ := io.ReadAll(resp.Body)
	var corpoResp map[string]any
	_ = json.Unmarshal(bruto, &corpoResp)
	return resp, corpoResp
}

func corpoOTP() string {
	expira := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	return `{"template":"otp-login","destinatario":"jota@exemplo.com","prioridade":"alta",
	         "variaveis":{"codigo":"482913"},"expira_em":"` + expira + `"}`
}

func TestCriarEBuscar(t *testing.T) {
	srv, _ := novoServidor(t)

	resp, corpo := requisitar(t, "POST", srv.URL+"/notificacoes", tokenBooklit, "desafio-1", corpoOTP())
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d: %v", resp.StatusCode, corpo)
	}
	if corpo["estado"] != "aceita" || corpo["canal"] != "email" {
		t.Errorf("corpo = %v", corpo)
	}
	if _, temVariaveis := corpo["variaveis"]; temVariaveis {
		t.Error("resposta devolveu as variáveis")
	}
	if resp.Header.Get("Idempotent-Replayed") != "" {
		t.Error("primeira chamada marcada como repetição")
	}

	id, _ := corpo["id"].(string)
	if resp.Header.Get("Location") != "/notificacoes/"+id {
		t.Errorf("Location = %q", resp.Header.Get("Location"))
	}

	resp, corpo = requisitar(t, "GET", srv.URL+"/notificacoes/"+id, tokenBooklit, "", "")
	if resp.StatusCode != http.StatusOK || corpo["id"] != id {
		t.Fatalf("GET status %d: %v", resp.StatusCode, corpo)
	}

	resp, _ = requisitar(t, "GET", srv.URL+"/notificacoes/"+id, tokenUptime, "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("outra origem recebeu %d em vez de 404", resp.StatusCode)
	}
}

func TestRepeticao(t *testing.T) {
	srv, _ := novoServidor(t)
	corpo := corpoOTP()

	_, primeira := requisitar(t, "POST", srv.URL+"/notificacoes", tokenBooklit, "desafio-1", corpo)
	resp, segunda := requisitar(t, "POST", srv.URL+"/notificacoes", tokenBooklit, "desafio-1", corpo)
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("status %d replay=%q", resp.StatusCode, resp.Header.Get("Idempotent-Replayed"))
	}
	if primeira["id"] != segunda["id"] {
		t.Errorf("ids diferentes: %v e %v", primeira["id"], segunda["id"])
	}

	// mesma chave, outra origem: são pedidos independentes
	_, terceira := requisitar(t, "POST", srv.URL+"/notificacoes", tokenUptime, "desafio-1", corpo)
	if terceira["id"] == primeira["id"] {
		t.Error("a chave de idempotência vazou entre origens")
	}
}

func TestErros(t *testing.T) {
	srv, _ := novoServidor(t)
	url := srv.URL + "/notificacoes"

	casos := []struct {
		nome, token, chave, corpo string
		status                    int
	}{
		{"sem token", "", "k", corpoOTP(), http.StatusUnauthorized},
		{"token errado", "nao-e-esse-token-de-jeito-nenhum-xx", "k", corpoOTP(), http.StatusUnauthorized},
		{"JSON quebrado", tokenBooklit, "k", `{"template":`, http.StatusBadRequest},
		{"campo desconhecido", tokenBooklit, "k", `{"template":"otp-login","origem":"uptime"}`, http.StatusBadRequest},
		{"dois objetos", tokenBooklit, "k", `{} {}`, http.StatusBadRequest},
		{"corpo gigante", tokenBooklit, "k", `{"template":"` + strings.Repeat("x", 20<<10) + `"}`, http.StatusBadRequest},
		{"sem chave", tokenBooklit, "", corpoOTP(), http.StatusUnprocessableEntity},
		{"variável faltando", tokenBooklit, "k2", `{"template":"otp-login","destinatario":"jota@exemplo.com"}`, http.StatusUnprocessableEntity},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			resp, corpo := requisitar(t, "POST", url, c.token, c.chave, c.corpo)
			if resp.StatusCode != c.status {
				t.Fatalf("status %d, esperava %d: %v", resp.StatusCode, c.status, corpo)
			}
			if _, ok := corpo["erro"]; !ok {
				t.Errorf("resposta de erro sem campo \"erro\": %v", corpo)
			}
		})
	}

	// conflito: mesma chave com outro destinatário
	requisitar(t, "POST", url, tokenBooklit, "desafio-9", corpoOTP())
	outro := strings.Replace(corpoOTP(), "jota@exemplo.com", "outra@exemplo.com", 1)
	if resp, _ := requisitar(t, "POST", url, tokenBooklit, "desafio-9", outro); resp.StatusCode != http.StatusConflict {
		t.Errorf("conflito de idempotência devolveu %d", resp.StatusCode)
	}
}

func TestSaudeSemToken(t *testing.T) {
	srv, _ := novoServidor(t)
	if resp, _ := requisitar(t, "GET", srv.URL+"/saude", "", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("/saude devolveu %d", resp.StatusCode)
	}
}

func TestCodigoNaoVazaEmLogNemResposta(t *testing.T) {
	srv, logs := novoServidor(t)
	_, corpo := requisitar(t, "POST", srv.URL+"/notificacoes", tokenBooklit, "desafio-1", corpoOTP())
	bruto, _ := json.Marshal(corpo)
	if strings.Contains(string(bruto), "482913") {
		t.Errorf("o código apareceu na resposta: %s", bruto)
	}
	if strings.Contains(logs.String(), "482913") {
		t.Errorf("o código apareceu no log:\n%s", logs.String())
	}
}

func TestLerTokens(t *testing.T) {
	longo := strings.Repeat("a", 32)
	tokens, err := LerTokens("booklit=" + longo + ", uptime=" + strings.Repeat("b", 32))
	if err != nil || tokens[longo] != "booklit" || len(tokens) != 2 {
		t.Fatalf("tokens=%v err=%v", tokens, err)
	}
	for _, ruim := range []string{"", "booklit", "booklit=curto", "=" + longo,
		"booklit=" + longo + ",uptime=" + longo} {
		if _, err := LerTokens(ruim); err == nil {
			t.Errorf("LerTokens(%q) deveria falhar", ruim)
		}
	}
}

// repoComoPostgres guarda horários com precisão de microssegundos, como o Postgres faz.
// Sem isso o teste não enxerga o defeito achado no lab: a primeira resposta (em memória,
// com nanossegundos) e a repetição (lida do banco) mostravam horários diferentes.
type repoComoPostgres struct{ *memoria.Repositorio }

func (r repoComoPostgres) Criar(ctx context.Context, n notificacao.Notificacao) (notificacao.Notificacao, bool, error) {
	n.CriadoEm = n.CriadoEm.Truncate(time.Microsecond)
	n.AtualizadoEm = n.AtualizadoEm.Truncate(time.Microsecond)
	if n.ExpiraEm != nil {
		t := n.ExpiraEm.Truncate(time.Microsecond)
		n.ExpiraEm = &t
	}
	return r.Repositorio.Criar(ctx, n)
}

func TestRepeticaoTemOsMesmosHorarios(t *testing.T) {
	repo := memoria.NovoRepositorio()
	repo.AdicionarTemplate(notificacao.Template{
		Nome: "otp-login", Versao: 1, Canal: notificacao.CanalEmail,
		Assunto: "Seu código", Corpo: "Código {{.codigo}}, vale {{.minutos}} min.", Variaveis: []string{"codigo"},
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(Rotas(
		aplicacao.NovoServico(repoComoPostgres{repo}, memoria.NovoPublicador(), log),
		Tokens{tokenBooklit: "booklit"}, log))
	t.Cleanup(srv.Close)

	// expira_em com nanossegundos: o cliente pode mandar o que quiser
	expira := time.Now().Add(10 * time.Minute).UTC().Format("2006-01-02T15:04:05.123456789Z")
	corpo := `{"template":"otp-login","destinatario":"jota@exemplo.com","variaveis":{"codigo":"482913"},"expira_em":"` + expira + `"}`

	_, primeira := requisitar(t, "POST", srv.URL+"/notificacoes", tokenBooklit, "desafio-1", corpo)
	_, segunda := requisitar(t, "POST", srv.URL+"/notificacoes", tokenBooklit, "desafio-1", corpo)
	for _, campo := range []string{"criado_em", "atualizado_em", "expira_em"} {
		if primeira[campo] == nil || primeira[campo] != segunda[campo] {
			t.Errorf("%s: primeira resposta %v, repetição %v", campo, primeira[campo], segunda[campo])
		}
	}
}
