package integration

import (
	"bytes"
	"context"
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
	t             *testing.T
	srv           *httptest.Server
	path, session string
	seq           int
}

func openWire(t *testing.T, srv *httptest.Server, path string) *wire {
	w := &wire{t: t, srv: srv, path: path}
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
	ro := openWire(t, srv, "/mcp")
	rw := openWire(t, srv, "/mcp/write")
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
	source := ref(rw.tool("save", hub.Params{Type: "source", Body: "runtime history", Provenance: "session", IdempotencyKey: "source"}))
	p := map[string]any{"type": "knowledge", "body": "必须刷新缓存", "scope": map[string]any{"version": "v1"}, "sources": []string{source.ID}, "reason": "Dream extracted", "idempotency_key": "knowledge"}
	k := ref(rw.tool("save", p))
	if ref(rw.tool("save", p)) != k {
		t.Fatal("idempotency")
	}
	ro.tool("read", hub.Params{Type: "knowledge", ID: k.ID, History: true})
	out := ro.tool("search", hub.Params{Query: "刷新", Scope: map[string]any{"version": "v1"}})
	if len(out.Data.([]any)) != 1 {
		t.Fatal("MCP search", out)
	}
	// New read sessions can query state saved by a separate Dream write session.
	reconnected := openWire(t, srv, "/mcp")
	reconnected.tool("read", hub.Params{Type: "knowledge", ID: k.ID, Revision: 1})
}

func TestDefaultWorkspaceHTTPAndEndpointIsolation(t *testing.T) {
	f := setup(t)
	srv := app(t, f)
	// No Authorization header or workspace parameter is sent by either client.
	payload := map[string]any{"type": "source", "body": "REST history", "provenance": "REST", "idempotency_key": "rest"}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", srv.URL+"/v1/save", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var saved hub.Result
	if err = json.NewDecoder(resp.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("anonymous save failed", resp.StatusCode, saved)
	}
	source := ref(saved)
	// A separate client reads the same shared default workspace.
	req, _ = http.NewRequest("POST", srv.URL+"/v1/read", strings.NewReader(`{"type":"source","id":"`+source.ID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("shared anonymous read failed", resp.StatusCode)
	}
	var read hub.Result
	if err = json.NewDecoder(resp.Body).Decode(&read); err != nil {
		t.Fatal(err)
	}
	object := read.Data.(map[string]any)["object"].(map[string]any)
	if object["body"] != "REST history" || object["workspace_id"] != nil || object["author"] != nil {
		t.Fatal("internal identity exposed", object)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/v1/search", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("anonymous search failed", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/v1/search", strings.NewReader(`{"workspace_id":"`+hub.NewID()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("workspace selection unexpectedly accepted", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://evil.example")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("untrusted Origin accepted")
	}
	rw := openWire(t, srv, "/mcp/write")
	req, _ = http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/list","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", rw.session)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("write session reused at read endpoint", resp.StatusCode)
	}
}
