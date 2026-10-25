package api

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// Tokens liga cada token a uma origem. A origem do pedido vem SEMPRE daqui, nunca do
// corpo: um sistema não consegue pedir aviso em nome de outro.
type Tokens map[string]string // token -> origem

// LerTokens lê "booklit=abc123,uptime=def456".
func LerTokens(s string) (Tokens, error) {
	tokens := Tokens{}
	for _, par := range strings.Split(s, ",") {
		par = strings.TrimSpace(par)
		if par == "" {
			continue
		}
		origem, token, ok := strings.Cut(par, "=")
		origem, token = strings.TrimSpace(origem), strings.TrimSpace(token)
		if !ok || origem == "" || token == "" {
			return nil, fmt.Errorf("token mal formado (use origem=token): %q", origem)
		}
		if len(token) < 32 {
			return nil, fmt.Errorf("token da origem %q curto demais: use pelo menos 32 caracteres", origem)
		}
		if _, repetido := tokens[token]; repetido {
			return nil, fmt.Errorf("o mesmo token aparece em duas origens")
		}
		tokens[token] = origem
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("nenhum token configurado")
	}
	return tokens, nil
}

// origem compara com todos os tokens em tempo constante, sem parar no primeiro que bate,
// para o tempo de resposta não revelar quantos caracteres estavam certos.
func (t Tokens) origem(recebido string) (string, bool) {
	achada := ""
	for token, origem := range t {
		if subtle.ConstantTimeCompare([]byte(token), []byte(recebido)) == 1 {
			achada = origem
		}
	}
	return achada, achada != ""
}

type chaveOrigem struct{}

func origemDe(ctx context.Context) string {
	o, _ := ctx.Value(chaveOrigem{}).(string)
	return o
}

func autenticar(tokens Tokens, proximo http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			responderErro(w, http.StatusUnauthorized, "token ausente", nil)
			return
		}
		origem, ok := tokens.origem(token)
		if !ok {
			responderErro(w, http.StatusUnauthorized, "token inválido", nil)
			return
		}
		proximo.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), chaveOrigem{}, origem)))
	})
}
