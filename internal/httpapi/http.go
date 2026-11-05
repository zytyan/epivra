package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"epivra/internal/hub"
)

type actorKey struct{}

func Actor(ctx context.Context) string { v, _ := ctx.Value(actorKey{}).(string); return v }
func Error(w http.ResponseWriter, err error) {
	var e *hub.Error
	if !errors.As(err, &e) {
		e = &hub.Error{Code: "internal", Message: "request failed"}
	}
	status := http.StatusBadRequest
	switch e.Code {
	case "unauthorized":
		status = 401
		w.Header().Set("WWW-Authenticate", "Bearer")
	case "forbidden":
		status = 403
	case "not_found":
		status = 404
	case "unknown_operation":
		status = 404
	case "version_conflict", "idempotency_conflict", "conflict":
		status = 409
	case "upload_too_large":
		status = 413
	case "semantic_unavailable":
		status = 503
	case "internal":
		status = 500
	}
	JSON(w, status, map[string]any{"error": e})
}
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func Authenticate(s *hub.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			Error(w, &hub.Error{Code: "unauthorized", Message: "Bearer API key required"})
			return
		}
		actor, err := s.Authenticate(r.Context(), parts[1])
		if err != nil {
			Error(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor)))
	})
}
func Limit(s *hub.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.UploadLimit()*4/3+(8<<20))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
func Origin(origins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			ok := false
			for _, trusted := range origins {
				if origin == trusted {
					ok = true
				}
			}
			if !ok {
				Error(w, &hub.Error{Code: "forbidden", Message: "untrusted Origin"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func Register(mux *http.ServeMux, s *hub.Service) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var n int
		err := s.DB.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version=1`).Scan(&n)
		if err != nil {
			JSON(w, 503, map[string]string{"status": "not_ready"})
			return
		}
		JSON(w, 200, map[string]string{"status": "ready"})
	})
	handle := func(op string) http.Handler {
		return Authenticate(s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if op == "" {
				call(w, r, s, r.PathValue("operation"))
				return
			}
			call(w, r, s, op)
		}))
	}
	mux.Handle("POST /v1/operations/{operation}", handle(""))
	for path, op := range map[string]string{"/v1/sources": "source_create", "/v1/sources/tags": "source_tags_update", "/v1/claims": "claim_create", "/v1/claims/revisions": "claim_revise", "/v1/verifications": "verification_create", "/v1/assessments": "assessment_create", "/v1/knowledge": "knowledge_publish", "/v1/read": "knowledge_read", "/v1/history": "knowledge_history", "/v1/search": "knowledge_search"} {
		mux.Handle("POST "+path, handle(op))
	}
	mux.Handle("GET /v1/attachments/{id}", Authenticate(s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, b, err := s.AttachmentBytes(r.Context(), Actor(r.Context()), r.URL.Query().Get("workspace_id"), r.PathValue("id"))
		if err != nil {
			Error(w, err)
			return
		}
		w.Header().Set("Content-Type", a.MIME)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Name}))
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Header().Set("ETag", `"`+a.SHA256+`"`)
		w.Write(b)
	})))
}
func call(w http.ResponseWriter, r *http.Request, s *hub.Service, op string) {
	var raw []byte
	var err error
	var p hub.Params
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media == "multipart/form-data" && op == "source_create" {
		if err = r.ParseMultipartForm(1 << 20); err == nil {
			defer r.MultipartForm.RemoveAll()
			raw = []byte(r.FormValue("metadata"))
			p, err = hub.DecodeOperation(op, raw)
		}
		if err == nil {
			var total int64
			for field, files := range r.MultipartForm.File {
				if field != "attachments" {
					err = &hub.Error{Code: "invalid_input", Message: "file field must be attachments"}
					break
				}
				for _, fh := range files {
					var f io.ReadCloser
					f, err = fh.Open()
					if err != nil {
						break
					}
					b, e := io.ReadAll(io.LimitReader(f, s.UploadLimit()+1))
					f.Close()
					if e != nil {
						err = e
						break
					}
					total += int64(len(b))
					if total > s.UploadLimit() {
						err = &hub.Error{Code: "upload_too_large", Message: "total attachments exceed limit"}
						break
					}
					mimeType := fh.Header.Get("Content-Type")
					if mimeType == "" {
						mimeType = http.DetectContentType(b)
					}
					p.Attachments = append(p.Attachments, hub.AttachmentInput{Name: fh.Filename, MIME: mimeType, Data: base64.StdEncoding.EncodeToString(b)})
				}
				if err != nil {
					break
				}
			}
		}
	} else if media == "application/json" {
		raw, err = io.ReadAll(r.Body)
		if err == nil {
			p, err = hub.DecodeOperation(op, raw)
		}
	} else {
		err = &hub.Error{Code: "invalid_input", Message: "application/json required (source_create also accepts multipart/form-data)"}
	}
	if err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			err = &hub.Error{Code: "upload_too_large", Message: "request exceeds limit"}
		}
		var he *hub.Error
		if !errors.As(err, &he) {
			err = &hub.Error{Code: "invalid_input", Message: "invalid request body"}
		}
		Error(w, err)
		return
	}
	out, err := s.Execute(r.Context(), Actor(r.Context()), op, p)
	if err != nil {
		Error(w, err)
		return
	}
	JSON(w, 200, out)
}
