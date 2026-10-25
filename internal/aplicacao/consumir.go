package aplicacao

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jotagm/client-go/internal/porta"
)

// tempoParaAplicar é quanto um worker tem para confirmar ou adiar depois que o
// desligamento começou. A decisão já foi tomada; só falta avisar a fila.
const tempoParaAplicar = 5 * time.Second

// Consumir lê da fila e processa com "workers" goroutines até ctx ser cancelado.
//
// No desligamento: para de receber, deixa os workers terminarem o que já pegaram e só
// então retorna. Mensagem recebida e não processada volta sozinha para a fila quando o
// visibility timeout vence.
func Consumir(ctx context.Context, fila string, consumidor porta.Consumidor, p *Processador,
	workers int, log *slog.Logger) {
	log = log.With("fila", fila)
	trabalho := make(chan porta.Mensagem)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range trabalho {
				processarUma(ctx, consumidor, p, m, log)
			}
		}()
	}

	log.Info("consumidor no ar", "workers", workers)
	espera := time.Second
	for ctx.Err() == nil {
		mensagens, err := consumidor.Receber(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Error("falha ao receber da fila", "erro", err, "nova_tentativa_em", espera)
			dormir(ctx, espera)
			espera = min(espera*2, 30*time.Second)
			continue
		}
		espera = time.Second
		for _, m := range mensagens {
			select {
			case trabalho <- m:
			case <-ctx.Done():
				// sobrou mensagem recebida sem worker: volta para a fila pelo visibility timeout
			}
		}
	}

	close(trabalho)
	wg.Wait()
	log.Info("consumidor parado")
}

func processarUma(ctx context.Context, consumidor porta.Consumidor, p *Processador, m porta.Mensagem, log *slog.Logger) {
	// O processamento não é interrompido no meio pelo desligamento (WithoutCancel):
	// cortar uma chamada ao provedor no meio deixaria a dúvida "foi ou não foi?".
	decisao := p.Processar(context.WithoutCancel(ctx), m)

	aplicar, cancelar := context.WithTimeout(context.WithoutCancel(ctx), tempoParaAplicar)
	defer cancelar()
	var err error
	if decisao.Confirmar {
		err = consumidor.Confirmar(aplicar, m)
	} else {
		err = consumidor.Adiar(aplicar, m, decisao.Espera)
	}
	if err != nil {
		// Se confirmar falhar, a mensagem volta e o estado no banco evita a entrega dupla.
		log.Error("falha ao aplicar a decisão na fila", "mensagem", m.ID, "confirmar", decisao.Confirmar, "erro", err)
	}
}

func dormir(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
