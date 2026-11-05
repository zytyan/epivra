package hub

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

type Hit struct {
	Ref           Ref      `json:"ref"`
	Snippet       string   `json:"snippet"`
	Methods       []string `json:"methods"`
	Score         float64  `json:"score"`
	PendingReview bool     `json:"pending_review"`
	Position      int      `json:"position,omitempty"`
}

const filters = `o.workspace_id=$1 AND (jsonb_array_length($2::jsonb)=0 OR o.kind IN (SELECT jsonb_array_elements_text($2::jsonb))) AND r.tags @> ARRAY(SELECT jsonb_array_elements_text($3::jsonb)) AND ($4::boolean IS NULL OR EXISTS(SELECT 1 FROM review_targets rt WHERE rt.workspace_id=o.workspace_id AND rt.knowledge_id=o.id AND rt.resolved_at IS NULL)=$4)`
const pendingSQL = `EXISTS(SELECT 1 FROM review_targets rt WHERE rt.workspace_id=o.workspace_id AND rt.knowledge_id=o.id AND rt.resolved_at IS NULL)`

func scanHits(rows *sql.Rows, method string) ([]Hit, error) {
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		h.Methods = []string{method}
		if err := rows.Scan(&h.Ref.Type, &h.Ref.ID, &h.Ref.Version, &h.Snippet, &h.Score, &h.PendingReview, &h.Position); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
func (s *Service) search(ctx context.Context, tx *sql.Tx, p Params) (Result, error) {
	if p.Mode == "" {
		p.Mode = "hybrid"
	}
	if p.Mode != "keyword" && p.Mode != "semantic" && p.Mode != "hybrid" {
		return Result{}, fail("invalid_input", "mode must be keyword, semantic or hybrid")
	}
	if p.Types == nil {
		p.Types = []string{}
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	for _, k := range p.Types {
		if k != "source" && k != "claim" && k != "knowledge" && k != "verification" && k != "assessment" {
			return Result{}, fail("invalid_input", "invalid search object type")
		}
	}
	if len(p.Query) > 4000 {
		return Result{}, fail("invalid_input", "query too long")
	}
	if p.Mode == "semantic" && strings.TrimSpace(p.Query) == "" {
		return Result{}, fail("invalid_input", "semantic query must not be empty")
	}
	count := p.Offset + p.Limit + 1
	candidateLimit := max(count, 100)
	var keyword, semantic []Hit
	warnings := []string{}
	semanticOK := false
	if p.Mode != "keyword" && strings.TrimSpace(p.Query) != "" {
		vectors, err := s.queryVector(ctx, tx, p.Query)
		if err != nil {
			if p.Mode == "semantic" {
				return Result{}, err
			}
			warnings = append(warnings, err.Error()+"; using keyword search")
		} else {
			fp := s.Embed.Fingerprint()
			var active bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM index_configs WHERE fingerprint=$1 AND active)`, fp).Scan(&active); err != nil {
				return Result{}, err
			}
			if !active {
				if p.Mode == "semantic" {
					return Result{}, fail("semantic_unavailable", "embedding model is awaiting activation")
				}
				warnings = append(warnings, "semantic_unavailable: model configuration changed; using keyword search")
			} else {
				rows, err := tx.QueryContext(ctx, `SELECT kind,id,version,content,score,pending,position FROM (
 SELECT DISTINCT ON(o.id) o.kind,o.id,r.version,c.content,1-(c.embedding <=> $6::public.vector) AS score,`+pendingSQL+` AS pending,c.position FROM objects o JOIN revisions r ON (r.workspace_id,r.object_id,r.version)=(o.workspace_id,o.id,o.current_version) JOIN chunks c ON (c.workspace_id,c.object_id,c.version)=(r.workspace_id,r.object_id,r.version) WHERE `+filters+` AND c.fingerprint=$5 ORDER BY o.id,c.embedding <=> $6::public.vector,c.position) q ORDER BY score DESC,id LIMIT $7`, p.WorkspaceID, marshal(p.Types), marshal(p.Tags), p.PendingReview, fp, marshal(vectors[0]), candidateLimit)
				if err != nil {
					return Result{}, err
				}
				semantic, err = scanHits(rows, "semantic")
				if err != nil {
					return Result{}, err
				}
				semanticOK = true
				var lag bool
				if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM objects o JOIN revisions r ON (r.workspace_id,r.object_id,r.version)=(o.workspace_id,o.id,o.current_version) WHERE `+filters+` AND o.kind IN ('source','claim','knowledge') AND NOT EXISTS(SELECT 1 FROM index_jobs j WHERE (j.workspace_id,j.object_id,j.version)=(o.workspace_id,o.id,o.current_version) AND j.fingerprint=$5 AND j.state='done'))`, p.WorkspaceID, marshal(p.Types), marshal(p.Tags), p.PendingReview, fp).Scan(&lag); err != nil {
					return Result{}, err
				}
				if lag {
					warnings = append(warnings, "index_incomplete: some current revisions are not indexed")
				}
			}
		}
	}
	if p.Mode != "semantic" {
		rows, err := tx.QueryContext(ctx, `SELECT o.kind,o.id,r.version,substring(r.body FROM greatest(1,strpos(lower(r.body),lower($5))-80) FOR 320),
 (ts_rank(to_tsvector('simple',r.body),plainto_tsquery('simple',$5))+similarity(r.body,$5)+CASE WHEN $5<>'' AND strpos(lower(r.body),lower($5))>0 THEN 1 ELSE 0 END)::float8 AS score,`+pendingSQL+`,0 FROM objects o JOIN revisions r ON (r.workspace_id,r.object_id,r.version)=(o.workspace_id,o.id,o.current_version) WHERE `+filters+` AND ($5='' OR to_tsvector('simple',r.body) @@ plainto_tsquery('simple',$5) OR r.body % $5 OR strpos(lower(r.body),lower($5))>0) ORDER BY score DESC,o.id LIMIT $6`, p.WorkspaceID, marshal(p.Types), marshal(p.Tags), p.PendingReview, p.Query, candidateLimit)
		if err != nil {
			return Result{}, err
		}
		keyword, err = scanHits(rows, "keyword")
		if err != nil {
			return Result{}, err
		}
	}
	hits := keyword
	if p.Mode == "semantic" {
		hits = semantic
	} else if p.Mode == "hybrid" && semanticOK {
		hits = RRF(keyword, semantic)
	}
	// Ranking uses a bounded candidate window. The limit grows with requested offset.
	hasMore := len(hits) > p.Offset+p.Limit
	start := min(p.Offset, len(hits))
	end := min(start+p.Limit, len(hits))
	hits = hits[start:end]
	if hits == nil {
		hits = []Hit{}
	}
	return Result{Data: hits, Page: &Page{p.Limit, p.Offset, hasMore}, Warnings: warnings}, nil
}
func RRF(lists ...[]Hit) []Hit {
	scores := map[Ref]*Hit{}
	for _, list := range lists {
		for i, h := range list {
			v := scores[h.Ref]
			if v == nil {
				copy := h
				copy.Score = 0
				copy.Methods = nil
				v = &copy
				scores[h.Ref] = v
			}
			v.Score += 1.0 / float64(60+i+1)
			for _, m := range h.Methods {
				found := false
				for _, old := range v.Methods {
					if old == m {
						found = true
					}
				}
				if !found {
					v.Methods = append(v.Methods, m)
				}
			}
		}
	}
	out := make([]Hit, 0, len(scores))
	for _, h := range scores {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Ref.ID < out[j].Ref.ID
		}
		return out[i].Score > out[j].Score
	})
	return out
}

// Capability detection does not reference a vector type or a missing table in
// parsed SQL, so it is safe on PostgreSQL installations without pgvector.
func vectorReady(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	var ready bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector' AND n.nspname='public') AND EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass(format('%I.chunks',current_schema())) AND attname='embedding' AND atttypid=to_regtype('public.vector') AND NOT attisdropped)`).Scan(&ready)
	return ready, err
}
func (s *Service) queryVector(ctx context.Context, tx *sql.Tx, query string) ([][]float64, error) {
	ready, err := vectorReady(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, fail("semantic_unavailable", "pgvector is not enabled; initialize a fresh database with the extension enabled to use semantic search")
	}
	return s.Embed.Vectors(ctx, []string{query})
}
