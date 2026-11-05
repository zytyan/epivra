package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestEmbeddingBatchesAndOrdering(t *testing.T) {
	calls := 0
	e := &Embedder{BaseURL: "https://embedding.test/v1", APIKey: "secret", Model: "test", Dimensions: 2, BatchSize: 2, Timeout: time.Second}
	e.Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("invalid endpoint or auth")
		}
		var p struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
		if len(p.Input) > 2 || p.Dimensions != 2 || p.Model != "test" {
			t.Fatal("bad request")
		}
		data := []any{}
		for i := len(p.Input) - 1; i >= 0; i-- {
			data = append(data, map[string]any{"index": i, "embedding": []float64{float64(i + 1), 1}})
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(marshal(map[string]any{"data": data}))), Header: http.Header{}}, nil
	})}
	vectors, err := e.Vectors(context.Background(), []string{"甲", "乙", "丙"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(vectors) != 3 || vectors[0][0] != 1 || vectors[1][0] != 2 {
		t.Fatalf("wrong batching: %v", vectors)
	}
	original := e.Fingerprint()
	e.APIKey = "rotated"
	if e.Fingerprint() != original {
		t.Fatal("key rotation should not rebuild")
	}
	e.Model = "new"
	if e.Fingerprint() == original {
		t.Fatal("model must change fingerprint")
	}
	e.Model = "test"
	e.Dimensions = 3
	if e.Fingerprint() == original {
		t.Fatal("dimensions must change fingerprint")
	}
}
func TestEmbeddingFailures(t *testing.T) {
	for _, body := range []string{`{"data":[]}`, `{"data":[{"index":0,"embedding":[1]}]}`, `{"data":[{"index":3,"embedding":[1,1]}]}`, `{"data":[{"index":0,"embedding":[0,0]}]}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			e := &Embedder{BaseURL: "https://embed.test", Model: "x", Dimensions: 2, BatchSize: 2, Timeout: time.Second, Client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			if _, err := e.Vectors(context.Background(), []string{"x"}); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
	var e *Embedder
	if _, err := e.Vectors(context.Background(), []string{"x"}); err == nil {
		t.Fatal("missing config must fail")
	}
}
func TestSplitUnicodeAndOverlap(t *testing.T) {
	text := strings.Repeat("中文🙂", 700)
	chunks := Split(text)
	if len(chunks) != 3 {
		t.Fatal(len(chunks))
	}
	for i, c := range chunks {
		if c.Position != i || c.Start != 700*i || c.Text != string([]rune(text)[c.Start:min(c.Start+800, len([]rune(text)))]) {
			t.Fatal("chunk positions do not preserve text")
		}
	}
	if len(Split("")) != 0 {
		t.Fatal("empty chunks")
	}
}
func TestRRF(t *testing.T) {
	a := Hit{Ref: Ref{"claim", "a", 1}, Methods: []string{"keyword"}}
	b := Hit{Ref: Ref{"claim", "b", 2}, Methods: []string{"keyword"}}
	c := b
	c.Methods = []string{"semantic"}
	out := RRF([]Hit{a, b}, []Hit{c})
	if len(out) != 2 || out[0].Ref != b.Ref || len(out[0].Methods) != 2 {
		t.Fatalf("unexpected fusion: %+v", out)
	}
	old := b
	old.Ref.Version = 1
	if len(RRF([]Hit{b}, []Hit{old})) != 2 {
		t.Fatal("versions collapsed")
	}
}
func TestInputValidation(t *testing.T) {
	for _, raw := range []string{`null`, `{} {}`, `{"workspace_id":"x","extra":1}`, `{"workspace_id":"x","id":null}`, `{"workspace_id":"x","id":"x","body":"ignored"}`} {
		if _, err := DecodeOperation("attachment_read", []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	raw := `{"workspace_id":"00000000-0000-0000-0000-000000000001","id":"00000000-0000-0000-0000-000000000002"}`
	if _, err := DecodeOperation("attachment_read", []byte(raw)); err != nil {
		t.Fatal(err)
	}
	for _, op := range Operations {
		if len(Schema(op)["properties"].(map[string]any)) < 2 {
			t.Fatal("missing schema", op)
		}
	}
	for i := 0; i < 50; i++ {
		if !UUID(NewID()) {
			t.Fatal("invalid generated UUID")
		}
	}
}
