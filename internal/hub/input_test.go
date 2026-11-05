package hub

import "testing"

func TestInputSchemas(t *testing.T) {
	for _, raw := range []string{`null`, `{} {}`, `{"workspace_id":"x","type":"claim","body":"x","claim":{}}`, `{"workspace_id":"x","type":"knowledge","body":"x","scope":null,"idempotency_key":"x"}`, `{"workspace_id":"x","type":"knowledge","body":"x","scope":[],"idempotency_key":"x"}`} {
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
