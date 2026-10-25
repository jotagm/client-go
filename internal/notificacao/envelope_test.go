package notificacao

import (
	"errors"
	"strings"
	"testing"
)

func TestEnvelopeIdaEVolta(t *testing.T) {
	corpo, err := CodificarEnvelope(Notificacao{ID: "abc", Destinatario: "jota@exemplo.com",
		Variaveis: map[string]string{"codigo": "482913"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(corpo), "jota@exemplo.com") {
		t.Errorf("o envelope não precisa levar o destinatário: %s", corpo)
	}

	e, err := DecodificarEnvelope(corpo)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "abc" || e.Variaveis["codigo"] != "482913" {
		t.Errorf("envelope = %+v", e)
	}
	if strings.Contains(e.String(), "482913") {
		t.Errorf("String() vazou o código: %s", e)
	}
}

func TestEnvelopeInvalido(t *testing.T) {
	for _, corpo := range []string{
		`não é json {"codigo":"482913"}`,
		`{"v":2,"id":"abc"}`,
		`{"v":1}`,
	} {
		_, err := DecodificarEnvelope([]byte(corpo))
		if !errors.Is(err, ErrEnvelopeInvalido) {
			t.Errorf("%q: esperava ErrEnvelopeInvalido, veio %v", corpo, err)
		}
		if err != nil && strings.Contains(err.Error(), "482913") {
			t.Errorf("o erro vazou o corpo: %v", err)
		}
	}
}
