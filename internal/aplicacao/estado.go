package aplicacao

import (
	"log/slog"
	"time"

	"github.com/jotagm/client-go/internal/notificacao"
)

// registrarEstado é a única linha de log de mudança de estado. Toda transição passa por
// aqui (aceitar, suprimir, falhar na publicação, enviar, expirar...), então dá para
// reconstruir a vida de uma notificação filtrando por "estado alterado" e pelo id.
//
// "de" vazio quer dizer que a notificação acabou de ser criada. O logger recebido já deve carregar o "id". Nunca recebe variáveis.
func registrarEstado(log *slog.Logger, de, para notificacao.Estado, motivo string, extra ...any) {
	attrs := []any{"de", string(de), "para", string(para), "motivo", motivo}
	log.Info("estado alterado", append(attrs, extra...)...)
}

// agoraMicro devolve o instante em UTC com precisão de microssegundos, que é o que o
// Postgres guarda. Sem isso, a primeira resposta do POST (em memória, com nanossegundos)
// e a repetição (lida do banco) mostrariam horários diferentes para o mesmo campo.
func agoraMicro(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}
