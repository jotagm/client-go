package notificacao

import (
	"encoding/json"
	"errors"
	"fmt"
)

// VersaoEnvelope muda quando o formato da mensagem mudar de um jeito incompatível.
const VersaoEnvelope = 1

// Envelope é o que trafega no SNS/SQS. Leva só o id e as variáveis: o resto (template,
// destinatário, estado, expiração) o consumidor lê do banco, que é a fonte da verdade.
// As variáveis vão aqui porque o banco não as guarda (o código do OTP).
type Envelope struct {
	Versao    int               `json:"v"`
	ID        string            `json:"id"`
	Variaveis map[string]string `json:"variaveis,omitempty"`
}

func CodificarEnvelope(n Notificacao) ([]byte, error) {
	return json.Marshal(Envelope{Versao: VersaoEnvelope, ID: n.ID, Variaveis: n.Variaveis})
}

var ErrEnvelopeInvalido = errors.New("envelope inválido")

func DecodificarEnvelope(corpo []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(corpo, &e); err != nil {
		// não inclui o corpo no erro: ele tem as variáveis
		return Envelope{}, fmt.Errorf("%w: JSON ilegível", ErrEnvelopeInvalido)
	}
	if e.Versao != VersaoEnvelope {
		return Envelope{}, fmt.Errorf("%w: versão %d não suportada", ErrEnvelopeInvalido, e.Versao)
	}
	if e.ID == "" {
		return Envelope{}, fmt.Errorf("%w: sem id", ErrEnvelopeInvalido)
	}
	return e, nil
}

// String sem as variáveis.
func (e Envelope) String() string {
	return fmt.Sprintf("Envelope[v=%d id=%s variaveis=%v]", e.Versao, e.ID, Nomes(e.Variaveis))
}
