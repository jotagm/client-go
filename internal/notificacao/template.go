package notificacao

import "sort"

// Template é um texto versionado. Uma versão publicada não muda: corrigir o texto é
// criar a próxima versão, assim o histórico sabe exatamente o que foi enviado.
type Template struct {
	Nome    string
	Versao  int
	Canal   Canal
	Assunto string
	Corpo   string
	// Variaveis que quem pede o aviso precisa mandar. Variáveis calculadas pelo próprio
	// Client_GO na hora de renderizar (como "minutos", a partir de ExpiraEm) não entram aqui.
	Variaveis []string
}

// ConferirVariaveis devolve o que faltou e o que veio a mais, ambos ordenados.
// Variável a mais é recusada: é o jeito de um segredo vazar sem ninguém perceber.
func (t Template) ConferirVariaveis(recebidas map[string]string) (faltando, sobrando []string) {
	esperadas := make(map[string]bool, len(t.Variaveis))
	for _, nome := range t.Variaveis {
		esperadas[nome] = true
		if _, ok := recebidas[nome]; !ok {
			faltando = append(faltando, nome)
		}
	}
	for nome := range recebidas {
		if !esperadas[nome] {
			sobrando = append(sobrando, nome)
		}
	}
	sort.Strings(faltando)
	sort.Strings(sobrando)
	return faltando, sobrando
}

// Nomes devolve as chaves de um mapa de variáveis, ordenadas.
func Nomes(variaveis map[string]string) []string {
	nomes := make([]string, 0, len(variaveis))
	for nome := range variaveis {
		nomes = append(nomes, nome)
	}
	sort.Strings(nomes)
	return nomes
}
