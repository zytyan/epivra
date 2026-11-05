package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"epivra/internal/httpapi"
	"epivra/internal/hub"
	"epivra/internal/mcpserver"
	"epivra/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func number(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be positive integer", name)
	}
	return n, nil
}
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		client := &http.Client{Timeout: 3 * time.Second}
		_, port, err := net.SplitHostPort(env("LISTEN_ADDR", ":8080"))
		if err != nil {
			return err
		}
		resp, err := client.Get("http://127.0.0.1:" + port + "/readyz")
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("not ready")
		}
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	ping, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = db.PingContext(ping)
	cancel()
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "migrate" {
		return migrations.Apply(ctx, db)
	}
	if command != "serve" {
		return admin(ctx, db, command, os.Args[2:])
	}
	if err = migrations.Apply(ctx, db); err != nil {
		return err
	}
	upload, err := number("MAX_UPLOAD_BYTES", 20<<20)
	if err != nil {
		return err
	}
	s := &hub.Service{DB: db, MaxUpload: int64(upload)}
	if os.Getenv("EMBEDDING_BASE_URL") != "" {
		dim, err := number("EMBEDDING_DIMENSIONS", 1536)
		if err != nil {
			return err
		}
		batch, err := number("EMBEDDING_BATCH_SIZE", 16)
		if err != nil {
			return err
		}
		timeout, err := time.ParseDuration(env("EMBEDDING_TIMEOUT", "30s"))
		if err != nil {
			return err
		}
		s.Embed = &hub.Embedder{BaseURL: os.Getenv("EMBEDDING_BASE_URL"), APIKey: os.Getenv("EMBEDDING_API_KEY"), Model: env("EMBEDDING_MODEL", "text-embedding-3-small"), Dimensions: dim, BatchSize: batch, Timeout: timeout}
		if err = s.Embed.Validate(); err != nil {
			return err
		}
	}
	if err = s.ConfigureIndex(ctx); err != nil {
		return err
	}
	mux := http.NewServeMux()
	httpapi.Register(mux, s)
	origins := []string{}
	for _, v := range strings.Split(os.Getenv("TRUSTED_ORIGINS"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			origins = append(origins, v)
		}
	}
	if err = mcpserver.Register(mux, s, mcpserver.Config{Path: env("MCP_PATH", "/mcp"), SSEPath: env("MCP_SSE_PATH", "/sse"), MessagesPath: env("MCP_MESSAGES_PATH", "/messages"), BaseURL: os.Getenv("PUBLIC_BASE_URL"), Origins: origins}); err != nil {
		return err
	}
	srv := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: httpapi.Limit(s, mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); s.Worker(ctx) }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			srv.Close()
		}
	}()
	slog.Info("knowledge hub listening", "addr", srv.Addr)
	err = srv.ListenAndServe()
	stop()
	<-workerDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func admin(ctx context.Context, db *sql.DB, command string, args []string) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	name := fs.String("name", "", "display name")
	workspace := fs.String("workspace", "", "workspace UUID")
	write := fs.Bool("write", false, "grant read and write")
	id := fs.String("id", "", "API key UUID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch command {
	case "workspace-create":
		if strings.TrimSpace(*name) == "" {
			return fmt.Errorf("--name required")
		}
		uid := hub.NewID()
		if _, err := db.ExecContext(ctx, `INSERT INTO workspaces(id,name) VALUES($1,$2)`, uid, *name); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"workspace_id": uid})
	case "key-issue":
		if !hub.UUID(*workspace) || *name == "" {
			return fmt.Errorf("--workspace and --name required")
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		token := "kh_" + base64.RawURLEncoding.EncodeToString(b)
		hash := sha256.Sum256([]byte(token))
		uid := hub.NewID()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `INSERT INTO api_keys(id,name,digest) VALUES($1,$2,$3)`, uid, *name, hash[:]); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO key_permissions(key_id,workspace_id,can_write) VALUES($1,$2,$3)`, uid, *workspace, *write); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"key_id": uid, "api_key": token})
	case "key-revoke":
		if !hub.UUID(*id) {
			return fmt.Errorf("--id required")
		}
		res, err := db.ExecContext(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, *id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		return json.NewEncoder(os.Stdout).Encode(map[string]int64{"revoked": n})
	default:
		return fmt.Errorf("commands: serve, migrate, workspace-create, key-issue, key-revoke, healthcheck")
	}
}
