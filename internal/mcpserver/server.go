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
	"time"

	"epivra/internal/httpapi"
	"epivra/internal/hub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Config struct {
	ReadPath, WritePath string
	Origins             []string
}

func (c Config) Validate() error {
	if c.ReadPath == c.WritePath {
		return fmt.Errorf("MCP paths must differ")
	}
	for _, p := range []string{c.ReadPath, c.WritePath} {
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " ?#{}") || p == "/" || strings.HasPrefix(p, "/v1/") || p == "/healthz" || p == "/readyz" {
			return fmt.Errorf("invalid MCP path")
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
func server(s *hub.Service, write bool) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "knowledge-hub", Version: "0.2.0"}, nil)
	for _, operation := range hub.Operations {
		if operation == "save" && !write {
			continue
		}
		op := operation
		srv.AddTool(&mcp.Tool{Name: op, Description: descriptions[op], InputSchema: hub.Schema(op)}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, err := hub.DecodeOperation(op, req.Params.Arguments)
			var out hub.Result
			if err == nil {
				out, err = s.Execute(ctx, op, p)
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
			if op == "read" {
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
	"search": "Search current knowledge revisions by keyword, JSONB scope containment, tags and optional since timestamp. Scope software versions are opaque: no version or recency implies correctness. Defaults to Knowledge; types may include Source.",
	"read":   "Read an immutable Source or current/fixed Knowledge revision, source and attachment manifests, and optional paginated revision history. Read attachment bytes explicitly using source type, id and attachment_id.",
	"save":   "Save an immutable Source, create atomic Knowledge, or append a full Knowledge revision. Knowledge requires scope object, source IDs and reason. Existing IDs require expected_revision. Use separate IDs for independently evolving software scopes; never automatically replaces another Knowledge. Intended for client-controlled Dream sessions.",
}

func Register(mux *http.ServeMux, s *hub.Service, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}

	for path, write := range map[string]bool{c.ReadPath: false, c.WritePath: true} {
		srv := server(s, write)
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{JSONResponse: true, SessionTimeout: 30 * time.Minute})
		mux.Handle(path, httpapi.Origin(c.Origins, handler))
	}
	return nil
}
