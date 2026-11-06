package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"epivra/internal/hub"
)

func Error(w http.ResponseWriter, err error) {
	var e *hub.Error
	if !errors.As(err, &e) {
		e = &hub.Error{Code: "internal", Message: "request failed"}
	}
	status := http.StatusBadRequest
	switch e.Code {
	case "forbidden":
		status = 403
	case "not_found":
		status = 404
	case "unknown_operation":
		status = 404
	case "revision_conflict", "idempotency_conflict", "conflict":
		status = 409
	case "upload_too_large":
		status = 413
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
		var initialized bool
		err := s.DB.QueryRowContext(ctx, `SELECT to_regclass(format('%I.knowledge',current_schema())) IS NOT NULL`).Scan(&initialized)
		if err != nil || !initialized {
			JSON(w, 503, map[string]string{"status": "not_ready"})
			return
		}
		JSON(w, 200, map[string]string{"status": "ready"})
	})

	for _, operation := range hub.Operations {
		op := operation
		mux.Handle("POST /v1/"+op, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { call(w, r, s, op) }))
	}
}
func call(w http.ResponseWriter, r *http.Request, s *hub.Service, op string) {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		Error(w, &hub.Error{Code: "invalid_input", Message: "application/json required"})
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			Error(w, &hub.Error{Code: "upload_too_large", Message: "request too large"})
		} else {
			Error(w, &hub.Error{Code: "invalid_input", Message: "invalid body"})
		}
		return
	}
	p, err := hub.DecodeOperation(op, raw)
	if err != nil {
		Error(w, err)
		return
	}
	out, err := s.Execute(r.Context(), op, p)
	if err != nil {
		Error(w, err)
		return
	}
	JSON(w, 200, out)
}
