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

//go:embed vector.sql
var vector string

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
	if err = tx.QueryRowContext(ctx, `SELECT to_regclass(format('%I.workspaces',current_schema())) IS NOT NULL`).Scan(&initialized); err != nil {
		return err
	}
	if initialized {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, core); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	var installed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector' AND n.nspname='public')`).Scan(&installed); err != nil {
		return err
	}
	if installed {
		if _, err = tx.ExecContext(ctx, vector); err != nil {
			return fmt.Errorf("initialize vector schema: %w", err)
		}
	}
	return tx.Commit()
}
