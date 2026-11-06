package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"strings"
)

// All requests use the single shared internal workspace.
const defaultWorkspaceID = "00000000-0000-0000-0000-000000000001"

type Service struct {
	DB        *sql.DB
	MaxUpload int64
}

func (s *Service) UploadLimit() int64 {
	if s.MaxUpload <= 0 {
		return 20 << 20
	}
	return s.MaxUpload
}
func Decode(raw []byte) (Params, error) {
	var p Params
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(&p); err != nil {
		return p, fail("invalid_input", err.Error())
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return p, fail("invalid_input", "exactly one JSON object required")
	}
	return p, nil
}
func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fail("not_found", "object or referenced revision not found")
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		switch state.SQLState() {
		case "23503", "23514", "22P02", "22007", "23502":
			return fail("invalid_reference", "invalid reference, revision or evidence constraint")
		case "23505", "40001", "40P01":
			return fail("conflict", "concurrent operation conflicted; retry with the same idempotency key")
		}
	}
	slog.Error("database operation failed", "error", err)
	return fail("internal", "database operation failed")
}
func (s *Service) Execute(ctx context.Context, op string, p Params) (out Result, err error) {
	defer func() { err = normalizeError(err) }()
	if !KnownOperation(op) {
		return out, fail("unknown_operation", "unknown operation")
	}
	p.workspaceID = defaultWorkspaceID
	p.ID = strings.ToLower(p.ID)
	if p.Limit == 0 {
		p.Limit = 20
	}
	if p.Limit < 1 || p.Limit > 100 || p.Offset < 0 || p.Offset > 100000 {
		return out, fail("invalid_input", "limit 1..100 and offset 0..100000 required")
	}
	if p.ID != "" && !UUID(p.ID) {
		return out, fail("invalid_input", "invalid id")
	}
	if IsWrite(op) && (strings.TrimSpace(p.IdempotencyKey) == "" || len(p.IdempotencyKey) > 200) {
		return out, fail("invalid_input", "idempotency_key (1..200 characters) required")
	}
	if len([]rune(p.Conditions)) > 2000 || len([]rune(p.Reason)) > 2000 || len(marshal(p.Scope)) > 65536 || len(p.Sources) > 100 {
		return out, fail("invalid_input", "conditions/reason limit 2000 characters, scope limit 64 KiB, sources limit 100")
	}
	if op == "save" && p.Type == "knowledge" && p.Scope == nil {
		return out, fail("invalid_input", "knowledge scope object required")
	}
	if len(p.Body) > 4<<20 || len(p.Tags) > 100 {
		return out, fail("invalid_input", "body or tag limit exceeded")
	}
	for _, tag := range p.Tags {
		if len(tag) == 0 || len(tag) > 200 {
			return out, fail("invalid_input", "tags must contain 1..200 bytes")
		}
	}
	opts := &sql.TxOptions{}
	if !IsWrite(op) {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.DB.BeginTx(ctx, opts)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return out, err
	}
	var digest string
	if IsWrite(op) {
		h := sha256.Sum256([]byte(marshal(struct {
			Params Params         `json:"params"`
			Scope  map[string]any `json:"scope"`
		}{p, p.Scope})))
		digest = hex.EncodeToString(h[:])
		scope := p.workspaceID + ":" + op + ":" + p.IdempotencyKey
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, scope); err != nil {
			return out, err
		}
		var previous, raw string
		err = tx.QueryRowContext(ctx, `SELECT digest,response FROM idempotency WHERE workspace_id=$1 AND operation=$2 AND key=$3`, p.workspaceID, op, p.IdempotencyKey).Scan(&previous, &raw)
		if err == nil {
			if previous != digest {
				return out, fail("idempotency_conflict", "key was already used with different input")
			}
			err = json.Unmarshal([]byte(raw), &out)
			return out, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		out, err = s.write(ctx, tx, p)
	} else {
		out, err = s.read(ctx, tx, op, p)
	}
	if err != nil {
		return out, err
	}
	if IsWrite(op) {
		_, err = tx.ExecContext(ctx, `INSERT INTO idempotency(workspace_id,operation,key,digest,response) VALUES($1,$2,$3,$4,$5::jsonb)`, p.workspaceID, op, p.IdempotencyKey, digest, marshal(out))
		if err != nil {
			return out, err
		}
	}
	err = tx.Commit()
	return out, err
}

func (s *Service) write(ctx context.Context, tx *sql.Tx, p Params) (Result, error) {
	if p.Type != "source" && p.Type != "knowledge" {
		return Result{}, fail("invalid_input", "type must be source or knowledge")
	}
	if strings.TrimSpace(p.Body) == "" {
		return Result{}, fail("invalid_input", "body required")
	}
	if p.Type == "source" {
		if p.ID != "" || p.ExpectedRevision != 0 || p.Scope != nil || len(p.Sources) > 0 || p.Conditions != "" || p.Reason != "" {
			return Result{}, fail("invalid_input", "Source is immutable; omit id, expected_revision and knowledge-only fields")
		}
		if strings.TrimSpace(p.Provenance) == "" {
			return Result{}, fail("invalid_input", "source provenance required")
		}
		id := NewID()
		if p.Metadata == nil {
			p.Metadata = map[string]any{}
		}
		if p.Tags == nil {
			p.Tags = []string{}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sources(workspace_id,id,body,provenance,source_time,metadata,tags) VALUES($1,$2,$3,$4,$5,$6::jsonb,ARRAY(SELECT jsonb_array_elements_text($7::jsonb)))`, p.workspaceID, id, p.Body, p.Provenance, p.SourceTime, marshal(p.Metadata), marshal(p.Tags)); err != nil {
			return Result{}, err
		}
		if len(p.Attachments) > 100 {
			return Result{}, fail("invalid_input", "at most 100 attachments")
		}
		var total int64
		for _, a := range p.Attachments {
			if a.Name == "" {
				return Result{}, fail("invalid_input", "attachment name required")
			}
			if _, _, err := mime.ParseMediaType(a.MIME); err != nil {
				return Result{}, fail("invalid_input", "valid attachment MIME required")
			}
			if int64(base64.StdEncoding.DecodedLen(len(a.Data))) > s.UploadLimit()+2 {
				return Result{}, fail("upload_too_large", "attachment too large")
			}
			b, err := base64.StdEncoding.DecodeString(a.Data)
			if err != nil {
				return Result{}, fail("invalid_input", "invalid base64 attachment")
			}
			total += int64(len(b))
			if total > s.UploadLimit() {
				return Result{}, fail("upload_too_large", "total attachments too large")
			}
			h := sha256.Sum256(b)
			if _, err = tx.ExecContext(ctx, `INSERT INTO attachments(workspace_id,id,source_id,name,mime,size,sha256,content) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.workspaceID, NewID(), id, a.Name, a.MIME, len(b), hex.EncodeToString(h[:]), b); err != nil {
				return Result{}, err
			}
		}
		return result(Ref{Type: "source", ID: id}), nil
	}
	if len([]rune(p.Body)) > 2000 {
		return Result{}, fail("invalid_input", "Knowledge must be atomic: body limit is 2000 characters")
	}
	if p.Scope == nil || len(p.Sources) == 0 || strings.TrimSpace(p.Reason) == "" {
		return Result{}, fail("invalid_input", "knowledge requires scope object, source IDs and reason")
	}
	if len(p.Attachments) > 0 || p.Provenance != "" || p.SourceTime != nil || p.Metadata != nil {
		return Result{}, fail("invalid_input", "source-only fields are not allowed for knowledge")
	}
	id := p.ID
	revision := 1
	if id == "" {
		if p.ExpectedRevision != 0 {
			return Result{}, fail("invalid_input", "omit expected_revision for new knowledge")
		}
		id = NewID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge(workspace_id,id,current_revision) VALUES($1,$2,1)`, p.workspaceID, id); err != nil {
			return Result{}, err
		}
	} else {
		if p.ExpectedRevision < 1 {
			return Result{}, fail("invalid_input", "expected_revision required for update")
		}
		var current int
		if err := tx.QueryRowContext(ctx, `SELECT current_revision FROM knowledge WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, p.workspaceID, id).Scan(&current); err != nil {
			return Result{}, err
		}
		if current != p.ExpectedRevision {
			return Result{}, &Error{Code: "revision_conflict", Message: "expected_revision differs from current revision", Details: map[string]int{"current_revision": current}}
		}
		revision = current + 1
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_revisions(workspace_id,knowledge_id,revision,body,scope,conditions,tags,reason) VALUES($1,$2,$3,$4,$5::jsonb,$6,ARRAY(SELECT jsonb_array_elements_text($7::jsonb)),$8)`, p.workspaceID, id, revision, p.Body, marshal(p.Scope), p.Conditions, marshal(p.Tags), p.Reason); err != nil {
		return Result{}, err
	}
	seen := map[string]bool{}
	for _, source := range p.Sources {
		if !UUID(source) {
			return Result{}, fail("invalid_reference", "invalid source UUID")
		}
		source = strings.ToLower(source)
		if seen[source] {
			continue
		}
		seen[source] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_sources(workspace_id,knowledge_id,revision,source_id) VALUES($1,$2,$3,$4)`, p.workspaceID, id, revision, source); err != nil {
			return Result{}, err
		}
	}
	if revision > 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE knowledge SET current_revision=$3 WHERE workspace_id=$1 AND id=$2`, p.workspaceID, id, revision); err != nil {
			return Result{}, err
		}
	}
	return result(Ref{Type: "knowledge", ID: id, Revision: revision}), nil
}
