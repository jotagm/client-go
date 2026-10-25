// Package memoria tem implementações das portas em memória, para teste e para rodar
// sem infraestrutura. Mesmo comportamento observável dos adapters reais.
package memoria

import (
	"context"
	"sync"
	"time"

	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

type Repositorio struct {
	mu         sync.Mutex
	templates  []notificacao.Template
	supressoes map[string]string // canal|destinatario -> motivo
	porID      map[string]notificacao.Notificacao
	porChave   map[string]string // origem|chave -> id
}

var _ porta.Repositorio = (*Repositorio)(nil)

func NovoRepositorio() *Repositorio {
	return &Repositorio{
		supressoes: map[string]string{},
		porID:      map[string]notificacao.Notificacao{},
		porChave:   map[string]string{},
	}
}

func (r *Repositorio) AdicionarTemplate(t notificacao.Template) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.templates = append(r.templates, t)
}

func (r *Repositorio) Suprimir(destinatario string, canal notificacao.Canal, motivo string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.supressoes[string(canal)+"|"+destinatario] = motivo
}

func (r *Repositorio) BuscarTemplate(_ context.Context, nome string, versao int) (notificacao.Template, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var achado *notificacao.Template
	for i, t := range r.templates {
		if t.Nome != nome {
			continue
		}
		if versao != 0 && t.Versao == versao {
			return t, nil
		}
		if versao == 0 && (achado == nil || t.Versao > achado.Versao) {
			achado = &r.templates[i]
		}
	}
	if achado == nil {
		return notificacao.Template{}, porta.ErrNaoEncontrado
	}
	return *achado, nil
}

func (r *Repositorio) MotivoSupressao(_ context.Context, destinatario string, canal notificacao.Canal) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.supressoes[string(canal)+"|"+destinatario], nil
}

func (r *Repositorio) Criar(_ context.Context, n notificacao.Notificacao) (notificacao.Notificacao, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	chave := n.Origem + "|" + n.ChaveIdempot
	if id, ok := r.porChave[chave]; ok {
		return r.porID[id], false, nil
	}
	n.Variaveis = nil // o banco de verdade também não guarda os valores
	r.porID[n.ID] = n
	r.porChave[chave] = n.ID
	return n, true, nil
}

func (r *Repositorio) Buscar(_ context.Context, id string) (notificacao.Notificacao, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.porID[id]
	if !ok {
		return notificacao.Notificacao{}, porta.ErrNaoEncontrado
	}
	return n, nil
}

func (r *Repositorio) MudarEstado(_ context.Context, id string, de, para notificacao.Estado, motivo string, em time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.porID[id]
	if !ok {
		return porta.ErrNaoEncontrado
	}
	if n.Estado != de {
		return porta.ErrConcorrencia
	}
	n.Estado, n.Motivo, n.AtualizadoEm = para, motivo, em
	r.porID[id] = n
	return nil
}
