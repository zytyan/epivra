package hub

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
)

type ReadData struct {
	Object          json.RawMessage   `json:"object"`
	Sources         []json.RawMessage `json:"sources"`
	Attachments     []Attachment      `json:"attachments"`
	History         []json.RawMessage `json:"history"`
	SourcesPage     Page              `json:"sources_page"`
	AttachmentsPage Page              `json:"attachments_page"`
	HistoryPage     Page              `json:"history_page"`
}

func trimPage[T any](items []T, p Params) ([]T, Page) {
	more := len(items) > p.Limit
	if more {
		items = items[:p.Limit]
	}
	return items, Page{p.Limit, p.Offset, more}
}
func rawRows(rows *sql.Rows) ([]json.RawMessage, error) {
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(b))
	}
	return out, rows.Err()
}

const revisionJSON = `(to_jsonb(r)-'created_tx'-'knowledge_id')||jsonb_build_object('id',r.knowledge_id,'type','knowledge','sources',(SELECT coalesce(jsonb_agg(source_id ORDER BY source_id),'[]'::jsonb) FROM knowledge_sources ks WHERE (ks.workspace_id,ks.knowledge_id,ks.revision)=(r.workspace_id,r.knowledge_id,r.revision)))`

func (s *Service) read(ctx context.Context, tx *sql.Tx, op string, p Params) (Result, error) {
	if op == "search" {
		return search(ctx, tx, p)
	}
	if !UUID(p.ID) || (p.Type != "source" && p.Type != "knowledge") || p.Revision < 0 {
		return Result{}, fail("invalid_input", "valid type, id and nonnegative revision required")
	}
	if p.Type == "source" && (p.Revision != 0 || p.History) {
		return Result{}, fail("invalid_input", "sources are immutable and have no revisions")
	}
	if p.AttachmentID != "" {
		if p.Type != "source" || !UUID(p.AttachmentID) {
			return Result{}, fail("invalid_input", "attachment_id requires source type and valid UUID")
		}
		var a Attachment
		var b []byte
		err := tx.QueryRowContext(ctx, `SELECT id,source_id,name,mime,size,sha256,content FROM attachments WHERE workspace_id=$1 AND source_id=$2 AND id=$3`, p.WorkspaceID, p.ID, p.AttachmentID).Scan(&a.ID, &a.SourceID, &a.Name, &a.MIME, &a.Size, &a.SHA256, &b)
		a.Data = base64.StdEncoding.EncodeToString(b)
		return result(a), err
	}
	d := ReadData{Sources: []json.RawMessage{}, Attachments: []Attachment{}, History: []json.RawMessage{}}
	var raw []byte
	revision := p.Revision
	if p.Type == "source" {
		if err := tx.QueryRowContext(ctx, `SELECT (to_jsonb(s)-'created_tx')||jsonb_build_object('type','source') FROM sources s WHERE workspace_id=$1 AND id=$2`, p.WorkspaceID, p.ID).Scan(&raw); err != nil {
			return Result{}, err
		}
	} else {
		if revision == 0 {
			if err := tx.QueryRowContext(ctx, `SELECT current_revision FROM knowledge WHERE workspace_id=$1 AND id=$2`, p.WorkspaceID, p.ID).Scan(&revision); err != nil {
				return Result{}, err
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT `+revisionJSON+` FROM knowledge_revisions r WHERE workspace_id=$1 AND knowledge_id=$2 AND revision=$3`, p.WorkspaceID, p.ID, revision).Scan(&raw); err != nil {
			return Result{}, err
		}
		rows, err := tx.QueryContext(ctx, `SELECT jsonb_build_object('id',s.id,'type','source','provenance',s.provenance,'created_at',s.created_at) FROM knowledge_sources ks JOIN sources s ON (s.workspace_id,s.id)=(ks.workspace_id,ks.source_id) WHERE ks.workspace_id=$1 AND ks.knowledge_id=$2 AND ks.revision=$3 ORDER BY s.id LIMIT $4 OFFSET $5`, p.WorkspaceID, p.ID, revision, p.Limit+1, p.Offset)
		if err != nil {
			return Result{}, err
		}
		d.Sources, err = rawRows(rows)
		if err != nil {
			return Result{}, err
		}
		if p.History {
			rows, err = tx.QueryContext(ctx, `SELECT `+revisionJSON+` FROM knowledge_revisions r WHERE workspace_id=$1 AND knowledge_id=$2 AND revision<=$3 ORDER BY revision DESC LIMIT $4 OFFSET $5`, p.WorkspaceID, p.ID, revision, p.Limit+1, p.Offset)
			if err != nil {
				return Result{}, err
			}
			d.History, err = rawRows(rows)
			if err != nil {
				return Result{}, err
			}
		}
	}
	d.Object = json.RawMessage(raw)
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.source_id,a.name,a.mime,a.size,a.sha256 FROM attachments a WHERE a.workspace_id=$1 AND (($4='source' AND a.source_id=$2) OR ($4='knowledge' AND EXISTS(SELECT 1 FROM knowledge_sources ks WHERE ks.workspace_id=$1 AND ks.knowledge_id=$2 AND ks.revision=$3 AND ks.source_id=a.source_id))) ORDER BY a.id LIMIT $5 OFFSET $6`, p.WorkspaceID, p.ID, revision, p.Type, p.Limit+1, p.Offset)
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
	d.Sources, d.SourcesPage = trimPage(d.Sources, p)
	d.Attachments, d.AttachmentsPage = trimPage(d.Attachments, p)
	d.History, d.HistoryPage = trimPage(d.History, p)
	return result(d), nil
}
