package source

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ianfoxdev/money-checks/internal/config"
)

type postgres struct {
	pool *pgxpool.Pool
}

func openPostgres(ctx context.Context, p config.Postgres) (*postgres, error) {
	dsn, err := config.Getenv(p.DSNEnv)
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	// The extended protocol runs one statement per query. The simple protocol would
	// run "select 1; commit; delete ..." and the delete would be outside the
	// read-only transaction, so a DSN cannot turn it on.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return &postgres{pool: pool}, nil
}

// Query runs the check in its own READ ONLY transaction with the check's timeout
// as statement_timeout, and rolls it back. Values come back as text, so numeric and
// float8 are never read through a Go float; NULL is nil.
func (p *postgres) Query(ctx context.Context, ch config.Check, each func(Row) error) error {
	ctx, cancel := context.WithTimeout(ctx, ch.Timeout)
	defer cancel()
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, fmt.Sprintf("set local statement_timeout = %d", ch.Timeout.Milliseconds())); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, ch.Query, pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		return err
	}
	defer rows.Close()
	var columns []string
	for _, f := range rows.FieldDescriptions() {
		columns = append(columns, f.Name)
	}
	for rows.Next() {
		raw := rows.RawValues()
		values := make([]any, len(raw))
		for i, v := range raw {
			if v != nil {
				values[i] = string(v)
			}
		}
		if err := each(Row{Columns: columns, Values: values}); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (p *postgres) Close(context.Context) error {
	p.pool.Close()
	return nil
}
