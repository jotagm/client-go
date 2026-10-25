// Package api expõe o Client_GO por HTTP.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jotagm/client-go/internal/aplicacao"
	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

const tamanhoMaxCorpo = 16 << 10 // 16 KiB: um pedido de aviso não chega perto disso

type API struct {
	servico *aplicacao.Servico
	log     *slog.Logger
}

// Rotas monta o roteador. /saude fica sem autenticação para o healthcheck do compose.
func Rotas(servico *aplicacao.Servico, tokens Tokens, log *slog.Logger) http.Handler {
	a := &API{servico: servico, log: log}

	protegidas := http.NewServeMux()
	protegidas.HandleFunc("POST /notificacoes", a.criar)
	protegidas.HandleFunc("GET /notificacoes/{id}", a.buscar)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /saude", func(w http.ResponseWriter, _ *http.Request) {
		responderJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("/notificacoes", autenticar(tokens, protegidas))
	mux.Handle("/notificacoes/", autenticar(tokens, protegidas))
	return mux
}

type pedidoJSON struct {
	Template     string            `json:"template"`
	Versao       int               `json:"versao"`
	Destinatario string            `json:"destinatario"`
	Prioridade   string            `json:"prioridade"`
	Variaveis    map[string]string `json:"variaveis"`
	ExpiraEm     *time.Time        `json:"expira_em"`
}

type notificacaoJSON struct {
	ID           string     `json:"id"`
	Template     string     `json:"template"`
	Versao       int        `json:"versao"`
	Canal        string     `json:"canal"`
	Prioridade   string     `json:"prioridade"`
	Estado       string     `json:"estado"`
	Motivo       string     `json:"motivo,omitempty"`
	ExpiraEm     *time.Time `json:"expira_em,omitempty"`
	CriadoEm     time.Time  `json:"criado_em"`
	AtualizadoEm time.Time  `json:"atualizado_em"`
}

func (a *API) criar(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, tamanhoMaxCorpo)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // campo com nome errado vira erro, não silêncio

	var corpo pedidoJSON
	if err := dec.Decode(&corpo); err != nil {
		// a mensagem do decoder não inclui valores, só posição e tipo
		responderErro(w, http.StatusBadRequest, "JSON inválido: "+err.Error(), nil)
		return
	}
	if dec.More() {
		responderErro(w, http.StatusBadRequest, "JSON inválido: conteúdo depois do objeto", nil)
		return
	}

	n, nova, err := a.servico.Aceitar(r.Context(), aplicacao.Pedido{
		Origem:         origemDe(r.Context()),
		ChaveIdempot:   r.Header.Get("Idempotency-Key"),
		Template:       corpo.Template,
		VersaoTemplate: corpo.Versao,
		Destinatario:   corpo.Destinatario,
		Prioridade:     notificacao.Prioridade(corpo.Prioridade),
		Variaveis:      corpo.Variaveis,
		ExpiraEm:       corpo.ExpiraEm,
	})

	var errValidacao *aplicacao.ErrValidacao
	switch {
	case errors.As(err, &errValidacao):
		responderErro(w, http.StatusUnprocessableEntity, "pedido inválido", errValidacao.Campos)
		return
	case errors.Is(err, aplicacao.ErrConflitoIdempotencia):
		responderErro(w, http.StatusConflict, err.Error(), nil)
		return
	case errors.Is(err, aplicacao.ErrPublicacao):
		responderErro(w, http.StatusServiceUnavailable, err.Error(), nil)
		return
	case err != nil:
		a.log.Error("erro ao aceitar notificação", "origem", origemDe(r.Context()), "erro", err)
		responderErro(w, http.StatusInternalServerError, "erro interno", nil)
		return
	}

	if !nova {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Location", "/notificacoes/"+n.ID)
	responderJSON(w, http.StatusAccepted, paraJSON(n))
}

func (a *API) buscar(w http.ResponseWriter, r *http.Request) {
	n, err := a.servico.Buscar(r.Context(), origemDe(r.Context()), r.PathValue("id"))
	if errors.Is(err, porta.ErrNaoEncontrado) {
		responderErro(w, http.StatusNotFound, "notificação não encontrada", nil)
		return
	}
	if err != nil {
		a.log.Error("erro ao buscar notificação", "erro", err)
		responderErro(w, http.StatusInternalServerError, "erro interno", nil)
		return
	}
	responderJSON(w, http.StatusOK, paraJSON(n))
}

// paraJSON não inclui destinatário nem variáveis: quem consulta é quem pediu e já sabe.
func paraJSON(n notificacao.Notificacao) notificacaoJSON {
	return notificacaoJSON{
		ID: n.ID, Template: n.Template, Versao: n.VersaoTemplate, Canal: string(n.Canal),
		Prioridade: string(n.Prioridade), Estado: string(n.Estado), Motivo: n.Motivo,
		ExpiraEm: n.ExpiraEm, CriadoEm: n.CriadoEm, AtualizadoEm: n.AtualizadoEm,
	}
}

func responderJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func responderErro(w http.ResponseWriter, status int, mensagem string, campos map[string]string) {
	corpo := map[string]any{"erro": mensagem}
	if len(campos) > 0 {
		corpo["campos"] = campos
	}
	responderJSON(w, status, corpo)
}
