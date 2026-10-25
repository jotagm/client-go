package main

import (
	"strings"
	"testing"
)

func ambiente(t *testing.T, extra map[string]string) {
	t.Helper()
	base := map[string]string{
		"AVISO_DB_URL": "postgres://aviso:aviso@localhost:5433/aviso",
		"AVISO_TOKENS": "booklit=booklit-token-de-teste-com-32-caracteres!",
	}
	for _, k := range []string{"AVISO_PROVEDOR", "AVISO_SES_REMETENTE", "AVISO_SES_CONJUNTO", "AVISO_FILAS", "AVISO_TOPICO_ARN", "AVISO_WORKERS", "AVISO_ENDERECO"} {
		t.Setenv(k, "")
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
	for k, v := range extra {
		t.Setenv(k, v)
	}
}

func TestConfigProvedorPadraoEhSimulado(t *testing.T) {
	ambiente(t, nil)
	cfg, err := lerConfig()
	if err != nil || cfg.provedor != provedorSimulado {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestConfigSES(t *testing.T) {
	ambiente(t, map[string]string{
		"AVISO_PROVEDOR": "SES", "AVISO_SES_REMETENTE": " Booklit <codigo@booklit.bio> ", "AVISO_SES_CONJUNTO": "eventos",
	})
	cfg, err := lerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.provedor != provedorSES || cfg.sesRemetente != "Booklit <codigo@booklit.bio>" || cfg.sesConjunto != "eventos" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestConfigRecusa(t *testing.T) {
	casos := []struct {
		nome  string
		env   map[string]string
		quero string
	}{
		{"ses sem remetente", map[string]string{"AVISO_PROVEDOR": "ses"}, "AVISO_SES_REMETENTE"},
		{"remetente com quebra de linha", map[string]string{"AVISO_PROVEDOR": "ses", "AVISO_SES_REMETENTE": "a@b.com\nBcc: c@d.com"}, "quebra de linha"},
		{"provedor desconhecido", map[string]string{"AVISO_PROVEDOR": "resend"}, "desconhecido"},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			ambiente(t, tc.env)
			_, err := lerConfig()
			if err == nil || !strings.Contains(err.Error(), tc.quero) {
				t.Fatalf("err = %v, queria algo com %q", err, tc.quero)
			}
		})
	}
}
