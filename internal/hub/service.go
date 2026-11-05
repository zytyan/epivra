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

type Service struct {
	DB        *sql.DB
	MaxUpload int64
	Embed     *Embedder
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
func (s *Service) Authenticate(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", fail("unauthorized", "Bearer API key required")
	}
	hash := sha256.Sum256([]byte(token))
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM api_keys WHERE digest=$1 AND revoked_at IS NULL`, hash[:]).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fail("unauthorized", "invalid API key")
	}
	return id, normalizeError(err)
}
func authorize(ctx context.Context, tx *sql.Tx, actor, workspace string, write bool) error {
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT p.can_write FROM key_permissions p JOIN api_keys k ON k.id=p.key_id WHERE p.key_id=$1 AND p.workspace_id=$2 AND k.revoked_at IS NULL FOR SHARE OF k,p`, actor, workspace).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) || err == nil && write && !allowed {
		return fail("forbidden", "workspace permission denied")
	}
	return err
}
func (s *Service) Execute(ctx context.Context, actor, op string, p Params) (out Result, err error) {
	defer func() { err = normalizeError(err) }()
	if !KnownOperation(op) {
		return out, fail("unknown_operation", "unknown operation")
	}
	if !UUID(actor) || !UUID(p.WorkspaceID) {
		return out, fail("invalid_input", "valid workspace_id required")
	}
	if p.Limit == 0 {
		p.Limit = 20
	}
	if p.Limit < 1 || p.Limit > 100 || p.Offset < 0 || p.Offset > 100000 || p.Depth < 0 || p.Depth > 5 {
		return out, fail("invalid_input", "limit 1..100, offset 0..100000, depth 0..5 required")
	}
	if p.ID != "" && !UUID(p.ID) {
		return out, fail("invalid_input", "invalid id")
	}
	if IsWrite(op) && (strings.TrimSpace(p.IdempotencyKey) == "" || len(p.IdempotencyKey) > 200) {
		return out, fail("invalid_input", "idempotency_key (1..200 characters) required")
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
	if err = authorize(ctx, tx, actor, p.WorkspaceID, IsWrite(op)); err != nil {
		return out, err
	}
	var digest string
	if IsWrite(op) {
		h := sha256.Sum256([]byte(marshal(p)))
		digest = hex.EncodeToString(h[:])
		scope := p.WorkspaceID + ":" + actor + ":" + op + ":" + p.IdempotencyKey
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, scope); err != nil {
			return out, err
		}
		var previous, raw string
		err = tx.QueryRowContext(ctx, `SELECT digest,response FROM idempotency WHERE workspace_id=$1 AND actor=$2 AND operation=$3 AND key=$4`, p.WorkspaceID, actor, op, p.IdempotencyKey).Scan(&previous, &raw)
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
		out, err = s.write(ctx, tx, actor, op, p)
	} else {
		out, err = s.read(ctx, tx, op, p)
	}
	if err != nil {
		return out, err
	}
	if IsWrite(op) {
		_, err = tx.ExecContext(ctx, `INSERT INTO idempotency(workspace_id,actor,operation,key,digest,response) VALUES($1,$2,$3,$4,$5,$6::jsonb)`, p.WorkspaceID, actor, op, p.IdempotencyKey, digest, marshal(out))
		if err != nil {
			return out, err
		}
	}
	err = tx.Commit()
	return out, err
}
func loadRevision(ctx context.Context, tx *sql.Tx, ws string, ref Ref) (Revision, error) {
	var r Revision
	if !UUID(ref.ID) || ref.Version < 0 {
		return r, fail("invalid_reference", "valid object reference required")
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT to_jsonb(r) FROM objects o JOIN revisions r ON r.workspace_id=o.workspace_id AND r.object_id=o.id AND r.version=CASE WHEN $3=0 THEN o.current_version ELSE $3 END WHERE o.workspace_id=$1 AND o.id=$2`, ws, ref.ID, ref.Version).Scan(&raw)
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	if r.Kind != ref.Type {
		return r, fail("invalid_reference", "reference type does not match object")
	}
	return r, nil
}
func reference(ctx context.Context, tx *sql.Tx, ws string, r Ref, kind string, pinned bool) (Ref, error) {
	if r.Type != kind || pinned && r.Version < 1 {
		return r, fail("invalid_reference", "expected "+kind+" reference with explicit version")
	}
	v, err := loadRevision(ctx, tx, ws, r)
	return v.Ref(), err
}
func insertRevision(ctx context.Context, tx *sql.Tx, r Revision) error {
	if r.Tags == nil {
		r.Tags = []string{}
	}
	if r.Metadata == nil {
		r.Metadata = map[string]any{}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO revisions(workspace_id,object_id,version,kind,body,tags,conditions,metadata,author,reason,provenance,source_time,source_type,method,performed_at,conclusion,valid_from,valid_until,claim_id,claim_version) VALUES($1,$2,$3,$4,$5,ARRAY(SELECT jsonb_array_elements_text($6::jsonb)),$7,$8::jsonb,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, r.WorkspaceID, r.ID, r.Version, r.Kind, r.Body, marshal(r.Tags), r.Conditions, marshal(r.Metadata), r.Author, r.Reason, r.Provenance, r.SourceTime, r.SourceType, r.Method, r.PerformedAt, r.Conclusion, r.ValidFrom, r.ValidUntil, r.ClaimID, r.ClaimVersion)
	return err
}
func link(ctx context.Context, tx *sql.Tx, r Revision, role string, refs []Ref, kind string, pinned bool) error {
	for _, ref := range refs {
		ref, err := reference(ctx, tx, r.WorkspaceID, ref, kind, pinned)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO links(workspace_id,from_id,from_version,role,to_id,to_version) VALUES($1,$2,$3,$4,$5,$6)`, r.WorkspaceID, r.ID, r.Version, role, ref.ID, ref.Version); err != nil {
			return err
		}
	}
	return nil
}
func record(ctx context.Context, tx *sql.Tx, r Revision, op string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(workspace_id,actor,operation,object_id,version) VALUES($1,$2,$3,$4,$5)`, r.WorkspaceID, r.Author, op, r.ID, r.Version); err != nil {
		return err
	}
	if r.Kind == "source" || r.Kind == "claim" || r.Kind == "knowledge" {
		_, err := tx.ExecContext(ctx, `INSERT INTO index_jobs(workspace_id,object_id,version,fingerprint) SELECT $1,$2,$3,fingerprint FROM index_configs WHERE active ON CONFLICT DO NOTHING`, r.WorkspaceID, r.ID, r.Version)
		return err
	}
	return nil
}
func (s *Service) createSource(ctx context.Context, tx *sql.Tx, actor, ws string, p SourceInput) (Revision, error) {
	r := Revision{WorkspaceID: ws, ID: NewID(), Version: 1, Kind: "source", Body: p.Body, Tags: p.Tags, Conditions: p.Conditions, Metadata: p.Metadata, Author: actor, Provenance: p.Provenance, SourceTime: p.SourceTime, SourceType: p.SourceType}
	if strings.TrimSpace(p.Provenance) == "" || strings.TrimSpace(p.SourceType) == "" || strings.TrimSpace(p.Body) == "" && len(p.Attachments) == 0 {
		return r, fail("invalid_input", "source requires provenance, source_type, and text or attachment")
	}
	if len(p.Body) > 4<<20 || len(p.Attachments) > 100 {
		return r, fail("invalid_input", "source too large")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO objects(workspace_id,id,kind,current_version) VALUES($1,$2,'source',1)`, ws, r.ID); err != nil {
		return r, err
	}
	if err := insertRevision(ctx, tx, r); err != nil {
		return r, err
	}
	var total int64
	for _, a := range p.Attachments {
		if a.Name == "" {
			return r, fail("invalid_input", "attachment name required")
		}
		if _, _, err := mime.ParseMediaType(a.MIME); err != nil {
			return r, fail("invalid_input", "valid MIME required")
		}
		if int64(base64.StdEncoding.DecodedLen(len(a.Data))) > s.UploadLimit()+2 {
			return r, fail("upload_too_large", "attachment exceeds upload limit")
		}
		b, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return r, fail("invalid_input", "attachment data must be base64")
		}
		total += int64(len(b))
		if total > s.UploadLimit() {
			return r, fail("upload_too_large", "total attachments exceed upload limit")
		}
		h := sha256.Sum256(b)
		if _, err = tx.ExecContext(ctx, `INSERT INTO attachments(workspace_id,id,source_id,name,mime,size,sha256,content) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ws, NewID(), r.ID, a.Name, a.MIME, len(b), hex.EncodeToString(h[:]), b); err != nil {
			return r, err
		}
	}
	return r, record(ctx, tx, r, "source_create")
}
func (s *Service) write(ctx context.Context, tx *sql.Tx, actor, op string, p Params) (Result, error) {
	if op == "source_create" {
		r, err := s.createSource(ctx, tx, actor, p.WorkspaceID, SourceInput{p.Body, p.Tags, p.Conditions, p.Metadata, p.Provenance, p.SourceTime, p.SourceType, p.Attachments})
		return result(r.Ref()), err
	}
	kind := map[string]string{"source_tags_update": "source", "claim_create": "claim", "claim_revise": "claim", "verification_create": "verification", "assessment_create": "assessment", "knowledge_publish": "knowledge"}[op]
	r := Revision{WorkspaceID: p.WorkspaceID, ID: p.ID, Version: 1, Kind: kind, Body: p.Body, Tags: p.Tags, Conditions: p.Conditions, Metadata: p.Metadata, Author: actor, Reason: p.Reason, Method: p.Method, PerformedAt: p.PerformedAt, ValidFrom: p.ValidFrom, ValidUntil: p.ValidUntil}
	revise := op == "claim_revise" || op == "source_tags_update" || op == "knowledge_publish" && p.ID != ""
	// Claim-level locks serialize evidence events with publication and explicit resolution.
	claimIDs := []string{}
	if kind == "claim" && revise {
		claimIDs = append(claimIDs, p.ID)
	}
	if p.Claim != nil {
		claimIDs = append(claimIDs, p.Claim.ID)
	}
	for _, c := range p.Claims {
		claimIDs = append(claimIDs, c.ID)
	}
	if err := lockClaims(ctx, tx, p.WorkspaceID, claimIDs); err != nil {
		return Result{}, err
	}
	if revise {
		if !UUID(r.ID) || p.ExpectedVersion < 1 || strings.TrimSpace(p.Reason) == "" {
			return Result{}, fail("invalid_input", "revision requires id, expected_version and reason")
		}
		var version int
		var currentKind string
		if err := tx.QueryRowContext(ctx, `SELECT current_version,kind FROM objects WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, p.WorkspaceID, r.ID).Scan(&version, &currentKind); err != nil {
			return Result{}, err
		}
		if currentKind != kind {
			return Result{}, fail("invalid_reference", "object type mismatch")
		}
		if version != p.ExpectedVersion {
			slog.Info("version conflict", "object_id", r.ID, "current_version", version)
			return Result{}, &Error{Code: "version_conflict", Message: "expected_version does not match current version", Details: map[string]int{"current_version": version}}
		}
		if kind == "source" {
			previous, err := loadRevision(ctx, tx, p.WorkspaceID, Ref{kind, r.ID, version})
			if err != nil {
				return Result{}, err
			}
			r = previous
			r.Tags = p.Tags
			r.Author = actor
			r.Reason = p.Reason
		}
		r.Version = version + 1
	} else {
		if p.ID != "" || p.ExpectedVersion != 0 {
			return Result{}, fail("invalid_input", "create must omit id and expected_version")
		}
		r.ID = NewID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO objects(workspace_id,id,kind,current_version) VALUES($1,$2,$3,1)`, p.WorkspaceID, r.ID, kind); err != nil {
			return Result{}, err
		}
	}
	if kind != "source" && strings.TrimSpace(r.Body) == "" {
		return Result{}, fail("invalid_input", "body required")
	}
	if kind == "verification" || kind == "assessment" {
		if p.Claim == nil {
			return Result{}, fail("invalid_input", "claim revision required")
		}
		c, err := reference(ctx, tx, p.WorkspaceID, *p.Claim, "claim", true)
		if err != nil {
			return Result{}, err
		}
		r.ClaimID = &c.ID
		r.ClaimVersion = &c.Version
		if p.Conclusion != "support" && p.Conclusion != "refute" && p.Conclusion != "uncertain" {
			return Result{}, fail("invalid_input", "conclusion must be support, refute or uncertain")
		}
		r.Conclusion = &p.Conclusion
	}
	if kind == "verification" && (p.Method == "" || p.PerformedAt == nil) {
		return Result{}, fail("invalid_input", "method and performed_at required")
	}
	if p.ValidFrom != nil && p.ValidUntil != nil && p.ValidFrom.After(*p.ValidUntil) {
		return Result{}, fail("invalid_input", "invalid validity range")
	}
	if err := insertRevision(ctx, tx, r); err != nil {
		return Result{}, err
	}
	if revise {
		if _, err := tx.ExecContext(ctx, `UPDATE objects SET current_version=$3 WHERE workspace_id=$1 AND id=$2`, p.WorkspaceID, r.ID, r.Version); err != nil {
			return Result{}, err
		}
	}
	var err error
	switch kind {
	case "claim":
		if len(p.Sources) == 0 {
			return Result{}, fail("invalid_input", "at least one source required")
		}
		err = link(ctx, tx, r, "source", p.Sources, "source", false)
	case "verification":
		if p.OutputSource != nil {
			src, e := s.createSource(ctx, tx, actor, p.WorkspaceID, *p.OutputSource)
			if e != nil {
				return Result{}, e
			}
			p.Evidence = append(p.Evidence, src.Ref())
		}
		if len(p.Evidence) == 0 {
			return Result{}, fail("invalid_input", "at least one evidence source required")
		}
		err = link(ctx, tx, r, "evidence", p.Evidence, "source", false)
	case "assessment":
		if len(p.Verifications) == 0 {
			return Result{}, fail("invalid_input", "at least one verification required")
		}
		err = link(ctx, tx, r, "verification", p.Verifications, "verification", true)
	case "knowledge":
		if len(p.Claims) == 0 || len(p.Assessments) == 0 {
			return Result{}, fail("invalid_input", "claim revisions and support assessment required")
		}
		if err = link(ctx, tx, r, "claim", p.Claims, "claim", true); err == nil {
			err = link(ctx, tx, r, "assessment", p.Assessments, "assessment", true)
		}
	}
	if err != nil {
		return Result{}, err
	}
	if kind == "knowledge" {
		if err = attachOutstandingEvents(ctx, tx, r); err != nil {
			return Result{}, err
		}
	}
	if err = record(ctx, tx, r, op); err != nil {
		return Result{}, err
	}
	if kind == "verification" || kind == "assessment" || kind == "claim" && revise {
		if err = createEvent(ctx, tx, r); err != nil {
			return Result{}, err
		}
	}
	if len(p.ResolveEvents) > 0 {
		if kind != "assessment" && kind != "knowledge" {
			return Result{}, fail("invalid_input", "only assessment or knowledge can resolve events")
		}
		if err = resolveEvents(ctx, tx, r, p.ResolveEvents); err != nil {
			return Result{}, err
		}
	}
	return result(r.Ref()), nil
}
func (s *Service) AttachmentBytes(ctx context.Context, actor, ws, id string) (Attachment, []byte, error) {
	p := Params{WorkspaceID: ws, ID: id}
	out, err := s.Execute(ctx, actor, "attachment_read", p)
	if err != nil {
		return Attachment{}, nil, err
	}
	a := out.Data.(Attachment)
	b, err := base64.StdEncoding.DecodeString(a.Data)
	a.Data = ""
	return a, b, err
}
