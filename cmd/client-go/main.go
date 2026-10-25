// Comando client-go: sobe a API HTTP e os consumidores das filas no mesmo processo.
//
// Variáveis de ambiente:
//
//	AVISO_ENDERECO    endereço da API (padrão :8081; 8080 é do Booklit)
//	AVISO_DB_URL      postgres://usuario:senha@host:porta/banco
//	AVISO_TOKENS      origem=token separados por vírgula, ex.: booklit=<32+ caracteres>
//	AVISO_TOPICO_ARN  tópico SNS; vazio = publicador em memória (nada sai do processo)
//	AVISO_FILAS       nome=url separados por vírgula, ex.: email-alta=http://...,email=http://...
//	AVISO_WORKERS     workers por fila (padrão 4)
//	AWS_REGION        padrão us-east-1
//	AWS_ENDPOINT_URL  aponta o SDK para o Floci (http://localhost:4566)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awssns "github.com/aws/aws-sdk-go-v2/service/sns"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jotagm/client-go/internal/adapter/memoria"
	"github.com/jotagm/client-go/internal/adapter/postgres"
	"github.com/jotagm/client-go/internal/adapter/ses"
	"github.com/jotagm/client-go/internal/adapter/sns"
	"github.com/jotagm/client-go/internal/adapter/sqs"
	"github.com/jotagm/client-go/internal/api"
	"github.com/jotagm/client-go/internal/aplicacao"
	"github.com/jotagm/client-go/internal/porta"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := rodar(log); err != nil {
		log.Error("client-go parou", "erro", err)
		os.Exit(1)
	}
}

func rodar(log *slog.Logger) error {
	cfg, err := lerConfig()
	if err != nil {
		return err
	}

	ctx, parar := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer parar()

	pool, err := conectar(ctx, cfg.dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	repo := postgres.NovoRepositorio(pool)

	var awsCfg aws.Config
	if cfg.topico != "" || len(cfg.filas) > 0 || cfg.provedor == provedorSES {
		awsCfg, err = awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(envOu("AWS_REGION", "us-east-1")))
		if err != nil {
			return fmt.Errorf("configuração da AWS: %w", err)
		}
	}

	var publicador porta.Publicador
	if cfg.topico != "" {
		publicador = sns.NovoPublicador(awssns.NewFromConfig(awsCfg), cfg.topico)
		log.Info("publicando no SNS", "topico", cfg.topico)
	} else {
		publicador = memoria.NovoPublicador()
		log.Warn("AVISO_TOPICO_ARN vazio: publicador em memória, nenhuma notificação sai do processo")
	}

	var entregador porta.Entregador
	switch cfg.provedor {
	case provedorSES:
		sesProvedor := ses.Novo(ses.NovoCliente(awsCfg), cfg.sesRemetente, cfg.sesConjunto, 0)
		entregador = aplicacao.NovoEntregadorEmail(repo, repo, sesProvedor, log)
		log.Info("entrega pelo SES", "remetente", cfg.sesRemetente, "configuration_set", cfg.sesConjunto)
	default:
		entregador = memoria.EntregaSimulada{Log: log}
		if len(cfg.filas) > 0 {
			log.Warn("entrega simulada: as notificações viram 'enviada' sem e-mail de verdade (AVISO_PROVEDOR=ses liga o SES)")
		}
	}

	servico := aplicacao.NovoServico(repo, publicador, log)
	processador := aplicacao.NovoProcessador(repo, entregador, aplicacao.RetentativaPadrao(), log)

	var wg sync.WaitGroup
	clienteSQS := (*awssqs.Client)(nil)
	if len(cfg.filas) > 0 {
		clienteSQS = awssqs.NewFromConfig(awsCfg)
	}
	for _, f := range cfg.filas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			aplicacao.Consumir(ctx, f.nome, sqs.NovoConsumidor(clienteSQS, f.url), processador, cfg.workers, log)
		}()
	}

	srv := &http.Server{
		Addr:              cfg.endereco,
		Handler:           api.Rotas(servico, cfg.tokens, log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	erroServidor := make(chan error, 1)
	go func() {
		log.Info("client-go no ar", "endereco", cfg.endereco)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			erroServidor <- err
		}
	}()

	var errFinal error
	select {
	case errFinal = <-erroServidor:
		parar() // derruba os consumidores também
	case <-ctx.Done():
	}

	desligar, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelar()
	if err := srv.Shutdown(desligar); err != nil {
		errFinal = errors.Join(errFinal, fmt.Errorf("desligamento da API: %w", err))
	}
	wg.Wait() // espera os workers terminarem o que já pegaram
	log.Info("client-go desligado")
	return errFinal
}

type fila struct{ nome, url string }

type config struct {
	endereco string
	dbURL    string
	tokens   api.Tokens
	topico   string
	filas    []fila
	workers  int

	provedor     string // "simulado" (padrão) ou "ses"
	sesRemetente string
	sesConjunto  string
}

const (
	provedorSimulado = "simulado"
	provedorSES      = "ses"
)

func lerConfig() (config, error) {
	cfg := config{
		endereco: envOu("AVISO_ENDERECO", ":8081"),
		dbURL:    os.Getenv("AVISO_DB_URL"),
		topico:   os.Getenv("AVISO_TOPICO_ARN"),

		provedor:     strings.ToLower(envOu("AVISO_PROVEDOR", provedorSimulado)),
		sesRemetente: strings.TrimSpace(os.Getenv("AVISO_SES_REMETENTE")),
		sesConjunto:  strings.TrimSpace(os.Getenv("AVISO_SES_CONJUNTO")),
	}
	if cfg.dbURL == "" {
		return cfg, errors.New("AVISO_DB_URL não definida")
	}
	var err error
	if cfg.tokens, err = api.LerTokens(os.Getenv("AVISO_TOKENS")); err != nil {
		return cfg, fmt.Errorf("AVISO_TOKENS: %w", err)
	}
	if cfg.filas, err = lerFilas(os.Getenv("AVISO_FILAS")); err != nil {
		return cfg, fmt.Errorf("AVISO_FILAS: %w", err)
	}
	switch cfg.provedor {
	case provedorSimulado:
	case provedorSES:
		if cfg.sesRemetente == "" {
			return cfg, errors.New("AVISO_PROVEDOR=ses precisa de AVISO_SES_REMETENTE (ex.: Booklit <codigo@booklit.bio>)")
		}
		if strings.ContainsAny(cfg.sesRemetente, "\r\n") {
			return cfg, errors.New("AVISO_SES_REMETENTE não pode ter quebra de linha")
		}
	default:
		return cfg, fmt.Errorf("AVISO_PROVEDOR %q desconhecido: use %s ou %s", cfg.provedor, provedorSimulado, provedorSES)
	}
	cfg.workers, err = strconv.Atoi(envOu("AVISO_WORKERS", "4"))
	if err != nil || cfg.workers < 1 || cfg.workers > 64 {
		return cfg, errors.New("AVISO_WORKERS precisa ser um número entre 1 e 64")
	}
	return cfg, nil
}

func lerFilas(s string) ([]fila, error) {
	vistas := map[string]bool{}
	var filas []fila
	for _, par := range strings.Split(s, ",") {
		par = strings.TrimSpace(par)
		if par == "" {
			continue
		}
		nome, url, ok := strings.Cut(par, "=")
		nome, url = strings.TrimSpace(nome), strings.TrimSpace(url)
		if !ok || nome == "" || !strings.HasPrefix(url, "http") {
			return nil, fmt.Errorf("use nome=url: %q", par)
		}
		if vistas[nome] {
			return nil, fmt.Errorf("fila %q repetida", nome)
		}
		vistas[nome] = true
		filas = append(filas, fila{nome: nome, url: url})
	}
	sort.Slice(filas, func(i, j int) bool { return filas[i].nome < filas[j].nome })
	return filas, nil
}

func conectar(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("AVISO_DB_URL inválida: %w", err)
	}
	cfg.MaxConns = 5 // a EC2 é pequena e o Postgres é dividido com o Booklit

	ctxConexao, cancelar := context.WithTimeout(ctx, 10*time.Second)
	defer cancelar()
	pool, err := pgxpool.NewWithConfig(ctxConexao, cfg)
	if err != nil {
		return nil, fmt.Errorf("conectar ao Postgres: %w", err)
	}
	if err := pool.Ping(ctxConexao); err != nil {
		pool.Close()
		return nil, fmt.Errorf("Postgres não respondeu: %w", err)
	}
	return pool, nil
}

func envOu(nome, padrao string) string {
	if v := os.Getenv(nome); v != "" {
		return v
	}
	return padrao
}
