package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"epivra/internal/httpapi"
	"epivra/internal/hub"
	"epivra/internal/mcpserver"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func app(t *testing.T, f *fixture) *httptest.Server {
	mux := http.NewServeMux()
	httpapi.Register(mux, f.s)
	if err := mcpserver.Register(mux, f.s, mcpserver.Config{ReadPath: "/mcp", WritePath: "/mcp/write", Origins: []string{"https://trusted.example"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.Limit(f.s, mux))
	t.Cleanup(srv.Close)
	return srv
}

type wire struct {
	t                  *testing.T
	srv                *httptest.Server
	path, key, session string
	seq                int
}

func openWire(t *testing.T, srv *httptest.Server, key, path string) *wire {
	w := &wire{t: t, srv: srv, key: key, path: path}
	r := w.rpc("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}, false)
	if r["result"].(map[string]any)["protocolVersion"] != "2025-11-25" {
		t.Fatal(r)
	}
	w.rpc("notifications/initialized", map[string]any{}, true)
	return w
}
func (w *wire) rpc(method string, params any, notify bool) map[string]any {
	w.t.Helper()
	w.seq++
	p := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if !notify {
		p["id"] = w.seq
	}
	b, _ := json.Marshal(p)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", w.srv.URL+w.path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+w.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	if w.session != "" {
		req.Header.Set("Mcp-Session-Id", w.session)
	}
	resp, err := w.srv.Client().Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		w.t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		w.session = id
	}
	if notify {
		return nil
	}
	var out map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		w.t.Fatal(err)
	}
	return out
}
func (w *wire) tool(name string, p any) hub.Result {
	w.t.Helper()
	r := w.rpc("tools/call", map[string]any{"name": name, "arguments": p}, false)
	res, ok := r["result"].(map[string]any)
	if !ok || res["isError"] == true {
		w.t.Fatal(r)
	}
	var out hub.Result
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		w.t.Fatal(err)
	}
	return out
}
func TestReadAndWriteMCP(t *testing.T) {
	f := setup(t)
	srv := app(t, f)
	ro := openWire(t, srv, f.key, "/mcp")
	rw := openWire(t, srv, f.key, "/mcp/write")
	for _, item := range []struct {
		w *wire
		n int
	}{{ro, 2}, {rw, 3}} {
		res := item.w.rpc("tools/list", map[string]any{}, false)
		tools := res["result"].(map[string]any)["tools"].([]any)
		if len(tools) != item.n {
			t.Fatal("wrong tool visibility", res)
		}
	}
	attempt := ro.rpc("tools/call", map[string]any{"name": "save", "arguments": map[string]any{}}, false)
	if attempt["error"] == nil {
		t.Fatal("read endpoint accepted hidden save", attempt)
	}
	source := ref(rw.tool("save", hub.Params{WorkspaceID: f.ws, Type: "source", Body: "runtime history", Provenance: "session", IdempotencyKey: "source"}))
	p := map[string]any{"workspace_id": f.ws, "type": "knowledge", "body": "必须刷新缓存", "scope": map[string]any{"version": "v1"}, "sources": []string{source.ID}, "reason": "Dream extracted", "idempotency_key": "knowledge"}
	k := ref(rw.tool("save", p))
	if ref(rw.tool("save", p)) != k {
		t.Fatal("idempotency")
	}
	ro.tool("read", hub.Params{WorkspaceID: f.ws, Type: "knowledge", ID: k.ID, History: true})
	out := ro.tool("search", hub.Params{WorkspaceID: f.ws, Query: "刷新", Scope: map[string]any{"version": "v1"}})
	if len(out.Data.([]any)) != 1 {
		t.Fatal("MCP search", out)
	}
	// New read sessions can query state saved by a separate Dream write session.
	reconnected := openWire(t, srv, f.key, "/mcp")
	reconnected.tool("read", hub.Params{WorkspaceID: f.ws, Type: "knowledge", ID: k.ID, Revision: 1})
}
func TestEndpointAuthenticationIsolationAndREST(t *testing.T) {
	f := setup(t)
	srv := app(t, f)
	for _, path := range []string{"/mcp", "/mcp/write", "/v1/search", "/v1/save"} {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(`{}`))
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatal(path, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("untrusted Origin")
	}
	rw := openWire(t, srv, f.key, "/mcp/write")
	actor2, key2 := hub.NewID(), "second_"+hub.NewID()
	h := sha256.Sum256([]byte(key2))
	f.s.DB.Exec(`INSERT INTO api_keys(id,name,digest) VALUES($1,'second',$2)`, actor2, h[:])
	f.s.DB.Exec(`INSERT INTO key_permissions VALUES($1,$2,false)`, actor2, f.ws)
	for _, other := range []struct{ key, path string }{{key2, "/mcp/write"}, {f.key, "/mcp"}} {
		req, _ := http.NewRequest("POST", srv.URL+other.path, strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/list","params":{}}`))
		req.Header.Set("Authorization", "Bearer "+other.key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Session-Id", rw.session)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 && resp.StatusCode != 403 {
			t.Fatal("session reused across endpoint or actor", resp.StatusCode)
		}
	}
	readonly := openWire(t, srv, key2, "/mcp/write")
	res := readonly.rpc("tools/call", map[string]any{"name": "save", "arguments": map[string]any{"workspace_id": f.ws, "type": "source", "body": "denied", "provenance": "x", "idempotency_key": "denied"}}, false)
	if res["result"].(map[string]any)["isError"] != true {
		t.Fatal("read-only key wrote")
	}
	payload := map[string]any{"workspace_id": f.ws, "type": "source", "body": "REST history", "provenance": "REST", "idempotency_key": "rest"}
	b, _ := json.Marshal(payload)
	req, _ = http.NewRequest("POST", srv.URL+"/v1/save", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("REST save", resp.StatusCode)
	}
	f.s.DB.Exec(`UPDATE api_keys SET revoked_at=now() WHERE id=$1`, f.actor)
	req, _ = http.NewRequest("POST", srv.URL+"/mcp/write", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("Mcp-Session-Id", rw.session)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("revoked key accepted")
	}
}
