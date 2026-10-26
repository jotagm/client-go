package migracao

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jotagm/client-go/db"
)

var silencio = slog.New(slog.NewTextHandler(io.Discard, nil))

// poolEmEsquemaNovo devolve um pool cujas conexões usam um esquema só delas: os testes não
// se pisam nem sujam o banco de desenvolvimento. Sem AVISO_TESTE_DB_URL, o teste é pulado.
func poolEmEsquemaNovo(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("AVISO_TESTE_DB_URL")
	if url == "" {
		t.Skip("AVISO_TESTE_DB_URL não definida")
	}
	ctx := context.Background()
	esquema := fmt.Sprintf("mig_%d", rand.Int63())

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+esquema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = esquema
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+esquema+" CASCADE")
		admin.Close()
	})
	return pool
}

func contar(t *testing.T, pool *pgxpool.Pool, consulta string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), consulta).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAplicaOsScriptsReaisDoProjetoEMarcaTodos(t *testing.T) {
	pool := poolEmEsquemaNovo(t)

	aplicadas, err := Aplicar(context.Background(), pool, db.Arquivos, silencio)
	if err != nil {
		t.Fatal(err)
	}
	if len(aplicadas) < 3 || aplicadas[0] != "001_inicial.sql" {
		t.Fatalf("scripts aplicados fora de ordem ou faltando: %v", aplicadas)
	}
	// Os três templates do OTP vêm do 002; a tabela de tentativas, do 001.
	if n := contar(t, pool, `SELECT count(*) FROM t_template WHERE nome LIKE 'otp-%'`); n != 3 {
		t.Fatalf("templates do OTP = %d, esperava 3", n)
	}
	if n := contar(t, pool, `SELECT count(*) FROM t_migracao`); n != len(aplicadas) {
		t.Fatalf("t_migracao tem %d linhas, esperava %d", n, len(aplicadas))
	}
}

func TestSegundaExecucaoNaoAplicaNada(t *testing.T) {
	pool := poolEmEsquemaNovo(t)
	ctx := context.Background()
	if _, err := Aplicar(ctx, pool, db.Arquivos, silencio); err != nil {
		t.Fatal(err)
	}
	aplicadas, err := Aplicar(ctx, pool, db.Arquivos, silencio)
	if err != nil {
		t.Fatal(err)
	}
	if len(aplicadas) != 0 {
		t.Fatalf("segunda execução aplicou %v", aplicadas)
	}
}

func TestScriptNovoEAplicadoSozinho(t *testing.T) {
	pool := poolEmEsquemaNovo(t)
	ctx := context.Background()
	v1 := fstest.MapFS{"001_a.sql": {Data: []byte(`CREATE TABLE t_a (id int);`)}}
	if _, err := Aplicar(ctx, pool, v1, silencio); err != nil {
		t.Fatal(err)
	}
	v2 := fstest.MapFS{
		"001_a.sql": v1["001_a.sql"],
		"002_b.sql": {Data: []byte(`CREATE TABLE t_b (id int); INSERT INTO t_b VALUES (1), (2);`)},
	}
	aplicadas, err := Aplicar(ctx, pool, v2, silencio)
	if err != nil {
		t.Fatal(err)
	}
	if len(aplicadas) != 1 || aplicadas[0] != "002_b.sql" {
		t.Fatalf("aplicadas = %v, esperava só 002_b.sql", aplicadas)
	}
	if n := contar(t, pool, `SELECT count(*) FROM t_b`); n != 2 {
		t.Fatalf("t_b tem %d linhas, esperava 2 (vários comandos num script)", n)
	}
}

func TestScriptComErroFazRollbackENaoERegistrado(t *testing.T) {
	pool := poolEmEsquemaNovo(t)
	ctx := context.Background()
	scripts := fstest.MapFS{
		"001_ok.sql":    {Data: []byte(`CREATE TABLE t_ok (id int);`)},
		"002_ruim.sql":  {Data: []byte(`CREATE TABLE t_meio (id int); SELECT 1/0;`)},
		"003_nunca.sql": {Data: []byte(`CREATE TABLE t_nunca (id int);`)},
	}
	aplicadas, err := Aplicar(ctx, pool, scripts, silencio)
	if err == nil {
		t.Fatal("esperava erro do 002")
	}
	if len(aplicadas) != 1 || aplicadas[0] != "001_ok.sql" {
		t.Fatalf("aplicadas = %v, esperava só o 001", aplicadas)
	}
	if n := contar(t, pool, `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name IN ('t_meio', 't_nunca')`); n != 0 {
		t.Fatalf("o script que falhou deixou %d tabela(s) para trás", n)
	}
	if n := contar(t, pool, `SELECT count(*) FROM t_migracao WHERE versao <> '001_ok.sql'`); n != 0 {
		t.Fatal("script que falhou foi registrado como aplicado")
	}
}

func TestInstanciasEmParaleloAplicamCadaScriptUmaVezSo(t *testing.T) {
	pool := poolEmEsquemaNovo(t)
	var wg sync.WaitGroup
	erros := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// CREATE TABLE sem IF NOT EXISTS: se dois aplicassem juntos, um falharia.
			_, err := Aplicar(context.Background(), pool, fstest.MapFS{
				"001_a.sql": {Data: []byte(`CREATE TABLE t_unica (id int);`)},
			}, silencio)
			erros <- err
		}()
	}
	wg.Wait()
	close(erros)
	for err := range erros {
		if err != nil {
			t.Fatalf("instância em paralelo falhou: %v", err)
		}
	}
	if n := contar(t, pool, `SELECT count(*) FROM t_migracao`); n != 1 {
		t.Fatalf("t_migracao tem %d linhas, esperava 1", n)
	}
}

func TestPastaSemScriptsEErro(t *testing.T) {
	pool := poolEmEsquemaNovo(t)
	if _, err := Aplicar(context.Background(), pool, fstest.MapFS{"LEIAME.md": {}}, silencio); err == nil {
		t.Fatal("esperava erro para pasta sem .sql")
	}
}
