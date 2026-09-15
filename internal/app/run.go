package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type Config struct {
	LogDirectory string                  `json:"log_directory"`
	DatabaseURL  string                  `json:"database_url"`
	Listen       string                  `json:"listen"`
	ProbeListen  string                  `json:"probe_listen"`
	CursorKey    string                  `json:"cursor_key"`
	Keys         map[string]corpus.Scope `json:"keys"`
}

func Run(command string) error {
	if command != "api" && command != "worker" && command != "migrate" {
		return errors.New("usage: quivr api|worker|migrate")
	}
	b, err := os.ReadFile(os.Getenv("QUIVR_CONFIG"))
	if err != nil {
		return errors.New("read QUIVR_CONFIG file failed")
	}
	var cfg Config
	if err = json.Unmarshal(b, &cfg); err != nil {
		return errors.New("invalid configuration JSON")
	}
	if cfg.LogDirectory != "" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(&rotatingLog{path: filepath.Join(cfg.LogDirectory, command+".log")}, nil)))
	}
	if cfg.DatabaseURL == "" || len(cfg.CursorKey) < 32 || len(cfg.Keys) == 0 {
		return errors.New("database_url, cursor_key (32+ bytes) and keys required")
	}
	for token, s := range cfg.Keys {
		if len(token) < 32 || s.Organization == "" || len(s.Actions) == 0 || len(s.Corpora) == 0 {
			return errors.New("invalid scoped credential configuration")
		}
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8080"
	}
	if cfg.ProbeListen == "" {
		cfg.ProbeListen = "127.0.0.1:8081"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	defer pool.Close()
	if command == "migrate" {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err = postgres.Migrate(ctx, pool); err != nil {
			return errors.New("migration failed; check database connectivity and schema")
		}
		slog.Info("migrations complete")
		return nil
	}
	ready := func(ctx context.Context) error {
		var exists bool
		err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='001_corpora.sql')").Scan(&exists)
		if err == nil && !exists {
			return errors.New("schema migration missing")
		}
		return err
	}
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = ready(startup)
	cancel()
	if err != nil {
		return errors.New("database/schema unavailable; run migrate")
	}
	probes := http.NewServeMux()
	probes.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	probes.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ready(ctx) != nil {
			http.Error(w, "database/schema unavailable", 503)
			return
		}
		w.WriteHeader(204)
	})
	servers := []*http.Server{{Addr: cfg.ProbeListen, Handler: probes, ReadHeaderTimeout: 5 * time.Second}}
	if command == "api" {
		handler, err := httpapi.New(postgres.Store{Pool: pool}, cfg.Keys, []byte(cfg.CursorKey))
		if err != nil {
			return fmt.Errorf("compile public request schema: %w", err)
		}
		servers = append(servers, &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384})
	} else {
		slog.Info("worker idle", "reason", "no background tasks in Corpus slice")
	}
	failures := make(chan error, len(servers))
	for _, server := range servers {
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			return errors.New("listen failed")
		}
		go func(s *http.Server, l net.Listener) { failures <- s.Serve(l) }(server, listener)
	}
	slog.Info("process ready", "component", command)
	select {
	case <-ctx.Done():
	case err = <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP server stopped")
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdown)
	}
	return nil
}
