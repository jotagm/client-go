package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

// Os testes rodam contra um Postgres de verdade, num schema novo a cada execução.
// Sem AVISO_TESTE_DB_URL, são pulados:
//
//	AVISO_TESTE_DB_URL=postgres://aviso:aviso@localhost:5433/aviso go test ./...
func novoRepo(t *testing.T) *Repositorio {
	t.Helper()
	url := os.Getenv("AVISO_TESTE_DB_URL")
	if url == "" {
		t.Skip("AVISO_TESTE_DB_URL não definida")
	}
	ctx := context.Background()

	schema := fmt.Sprintf("teste_%d", time.Now().UnixNano())
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	scripts, _ := filepath.Glob("../../../db/*.sql")
	sort.Strings(scripts)
	if len(scripts) == 0 {
		t.Fatal("nenhum script em db/")
	}
	for _, arquivo := range scripts {
		sql, err := os.ReadFile(arquivo)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", arquivo, err)
		}
	}
	return NovoRepositorio(pool)
}

func notificacaoDeTeste(chave string) notificacao.Notificacao {
	agora := time.Now().UTC().Truncate(time.Microsecond) // precisão do timestamptz
	expira := agora.Add(10 * time.Minute)
	return notificacao.Notificacao{
		ID: notificacao.NovoID(), Origem: "booklit", ChaveIdempot: chave,
		Impressao: fmt.Sprintf("%064d", 1), Canal: notificacao.CanalEmail,
		Prioridade: notificacao.PrioridadeAlta, Destinatario: "jota@exemplo.com",
		Template: "otp-login", VersaoTemplate: 1,
		Variaveis:      map[string]string{"codigo": "482913"},
		NomesVariaveis: []string{"codigo"},
		Estado:         notificacao.EstadoAceita, ExpiraEm: &expira,
		CriadoEm: agora, AtualizadoEm: agora,
	}
}

func TestTemplatesDoSeed(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()

	for _, nome := range []string{"otp-login", "otp-verificar-email", "otp-redefinir-pin"} {
		tpl, err := repo.BuscarTemplate(ctx, nome, 0)
		if err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
		if tpl.Canal != notificacao.CanalEmail || len(tpl.Variaveis) != 1 || tpl.Variaveis[0] != "codigo" {
			t.Errorf("%s: %+v", nome, tpl)
		}
	}
	if _, err := repo.BuscarTemplate(ctx, "otp-login", 99); !errors.Is(err, porta.ErrNaoEncontrado) {
		t.Errorf("versão inexistente: %v", err)
	}
}

func TestVersaoMaisRecente(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()
	_, err := repo.pool.Exec(ctx, `INSERT INTO t_template (nome, versao, canal, corpo, variaveis)
		VALUES ('otp-login', 2, 'email', 'v2 {{.codigo}}', ARRAY['codigo'])`)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := repo.BuscarTemplate(ctx, "otp-login", 0)
	if err != nil || tpl.Versao != 2 {
		t.Fatalf("versão %d, err %v", tpl.Versao, err)
	}
	tpl, err = repo.BuscarTemplate(ctx, "otp-login", 1)
	if err != nil || tpl.Versao != 1 {
		t.Fatalf("pedindo v1 veio v%d, err %v", tpl.Versao, err)
	}
}

func TestCriarEBuscarIdaEVolta(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()
	n := notificacaoDeTeste("desafio-1")

	salva, criada, err := repo.Criar(ctx, n)
	if err != nil || !criada {
		t.Fatalf("criada=%v err=%v", criada, err)
	}
	if salva.Variaveis != nil {
		t.Error("Criar devolveu os valores das variáveis")
	}

	lida, err := repo.Buscar(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lida.Variaveis != nil {
		t.Errorf("valores das variáveis voltaram do banco: %v", lida.Variaveis)
	}
	if lida.Impressao != n.Impressao || lida.Destinatario != n.Destinatario ||
		lida.Estado != n.Estado || lida.Prioridade != n.Prioridade || lida.Canal != n.Canal ||
		!lida.CriadoEm.Equal(n.CriadoEm) || lida.ExpiraEm == nil || !lida.ExpiraEm.Equal(*n.ExpiraEm) ||
		len(lida.NomesVariaveis) != 1 || lida.NomesVariaveis[0] != "codigo" {
		t.Errorf("ida e volta diferente:\n gravada %+v\n lida    %+v", n, lida)
	}
}

func TestCodigoNaoVaiParaOBanco(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()
	if _, _, err := repo.Criar(ctx, notificacaoDeTeste("desafio-1")); err != nil {
		t.Fatal(err)
	}
	var achou bool
	err := repo.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM t_notificacao n WHERE n::text LIKE '%482913%')`).Scan(&achou)
	if err != nil {
		t.Fatal(err)
	}
	if achou {
		t.Fatal("o código foi gravado em t_notificacao")
	}
}

func TestCriarRepetidoDevolveExistente(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()
	primeira := notificacaoDeTeste("desafio-1")
	if _, _, err := repo.Criar(ctx, primeira); err != nil {
		t.Fatal(err)
	}

	segunda := notificacaoDeTeste("desafio-1") // outro ID, mesma chave
	existente, criada, err := repo.Criar(ctx, segunda)
	if err != nil {
		t.Fatal(err)
	}
	if criada || existente.ID != primeira.ID {
		t.Fatalf("criada=%v id=%s, esperava a existente %s", criada, existente.ID, primeira.ID)
	}

	outraOrigem := notificacaoDeTeste("desafio-1")
	outraOrigem.Origem = "uptime"
	if _, criada, err := repo.Criar(ctx, outraOrigem); err != nil || !criada {
		t.Fatalf("mesma chave em outra origem: criada=%v err=%v", criada, err)
	}
}

func TestCriarConcorrente(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()

	const n = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		criadas int
		ids     = map[string]bool{}
	)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			salva, criada, err := repo.Criar(ctx, notificacaoDeTeste("mesma-chave"))
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids[salva.ID] = true
			if criada {
				criadas++
			}
		}()
	}
	wg.Wait()
	if criadas != 1 || len(ids) != 1 {
		t.Fatalf("%d criadas e %d ids distintos; esperava 1 e 1", criadas, len(ids))
	}
}

func TestMudarEstado(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()
	n := notificacaoDeTeste("desafio-1")
	if _, _, err := repo.Criar(ctx, n); err != nil {
		t.Fatal(err)
	}

	em := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.MudarEstado(ctx, n.ID, notificacao.EstadoAceita, notificacao.EstadoFalhou, "publicacao", em); err != nil {
		t.Fatal(err)
	}
	lida, _ := repo.Buscar(ctx, n.ID)
	if lida.Estado != notificacao.EstadoFalhou || lida.Motivo != "publicacao" || !lida.AtualizadoEm.Equal(em) {
		t.Errorf("depois de mudar: %+v", lida)
	}

	// estado já mudou: trava otimista
	err := repo.MudarEstado(ctx, n.ID, notificacao.EstadoAceita, notificacao.EstadoEnviada, "", em)
	if !errors.Is(err, porta.ErrConcorrencia) {
		t.Errorf("esperava ErrConcorrencia, veio %v", err)
	}
	err = repo.MudarEstado(ctx, notificacao.NovoID(), notificacao.EstadoAceita, notificacao.EstadoEnviada, "", em)
	if !errors.Is(err, porta.ErrNaoEncontrado) {
		t.Errorf("id inexistente: esperava ErrNaoEncontrado, veio %v", err)
	}
}

func TestBuscarIdInvalido(t *testing.T) {
	repo := novoRepo(t)
	if _, err := repo.Buscar(context.Background(), "nao-e-uuid"); !errors.Is(err, porta.ErrNaoEncontrado) {
		t.Fatalf("esperava ErrNaoEncontrado, veio %v", err)
	}
}

func TestSupressao(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()
	if motivo, err := repo.MotivoSupressao(ctx, "jota@exemplo.com", notificacao.CanalEmail); err != nil || motivo != "" {
		t.Fatalf("motivo=%q err=%v", motivo, err)
	}
	_, err := repo.pool.Exec(ctx, `INSERT INTO t_supressao (destinatario, canal, motivo)
		VALUES ('jota@exemplo.com', 'email', 'opt_out')`)
	if err != nil {
		t.Fatal(err)
	}
	if motivo, _ := repo.MotivoSupressao(ctx, "jota@exemplo.com", notificacao.CanalEmail); motivo != "opt_out" {
		t.Errorf("motivo=%q", motivo)
	}
	if motivo, _ := repo.MotivoSupressao(ctx, "jota@exemplo.com", notificacao.CanalPush); motivo != "" {
		t.Errorf("supressão de e-mail vazou para push: %q", motivo)
	}
}

func TestMudarEstadoRecusaTransicaoInvalida(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()
	n := notificacaoDeTeste("desafio-1")
	if _, _, err := repo.Criar(ctx, n); err != nil {
		t.Fatal(err)
	}
	em := time.Now().UTC().Truncate(time.Microsecond)

	// aceita -> entregue pula o "enviada": só o recibo do provedor leva a "entregue"
	err := repo.MudarEstado(ctx, n.ID, notificacao.EstadoAceita, notificacao.EstadoEntregue, "", em)
	if !errors.Is(err, notificacao.ErrTransicaoInvalida) {
		t.Fatalf("esperava ErrTransicaoInvalida, veio %v", err)
	}
	lida, _ := repo.Buscar(ctx, n.ID)
	if lida.Estado != notificacao.EstadoAceita {
		t.Errorf("a transição inválida mudou o estado para %s", lida.Estado)
	}
}

func TestTentativas(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()
	n := notificacaoDeTeste("desafio-1")
	if _, _, err := repo.Criar(ctx, n); err != nil {
		t.Fatal(err)
	}
	quando := time.Now().UTC().Truncate(time.Microsecond)

	primeira := porta.Tentativa{
		NotificacaoID: n.ID, Provedor: "ses", StatusHTTP: 429, Resultado: porta.ResultadoTemporario,
		Motivo: "throttling", Resposta: "Rate exceeded", Latencia: 83 * time.Millisecond, TentadoEm: quando,
	}
	segunda := porta.Tentativa{
		NotificacaoID: n.ID, Provedor: "ses", IDNoProvedor: "0100018c-abc", Resultado: porta.ResultadoSucesso,
		Latencia: 120 * time.Millisecond, TentadoEm: quando.Add(2 * time.Second),
	}
	for _, tt := range []porta.Tentativa{primeira, segunda} {
		if err := repo.RegistrarTentativa(ctx, tt); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.Tentativas(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	primeira.Numero, segunda.Numero = 1, 2
	if len(got) != 2 || got[0] != primeira || got[1] != segunda {
		t.Fatalf("got = %+v\nquer = %+v e %+v", got, primeira, segunda)
	}
}

func TestTentativaDeNotificacaoInexistente(t *testing.T) {
	repo := novoRepo(t)
	err := repo.RegistrarTentativa(context.Background(), porta.Tentativa{
		NotificacaoID: notificacao.NovoID(), Provedor: "ses", Resultado: porta.ResultadoSucesso, TentadoEm: time.Now(),
	})
	if !errors.Is(err, porta.ErrNaoEncontrado) {
		t.Fatalf("esperava ErrNaoEncontrado, veio %v", err)
	}
}

// Vários workers registrando a mesma notificação ao mesmo tempo: nenhum número repetido,
// nenhuma tentativa perdida.
func TestTentativasConcorrentes(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()
	n := notificacaoDeTeste("desafio-1")
	if _, _, err := repo.Criar(ctx, n); err != nil {
		t.Fatal(err)
	}

	const workers = 4
	var wg sync.WaitGroup
	erros := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			erros <- repo.RegistrarTentativa(ctx, porta.Tentativa{
				NotificacaoID: n.ID, Provedor: "ses", Resultado: porta.ResultadoSucesso, TentadoEm: time.Now().UTC(),
			})
		}()
	}
	wg.Wait()
	close(erros)
	for err := range erros {
		if err != nil {
			t.Error(err)
		}
	}

	got, err := repo.Tentativas(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != workers {
		t.Fatalf("tentativas = %d, queria %d", len(got), workers)
	}
	for i, tt := range got {
		if tt.Numero != i+1 {
			t.Errorf("posição %d tem número %d", i, tt.Numero)
		}
	}
}

// Uma chamada local ao provedor leva menos de 1 ms. A latência não pode virar NULL por isso:
// NULL quer dizer "o provedor nem foi chamado" (renderização recusada).
func TestTentativaRapidaGuardaLatenciaDeAoMenos1ms(t *testing.T) {
	repo := novoRepo(t)
	ctx := context.Background()
	n := notificacaoDeTeste("desafio-1")
	if _, _, err := repo.Criar(ctx, n); err != nil {
		t.Fatal(err)
	}
	agora := time.Now().UTC()
	for _, tt := range []porta.Tentativa{
		{NotificacaoID: n.ID, Provedor: "ses", Resultado: porta.ResultadoSucesso, Latencia: 300 * time.Microsecond, TentadoEm: agora},
		{NotificacaoID: n.ID, Provedor: "ses", Resultado: porta.ResultadoPermanente, Motivo: "dados_invalidos", TentadoEm: agora}, // sem chamada
	} {
		if err := repo.RegistrarTentativa(ctx, tt); err != nil {
			t.Fatal(err)
		}
	}
	var nulos []bool
	linhas, err := repo.pool.Query(ctx, `SELECT latencia_ms IS NULL FROM t_tentativa_envio ORDER BY numero`)
	if err != nil {
		t.Fatal(err)
	}
	defer linhas.Close()
	for linhas.Next() {
		var nulo bool
		if err := linhas.Scan(&nulo); err != nil {
			t.Fatal(err)
		}
		nulos = append(nulos, nulo)
	}
	if len(nulos) != 2 || nulos[0] || !nulos[1] {
		t.Errorf("latencia_ms NULL = %v; queria [false true]", nulos)
	}
}
