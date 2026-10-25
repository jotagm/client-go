package memoria

import (
	"context"
	"sync"
	"time"

	"github.com/jotagm/client-go/internal/porta"
)

// Provedor programável para teste: devolve os erros da lista em ordem e depois sucesso.
// Guarda o que recebeu, inclusive nas tentativas que falharam.
type Provedor struct {
	mu     sync.Mutex
	erros  []error
	envios []porta.Envio
	// Atraso simula um provedor lento; respeita o cancelamento do contexto.
	Atraso time.Duration
}

var _ porta.Provedor = (*Provedor)(nil)

func NovoProvedor(erros ...error) *Provedor { return &Provedor{erros: erros} }

func (p *Provedor) Nome() string { return "memoria" }

func (p *Provedor) Enviar(ctx context.Context, e porta.Envio) (porta.Resultado, error) {
	if p.Atraso > 0 {
		select {
		case <-time.After(p.Atraso):
		case <-ctx.Done():
			return porta.Resultado{}, &porta.ErroProvedor{Classe: porta.ErrTemporario, Motivo: "timeout"}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.envios = append(p.envios, e)
	if len(p.erros) > 0 {
		err := p.erros[0]
		p.erros = p.erros[1:]
		if err != nil {
			return porta.Resultado{}, err
		}
	}
	return porta.Resultado{IDNoProvedor: "msg-" + e.NotificacaoID[:8], Latencia: p.Atraso}, nil
}

// Envios devolve tudo o que o provedor recebeu.
func (p *Provedor) Envios() []porta.Envio {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]porta.Envio(nil), p.envios...)
}
