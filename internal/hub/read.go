package hub

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
)

type ReadData struct {
	Revision        Revision          `json:"revision"`
	Links           []Link            `json:"links"`
	Attachments     []Attachment      `json:"attachments"`
	Evidence        []Revision        `json:"evidence"`
	ReviewEvents    []json.RawMessage `json:"review_events"`
	Assessments     []Revision        `json:"assessments"`
	RelatedPage     Page              `json:"related_page"`
	ReviewPage      Page              `json:"review_page"`
	LinksPage       Page              `json:"links_page"`
	AttachmentsPage Page              `json:"attachments_page"`
}

func scanRevisions(rows *sql.Rows) ([]Revision, error) {
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var b []byte
		var r Revision
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func trimPage[T any](items []T, p Params) ([]T, Page) {
	more := len(items) > p.Limit
	if more {
		items = items[:p.Limit]
	}
	return items, Page{p.Limit, p.Offset, more}
}
func (s *Service) read(ctx context.Context, tx *sql.Tx, op string, p Params) (Result, error) {
	switch op {
	case "knowledge_search":
		return s.search(ctx, tx, p)
	case "attachment_read":
		if !UUID(p.ID) {
			return Result{}, fail("invalid_input", "attachment id required")
		}
		var a Attachment
		var b []byte
		err := tx.QueryRowContext(ctx, `SELECT id,source_id,name,mime,size,sha256,content FROM attachments WHERE workspace_id=$1 AND id=$2`, p.WorkspaceID, p.ID).Scan(&a.ID, &a.SourceID, &a.Name, &a.MIME, &a.Size, &a.SHA256, &b)
		a.Data = base64.StdEncoding.EncodeToString(b)
		return result(a), err
	}
	if p.Ref == nil {
		return Result{}, fail("invalid_input", "ref required")
	}
	r, err := loadRevision(ctx, tx, p.WorkspaceID, *p.Ref)
	if err != nil {
		return Result{}, err
	}
	if op == "knowledge_history" {
		return history(ctx, tx, p, r)
	}
	d := ReadData{Revision: r, Links: []Link{}, Attachments: []Attachment{}, Evidence: []Revision{}, ReviewEvents: []json.RawMessage{}, Assessments: []Revision{}}
	rows, err := tx.QueryContext(ctx, `SELECT l.role,t.kind,l.to_id,l.to_version FROM links l JOIN objects t ON (t.workspace_id,t.id)=(l.workspace_id,l.to_id) WHERE l.workspace_id=$1 AND l.from_id=$2 AND l.from_version=$3 ORDER BY l.role,l.to_id,l.to_version LIMIT $4 OFFSET $5`, p.WorkspaceID, r.ID, r.Version, p.Limit+1, p.Offset)
	if err != nil {
		return Result{}, err
	}
	for rows.Next() {
		var l Link
		if err = rows.Scan(&l.Role, &l.Ref.Type, &l.Ref.ID, &l.Ref.Version); err != nil {
			rows.Close()
			return Result{}, err
		}
		d.Links = append(d.Links, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Result{}, err
	}
	d.Links, d.LinksPage = trimPage(d.Links, p)
	// Verification and assessment have a structural link to their exact claim revision.
	if r.ClaimID != nil && p.Offset == 0 {
		d.Links = append(d.Links, Link{"claim", Ref{"claim", *r.ClaimID, *r.ClaimVersion}})
	}
	rows, err = tx.QueryContext(ctx, `WITH RECURSIVE edges AS (
 SELECT from_id,from_version,to_id,to_version FROM links WHERE workspace_id=$1
 UNION SELECT object_id,version,claim_id,claim_version FROM revisions WHERE workspace_id=$1 AND claim_id IS NOT NULL),
 tree(id,version) AS (SELECT $2::uuid,$3::integer UNION SELECT e.to_id,e.to_version FROM tree t JOIN edges e ON e.from_id=t.id AND e.from_version=t.version)
 SELECT id,source_id,name,mime,size,sha256 FROM attachments a WHERE a.workspace_id=$1 AND EXISTS(SELECT 1 FROM tree t WHERE t.id=a.source_id) ORDER BY a.id LIMIT $4 OFFSET $5`, p.WorkspaceID, r.ID, r.Version, p.Limit+1, p.Offset)
	if err != nil {
		return Result{}, err
	}
	for rows.Next() {
		var a Attachment
		if err = rows.Scan(&a.ID, &a.SourceID, &a.Name, &a.MIME, &a.Size, &a.SHA256); err != nil {
			rows.Close()
			return Result{}, err
		}
		d.Attachments = append(d.Attachments, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Result{}, err
	}
	d.Attachments, d.AttachmentsPage = trimPage(d.Attachments, p)
	page := Page{p.Limit, p.Offset, false}
	if p.Depth > 0 {
		rows, err = tx.QueryContext(ctx, `WITH RECURSIVE edges AS (
 SELECT workspace_id,from_id,from_version,to_id,to_version FROM links WHERE workspace_id=$1
 UNION SELECT workspace_id,object_id,version,claim_id,claim_version FROM revisions WHERE workspace_id=$1 AND claim_id IS NOT NULL),
 tree(id,version,depth) AS (SELECT $2::uuid,$3::integer,0 UNION SELECT e.to_id,e.to_version,t.depth+1 FROM tree t JOIN edges e ON e.from_id=t.id AND e.from_version=t.version WHERE t.depth<$4),
 unique_refs AS (SELECT id,version,min(depth) AS depth FROM tree WHERE depth>0 GROUP BY id,version)
 SELECT to_jsonb(r) FROM unique_refs t JOIN revisions r ON r.workspace_id=$1 AND r.object_id=t.id AND r.version=t.version ORDER BY t.depth,r.object_id,r.version LIMIT $5 OFFSET $6`, p.WorkspaceID, r.ID, r.Version, p.Depth, p.Limit+1, p.Offset)
		if err != nil {
			return Result{}, err
		}
		d.Evidence, err = scanRevisions(rows)
		if err != nil {
			return Result{}, err
		}
		d.Evidence, page = trimPage(d.Evidence, p)
	}
	rows, err = tx.QueryContext(ctx, `SELECT to_jsonb(a) FROM revisions a WHERE a.workspace_id=$1 AND a.kind='assessment' AND ((a.claim_id=$2 AND a.claim_version=$3) OR EXISTS(SELECT 1 FROM links l WHERE l.workspace_id=$1 AND l.from_id=$2 AND l.from_version=$3 AND l.role='claim' AND l.to_id=a.claim_id AND l.to_version=a.claim_version)) ORDER BY a.created_at,a.object_id LIMIT $4 OFFSET $5`, p.WorkspaceID, r.ID, r.Version, p.Limit+1, p.Offset)
	if err != nil {
		return Result{}, err
	}
	d.Assessments, err = scanRevisions(rows)
	if err != nil {
		return Result{}, err
	}
	d.Assessments, d.RelatedPage = trimPage(d.Assessments, p)
	rows, err = tx.QueryContext(ctx, `SELECT to_jsonb(e) FROM review_events e WHERE e.workspace_id=$1 AND ((e.claim_id=$2 AND NOT EXISTS(SELECT 1 FROM event_resolutions er JOIN objects o ON o.workspace_id=er.workspace_id AND o.id=er.resolver_id WHERE er.workspace_id=e.workspace_id AND er.event_id=e.id AND o.kind='assessment')) OR EXISTS(SELECT 1 FROM review_targets t WHERE t.workspace_id=e.workspace_id AND t.event_id=e.id AND t.knowledge_id=$2 AND t.resolved_at IS NULL)) ORDER BY e.created_at,e.id LIMIT $3 OFFSET $4`, p.WorkspaceID, r.ID, p.Limit+1, p.Offset)
	if err != nil {
		return Result{}, err
	}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return Result{}, err
		}
		d.ReviewEvents = append(d.ReviewEvents, json.RawMessage(b))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Result{}, err
	}
	d.ReviewEvents, d.ReviewPage = trimPage(d.ReviewEvents, p)
	out := result(d)
	out.Page = &page
	return out, nil
}
func history(ctx context.Context, tx *sql.Tx, p Params, r Revision) (Result, error) {
	// The root version bounds its revisions; related records always refer to pinned claim versions.
	rows, err := tx.QueryContext(ctx, `WITH root AS (SELECT * FROM revisions WHERE workspace_id=$1 AND object_id=$2 AND version<=$3), claims AS (
 SELECT object_id AS id,version FROM root WHERE kind='claim'
 UNION SELECT claim_id,claim_version FROM root WHERE claim_id IS NOT NULL
 UNION SELECT l.to_id,l.to_version FROM links l JOIN root r ON l.from_id=r.object_id AND l.from_version=r.version WHERE l.workspace_id=$1 AND l.role='claim')
 SELECT to_jsonb(r) FROM revisions r WHERE r.workspace_id=$1 AND ((r.object_id=$2 AND r.version<=$3) OR (r.kind IN ('verification','assessment') AND EXISTS(SELECT 1 FROM claims c WHERE c.id=r.claim_id AND c.version=r.claim_version))) ORDER BY r.created_at,r.object_id,r.version LIMIT $4 OFFSET $5`, p.WorkspaceID, r.ID, r.Version, p.Limit+1, p.Offset)
	if err != nil {
		return Result{}, err
	}
	revs, err := scanRevisions(rows)
	if err != nil {
		return Result{}, err
	}
	revs, page := trimPage(revs, p)
	out := result(revs)
	out.Page = &page
	return out, nil
}
