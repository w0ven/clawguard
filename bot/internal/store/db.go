package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Pinger interface {
	Ping(context.Context) error
}

type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Queries struct {
	db DBTX
}

func New(db DBTX) *Queries {
	return &Queries{db: db}
}

func (q *Queries) WithTx(tx pgx.Tx) *Queries {
	return &Queries{db: tx}
}

func (q *Queries) Transact(ctx context.Context, fn func(*Queries) error) error {
	beginner, ok := q.db.(Beginner)
	if !ok {
		return fmt.Errorf("store db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := fn(q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func (q *Queries) Ping(ctx context.Context) error {
	pinger, ok := q.db.(Pinger)
	if !ok {
		return nil
	}
	return pinger.Ping(ctx)
}
