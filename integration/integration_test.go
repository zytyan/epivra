package integration

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"epivra/internal/hub"
	"epivra/schema"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type fixture struct {
	t   *testing.T
	s   *hub.Service
	ws  string
	ctx context.Context
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run real PostgreSQL integration tests")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := "test_" + strings.ReplaceAll(hub.NewID(), "-", "")
	if _, err = admin.Exec(`CREATE SCHEMA ` + schemaName); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schemaName+",public")
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(20)
	t.Cleanup(func() { db.Close(); admin.Exec(`DROP SCHEMA ` + schemaName + ` CASCADE`); admin.Close() })
	ctx := context.Background()
	if err = schema.Initialize(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = schema.Initialize(ctx, db); err != nil {
		t.Fatal("initialization not idempotent", err)
	}
	f := &fixture{t: t, s: &hub.Service{DB: db}, ctx: ctx}
	if err = db.QueryRow(`SELECT id FROM workspaces WHERE name='default'`).Scan(&f.ws); err != nil {
		t.Fatal(err)
	}

	return f
}
func (f *fixture) call(op string, p hub.Params) hub.Result {
	f.t.Helper()
	if hub.IsWrite(op) && p.IdempotencyKey == "" {
		p.IdempotencyKey = hub.NewID()
	}
	r, err := f.s.Execute(f.ctx, op, p)
	if err != nil {
		f.t.Fatalf("%s: %v", op, err)
	}
	return r
}
func ref(r hub.Result) hub.Ref {
	b, _ := json.Marshal(r.Data)
	var v hub.Ref
	json.Unmarshal(b, &v)
	return v
}
func (f *fixture) source(body string) hub.Ref {
	return ref(f.call("save", hub.Params{Type: "source", Body: body, Provenance: "test log"}))
}
func (f *fixture) knowledge(src hub.Ref, scope map[string]any) hub.Ref {
	return ref(f.call("save", hub.Params{Type: "knowledge", Body: "缓存需要显式刷新", Scope: scope, Sources: []string{src.ID}, Reason: "Dream extracted observation"}))
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var e *hub.Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("expected %s, got %v", want, err)
	}
}
func object(t *testing.T, out hub.Result) map[string]any {
	t.Helper()
	d := out.Data.(hub.ReadData)
	var o map[string]any
	if err := json.Unmarshal(d.Object, &o); err != nil {
		t.Fatal(err)
	}
	return o
}
func TestAtomicKnowledgeScopesAndHistory(t *testing.T) {
	f := setup(t)
	s1 := f.source("v1 source")
	s2 := f.source("v2 source")
	oldScope := map[string]any{"software": map[string]any{"version": "1.0", "branch": "stable"}, "hardware": "arm64"}
	newScope := map[string]any{"software": map[string]any{"version": "2.0", "branch": "main"}, "hardware": "arm64"}
	k1 := f.knowledge(s1, oldScope)
	k2 := f.knowledge(s2, newScope)
	updated := ref(f.call("save", hub.Params{Type: "knowledge", ID: k1.ID, ExpectedRevision: 1, Body: "旧软件也可以补充知识", Scope: oldScope, Sources: []string{s1.ID, s2.ID}, Reason: "Dream incorporated another source"}))
	if updated.Revision != 2 {
		t.Fatal(updated)
	}
	old := object(t, f.call("read", hub.Params{Type: "knowledge", ID: k1.ID, Revision: 1}))
	if old["body"] != "缓存需要显式刷新" || len(old["sources"].([]any)) != 1 {
		t.Fatal("historical snapshot changed", old)
	}
	current := f.call("read", hub.Params{Type: "knowledge", ID: k1.ID, History: true, Limit: 1})
	if len(current.Data.(hub.ReadData).History) != 1 || !current.Data.(hub.ReadData).HistoryPage.HasMore {
		t.Fatal("history pagination")
	}
	if object(t, f.call("read", hub.Params{Type: "knowledge", ID: k2.ID}))["revision"] != float64(1) {
		t.Fatal("newer software knowledge overwritten")
	}
	// A new independent fact about old software receives a fresh Knowledge ID.
	extra := f.knowledge(s1, oldScope)
	if extra.ID == k1.ID {
		t.Fatal("implicit scope merge")
	}
	result := f.call("search", hub.Params{Scope: map[string]any{"software": map[string]any{"version": "1.0"}}, Limit: 100})
	hits := result.Data.([]json.RawMessage)
	if len(hits) != 2 {
		t.Fatalf("JSONB scope filter: %s", hits)
	}
	result = f.call("search", hub.Params{Query: "要显式", Scope: newScope})
	if len(result.Data.([]json.RawMessage)) != 1 {
		t.Fatal("Chinese keyword and scope filter")
	}
	result = f.call("search", hub.Params{Scope: map[string]any{"software": map[string]any{"version": "3.0"}}})
	if len(result.Data.([]json.RawMessage)) != 0 {
		t.Fatal("version comparisons inferred")
	}
	result = f.call("search", hub.Params{Since: ptrTime(time.Now().Add(time.Hour)), Types: []string{"source"}})
	if len(result.Data.([]json.RawMessage)) != 0 {
		t.Fatal("incremental source filter")
	}
}
func ptrTime(t time.Time) *time.Time { return &t }
func TestDefaultWorkspaceIdempotencyAndCAS(t *testing.T) {
	f := setup(t)
	p := hub.Params{Type: "source", Body: "same history", Provenance: "log", IdempotencyKey: "same"}
	var wg sync.WaitGroup
	results := make(chan hub.Ref, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := f.s.Execute(f.ctx, "save", p)
			if err != nil {
				errs <- err
			} else {
				results <- ref(r)
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var source hub.Ref
	for r := range results {
		if source.ID != "" && source.ID != r.ID {
			t.Fatal("duplicated source")
		}
		source = r
	}
	p.Body = "different"
	_, err := f.s.Execute(f.ctx, "save", p)
	code(t, err, "idempotency_conflict")
	k := f.knowledge(source, map[string]any{"version": "1"})
	errs = make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.s.Execute(f.ctx, "save", hub.Params{Type: "knowledge", ID: k.ID, ExpectedRevision: 1, Body: "revised", Scope: map[string]any{"version": "1"}, Sources: []string{source.ID}, Reason: "reviewed", IdempotencyKey: hub.NewID()})
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		} else {
			code(t, e, "revision_conflict")
		}
	}
	if success != 1 {
		t.Fatal("lost update")
	}

	_, err = f.s.Execute(f.ctx, "save", hub.Params{Type: "knowledge", Body: "missing evidence", Scope: map[string]any{}, Sources: []string{hub.NewID()}, Reason: "bad reference", IdempotencyKey: hub.NewID()})
	code(t, err, "invalid_reference")
	var count int
	var name string
	if err = f.s.DB.QueryRow(`SELECT count(*),min(name) FROM workspaces`).Scan(&count, &name); err != nil || count != 1 || name != "default" {
		t.Fatal("default workspace initialization", count, name, err)
	}
	if _, err = f.s.DB.Exec(`INSERT INTO workspaces(id,name) VALUES($1,'other')`, hub.NewID()); err == nil {
		t.Fatal("unexpected second workspace")
	}

}
func TestImmutableEvidenceAndAttachments(t *testing.T) {
	f := setup(t)
	source := ref(f.call("save", hub.Params{Type: "source", Body: "binary description", Provenance: "file", Attachments: []hub.AttachmentInput{{Name: "x.bin", MIME: "application/octet-stream", Data: base64.StdEncoding.EncodeToString([]byte{0, 1, 255})}}}))
	k := f.knowledge(source, map[string]any{})
	d := f.call("read", hub.Params{Type: "knowledge", ID: k.ID}).Data.(hub.ReadData)
	if len(d.Attachments) != 1 || d.Attachments[0].Data != "" {
		t.Fatal("bad attachment manifest")
	}
	a := f.call("read", hub.Params{Type: "source", ID: source.ID, AttachmentID: d.Attachments[0].ID}).Data.(hub.Attachment)
	if a.Data != "AAH/" || a.Size != 3 {
		t.Fatal("binary changed")
	}
	other := f.source("different")
	_, err := f.s.Execute(f.ctx, "read", hub.Params{Type: "source", ID: other.ID, AttachmentID: a.ID})
	code(t, err, "not_found")
	checks := []string{`UPDATE sources SET body='changed' WHERE workspace_id=$1 AND id=$2`, `DELETE FROM sources WHERE workspace_id=$1 AND id=$2`, `INSERT INTO attachments(workspace_id,id,source_id,name,mime,size,sha256,content) VALUES($1,gen_random_uuid(),$2,'late','text/plain',1,'x','x')`}
	for _, q := range checks {
		if _, err = f.s.DB.Exec(q, f.ws, source.ID); err == nil {
			t.Fatal("source mutation allowed", q)
		}
	}
	for _, q := range []string{`UPDATE knowledge_revisions SET reason='changed' WHERE workspace_id=$1 AND knowledge_id=$2`, `DELETE FROM knowledge_sources WHERE workspace_id=$1 AND knowledge_id=$2`, `INSERT INTO knowledge_sources VALUES($1,$2,1,$3)`} {
		args := []any{f.ws, k.ID}
		if strings.HasPrefix(q, "INSERT") {
			args = append(args, other.ID)
		}
		if _, err = f.s.DB.Exec(q, args...); err == nil {
			t.Fatal("historical evidence mutable", q)
		}
	}

}
func TestKnowledgeValidationAndRollback(t *testing.T) {
	f := setup(t)
	source := f.source("seed")
	cases := []hub.Params{
		{Type: "knowledge", Body: strings.Repeat("中", 2001), Scope: map[string]any{}, Sources: []string{source.ID}, Reason: "too long"},
		{Type: "knowledge", Body: "no scope", Sources: []string{source.ID}, Reason: "x"},
		{Type: "knowledge", Body: "no source", Scope: map[string]any{}, Reason: "x"},
		{Type: "knowledge", Body: "no reason", Scope: map[string]any{}, Sources: []string{source.ID}},
		{Type: "source", ID: source.ID, Body: "overwrite", Provenance: "x"},
	}
	for _, p := range cases {
		p.IdempotencyKey = hub.NewID()
		_, err := f.s.Execute(f.ctx, "save", p)
		code(t, err, "invalid_input")
	}
	var count int
	if err := f.s.DB.QueryRow(`SELECT count(*) FROM knowledge`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial writes", count, err)
	}
	// New schema has no workflow, indexing, migration or vector objects.
	for _, table := range []string{"claims", "verifications", "assessments", "review_events", "index_jobs", "chunks", "schema_migrations", "api_keys", "key_permissions"} {
		var name sql.NullString
		if err := f.s.DB.QueryRow(`SELECT to_regclass(format('%I.%I',current_schema(),$1::text))::text`, table).Scan(&name); err != nil || name.Valid {
			t.Fatal("legacy table exists", table, err)
		}
	}
}
