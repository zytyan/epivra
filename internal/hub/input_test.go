package hub

import "testing"

func TestInputSchemas(t *testing.T) {
	for _, raw := range []string{`null`, `{} {}`, `{"type":"claim","body":"x","claim":{}}`, `{"type":"knowledge","body":"x","scope":null,"idempotency_key":"x"}`, `{"type":"knowledge","body":"x","scope":[],"idempotency_key":"x"}`} {
		if _, err := DecodeOperation("save", []byte(raw)); err == nil {
			t.Fatal("invalid input accepted", raw)
		}
	}
	for _, op := range []string{"claim_create", "knowledge_read", "source_create"} {
		if _, err := DecodeOperation(op, []byte(`{}`)); err == nil {
			t.Fatal("legacy operation accepted", op)
		}
	}
	for _, op := range Operations {
		if _, ok := Schema(op)["properties"].(map[string]any)["workspace_id"]; ok {
			t.Fatal("workspace is exposed", op)
		}
	}
	for _, raw := range []string{`{"type":"source","body":"x","provenance":"x","idempotency_key":"x"}`, `{"type":"knowledge","body":"x","scope":{},"sources":["x"],"reason":"x","idempotency_key":"x"}`} {
		if _, err := DecodeOperation("save", []byte(raw)); err != nil {
			t.Fatal("default workspace input rejected", err)
		}
	}
	for _, op := range Operations {
		if len(Schema(op)["properties"].(map[string]any)) < 2 {
			t.Fatal("empty schema", op)
		}
	}
	for i := 0; i < 10; i++ {
		if !UUID(NewID()) {
			t.Fatal("invalid generated UUID")
		}
	}
}
