// Package postgres implementa porta.Repositorio com pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

type Repositorio struct {
	pool *pgxpool.Pool
}

var _ porta.Repositorio = (*Repositorio)(nil)

func NovoRepositorio(pool *pgxpool.Pool) *Repositorio {
	return &Repositorio{pool: pool}
}

func (r *Repositorio) BuscarTemplate(ctx context.Context, nome string, versao int) (notificacao.Template, error) {
	// versao 0 = mais recente; o "OR $2 = 0" evita duas queries quase iguais
	const sql = `
		SELECT nome, versao, canal, COALESCE(assunto, ''), corpo, variaveis
		  FROM t_template
		 WHERE nome = $1 AND ($2 = 0 OR versao = $2)
		 ORDER BY versao DESC
		 LIMIT 1`
	var t notificacao.Template
	var canal string
	err := r.pool.QueryRow(ctx, sql, nome, versao).
		Scan(&t.Nome, &t.Versao, &canal, &t.Assunto, &t.Corpo, &t.Variaveis)
	if errors.Is(err, pgx.ErrNoRows) {
		return notificacao.Template{}, porta.ErrNaoEncontrado
	}
	if err != nil {
		return notificacao.Template{}, err
	}
	t.Canal = notificacao.Canal(canal)
	return t, nil
}

func (r *Repositorio) MotivoSupressao(ctx context.Context, destinatario string, canal notificacao.Canal) (string, error) {
	var motivo string
	err := r.pool.QueryRow(ctx,
		`SELECT motivo FROM t_supressao WHERE destinatario = $1 AND canal = $2`,
		destinatario, string(canal)).Scan(&motivo)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return motivo, err
}

func (r *Repositorio) Criar(ctx context.Context, n notificacao.Notificacao) (notificacao.Notificacao, bool, error) {
	// ON CONFLICT DO NOTHING + RETURNING: sem linha devolvida, a chave já existia.
	// Duas chamadas simultâneas com a mesma chave não geram duas notificações: a UNIQUE
	// (origem, chave_idempot) decide quem ganha.
	const inserir = `
		INSERT INTO t_notificacao (
			id, origem, chave_idempot, impressao, canal, prioridade, destinatario,
			template_nome, template_versao, variaveis_nomes, estado, motivo,
			expira_em, criado_em, atualizado_em)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, ''), $13, $14, $15)
		ON CONFLICT (origem, chave_idempot) DO NOTHING
		RETURNING id`
	var id string
	err := r.pool.QueryRow(ctx, inserir,
		n.ID, n.Origem, n.ChaveIdempot, n.Impressao, string(n.Canal), string(n.Prioridade),
		n.Destinatario, n.Template, n.VersaoTemplate, nomes(n.NomesVariaveis),
		string(n.Estado), n.Motivo, n.ExpiraEm, n.CriadoEm, n.AtualizadoEm,
	).Scan(&id)
	if err == nil {
		n.Variaveis = nil
		return n, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return notificacao.Notificacao{}, false, fmt.Errorf("inserir: %w", err)
	}

	existente, err := r.buscarUma(ctx, `WHERE origem = $1 AND chave_idempot = $2`, n.Origem, n.ChaveIdempot)
	if err != nil {
		return notificacao.Notificacao{}, false, fmt.Errorf("buscar existente: %w", err)
	}
	return existente, false, nil
}

func (r *Repositorio) Buscar(ctx context.Context, id string) (notificacao.Notificacao, error) {
	if !pareceUUID(id) {
		// evita o erro de cast do Postgres para um id que nunca existiria
		return notificacao.Notificacao{}, porta.ErrNaoEncontrado
	}
	return r.buscarUma(ctx, `WHERE id = $1`, id)
}

func (r *Repositorio) MudarEstado(ctx context.Context, id string, de, para notificacao.Estado, motivo string, em time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE t_notificacao
		   SET estado = $3, motivo = NULLIF($4, ''), atualizado_em = $5
		 WHERE id = $1 AND estado = $2`,
		id, string(de), string(para), motivo, em)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// nenhuma linha: ou o id não existe, ou o estado já mudou
	var existe bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM t_notificacao WHERE id = $1)`, id).Scan(&existe); err != nil {
		return err
	}
	if !existe {
		return porta.ErrNaoEncontrado
	}
	return porta.ErrConcorrencia
}

func (r *Repositorio) buscarUma(ctx context.Context, onde string, args ...any) (notificacao.Notificacao, error) {
	sql := `
		SELECT id, origem, chave_idempot, impressao, canal, prioridade, destinatario,
		       template_nome, template_versao, variaveis_nomes, estado, COALESCE(motivo, ''),
		       expira_em, criado_em, atualizado_em
		  FROM t_notificacao ` + onde
	var (
		n                         notificacao.Notificacao
		canal, prioridade, estado string
	)
	err := r.pool.QueryRow(ctx, sql, args...).Scan(
		&n.ID, &n.Origem, &n.ChaveIdempot, &n.Impressao, &canal, &prioridade, &n.Destinatario,
		&n.Template, &n.VersaoTemplate, &n.NomesVariaveis, &estado, &n.Motivo,
		&n.ExpiraEm, &n.CriadoEm, &n.AtualizadoEm)
	if errors.Is(err, pgx.ErrNoRows) {
		return notificacao.Notificacao{}, porta.ErrNaoEncontrado
	}
	if err != nil {
		return notificacao.Notificacao{}, err
	}
	n.Canal = notificacao.Canal(canal)
	n.Prioridade = notificacao.Prioridade(prioridade)
	n.Estado = notificacao.Estado(estado)
	n.CriadoEm, n.AtualizadoEm = n.CriadoEm.UTC(), n.AtualizadoEm.UTC()
	if n.ExpiraEm != nil {
		u := n.ExpiraEm.UTC()
		n.ExpiraEm = &u
	}
	return n, nil
}

// nomes garante array vazio em vez de NULL na coluna NOT NULL.
func nomes(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func pareceUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}
