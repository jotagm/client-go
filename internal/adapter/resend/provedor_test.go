package resend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jotagm/client-go/internal/porta"
)

const chaveTeste = "re_chave_de_teste"

type fake struct {
	*httptest.Server
	chamadas atomic.Int32
	cab      atomic.Value // http.Header
	corpo    atomic.Value // []byte
}

func novoFake(t *testing.T, h http.HandlerFunc) *fake {
	t.Helper()
	f := &fake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.chamadas.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.corpo.Store(b)
		f.cab.Store(r.Header.Clone())
		if r.Method != http.MethodPost || r.URL.Path != "/emails" {
			t.Errorf("chamada inesperada: %s %s", r.Method, r.URL.Path)
		}
		h(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) provedor(t *testing.T, timeout time.Duration) *Provedor {
	t.Helper()
	p, err := Novo(chaveTeste, "Booklit <no-reply@booklit.bio>", f.URL, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func envio() porta.Envio {
	return porta.Envio{NotificacaoID: "n1", Destinatario: "jota@exemplo.com",
		Assunto: "Seu código de acesso", Corpo: "Código 482913, vale 10 min."}
}

func responde(status int, corpo string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(corpo))
	}
}

func TestEnviarSucesso(t *testing.T) {
	f := novoFake(t, responde(200, `{"id":"49a3999c-0ce1-4ea6-ab68-afcd6dc2e794"}`))
	res, err := f.provedor(t, 0).Enviar(context.Background(), envio())
	if err != nil {
		t.Fatal(err)
	}
	if res.IDNoProvedor != "49a3999c-0ce1-4ea6-ab68-afcd6dc2e794" || res.Latencia <= 0 {
		t.Fatalf("resultado: %+v", res)
	}
	cab := f.cab.Load().(http.Header)
	if cab.Get("Authorization") != "Bearer "+chaveTeste || cab.Get("Idempotency-Key") != "n1" ||
		cab.Get("Content-Type") != "application/json" || cab.Get("User-Agent") == "" {
		t.Fatalf("cabeçalhos: %v", cab)
	}
	var p map[string]any
	if err := json.Unmarshal(f.corpo.Load().([]byte), &p); err != nil {
		t.Fatal(err)
	}
	para, _ := p["to"].([]any)
	if p["from"] != "Booklit <no-reply@booklit.bio>" || len(para) != 1 || para[0] != "jota@exemplo.com" ||
		p["subject"] != "Seu código de acesso" || p["text"] != "Código 482913, vale 10 min." {
		t.Fatalf("corpo: %v", p)
	}
}

func TestClassificacaoPorStatus(t *testing.T) {
	casos := []struct {
		nome   string
		status int
		corpo  string
		classe error
		motivo string
	}{
		{"validação", 422, `{"statusCode":422,"name":"validation_error","message":"Invalid ` + "`to`" + ` field"}`, porta.ErrPermanente, "validation_error"},
		{"remetente inválido", 422, `{"statusCode":422,"name":"invalid_from_address","message":"x"}`, porta.ErrPermanente, "invalid_from_address"},
		{"domínio não verificado", 403, `{"statusCode":403,"name":"validation_error","message":"The booklit.bio domain is not verified"}`, porta.ErrPermanente, "validation_error"},
		{"chave inválida", 403, `{"statusCode":403,"name":"invalid_api_key","message":"API key is invalid"}`, porta.ErrPermanente, "invalid_api_key"},
		{"sem chave", 401, `{"statusCode":401,"name":"missing_api_key","message":"x"}`, porta.ErrPermanente, "missing_api_key"},
		{"idempotência com payload diferente", 409, `{"statusCode":409,"name":"invalid_idempotent_request","message":"x"}`, porta.ErrPermanente, "invalid_idempotent_request"},
		{"idempotência em andamento", 409, `{"statusCode":409,"name":"concurrent_idempotent_requests","message":"x"}`, porta.ErrTemporario, "concurrent_idempotent_requests"},
		{"taxa", 429, `{"statusCode":429,"name":"rate_limit_exceeded","message":"x"}`, porta.ErrTemporario, "rate_limit_exceeded"},
		{"cota diária", 429, `{"statusCode":429,"name":"daily_quota_exceeded","message":"x"}`, porta.ErrTemporario, "daily_quota_exceeded"},
		{"erro interno", 500, `{"statusCode":500,"name":"internal_server_error","message":"x"}`, porta.ErrTemporario, "internal_server_error"},
		{"503 com HTML", 503, `<html>Service Unavailable</html>`, porta.ErrTemporario, "desconhecido"},
		{"400 sem corpo", 400, ``, porta.ErrPermanente, "desconhecido"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			f := novoFake(t, responde(c.status, c.corpo))
			_, err := f.provedor(t, 0).Enviar(context.Background(), envio())
			var ep *porta.ErroProvedor
			if !errors.As(err, &ep) {
				t.Fatalf("esperava *ErroProvedor, veio %v", err)
			}
			if !errors.Is(err, c.classe) || ep.Motivo != c.motivo || ep.StatusHTTP != c.status {
				t.Fatalf("classe/motivo/status = %v/%s/%d, quer %v/%s/%d", ep.Classe, ep.Motivo, ep.StatusHTTP, c.classe, c.motivo, c.status)
			}
		})
	}
}

func TestSucessoSemIDEhTemporario(t *testing.T) {
	for _, corpo := range []string{`{}`, `não é json`, ``} {
		f := novoFake(t, responde(200, corpo))
		_, err := f.provedor(t, 0).Enviar(context.Background(), envio())
		var ep *porta.ErroProvedor
		if !errors.As(err, &ep) || ep.Classe != porta.ErrTemporario || ep.Motivo != "resposta_sem_id" {
			t.Fatalf("corpo %q: %v", corpo, err)
		}
	}
}

func TestTimeoutEhTemporario(t *testing.T) {
	f := novoFake(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	_, err := f.provedor(t, 50*time.Millisecond).Enviar(context.Background(), envio())
	var ep *porta.ErroProvedor
	if !errors.As(err, &ep) || ep.Classe != porta.ErrTemporario || ep.Motivo != "timeout" || ep.StatusHTTP != 0 {
		t.Fatalf("erro: %v", err)
	}
}

func TestRedeIndisponivelEhTemporario(t *testing.T) {
	f := novoFake(t, responde(200, `{}`))
	p := f.provedor(t, 0)
	f.Close()
	_, err := p.Enviar(context.Background(), envio())
	var ep *porta.ErroProvedor
	if !errors.As(err, &ep) || ep.Classe != porta.ErrTemporario || ep.Motivo != "rede" {
		t.Fatalf("erro: %v", err)
	}
}

func TestNaoSegueRedirecionamentoComAChave(t *testing.T) {
	alvo := novoFake(t, responde(200, `{"id":"x"}`))
	f := novoFake(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, alvo.URL+"/emails", http.StatusTemporaryRedirect)
	})
	_, err := f.provedor(t, 0).Enviar(context.Background(), envio())
	if err == nil || alvo.chamadas.Load() != 0 {
		t.Fatalf("o redirecionamento foi seguido (chamadas no alvo=%d, err=%v)", alvo.chamadas.Load(), err)
	}
}

func TestDetalheNaoVazaChaveNemCorpo(t *testing.T) {
	f := novoFake(t, responde(422, `{"statusCode":422,"name":"validation_error","message":"campo inválido"}`))
	_, err := f.provedor(t, 0).Enviar(context.Background(), envio())
	if s := err.Error(); strings.Contains(s, chaveTeste) || strings.Contains(s, "482913") {
		t.Fatalf("erro vaza segredo: %s", s)
	}
}

func TestDetalheTruncaSemCortarUTF8(t *testing.T) {
	s := detalhe("", strings.Repeat("ç", 400))
	if len(s) > tamanhoMaxDetalhe+len("…") || !strings.HasSuffix(s, "…") || strings.ContainsRune(s, '�') {
		t.Fatalf("detalhe: %d bytes", len(s))
	}
}

func TestNovoValida(t *testing.T) {
	casos := []struct{ nome, chave, remetente, url string }{
		{"sem chave", " ", "a@b.com", ""},
		{"sem remetente", "re_x", "", ""},
		{"remetente com quebra de linha", "re_x", "a@b.com\nBcc: c@d.com", ""},
		{"URL inválida", "re_x", "a@b.com", "ftp://x"},
	}
	for _, c := range casos {
		if _, err := Novo(c.chave, c.remetente, c.url, 0); err == nil {
			t.Errorf("%s: esperava erro", c.nome)
		}
	}
	p, err := Novo("re_x", "a@b.com", "", 0)
	if err != nil || p.endpoint != "https://api.resend.com/emails" || p.Nome() != "resend" {
		t.Fatalf("padrão: %+v %v", p, err)
	}
}
