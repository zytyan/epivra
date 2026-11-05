// Package schema initializes fresh test databases. It does not upgrade existing schemas.
package schema

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed core.sql
var core string

func Initialize(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(684923541)`); err != nil {
		return err
	}
	var initialized bool
	if err = tx.QueryRowContext(ctx, `SELECT to_regclass(format('%I.knowledge',current_schema())) IS NOT NULL`).Scan(&initialized); err != nil {
		return err
	}
	if initialized {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, core); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	return tx.Commit()
}
