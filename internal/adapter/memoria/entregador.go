package memoria

import (
	"context"
	"log/slog"
	"sync"

	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

// EntregaSimulada não envia nada: registra no log (sem as variáveis) e devolve sucesso.
// É o entregador da fase 2, para ver o fluxo inteiro rodando antes do provedor real.
type EntregaSimulada struct {
	Log *slog.Logger
}

var _ porta.Entregador = EntregaSimulada{}

func (e EntregaSimulada) Entregar(_ context.Context, n notificacao.Notificacao, variaveis map[string]string) error {
	e.Log.Info("entrega simulada", "id", n.ID, "canal", n.Canal, "template",
		n.Template, "versao", n.VersaoTemplate, "variaveis", notificacao.Nomes(variaveis))
	return nil
}

// Entregador programável para teste: devolve os erros da lista em ordem e depois nil.
type Entregador struct {
	mu       sync.Mutex
	erros    []error
	entregas []Entrega
}

type Entrega struct {
	ID        string
	Variaveis map[string]string
}

var _ porta.Entregador = (*Entregador)(nil)

func NovoEntregador(erros ...error) *Entregador { return &Entregador{erros: erros} }

func (e *Entregador) Entregar(_ context.Context, n notificacao.Notificacao, variaveis map[string]string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entregas = append(e.entregas, Entrega{ID: n.ID, Variaveis: variaveis})
	if len(e.erros) == 0 {
		return nil
	}
	err := e.erros[0]
	e.erros = e.erros[1:]
	return err
}

// Chamadas devolve todas as tentativas de entrega, inclusive as que falharam.
func (e *Entregador) Chamadas() []Entrega {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Entrega(nil), e.entregas...)
}
