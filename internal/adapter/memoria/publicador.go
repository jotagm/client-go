package memoria

import (
	"context"
	"sync"

	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

// Publicador guarda o que foi publicado. Falha pode ser simulada com Falhar.
type Publicador struct {
	mu         sync.Mutex
	publicadas []notificacao.Notificacao
	falha      error
}

var _ porta.Publicador = (*Publicador)(nil)

func NovoPublicador() *Publicador { return &Publicador{} }

// Falhar faz as próximas publicações devolverem err (nil volta ao normal).
func (p *Publicador) Falhar(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.falha = err
}

func (p *Publicador) Publicar(_ context.Context, n notificacao.Notificacao) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.falha != nil {
		return p.falha
	}
	p.publicadas = append(p.publicadas, n)
	return nil
}

func (p *Publicador) Publicadas() []notificacao.Notificacao {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]notificacao.Notificacao(nil), p.publicadas...)
}
