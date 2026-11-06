package hub

import (
	"context"
	"database/sql"
)

type Hit struct {
	Type     string         `json:"type"`
	ID       string         `json:"id"`
	Revision int            `json:"revision,omitempty"`
	Snippet  string         `json:"snippet"`
	Scope    map[string]any `json:"scope"`
	Score    float64        `json:"score"`
}

func search(ctx context.Context, tx *sql.Tx, p Params) (Result, error) {
	if len(p.Query) > 4000 {
		return Result{}, fail("invalid_input", "query limit is 4000 bytes")
	}
	if len(p.Types) == 0 {
		p.Types = []string{"knowledge"}
	}
	for _, kind := range p.Types {
		if kind != "source" && kind != "knowledge" {
			return Result{}, fail("invalid_input", "types may contain source and knowledge only")
		}
	}
	if p.Scope == nil {
		p.Scope = map[string]any{}
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	rows, err := tx.QueryContext(ctx, `WITH entries AS (
 SELECT 'knowledge' AS type,k.id,r.revision,r.body,r.scope,r.tags,r.created_at FROM knowledge k JOIN knowledge_revisions r ON (r.workspace_id,r.knowledge_id,r.revision)=(k.workspace_id,k.id,k.current_revision) WHERE k.workspace_id=$1
 UNION ALL SELECT 'source',id,0,body,'{}'::jsonb,tags,created_at FROM sources WHERE workspace_id=$1)
 SELECT jsonb_build_object('type',type,'id',id,'revision',revision,'snippet',substring(body FROM greatest(1,strpos(lower(body),lower($2))-80) FOR 320),'scope',scope,
 'score',ts_rank(to_tsvector('simple',body),plainto_tsquery('simple',$2))+similarity(body,$2)+CASE WHEN $2<>'' AND strpos(lower(body),lower($2))>0 THEN 1 ELSE 0 END) AS hit
 FROM entries WHERE type IN(SELECT jsonb_array_elements_text($3::jsonb)) AND scope @> $4::jsonb AND tags @> ARRAY(SELECT jsonb_array_elements_text($5::jsonb)) AND ($6::timestamptz IS NULL OR created_at>$6)
 AND ($2='' OR to_tsvector('simple',body) @@ plainto_tsquery('simple',$2) OR body % $2 OR strpos(lower(body),lower($2))>0)
 ORDER BY (ts_rank(to_tsvector('simple',body),plainto_tsquery('simple',$2))+similarity(body,$2)+CASE WHEN $2<>'' AND strpos(lower(body),lower($2))>0 THEN 1 ELSE 0 END) DESC,type,id LIMIT $7 OFFSET $8`, p.workspaceID, p.Query, marshal(p.Types), marshal(p.Scope), marshal(p.Tags), p.Since, p.Limit+1, p.Offset)
	if err != nil {
		return Result{}, err
	}
	items, err := rawRows(rows)
	if err != nil {
		return Result{}, err
	}
	items, page := trimPage(items, p)
	out := result(items)
	out.Page = &page
	return out, nil
}
