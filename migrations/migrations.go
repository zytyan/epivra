package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed 001_init.sql
var initial string

func Apply(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(684923541)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=1`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err = tx.ExecContext(ctx, initial); err != nil {
			return fmt.Errorf("migration 1: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES(1)`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
