package notificacao

import (
	"crypto/rand"
	"fmt"
)

// NovoID gera um UUID v4. Feito à mão para não puxar dependência por 10 linhas.
func NovoID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand indisponível: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // versão 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
