package hub

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"
)

type Job struct {
	ID                    int64
	WorkspaceID, ObjectID string
	Version, Attempts     int
	Fingerprint, Token    string
}

func (s *Service) ConfigureIndex(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(684923542)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE index_configs SET active=false WHERE active`); err != nil {
		return err
	}
	ready, err := vectorReady(ctx, tx)
	if err != nil {
		return err
	}
	if s.Embed != nil && !ready {
		slog.Warn("semantic indexing disabled: pgvector is not enabled; keyword search remains available")
	}
	if s.Embed != nil && ready {
		if err = s.Embed.Validate(); err != nil {
			return err
		}
		fp := s.Embed.Fingerprint()
		if _, err = tx.ExecContext(ctx, `INSERT INTO index_configs(fingerprint,model,dimensions,active) VALUES($1,$2,$3,true) ON CONFLICT(fingerprint) DO UPDATE SET active=true`, fp, s.Embed.Model, s.Embed.Dimensions); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO index_jobs(workspace_id,object_id,version,fingerprint) SELECT workspace_id,id,current_version,$1 FROM objects WHERE kind IN ('source','claim','knowledge') ON CONFLICT DO NOTHING`, fp); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Service) ClaimJob(ctx context.Context) (Job, error) {
	j := Job{Token: NewID()}
	if s.Embed == nil {
		return j, sql.ErrNoRows
	}
	ready, err := vectorReady(ctx, s.DB)
	if err != nil {
		return j, err
	}
	if !ready {
		return j, sql.ErrNoRows
	}
	// Exhausted leases are terminal even if a process died before recording failure.
	if _, err := s.DB.ExecContext(ctx, `UPDATE index_jobs SET state='failed',last_error='lease expired after maximum attempts' WHERE state='running' AND lease_until<now() AND attempts>=5`); err != nil {
		return j, err
	}
	err = s.DB.QueryRowContext(ctx, `WITH candidate AS (
 SELECT j.id FROM index_jobs j JOIN index_configs c ON c.fingerprint=j.fingerprint AND c.active
 WHERE j.fingerprint=$1 AND j.attempts<5 AND ((j.state='pending' AND j.available_at<=now()) OR (j.state='running' AND j.lease_until<now())) ORDER BY j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1)
 UPDATE index_jobs j SET state='running',attempts=attempts+1,lease_until=now()+interval '2 minutes',lease_token=$2 FROM candidate c WHERE j.id=c.id RETURNING j.id,j.workspace_id,j.object_id,j.version,j.fingerprint,j.attempts`, s.Embed.Fingerprint(), j.Token).Scan(&j.ID, &j.WorkspaceID, &j.ObjectID, &j.Version, &j.Fingerprint, &j.Attempts)
	return j, err
}
func (s *Service) RunJob(ctx context.Context, j Job) error {
	ready, err := vectorReady(ctx, s.DB)
	if err != nil {
		return err
	}
	if !ready {
		return fail("semantic_unavailable", "pgvector is not enabled")
	}
	// A lease heartbeat is separate from the external API call; ownership is checked again on commit.
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-taskCtx.Done():
				return
			case <-ticker.C:
				res, err := s.DB.ExecContext(taskCtx, `UPDATE index_jobs SET lease_until=now()+interval '2 minutes' WHERE id=$1 AND lease_token=$2 AND state='running'`, j.ID, j.Token)
				if err != nil {
					cancel()
					return
				}
				n, _ := res.RowsAffected()
				if n == 0 {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	var body string
	var current int
	err = s.DB.QueryRowContext(taskCtx, `SELECT r.body||E'\n'||r.conditions,o.current_version FROM revisions r JOIN objects o ON (o.workspace_id,o.id)=(r.workspace_id,r.object_id) WHERE r.workspace_id=$1 AND r.object_id=$2 AND r.version=$3`, j.WorkspaceID, j.ObjectID, j.Version).Scan(&body, &current)
	chunks := Split(body)
	var vectors [][]float64
	if err == nil && current == j.Version {
		vectors, err = s.Embed.Vectors(taskCtx, chunkTexts(chunks))
	}
	if err != nil {
		return s.failJob(ctx, j, err)
	}
	tx, err := s.DB.BeginTx(taskCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var token string
	var active bool
	if err = tx.QueryRowContext(taskCtx, `SELECT lease_token FROM index_jobs WHERE id=$1 AND state='running' AND lease_until>now() FOR UPDATE`, j.ID).Scan(&token); err != nil {
		return err
	}
	if token != j.Token {
		return fail("lease_lost", "index job lease changed")
	}
	if err = tx.QueryRowContext(taskCtx, `SELECT current_version FROM objects WHERE workspace_id=$1 AND id=$2 FOR SHARE`, j.WorkspaceID, j.ObjectID).Scan(&current); err != nil {
		return err
	}
	if err = tx.QueryRowContext(taskCtx, `SELECT active FROM index_configs WHERE fingerprint=$1 FOR SHARE`, j.Fingerprint).Scan(&active); err != nil {
		return err
	}
	if current == j.Version && active {
		if _, err = tx.ExecContext(taskCtx, `DELETE FROM chunks WHERE workspace_id=$1 AND object_id=$2 AND version=$3 AND fingerprint=$4`, j.WorkspaceID, j.ObjectID, j.Version, j.Fingerprint); err != nil {
			return err
		}
		for i, c := range chunks {
			if _, err = tx.ExecContext(taskCtx, `INSERT INTO chunks(workspace_id,object_id,version,fingerprint,position,start_rune,content,embedding) VALUES($1,$2,$3,$4,$5,$6,$7,$8::public.vector)`, j.WorkspaceID, j.ObjectID, j.Version, j.Fingerprint, c.Position, c.Start, c.Text, marshal(vectors[i])); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(taskCtx, `UPDATE index_jobs SET state='done',lease_until=NULL,last_error=NULL WHERE id=$1 AND lease_token=$2`, j.ID, j.Token); err != nil {
		return err
	}
	return tx.Commit()
}
func chunkTexts(chunks []Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Text
	}
	return out
}
func (s *Service) failJob(ctx context.Context, j Job, cause error) error {
	slog.Warn("embedding job failed", "job_id", j.ID, "attempt", j.Attempts, "error", cause)
	_, err := s.DB.ExecContext(ctx, `UPDATE index_jobs SET state=CASE WHEN attempts>=5 THEN 'failed' ELSE 'pending' END, available_at=now()+($3*interval '1 second'),lease_until=NULL,last_error='embedding unavailable' WHERE id=$1 AND lease_token=$2 AND state='running'`, j.ID, j.Token, 1<<min(j.Attempts, 8))
	if err != nil {
		return err
	}
	return cause
}
func (s *Service) Worker(ctx context.Context) {
	if s.Embed == nil {
		return
	}
	ready, err := vectorReady(ctx, s.DB)
	if err != nil {
		slog.Error("vector capability check failed", "error", err)
		return
	}
	if !ready {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastLog := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j, err := s.ClaimJob(ctx)
			if err == nil {
				if err = s.RunJob(ctx, j); err != nil && !errors.Is(err, context.Canceled) {
					slog.Warn("index task incomplete", "job_id", j.ID, "error", err)
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				slog.Error("index claim failed", "error", err)
			}
			if time.Since(lastLog) > time.Minute {
				var backlog int
				if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM index_jobs WHERE state IN ('pending','running','failed') AND fingerprint=$1`, s.Embed.Fingerprint()).Scan(&backlog); err == nil {
					slog.Info("index backlog", "jobs", backlog)
				}
				lastLog = time.Now()
			}
		}
	}
}
