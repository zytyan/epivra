package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"epivra/internal/httpapi"
	"epivra/internal/hub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Config struct {
	Path, SSEPath, MessagesPath, BaseURL string
	Origins                              []string
}

func (c Config) Validate() error {
	seen := map[string]bool{}
	for _, p := range []string{c.Path, c.SSEPath, c.MessagesPath} {
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " ?#{}") || p == "/" || strings.HasPrefix(p, "/v1/") || p == "/healthz" || p == "/readyz" || seen[p] {
			return fmt.Errorf("invalid or duplicate MCP path: %q", p)
		}
		seen[p] = true
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return fmt.Errorf("invalid PUBLIC_BASE_URL")
		}
	}
	for _, o := range c.Origins {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid trusted origin")
		}
	}
	return nil
}
func server(s *hub.Service, actor string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "knowledge-hub", Version: "0.1.0"}, nil)
	for _, operation := range hub.Operations {
		op := operation
		srv.AddTool(&mcp.Tool{Name: op, Description: descriptions[op], InputSchema: hub.Schema(op)}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, err := hub.DecodeOperation(op, req.Params.Arguments)
			var out hub.Result
			if err == nil {
				out, err = s.Execute(ctx, actor, op, p)
			}
			if err != nil {
				var e *hub.Error
				if !errors.As(err, &e) {
					e = &hub.Error{Code: "internal", Message: "request failed"}
				}
				b, _ := json.Marshal(map[string]any{"error": e})
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
			}
			var img *mcp.ImageContent
			if op == "attachment_read" {
				if a, ok := out.Data.(hub.Attachment); ok && (a.MIME == "image/png" || a.MIME == "image/jpeg" || a.MIME == "image/webp" || a.MIME == "image/gif") {
					b, _ := base64.StdEncoding.DecodeString(a.Data)
					img = &mcp.ImageContent{Data: b, MIMEType: a.MIME}
					a.Data = ""
					out.Data = a
				}
			}
			b, _ := json.Marshal(out)
			res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
			if img != nil {
				res.Content = append(res.Content, img)
			}
			return res, nil
		})
	}
	return srv
}

var descriptions = map[string]string{
	"source_create":      "Append immutable source content and optional base64 attachments; external URLs are provenance only.",
	"source_tags_update": "Replace Source tags using expected_version; preserves content and records reason.",
	"claim_create":       "Create a claim with at least one source reference.", "claim_revise": "Append a claim revision with expected_version, reason and source references.",
	"verification_create": "Append support/refute/uncertain evidence for an explicit claim revision; may create output_source atomically.",
	"assessment_create":   "Append an assessment synthesizing verifications of the same claim revision; resolve only listed event IDs.",
	"knowledge_publish":   "Create or revise Knowledge grounded in explicit claim revisions and at least one support assessment. Revisions require id, expected_version and reason.",
	"knowledge_read":      "Read any typed object at current or fixed version. Expand pinned evidence up to depth 5; all lists are paginated.",
	"knowledge_history":   "Read revision reasons and related verifications/assessments, bounded by the selected root version.",
	"knowledge_search":    "Workspace-isolated keyword, semantic or hybrid search with tags, types and pending_review filters. Hybrid reports fallback warnings.",
	"attachment_read":     "Read an attachment by id; images yield MCP image content, other files yield base64 data.",
}

type legacySession struct {
	owner     string
	transport *mcp.SSEServerTransport
	ready     chan struct{}
}

func Register(mux *http.ServeMux, s *hub.Service, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	var mu sync.Mutex
	// Separate SDK session stores per API key prevent cross-caller session reuse.
	streams := map[string]*mcp.StreamableHTTPHandler{}
	legacy := map[string]legacySession{}
	wrap := func(h http.Handler) http.Handler { return httpapi.Origin(c.Origins, httpapi.Authenticate(s, h)) }
	mux.Handle(c.Path, wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := httpapi.Actor(r.Context())
		mu.Lock()
		h := streams[actor]
		if h == nil {
			srv := server(s, actor)
			h = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{JSONResponse: true, SessionTimeout: 30 * time.Minute})
			streams[actor] = h
		}
		mu.Unlock()
		h.ServeHTTP(w, r)
	})))
	mux.Handle("GET "+c.SSEPath, wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := httpapi.Actor(r.Context())
		id := hub.NewID()
		endpoint := strings.TrimRight(c.BaseURL, "/") + c.MessagesPath + "?session_id=" + url.QueryEscape(id)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		tr := &mcp.SSEServerTransport{Endpoint: endpoint, Response: w}
		ready := make(chan struct{})
		mu.Lock()
		legacy[id] = legacySession{actor, tr, ready}
		mu.Unlock()
		session, err := server(s, actor).Connect(r.Context(), tr, nil)
		close(ready)
		if err != nil {
			mu.Lock()
			delete(legacy, id)
			mu.Unlock()
			return
		}
		defer func() { mu.Lock(); delete(legacy, id); mu.Unlock(); session.Close() }()
		done := make(chan struct{})
		go func() { session.Wait(); close(done) }()
		select {
		case <-r.Context().Done():
		case <-done:
		}
	})))
	mux.Handle("POST "+c.MessagesPath, wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sess, ok := legacy[r.URL.Query().Get("session_id")]
		mu.Unlock()
		if !ok || sess.owner != httpapi.Actor(r.Context()) {
			httpapi.Error(w, &hub.Error{Code: "not_found", Message: "session not found"})
			return
		}
		select {
		case <-sess.ready:
		case <-r.Context().Done():
			return
		}
		sess.transport.ServeHTTP(w, r)
	})))
	return nil
}
