package aplicacao

import (
	"math/rand/v2"
	"time"
)

// Retentativa define quanto esperar entre tentativas e quando desistir.
type Retentativa struct {
	Base          time.Duration // espera da primeira retentativa
	Teto          time.Duration // espera máxima
	MaxTentativas int           // na tentativa MaxTentativas, desiste e marca falhou
	sortear       func(n int64) int64
}

// RetentativaPadrao: 2s, 4s, 8s, 16s (com jitter) e desiste na 5ª tentativa.
// A redrive policy da fila fica acima disso (ver infra/): ela é a rede de segurança para
// o que o Client_GO não consegue nem processar, como envelope ilegível ou banco fora do ar.
func RetentativaPadrao() Retentativa {
	return Retentativa{Base: 2 * time.Second, Teto: 5 * time.Minute, MaxTentativas: 5}
}

// Espera para a próxima tentativa, sabendo que "tentativa" acabou de falhar (1 = a primeira).
//
// Backoff exponencial com "equal jitter": metade fixa, metade sorteada. A metade fixa
// garante que a espera cresce; a sorteada espalha as retentativas, para que mil mensagens
// que falharam juntas (provedor caiu) não voltem todas no mesmo segundo.
func (r Retentativa) Espera(tentativa int) time.Duration {
	if tentativa < 1 {
		tentativa = 1
	}
	espera := r.Teto
	if tentativa <= 30 { // 2^30 * base já passou de qualquer teto razoável
		if e := r.Base << (tentativa - 1); e > 0 && e < r.Teto {
			espera = e
		}
	}
	metade := int64(espera / 2)
	if metade <= 0 {
		return espera
	}
	sortear := r.sortear
	if sortear == nil {
		sortear = rand.Int64N
	}
	return time.Duration(metade + sortear(metade+1))
}

// Esgotou diz se a tentativa que acabou de falhar era a última.
func (r Retentativa) Esgotou(tentativa int) bool {
	return tentativa >= r.MaxTentativas
}
