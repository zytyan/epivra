package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	t              *testing.T
	s              *hub.Service
	actor, ws, key string
	ctx            context.Context
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run real PostgreSQL/pgvector integration tests")
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
	f := &fixture{t: t, s: &hub.Service{DB: db}, actor: hub.NewID(), ws: hub.NewID(), key: "test_" + hub.NewID(), ctx: ctx}
	hash := sha256.Sum256([]byte(f.key))
	if _, err = db.Exec(`INSERT INTO workspaces(id,name) VALUES($1,'test')`, f.ws); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO api_keys(id,name,digest) VALUES($1,'agent',$2)`, f.actor, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO key_permissions(key_id,workspace_id,can_write) VALUES($1,$2,true)`, f.actor, f.ws); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) call(op string, p hub.Params) hub.Result {
	f.t.Helper()
	p.WorkspaceID = f.ws
	if hub.IsWrite(op) && p.IdempotencyKey == "" {
		p.IdempotencyKey = hub.NewID()
	}
	r, err := f.s.Execute(f.ctx, f.actor, op, p)
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
	return ref(f.call("source_create", hub.Params{Body: body, Provenance: "integration", SourceType: "text", Tags: []string{"中文"}}))
}
func (f *fixture) loop() (hub.Ref, hub.Ref, hub.Ref, hub.Ref) {
	src := f.source("中文证据：实验成功")
	c := ref(f.call("claim_create", hub.Params{Body: "中文主张", Sources: []hub.Ref{src}}))
	now := time.Now().UTC()
	v := ref(f.call("verification_create", hub.Params{Body: "observed", Claim: &c, Method: "experiment", PerformedAt: &now, Conclusion: "support", Evidence: []hub.Ref{src}}))
	a := ref(f.call("assessment_create", hub.Params{Body: "supported within conditions", Claim: &c, Conclusion: "support", Verifications: []hub.Ref{v}}))
	k := ref(f.call("knowledge_publish", hub.Params{Body: "中文知识", Claims: []hub.Ref{c}, Assessments: []hub.Ref{a}, Tags: []string{"中文"}}))
	return src, c, a, k
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var e *hub.Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("expected %s, got %v", want, err)
	}
}
func TestLifecycleAndDatabaseGuards(t *testing.T) {
	f := setup(t)
	src, c, a, k := f.loop()
	source2 := ref(f.call("source_tags_update", hub.Params{ID: src.ID, ExpectedVersion: 1, Reason: "retag", Tags: []string{"new"}}))
	if source2.Version != 2 {
		t.Fatal(source2)
	}
	f.call("claim_revise", hub.Params{ID: c.ID, ExpectedVersion: 1, Body: "changed claim", Reason: "new context", Sources: []hub.Ref{source2}})
	read := f.call("knowledge_read", hub.Params{Ref: &k, Depth: 5, Limit: 100}).Data.(hub.ReadData)
	found := false
	for _, r := range read.Evidence {
		if r.ID == c.ID {
			found = true
			if r.Version != 1 || r.Body != "中文主张" {
				t.Fatal("historical evidence changed")
			}
		}
	}
	if !found {
		t.Fatal("claim missing")
	}
	if len(read.ReviewEvents) != 1 {
		t.Fatalf("missing review event: %v", read.ReviewEvents)
	}
	now := time.Now().UTC()
	v := ref(f.call("verification_create", hub.Params{Body: "contradiction", Claim: &c, Method: "counterexample", PerformedAt: &now, Conclusion: "refute", Evidence: []hub.Ref{src}}))
	other := ref(f.call("assessment_create", hub.Params{Body: "refuted", Claim: &c, Conclusion: "refute", Verifications: []hub.Ref{v}}))
	read = f.call("knowledge_read", hub.Params{Ref: &k, Depth: 1, Limit: 100}).Data.(hub.ReadData)
	if len(read.Assessments) != 2 {
		t.Fatal("contradicting assessments must coexist")
	}
	_, err := f.s.Execute(f.ctx, f.actor, "knowledge_publish", hub.Params{WorkspaceID: f.ws, IdempotencyKey: hub.NewID(), Body: "unsupported", Claims: []hub.Ref{c}, Assessments: []hub.Ref{other}})
	code(t, err, "invalid_reference")
	queries := []string{
		`UPDATE revisions SET body='tampered' WHERE workspace_id=$1 AND object_id=$2`,
		`DELETE FROM objects WHERE workspace_id=$1 AND id=$2`,
		`DELETE FROM revisions WHERE workspace_id=$1 AND object_id=$2`,
		`INSERT INTO attachments(workspace_id,id,source_id,name,mime,size,sha256,content) VALUES($1,gen_random_uuid(),$2,'late','text/plain',1,'x','x')`,
	}
	for _, q := range queries {
		if _, err = f.s.DB.Exec(q, f.ws, src.ID); err == nil {
			t.Fatal("database allowed immutable mutation", q)
		}
	}
	tx, _ := f.s.DB.Begin()
	_, err = tx.Exec(`INSERT INTO revisions(workspace_id,object_id,version,kind,body,tags,author,provenance,source_type) VALUES($1,$2,3,'source','changed','{}',$3,'integration','text')`, f.ws, src.ID, f.actor)
	tx.Rollback()
	if err == nil {
		t.Fatal("database allowed source content revision")
	}
	// A foreign workspace can never be linked, even if the actor has access to both.
	ws2 := hub.NewID()
	f.s.DB.Exec(`INSERT INTO workspaces(id,name) VALUES($1,'second')`, ws2)
	f.s.DB.Exec(`INSERT INTO key_permissions(key_id,workspace_id,can_write) VALUES($1,$2,true)`, f.actor, ws2)
	_, err = f.s.Execute(f.ctx, f.actor, "claim_create", hub.Params{WorkspaceID: ws2, IdempotencyKey: hub.NewID(), Body: "cross workspace", Sources: []hub.Ref{src}})
	code(t, err, "not_found")
	_ = a
}
func TestIdempotencyAndConcurrentVersions(t *testing.T) {
	f := setup(t)
	p := hub.Params{WorkspaceID: f.ws, IdempotencyKey: "same", Body: "repeated", Provenance: "test", SourceType: "text"}
	var wg sync.WaitGroup
	ids := make(chan hub.Ref, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := f.s.Execute(f.ctx, f.actor, "source_create", p)
			if err != nil {
				errs <- err
			} else {
				ids <- ref(r)
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var id string
	for r := range ids {
		if id != "" && id != r.ID {
			t.Fatal("duplicate creation")
		}
		id = r.ID
	}
	p.Body = "different"
	_, err := f.s.Execute(f.ctx, f.actor, "source_create", p)
	code(t, err, "idempotency_conflict")
	versions := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := f.s.Execute(f.ctx, f.actor, "source_tags_update", hub.Params{WorkspaceID: f.ws, IdempotencyKey: fmt.Sprint(i), ID: id, ExpectedVersion: 1, Reason: "race", Tags: []string{fmt.Sprint(i)}})
			versions <- e
		}(i)
	}
	wg.Wait()
	close(versions)
	success, conflict := 0, 0
	for e := range versions {
		if e == nil {
			success++
		} else {
			code(t, e, "version_conflict")
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("lost update", success, conflict)
	}
}
func TestReviewAcknowledgesOnlyExplicitEvents(t *testing.T) {
	f := setup(t)
	src, c, a, k := f.loop()
	now := time.Now().UTC()
	verify := func() hub.Ref {
		return ref(f.call("verification_create", hub.Params{Body: "new evidence", Claim: &c, Method: "test", PerformedAt: &now, Conclusion: "uncertain", Evidence: []hub.Ref{src}}))
	}
	verify()
	d := f.call("knowledge_read", hub.Params{Ref: &k}).Data.(hub.ReadData)
	var event struct {
		ID string `json:"id"`
	}
	json.Unmarshal(d.ReviewEvents[0], &event)
	// Concurrent evidence may arrive after the agent read the review queue.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, e := f.s.Execute(f.ctx, f.actor, "verification_create", hub.Params{WorkspaceID: f.ws, IdempotencyKey: hub.NewID(), Body: "arrived during review", Claim: &c, Method: "test", PerformedAt: &now, Conclusion: "refute", Evidence: []hub.Ref{src}})
		errs <- e
	}()
	go func() {
		defer wg.Done()
		_, e := f.s.Execute(f.ctx, f.actor, "knowledge_publish", hub.Params{WorkspaceID: f.ws, IdempotencyKey: hub.NewID(), ID: k.ID, ExpectedVersion: 1, Reason: "review", Body: "reviewed knowledge", Claims: []hub.Ref{c}, Assessments: []hub.Ref{a}, ResolveEvents: []string{event.ID}})
		errs <- e
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	current := k
	current.Version = 0
	d = f.call("knowledge_read", hub.Params{Ref: &current}).Data.(hub.ReadData)
	if len(d.ReviewEvents) != 1 {
		t.Fatalf("concurrent event lost: %s", mustJSON(d.ReviewEvents))
	}
	pending := true
	hits := f.call("knowledge_search", hub.Params{Mode: "keyword", PendingReview: &pending, Types: []string{"knowledge"}}).Data.([]hub.Hit)
	if len(hits) != 1 {
		t.Fatal("review filter")
	}
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func TestSearchIndexAndLeaseRecovery(t *testing.T) {
	f := setup(t)
	requireVector(t, f)
	src, _, _, k := f.loop()
	hits := f.call("knowledge_search", hub.Params{Mode: "keyword", Query: "文知", Tags: []string{"中文"}}).Data.([]hub.Hit)
	if len(hits) != 1 || hits[0].Ref != k {
		t.Fatalf("Chinese substring or tag filter failed: %+v", hits)
	}
	fallback := f.call("knowledge_search", hub.Params{Mode: "hybrid", Query: "中文"})
	if len(fallback.Warnings) == 0 {
		t.Fatal("missing fallback warning")
	}
	_, err := f.s.Execute(f.ctx, f.actor, "knowledge_search", hub.Params{WorkspaceID: f.ws, Mode: "semantic", Query: "中文"})
	code(t, err, "semantic_unavailable")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		data := []any{}
		for i := range q.Input {
			v := make([]float64, q.Dimensions)
			v[0] = 1
			data = append(data, map[string]any{"index": i, "embedding": v})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer api.Close()
	f.s.Embed = &hub.Embedder{BaseURL: api.URL, Model: "test", Dimensions: 3, BatchSize: 2, Timeout: time.Second}
	if err = f.s.ConfigureIndex(f.ctx); err != nil {
		t.Fatal(err)
	}
	lag := f.call("knowledge_search", hub.Params{Mode: "semantic", Query: "中文"})
	if len(lag.Warnings) == 0 {
		t.Fatal("missing lag warning")
	}
	jobs := make(chan hub.Job, 20)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, e := f.s.ClaimJob(f.ctx)
			if e == nil {
				jobs <- j
			} else if !errors.Is(e, sql.ErrNoRows) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	close(jobs)
	seen := map[int64]bool{}
	var first hub.Job
	for j := range jobs {
		if seen[j.ID] {
			t.Fatal("duplicate lease")
		}
		seen[j.ID] = true
		if first.ID == 0 {
			first = j
		} else if err = f.s.RunJob(f.ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if first.ID == 0 {
		t.Fatal("no indexing jobs")
	}
	f.s.DB.Exec(`UPDATE index_jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, first.ID)
	recovered, e := f.s.ClaimJob(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	if recovered.ID != first.ID || recovered.Token == first.Token {
		t.Fatal("lease not recovered")
	}
	if err = f.s.RunJob(f.ctx, first); err == nil {
		t.Fatal("stale owner committed")
	}
	if err = f.s.RunJob(f.ctx, recovered); err != nil {
		t.Fatal(err)
	}
	semantic := f.call("knowledge_search", hub.Params{Mode: "semantic", Query: "中文"})
	if len(semantic.Data.([]hub.Hit)) == 0 || len(semantic.Warnings) != 0 {
		t.Fatal("index incomplete", semantic)
	}
	// An old revision task cannot make new revision appear indexed.
	f.call("source_tags_update", hub.Params{ID: src.ID, ExpectedVersion: 1, Reason: "tags", Tags: []string{"changed"}})
	old, e := f.s.ClaimJob(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	f.call("source_tags_update", hub.Params{ID: src.ID, ExpectedVersion: 2, Reason: "tags again", Tags: []string{"latest"}})
	if err = f.s.RunJob(f.ctx, old); err != nil {
		t.Fatal(err)
	}
	lag = f.call("knowledge_search", hub.Params{Mode: "semantic", Query: "中文"})
	if len(lag.Warnings) == 0 {
		t.Fatal("old job masked new revision lag")
	}
	oldFP := f.s.Embed.Fingerprint()
	f.s.Embed.Model = "new-model"
	f.s.Embed.Dimensions = 4
	if err = f.s.ConfigureIndex(f.ctx); err != nil {
		t.Fatal(err)
	}
	if oldFP == f.s.Embed.Fingerprint() {
		t.Fatal("model fingerprint unchanged")
	}
	changed := f.call("knowledge_search", hub.Params{Mode: "semantic", Query: "中文"})
	if len(changed.Data.([]hub.Hit)) != 0 || len(changed.Warnings) == 0 {
		t.Fatal("model vectors mixed")
	}
	api.Close()
	failed := f.call("knowledge_search", hub.Params{Mode: "hybrid", Query: "中文"})
	if len(failed.Data.([]hub.Hit)) == 0 || len(failed.Warnings) == 0 {
		t.Fatal("API failure did not degrade")
	}
}
func TestAttachmentAndReadOnlyKey(t *testing.T) {
	f := setup(t)
	src := ref(f.call("source_create", hub.Params{Provenance: "test", SourceType: "binary", Attachments: []hub.AttachmentInput{{Name: "x.bin", MIME: "application/octet-stream", Data: base64.StdEncoding.EncodeToString([]byte{0, 1, 255})}}}))
	d := f.call("knowledge_read", hub.Params{Ref: &src}).Data.(hub.ReadData)
	if len(d.Attachments) != 1 || d.Attachments[0].Data != "" {
		t.Fatal("attachment manifest invalid")
	}
	a := f.call("attachment_read", hub.Params{ID: d.Attachments[0].ID}).Data.(hub.Attachment)
	if a.Data != "AAH/" || a.Size != 3 {
		t.Fatal(a)
	}
	f.s.DB.Exec(`UPDATE key_permissions SET can_write=false WHERE key_id=$1`, f.actor)
	_, err := f.s.Execute(f.ctx, f.actor, "source_create", hub.Params{WorkspaceID: f.ws, IdempotencyKey: "denied"})
	code(t, err, "forbidden")
	f.call("knowledge_read", hub.Params{Ref: &src})
	f.s.DB.Exec(`UPDATE api_keys SET revoked_at=now() WHERE id=$1`, f.actor)
	_, err = f.s.Authenticate(f.ctx, f.key)
	code(t, err, "unauthorized")
}

func TestOutstandingEventsBeforePublicationAndRemovedClaims(t *testing.T) {
	f := setup(t)
	src, c, a, k := f.loop()
	now := time.Now().UTC()
	f.call("verification_create", hub.Params{Body: "new issue", Claim: &c, Method: "test", PerformedAt: &now, Conclusion: "refute", Evidence: []hub.Ref{src}})
	// Newly published knowledge must see relevant events that predate publication.
	newer := ref(f.call("knowledge_publish", hub.Params{Body: "new publication", Claims: []hub.Ref{c}, Assessments: []hub.Ref{a}}))
	d := f.call("knowledge_read", hub.Params{Ref: &newer}).Data.(hub.ReadData)
	if len(d.ReviewEvents) != 1 {
		t.Fatal("pre-publication evidence lost")
	}
	var ev struct {
		ID string `json:"id"`
	}
	json.Unmarshal(d.ReviewEvents[0], &ev)
	_, otherClaim, otherAssessment, _ := f.loop()
	// Explicitly removing a previously depended-on claim can acknowledge its event.
	f.call("knowledge_publish", hub.Params{ID: k.ID, ExpectedVersion: 1, Reason: "remove old claim", Body: "uses another claim", Claims: []hub.Ref{otherClaim}, Assessments: []hub.Ref{otherAssessment}, ResolveEvents: []string{ev.ID}})
	k.Version = 0
	after := f.call("knowledge_read", hub.Params{Ref: &k}).Data.(hub.ReadData)
	if len(after.ReviewEvents) != 0 {
		t.Fatal("removed claim event cannot be acknowledged")
	}
	ws2 := hub.NewID()
	f.s.DB.Exec(`INSERT INTO workspaces(id,name) VALUES($1,'empty')`, ws2)
	f.s.DB.Exec(`INSERT INTO key_permissions(key_id,workspace_id,can_write) VALUES($1,$2,true)`, f.actor, ws2)
	isolated, err := f.s.Execute(f.ctx, f.actor, "knowledge_search", hub.Params{WorkspaceID: ws2, Mode: "keyword", Query: "中文"})
	if err != nil {
		t.Fatal(err)
	}
	if len(isolated.Data.([]hub.Hit)) != 0 {
		t.Fatal("cross workspace search leak")
	}
}

func TestInlineEvidenceAndRetryExhaustion(t *testing.T) {
	f := setup(t)
	requireVector(t, f)
	source := f.source("seed")
	claim := ref(f.call("claim_create", hub.Params{Body: "test claim", Sources: []hub.Ref{source}}))
	now := time.Now().UTC()
	verification := ref(f.call("verification_create", hub.Params{Body: "inline output", Claim: &claim, Method: "test", PerformedAt: &now, Conclusion: "support", OutputSource: &hub.SourceInput{Body: "output source", Provenance: "tool", SourceType: "result"}}))
	d := f.call("knowledge_read", hub.Params{Ref: &verification, Depth: 2, Limit: 100}).Data.(hub.ReadData)
	found := false
	for _, r := range d.Evidence {
		if r.Kind == "source" && r.Body == "output source" {
			found = true
		}
	}
	if !found {
		t.Fatal("inline output source missing")
	}
	badAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer badAPI.Close()
	f.s.Embed = &hub.Embedder{BaseURL: badAPI.URL, Model: "test", Dimensions: 3, BatchSize: 2, Timeout: time.Second}
	if err := f.s.ConfigureIndex(f.ctx); err != nil {
		t.Fatal(err)
	}
	var target int64
	for i := 0; i < 5; i++ {
		j, err := f.s.ClaimJob(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if target == 0 {
			target = j.ID
		}
		if j.ID != target {
			t.Fatal("unexpected retry job")
		}
		if err = f.s.RunJob(f.ctx, j); err == nil {
			t.Fatal("503 should fail job")
		}
		if _, err = f.s.DB.Exec(`UPDATE index_jobs SET available_at=now() WHERE id=$1`, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	var attempts int
	if err := f.s.DB.QueryRow(`SELECT state,attempts FROM index_jobs WHERE id=$1`, target).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || attempts != 5 {
		t.Fatal("unbounded retry", state, attempts)
	}
}

func requireVector(t *testing.T, f *fixture) {
	t.Helper()
	var enabled bool
	if err := f.s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='vector')`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("semantic indexing test requires pgvector enabled in TEST_DATABASE_URL")
	}
}

func TestWithoutVector(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	// A separate database guarantees the extension is absent without dropping it
	// from the caller's database or from another concurrently running test.
	name := "optional_" + strings.ReplaceAll(hub.NewID(), "-", "")
	if _, err = admin.Exec(`CREATE DATABASE ` + name + ` TEMPLATE template0 ENCODING 'UTF8'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP DATABASE ` + name); err != nil {
			t.Error(err)
		}
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	t.Setenv("TEST_DATABASE_URL", u.String())
	f := setup(t)
	_, _, _, k := f.loop()
	var chunks sql.NullString
	if err = f.s.DB.QueryRow(`SELECT to_regclass('chunks')::text`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks.Valid {
		t.Fatal("vector table created without extension")
	}
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var q struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		data := []any{}
		for i := range q.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 1, 1}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer api.Close()
	f.s.Embed = &hub.Embedder{BaseURL: api.URL, Model: "optional", Dimensions: 3, BatchSize: 16, Timeout: time.Second}
	if err = f.s.ConfigureIndex(f.ctx); err != nil {
		t.Fatal("missing plugin prevented startup", err)
	}
	keyword := f.call("knowledge_search", hub.Params{Mode: "keyword", Query: "中文知识"})
	if len(keyword.Data.([]hub.Hit)) != 1 {
		t.Fatal("keyword unavailable")
	}
	hybrid := f.call("knowledge_search", hub.Params{Mode: "hybrid", Query: "中文知识"})
	if len(hybrid.Data.([]hub.Hit)) != 1 || len(hybrid.Warnings) == 0 || !strings.Contains(hybrid.Warnings[0], "pgvector") {
		t.Fatal("missing explicit fallback", hybrid)
	}
	_, err = f.s.Execute(f.ctx, f.actor, "knowledge_search", hub.Params{WorkspaceID: f.ws, Mode: "semantic", Query: "中文知识"})
	code(t, err, "semantic_unavailable")
	if calls != 0 {
		t.Fatal("embedding API called without pgvector")
	}
	if _, err = f.s.ClaimJob(f.ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("index work claimed without plugin", err)
	}
	var jobs int
	if err = f.s.DB.QueryRow(`SELECT count(*) FROM index_jobs`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("jobs enqueued without plugin", jobs, err)
	}
	f.call("knowledge_read", hub.Params{Ref: &k, Depth: 5})
}
