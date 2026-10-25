package notificacao

import (
	"reflect"
	"regexp"
	"testing"
)

func TestConferirVariaveis(t *testing.T) {
	tpl := Template{Variaveis: []string{"codigo", "nome"}}

	faltando, sobrando := tpl.ConferirVariaveis(map[string]string{"codigo": "1", "senha": "x"})
	if !reflect.DeepEqual(faltando, []string{"nome"}) {
		t.Errorf("faltando = %v", faltando)
	}
	if !reflect.DeepEqual(sobrando, []string{"senha"}) {
		t.Errorf("sobrando = %v", sobrando)
	}

	faltando, sobrando = tpl.ConferirVariaveis(map[string]string{"codigo": "1", "nome": "Jota"})
	if faltando != nil || sobrando != nil {
		t.Errorf("esperava tudo certo, veio faltando=%v sobrando=%v", faltando, sobrando)
	}
}

func TestNovoID(t *testing.T) {
	formato := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	vistos := map[string]bool{}
	for range 1000 {
		id := NovoID()
		if !formato.MatchString(id) {
			t.Fatalf("id fora do formato UUID v4: %s", id)
		}
		if vistos[id] {
			t.Fatalf("id repetido: %s", id)
		}
		vistos[id] = true
	}
}
