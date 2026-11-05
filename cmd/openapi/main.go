// Command openapi emits the REST contract from the same schemas used by MCP.
package main

import (
	"encoding/json"
	"epivra/internal/hub"
	"os"
)

func main() {
	paths := map[string]any{}
	aliases := map[string]string{"source_create": "/v1/sources", "source_tags_update": "/v1/sources/tags", "claim_create": "/v1/claims", "claim_revise": "/v1/claims/revisions", "verification_create": "/v1/verifications", "assessment_create": "/v1/assessments", "knowledge_publish": "/v1/knowledge", "knowledge_read": "/v1/read", "knowledge_history": "/v1/history", "knowledge_search": "/v1/search"}
	for _, op := range hub.Operations {
		content := map[string]any{"application/json": map[string]any{"schema": hub.Schema(op)}}
		if op == "source_create" {
			content["multipart/form-data"] = map[string]any{"schema": map[string]any{"type": "object", "required": []string{"metadata"}, "properties": map[string]any{"metadata": map[string]any{"type": "string", "description": "JSON source_create parameters, including workspace_id and idempotency_key"}, "attachments": map[string]any{"type": "array", "items": map[string]any{"type": "string", "format": "binary"}}}}}
		}
		responses := map[string]any{"200": map[string]any{"description": "Operation completed", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Result"}}}}}
		for _, status := range []string{"400", "401", "403", "404", "409", "413", "500", "503"} {
			responses[status] = map[string]any{"description": "Stable error code and optional details", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}}}}
		}
		method := map[string]any{"operationId": op, "requestBody": map[string]any{"required": true, "content": content}, "responses": responses}
		path := aliases[op]
		if path == "" {
			path = "/v1/operations/" + op
		}
		paths[path] = map[string]any{"post": method}
		if path != "/v1/operations/"+op {
			alias := map[string]any{}
			for k, v := range method {
				alias[k] = v
			}
			alias["operationId"] = op + "_operation"
			paths["/v1/operations/"+op] = map[string]any{"post": alias}
		}
	}
	paths["/v1/attachments/{id}"] = map[string]any{"get": map[string]any{"operationId": "attachment_download", "parameters": []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}}, map[string]any{"name": "workspace_id", "in": "query", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}}}, "responses": map[string]any{"200": map[string]any{"description": "Attachment bytes; Content-Disposition attachment, ETag SHA-256", "content": map[string]any{"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}}}}}
	for _, p := range []string{"/healthz", "/readyz"} {
		paths[p] = map[string]any{"get": map[string]any{"security": []any{}, "responses": map[string]any{"200": map[string]any{"description": "Healthy/ready"}, "503": map[string]any{"description": "Database not ready"}}}}
	}
	spec := map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Knowledge Hub", "version": "0.1.0", "description": "All writes require idempotency_key. claim_revise, source_tags_update, and knowledge_publish with id require expected_version and reason. Evidence references are pinned to versions. Timestamps use RFC3339. No automatic adjudication."}, "paths": paths, "security": []any{map[string]any{"bearerAuth": []any{}}}, "components": map[string]any{"securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"}}, "schemas": map[string]any{
		"Result": map[string]any{"type": "object", "required": []string{"data", "warnings"}, "properties": map[string]any{"data": map[string]any{}, "warnings": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "page": map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer"}, "offset": map[string]any{"type": "integer"}, "has_more": map[string]any{"type": "boolean"}}}}},
		"Error":  map[string]any{"type": "object", "required": []string{"error"}, "properties": map[string]any{"error": map[string]any{"type": "object", "required": []string{"code", "message"}, "properties": map[string]any{"code": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"}, "details": map[string]any{}}}}},
	}}}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(spec); err != nil {
		panic(err)
	}
}
