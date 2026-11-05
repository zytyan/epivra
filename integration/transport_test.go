package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"epivra/internal/httpapi"
	"epivra/internal/hub"
	"epivra/internal/mcpserver"
)

type wire struct {
	t                                      *testing.T
	base, key, endpoint, session, protocol string
	legacy                                 bool
	seq                                    int
	client                                 *http.Client
	events                                 chan map[string]any
	cancel                                 context.CancelFunc
	body                                   io.Closer
}

func app(t *testing.T, f *fixture) *httptest.Server {
	mux := http.NewServeMux()
	httpapi.Register(mux, f.s)
	srv := httptest.NewServer(httpapi.Limit(f.s, mux))
	err := mcpserver.Register(mux, f.s, mcpserver.Config{Path: "/agent/mcp", SSEPath: "/agent/sse", MessagesPath: "/agent/messages", BaseURL: srv.URL, Origins: []string{"https://trusted.example"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}
func openWire(t *testing.T, srv *httptest.Server, key string, legacy bool) *wire {
	t.Helper()
	w := &wire{t: t, base: srv.URL, key: key, endpoint: srv.URL + "/agent/mcp", client: srv.Client(), legacy: legacy, protocol: "2025-11-25"}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	if legacy {
		w.protocol = "2024-11-05"
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/agent/sse", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Accept", "text/event-stream")
		resp, err := w.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("SSE GET: %s", b)
		}
		w.body = resp.Body
		endpoints := make(chan string, 1)
		w.events = make(chan map[string]any, 20)
		go func() {
			defer close(w.events)
			scan := bufio.NewScanner(resp.Body)
			scan.Buffer(make([]byte, 4096), 40<<20)
			event := ""
			for scan.Scan() {
				line := scan.Text()
				if strings.HasPrefix(line, "event: ") {
					event = strings.TrimPrefix(line, "event: ")
				}
				if strings.HasPrefix(line, "data: ") {
					data := strings.TrimPrefix(line, "data: ")
					if event == "endpoint" {
						endpoints <- data
					} else {
						var obj map[string]any
						if json.Unmarshal([]byte(data), &obj) == nil {
							select {
							case w.events <- obj:
							case <-ctx.Done():
								return
							}
						}
					}
				}
			}
		}()
		select {
		case endpoint := <-endpoints:
			u, _ := url.Parse(srv.URL)
			rel, _ := url.Parse(endpoint)
			w.endpoint = u.ResolveReference(rel).String()
			if !strings.Contains(w.endpoint, "/agent/messages?") {
				t.Fatal("configured messages path ignored")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing SSE endpoint")
		}
	}
	t.Cleanup(w.close)
	init := w.rpc("initialize", map[string]any{"protocolVersion": w.protocol, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "integration", "version": "1"}}, false)
	result, ok := init["result"].(map[string]any)
	if !ok || result["protocolVersion"] != w.protocol {
		t.Fatalf("protocol negotiation failed: %v", init)
	}
	w.rpc("notifications/initialized", map[string]any{}, true)
	return w
}
func (w *wire) close() {
	w.cancel()
	if w.body != nil {
		w.body.Close()
	}
}
func (w *wire) rpc(method string, params any, notify bool) map[string]any {
	w.t.Helper()
	w.seq++
	payload := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if !notify {
		payload["id"] = w.seq
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", w.endpoint, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+w.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if w.session != "" {
		req.Header.Set("Mcp-Session-Id", w.session)
	}
	req.Header.Set("Mcp-Protocol-Version", w.protocol)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := w.client.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	if session := resp.Header.Get("Mcp-Session-Id"); session != "" {
		w.session = session
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		w.t.Fatalf("%s HTTP %d: %s", method, resp.StatusCode, b)
	}
	if notify {
		return nil
	}
	if w.legacy {
		for {
			select {
			case obj, ok := <-w.events:
				if !ok {
					w.t.Fatal("SSE disconnected")
				}
				if id, ok := obj["id"].(float64); ok && int(id) == w.seq {
					return obj
				}
			case <-ctx.Done():
				w.t.Fatal("SSE response timeout")
			}
		}
	}
	var result map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		w.t.Fatal(err)
	}
	return result
}
func (w *wire) tool(name string, p hub.Params) (hub.Result, map[string]any) {
	w.t.Helper()
	obj := w.rpc("tools/call", map[string]any{"name": name, "arguments": p}, false)
	r, ok := obj["result"].(map[string]any)
	if !ok {
		w.t.Fatalf("no tool result: %v", obj)
	}
	if r["isError"] == true {
		w.t.Fatalf("tool failed: %v", r)
	}
	content := r["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	var result hub.Result
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		w.t.Fatal(err)
	}
	return result, r
}
func TestMCPBothTransports(t *testing.T) {
	f := setup(t)
	srv := app(t, f)
	for _, legacy := range []bool{false, true} {
		name := "streamable-2025-11-25"
		if legacy {
			name = "sse-2024-11-05"
		}
		t.Run(name, func(t *testing.T) {
			w := openWire(t, srv, f.key, legacy)
			defer w.close()
			list := w.rpc("tools/list", map[string]any{}, false)
			tools := list["result"].(map[string]any)["tools"].([]any)
			if len(tools) != 11 {
				t.Fatalf("tool count: %d", len(tools))
			}
			p := hub.Params{WorkspaceID: f.ws, IdempotencyKey: hub.NewID(), Body: "MCP 中文 source", Provenance: "transport test", SourceType: "image", Attachments: []hub.AttachmentInput{{Name: "image.png", MIME: "image/png", Data: "iVBORw0KGgo="}}}
			out, _ := w.tool("source_create", p)
			src := ref(out)
			again, _ := w.tool("source_create", p)
			if ref(again) != src {
				t.Fatal("MCP idempotency")
			}
			read, _ := w.tool("knowledge_read", hub.Params{WorkspaceID: f.ws, Ref: &src})
			b, _ := json.Marshal(read.Data)
			var data hub.ReadData
			json.Unmarshal(b, &data)
			if len(data.Attachments) != 1 {
				t.Fatal("attachment not listed")
			}
			_, attachment := w.tool("attachment_read", hub.Params{WorkspaceID: f.ws, ID: data.Attachments[0].ID})
			if len(attachment["content"].([]any)) != 2 {
				t.Fatal("missing image content")
			}
			search, _ := w.tool("knowledge_search", hub.Params{WorkspaceID: f.ws, Query: "MCP", Mode: "keyword"})
			if len(search.Data.([]any)) == 0 {
				t.Fatal("empty MCP search")
			}
			// Disconnect, then establish a new session against the same stored business state.
			w.close()
			reconnected := openWire(t, srv, f.key, legacy)
			defer reconnected.close()
			reconnected.tool("knowledge_read", hub.Params{WorkspaceID: f.ws, Ref: &src})
		})
	}
}
func TestHTTPAuthorizationAndMultipart(t *testing.T) {
	f := setup(t)
	srv := app(t, f)
	for _, path := range []string{"/agent/mcp", "/agent/sse", "/agent/messages", "/v1/search"} {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(`{}`))
		if path == "/agent/sse" {
			req.Method = "GET"
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("%s unauthenticated: %d", path, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", srv.URL+"/agent/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("untrusted origin accepted")
	}
	body := new(bytes.Buffer)
	mw := multipart.NewWriter(body)
	metadata, _ := mw.CreateFormField("metadata")
	json.NewEncoder(metadata).Encode(hub.Params{WorkspaceID: f.ws, IdempotencyKey: hub.NewID(), Provenance: "multipart", SourceType: "file"})
	file, _ := mw.CreateFormFile("attachments", "x.txt")
	file.Write([]byte("attachment text"))
	mw.Close()
	req, _ = http.NewRequest("POST", srv.URL+"/v1/sources", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+f.key)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("multipart: %s", b)
	}
	var out hub.Result
	json.NewDecoder(resp.Body).Decode(&out)
	src := ref(out)
	data := f.call("knowledge_read", hub.Params{Ref: &src}).Data.(hub.ReadData)
	req, _ = http.NewRequest("GET", srv.URL+"/v1/attachments/"+data.Attachments[0].ID+"?workspace_id="+f.ws, nil)
	req.Header.Set("Authorization", "Bearer "+f.key)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "attachment text" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatal("download failed")
	}
}

func TestMCPSessionsAreBoundToCaller(t *testing.T) {
	f := setup(t)
	srv := app(t, f)
	secondID, secondKey := hub.NewID(), "second_"+hub.NewID()
	digest := sha256.Sum256([]byte(secondKey))
	if _, err := f.s.DB.Exec(`INSERT INTO api_keys(id,name,digest) VALUES($1,'second',$2)`, secondID, digest[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(`INSERT INTO key_permissions(key_id,workspace_id,can_write) VALUES($1,$2,true)`, secondID, f.ws); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		w := openWire(t, srv, f.key, legacy)
		payload := `{"jsonrpc":"2.0","id":100,"method":"tools/list","params":{}}`
		req, _ := http.NewRequest("POST", w.endpoint, strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+secondKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Session-Id", w.session)
		req.Header.Set("Mcp-Protocol-Version", w.protocol)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 && resp.StatusCode != 403 {
			t.Fatalf("another caller reused session: %d", resp.StatusCode)
		}
		w.close()
	}
	// Revocation is checked for already-established sessions on every HTTP request.
	w := openWire(t, srv, f.key, false)
	defer w.close()
	f.s.DB.Exec(`UPDATE api_keys SET revoked_at=now() WHERE id=$1`, f.actor)
	req, _ := http.NewRequest("POST", w.endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("Mcp-Session-Id", w.session)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("revoked session accepted")
	}
}
