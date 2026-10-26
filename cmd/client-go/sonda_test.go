package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUrlDaSonda(t *testing.T) {
	casos := map[string]string{
		":8081":          "http://127.0.0.1:8081/saude",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/saude",
		"127.0.0.1:8081": "http://127.0.0.1:8081/saude",
		"[::]:8081":      "http://127.0.0.1:8081/saude",
		"lixo":           "http://127.0.0.1:8081/saude",
	}
	for endereco, esperado := range casos {
		if obtido := urlDaSonda(endereco); obtido != esperado {
			t.Errorf("urlDaSonda(%q) = %q, esperava %q", endereco, obtido, esperado)
		}
	}
}

func TestConsultarSaude(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	cliente := &http.Client{Timeout: time.Second}

	if err := consultarSaude(cliente, srv.URL); err != nil {
		t.Fatalf("200 devia passar: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := consultarSaude(cliente, srv.URL); err == nil {
		t.Fatal("503 devia falhar")
	}
	srv.Close()
	if err := consultarSaude(cliente, srv.URL); err == nil {
		t.Fatal("servidor fora do ar devia falhar")
	}
}
