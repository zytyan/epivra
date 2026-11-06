// Command openapi emits REST input schemas shared with MCP.
package main

import (
	"encoding/json"
	"epivra/internal/hub"
	"os"
)

func main() {
	paths := map[string]any{}
	for _, op := range hub.Operations {
		responses := map[string]any{}
		for _, code := range []string{"200", "400", "404", "409", "413", "500"} {
			responses[code] = map[string]any{"description": "200: data, warnings and optional page; errors: error.code, error.message and optional error.details"}
		}
		paths["/v1/"+op] = map[string]any{"post": map[string]any{"operationId": op, "requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": hub.Schema(op)}}}, "responses": responses}}
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		paths[path] = map[string]any{"get": map[string]any{"security": []any{}, "responses": map[string]any{"200": map[string]any{"description": "ok"}, "503": map[string]any{"description": "not ready"}}}}
	}
	spec := map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Epivra", "version": "0.2.0", "description": "Source and atomic Knowledge in a shared default workspace. No authentication or workspace selection. Knowledge revisions and software versions in JSONB scope are independent. Save requires idempotency_key; Knowledge requires scope, sources, reason, and expected_revision when id is supplied."}, "paths": paths, "security": []any{}}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(spec); err != nil {
		panic(err)
	}
}
