package main

import (
	"context"
	"database/sql"
	"errors"
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
	"epivra/schema"
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
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "help" || command == "--help" {
		fmt.Fprintln(os.Stdout, "commands: serve (default), init-db, healthcheck; configuration: DATABASE_URL, LISTEN_ADDR")
		return nil
	}
	if command != "serve" && command != "init-db" {
		return fmt.Errorf("commands: serve, init-db, healthcheck")
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
	if command == "init-db" {
		return schema.Initialize(ctx, db)
	}
	if err = schema.Initialize(ctx, db); err != nil {
		return err
	}
	upload, err := number("MAX_UPLOAD_BYTES", 20<<20)
	if err != nil {
		return err
	}
	s := &hub.Service{DB: db, MaxUpload: int64(upload)}
	mux := http.NewServeMux()
	httpapi.Register(mux, s)
	origins := []string{}
	for _, v := range strings.Split(os.Getenv("TRUSTED_ORIGINS"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			origins = append(origins, v)
		}
	}
	if err = mcpserver.Register(mux, s, mcpserver.Config{ReadPath: env("MCP_READ_PATH", "/mcp"), WritePath: env("MCP_WRITE_PATH", "/mcp/write"), Origins: origins}); err != nil {
		return err
	}
	srv := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: httpapi.Limit(s, mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
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
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
