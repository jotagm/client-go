// Package migracao aplica os scripts SQL numerados (db/NNN_*.sql) uma única vez cada, em
// ordem, e guarda o que já rodou em t_migracao.
//
// É propositalmente pequeno: só para frente, um script por transação, sem "desfazer". Quem
// quiser voltar uma versão da imagem volta a imagem; o esquema só ganha colunas e tabelas,
// então a versão anterior continua funcionando em cima dele.
package migracao

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// chaveTrava é o número do advisory lock: duas instâncias subindo juntas não aplicam o
// mesmo script ao mesmo tempo (a segunda espera e depois vê que já foi aplicado).
const chaveTrava int64 = 0x636c69656e74676f // "clientgo"

// Aplicar roda os scripts ainda não registrados e devolve os nomes dos que aplicou.
func Aplicar(ctx context.Context, pool *pgxpool.Pool, scripts fs.FS, log *slog.Logger) ([]string, error) {
	nomes, err := listar(scripts)
	if err != nil {
		return nil, err
	}

	// A trava vale por conexão: segura uma conexão só para isso até o fim.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrações: obter conexão: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, chaveTrava); err != nil {
		return nil, fmt.Errorf("migrações: travar: %w", err)
	}
	defer func() {
		// Contexto novo: se o ctx foi cancelado no meio, a trava ainda precisa ser solta.
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, chaveTrava)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS t_migracao (
		versao      VARCHAR(200) PRIMARY KEY,
		aplicada_em TIMESTAMPTZ  NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, fmt.Errorf("migrações: criar t_migracao: %w", err)
	}

	var aplicadas []string
	for _, nome := range nomes {
		var existe bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM t_migracao WHERE versao = $1)`, nome).Scan(&existe); err != nil {
			return aplicadas, fmt.Errorf("migrações: consultar %s: %w", nome, err)
		}
		if existe {
			continue
		}
		sql, err := fs.ReadFile(scripts, nome)
		if err != nil {
			return aplicadas, fmt.Errorf("migrações: ler %s: %w", nome, err)
		}
		if err := aplicarUm(ctx, conn.Conn(), nome, string(sql)); err != nil {
			return aplicadas, err
		}
		log.Info("migração aplicada", "script", nome)
		aplicadas = append(aplicadas, nome)
	}
	if len(aplicadas) == 0 {
		log.Info("banco já está atualizado", "scripts", len(nomes))
	}
	return aplicadas, nil
}

// aplicarUm roda o script e registra a versão na mesma transação: ou os dois acontecem, ou
// nenhum. Sem argumentos, o pgx usa o protocolo simples, que aceita vários comandos por texto.
func aplicarUm(ctx context.Context, conn *pgx.Conn, nome, sql string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrações: iniciar transação de %s: %w", nome, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("migrações: %s: %w", nome, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO t_migracao (versao) VALUES ($1)`, nome); err != nil {
		return fmt.Errorf("migrações: registrar %s: %w", nome, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrações: confirmar %s: %w", nome, err)
	}
	return nil
}

// listar devolve os .sql da raiz do sistema de arquivos, em ordem de nome (001_, 002_...).
func listar(scripts fs.FS) ([]string, error) {
	entradas, err := fs.ReadDir(scripts, ".")
	if err != nil {
		return nil, fmt.Errorf("migrações: listar scripts: %w", err)
	}
	var nomes []string
	for _, e := range entradas {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			nomes = append(nomes, e.Name())
		}
	}
	sort.Strings(nomes)
	if len(nomes) == 0 {
		return nil, fmt.Errorf("migrações: nenhum script .sql encontrado")
	}
	return nomes, nil
}
