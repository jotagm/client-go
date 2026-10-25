package memoria

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/jotagm/client-go/internal/porta"
)

// Fila imita o essencial do SQS: Adiar esconde a mensagem por um tempo, cada recebimento
// conta, e Confirmar apaga. Não imita visibility timeout de mensagem esquecida.
type Fila struct {
	mu        sync.Mutex
	proximoID int
	itens     map[string]*item
	novidade  chan struct{}
	agora     func() time.Time
}

type item struct {
	corpo       []byte
	visivelEm   time.Time
	recebimento int
	emUso       bool
}

var _ porta.Consumidor = (*Fila)(nil)

func NovaFila() *Fila {
	return &Fila{itens: map[string]*item{}, novidade: make(chan struct{}, 1), agora: time.Now}
}

func (f *Fila) Enviar(corpo []byte) {
	f.mu.Lock()
	f.proximoID++
	f.itens[strconv.Itoa(f.proximoID)] = &item{corpo: corpo, visivelEm: f.agora()}
	f.mu.Unlock()
	f.avisar()
}

// Pendentes conta o que ainda não foi confirmado.
func (f *Fila) Pendentes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.itens)
}

// Receber espera até 50ms por mensagens visíveis (o "long polling" da fila em memória).
func (f *Fila) Receber(ctx context.Context) ([]porta.Mensagem, error) {
	prazo := time.NewTimer(50 * time.Millisecond)
	defer prazo.Stop()
	for {
		if ms := f.pegarVisiveis(); len(ms) > 0 {
			return ms, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.novidade:
		case <-prazo.C:
			return nil, nil
		}
	}
}

func (f *Fila) pegarVisiveis() []porta.Mensagem {
	f.mu.Lock()
	defer f.mu.Unlock()
	agora := f.agora()
	var ms []porta.Mensagem
	for id, it := range f.itens {
		if it.emUso || it.visivelEm.After(agora) {
			continue
		}
		it.emUso = true
		it.recebimento++
		ms = append(ms, porta.Mensagem{ID: id, Recibo: id, Corpo: it.corpo, Recebimento: it.recebimento})
		if len(ms) == 10 {
			break
		}
	}
	return ms
}

func (f *Fila) Confirmar(_ context.Context, m porta.Mensagem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.itens, m.Recibo)
	return nil
}

func (f *Fila) Adiar(_ context.Context, m porta.Mensagem, espera time.Duration) error {
	f.mu.Lock()
	if it, ok := f.itens[m.Recibo]; ok {
		it.emUso = false
		it.visivelEm = f.agora().Add(espera)
	}
	f.mu.Unlock()
	time.AfterFunc(espera, f.avisar)
	return nil
}

func (f *Fila) avisar() {
	select {
	case f.novidade <- struct{}{}:
	default:
	}
}
