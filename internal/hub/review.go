package hub

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

func lockClaims(ctx context.Context, tx *sql.Tx, ws string, ids []string) error {
	for i := range ids {
		ids[i] = strings.ToLower(ids[i])
	}
	ws = strings.ToLower(ws)
	sort.Strings(ids)
	for i, id := range ids {
		if !UUID(id) {
			return fail("invalid_reference", "invalid claim id")
		}
		if i > 0 && id == ids[i-1] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1))`, ws+":"+id); err != nil {
			return err
		}
	}
	return nil
}
func createEvent(ctx context.Context, tx *sql.Tx, r Revision) error {
	claimID := r.ID
	if r.ClaimID != nil {
		claimID = *r.ClaimID
	}
	event := NewID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO review_events(workspace_id,id,claim_id,cause_id,cause_version,event_type) VALUES($1,$2,$3,$4,$5,$6)`, r.WorkspaceID, event, claimID, r.ID, r.Version, r.Kind); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO review_targets(workspace_id,event_id,knowledge_id) SELECT DISTINCT $1::uuid,$2::uuid,o.id FROM objects o JOIN links l ON (l.workspace_id,l.from_id,l.from_version)=(o.workspace_id,o.id,o.current_version) WHERE o.workspace_id=$1 AND o.kind='knowledge' AND l.role='claim' AND l.to_id=$3 ON CONFLICT DO NOTHING`, r.WorkspaceID, event, claimID)
	return err
}
func resolveEvents(ctx context.Context, tx *sql.Tx, r Revision, events []string) error {
	for _, id := range events {
		if !UUID(id) {
			return fail("invalid_input", "invalid review event id")
		}
		var claim string
		if err := tx.QueryRowContext(ctx, `SELECT claim_id FROM review_events WHERE workspace_id=$1 AND id=$2`, r.WorkspaceID, id).Scan(&claim); err != nil {
			return err
		}
		if r.Kind == "assessment" {
			if r.ClaimID == nil || claim != *r.ClaimID {
				return fail("invalid_reference", "event does not concern assessed claim")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE review_targets SET resolved_by=$3,resolved_version=$4,resolved_at=now() WHERE workspace_id=$1 AND event_id=$2 AND resolved_at IS NULL`, r.WorkspaceID, id, r.ID, r.Version); err != nil {
				return err
			}
		} else {
			var linked bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM links WHERE workspace_id=$1 AND from_id=$2 AND from_version=$3 AND role='claim' AND to_id=$4) OR EXISTS(SELECT 1 FROM review_targets WHERE workspace_id=$1 AND knowledge_id=$2 AND event_id=$5)`, r.WorkspaceID, r.ID, r.Version, claim, id).Scan(&linked); err != nil {
				return err
			}
			if !linked {
				return fail("invalid_reference", "event does not concern published claims")
			}
			// An explicit acknowledgement may precede this Knowledge's first publication.
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_targets(workspace_id,event_id,knowledge_id,resolved_by,resolved_version,resolved_at) VALUES($1,$2,$3,$3,$4,now()) ON CONFLICT(workspace_id,event_id,knowledge_id) DO UPDATE SET resolved_by=EXCLUDED.resolved_by,resolved_version=EXCLUDED.resolved_version,resolved_at=EXCLUDED.resolved_at WHERE review_targets.resolved_at IS NULL`, r.WorkspaceID, id, r.ID, r.Version); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_resolutions(workspace_id,event_id,resolver_id,resolver_version) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, r.WorkspaceID, id, r.ID, r.Version); err != nil {
			return err
		}
	}
	return nil
}

// Publication and event creation take the same ordered claim locks. Events that
// arrived before publication are retained unless present in the pinned evidence
// graph or explicitly acknowledged by an assessment.
func attachOutstandingEvents(ctx context.Context, tx *sql.Tx, r Revision) error {
	_, err := tx.ExecContext(ctx, `WITH RECURSIVE edges AS (
 SELECT from_id,from_version,to_id,to_version FROM links WHERE workspace_id=$1
 UNION SELECT object_id,version,claim_id,claim_version FROM revisions WHERE workspace_id=$1 AND claim_id IS NOT NULL),
 tree(id,version) AS (SELECT $2::uuid,$3::integer UNION SELECT e.to_id,e.to_version FROM tree t JOIN edges e ON e.from_id=t.id AND e.from_version=t.version)
 INSERT INTO review_targets(workspace_id,event_id,knowledge_id)
 SELECT DISTINCT $1::uuid,e.id,$2::uuid FROM review_events e JOIN links l ON l.workspace_id=e.workspace_id AND l.to_id=e.claim_id AND l.role='claim' AND l.from_id=$2 AND l.from_version=$3
 WHERE e.workspace_id=$1 AND NOT EXISTS(SELECT 1 FROM tree t WHERE t.id=e.cause_id AND t.version=e.cause_version)
 AND NOT EXISTS(SELECT 1 FROM event_resolutions er JOIN objects o ON o.workspace_id=er.workspace_id AND o.id=er.resolver_id WHERE er.workspace_id=e.workspace_id AND er.event_id=e.id AND o.kind='assessment')
 ON CONFLICT DO NOTHING`, r.WorkspaceID, r.ID, r.Version)
	return err
}
