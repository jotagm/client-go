// Package resend implementa porta.Provedor com a API HTTP da Resend (https://resend.com).
//
// É o primeiro provedor fora da AWS. O papel é o mesmo do pacote ses: mandar o e-mail e
// classificar a falha em temporária ou permanente. Quem decide quando tentar de novo é o
// Processador. Só a biblioteca padrão: a API é um POST com JSON.
package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jotagm/client-go/internal/porta"
)

const (
	nome              = "resend"
	URLPadrao         = "https://api.resend.com"
	TimeoutPadrao     = 5 * time.Second
	tamanhoMaxDetalhe = 300
	limiteResposta    = 64 << 10 // a resposta da Resend tem poucas dezenas de bytes
	userAgent         = "client-go/1"
)

type Provedor struct {
	http      *http.Client
	endpoint  string
	chave     string
	remetente string
	timeout   time.Duration
}

var _ porta.Provedor = (*Provedor)(nil)

// Novo cria o provedor. chave é a API key (re_...), remetente o endereço de origem num domínio
// verificado na Resend (ex.: "Booklit <no-reply@booklit.bio>"), baseURL pode ficar vazia
// (usa a API da Resend; o teste aponta para um servidor local) e timeout vale por chamada.
func Novo(chave, remetente, baseURL string, timeout time.Duration) (*Provedor, error) {
	if strings.TrimSpace(chave) == "" {
		return nil, errors.New("resend: chave da API vazia")
	}
	if strings.TrimSpace(remetente) == "" || strings.ContainsAny(remetente, "\r\n") {
		return nil, errors.New("resend: remetente vazio ou com quebra de linha")
	}
	if baseURL == "" {
		baseURL = URLPadrao
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("resend: URL base inválida: %q", baseURL)
	}
	if timeout <= 0 {
		timeout = TimeoutPadrao
	}
	return &Provedor{
		http: &http.Client{
			// A chave vai no cabeçalho Authorization: nunca seguir um redirecionamento com ela.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		endpoint:  strings.TrimRight(baseURL, "/") + "/emails",
		chave:     chave,
		remetente: remetente,
		timeout:   timeout,
	}, nil
}

func (p *Provedor) Nome() string { return nome }

type pedido struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
}

type resposta struct {
	ID         string `json:"id"`
	StatusCode int    `json:"statusCode"`
	Name       string `json:"name"`
	Message    string `json:"message"`
}

func (p *Provedor) Enviar(ctx context.Context, e porta.Envio) (porta.Resultado, error) {
	ctx, cancelar := context.WithTimeout(ctx, p.timeout)
	defer cancelar()

	corpo, err := json.Marshal(pedido{From: p.remetente, To: []string{e.Destinatario}, Subject: e.Assunto, Text: e.Corpo})
	if err != nil {
		return porta.Resultado{}, &porta.ErroProvedor{Classe: porta.ErrPermanente, Motivo: "requisicao_invalida", Detalhe: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(corpo))
	if err != nil {
		return porta.Resultado{}, &porta.ErroProvedor{Classe: porta.ErrPermanente, Motivo: "requisicao_invalida", Detalhe: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+p.chave)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	// Mesma chave em todas as tentativas da mesma notificação: se a resposta se perder depois
	// de a Resend ter aceitado, a nova tentativa devolve o envio original em vez de mandar
	// outro e-mail com o mesmo código.
	req.Header.Set("Idempotency-Key", e.NotificacaoID)

	inicio := time.Now()
	resp, err := p.http.Do(req)
	latencia := time.Since(inicio)
	if err != nil {
		return porta.Resultado{Latencia: latencia}, classificarRede(ctx, err)
	}
	defer resp.Body.Close()

	bruto, err := io.ReadAll(io.LimitReader(resp.Body, limiteResposta))
	var r resposta
	jsonOK := err == nil && json.Unmarshal(bruto, &r) == nil

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// 2xx sem id: não dá para casar o recibo depois. O e-mail provavelmente saiu, mas
		// não sabemos; temporário (um código repetido é inofensivo, um perdido não é).
		if !jsonOK || r.ID == "" {
			return porta.Resultado{Latencia: latencia}, &porta.ErroProvedor{
				Classe: porta.ErrTemporario, Motivo: "resposta_sem_id", StatusHTTP: resp.StatusCode,
				Detalhe: "a Resend respondeu sem id"}
		}
		return porta.Resultado{IDNoProvedor: r.ID, Latencia: latencia}, nil
	}
	return porta.Resultado{Latencia: latencia}, Classificar(resp.StatusCode, r.Name, r.Message)
}

// Classificar traduz o status e o erro da API em *porta.ErroProvedor.
//
//   - permanente: a Resend entendeu e recusou (payload inválido, remetente ou domínio não
//     verificado, chave inválida). Repetir produz a mesma recusa.
//   - temporário: limite de taxa ou de cota, 5xx, requisição idêntica ainda em andamento.
//     Na dúvida, temporário: o limite de tentativas e a validade do código impedem laço infinito.
func Classificar(status int, nomeErro, mensagem string) *porta.ErroProvedor {
	motivo := nomeErro
	if motivo == "" {
		motivo = "desconhecido"
	}
	novo := func(classe error, m string) *porta.ErroProvedor {
		return &porta.ErroProvedor{Classe: classe, Motivo: m, StatusHTTP: status, Detalhe: detalhe(nomeErro, mensagem)}
	}
	switch {
	case status == 409 && nomeErro == "concurrent_idempotent_requests":
		return novo(porta.ErrTemporario, motivo)
	case status == 429:
		return novo(porta.ErrTemporario, motivo) // rate_limit_exceeded, daily_quota_exceeded...
	case status >= 500:
		return novo(porta.ErrTemporario, motivo)
	case status >= 400:
		return novo(porta.ErrPermanente, motivo)
	}
	// 1xx e 3xx: a API não responde assim; redirecionamento não é seguido.
	return novo(porta.ErrTemporario, motivo)
}

func classificarRede(ctx context.Context, err error) *porta.ErroProvedor {
	novo := func(m string) *porta.ErroProvedor {
		return &porta.ErroProvedor{Classe: porta.ErrTemporario, Motivo: m, Detalhe: detalhe("", err.Error())}
	}
	var rede net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return novo("timeout")
	case errors.Is(err, context.Canceled):
		return novo("cancelado")
	case errors.As(err, &rede) && rede.Timeout():
		return novo("timeout")
	}
	return novo("rede")
}

// detalhe junta nome e mensagem em uma linha de tamanho limitado. Erros de rede do net/http
// incluem a URL, nunca a chave (ela só vai no cabeçalho) nem o corpo do e-mail.
func detalhe(nomeErro, mensagem string) string {
	s := strings.Join(strings.Fields(nomeErro+" "+mensagem), " ")
	if len(s) > tamanhoMaxDetalhe {
		corte := tamanhoMaxDetalhe
		for corte > 0 && s[corte]&0xC0 == 0x80 { // não corta no meio de um caractere UTF-8
			corte--
		}
		s = s[:corte] + "…"
	}
	return s
}
