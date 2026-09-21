package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/postgres"
	s3store "github.com/The-Vibe-Company/quivr-v2/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tokenizer"
	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/corpus"
	orchestration "github.com/The-Vibe-Company/quivr-v2/internal/orchestration/temporal"
	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
	"github.com/The-Vibe-Company/quivr-v2/internal/retrieval"
	"github.com/The-Vibe-Company/quivr-v2/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr-v2/internal/uploads"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

type Config struct {
	TEIURL          string                  `json:"tei_url"`
	Tokenizer       tokenizer.Config        `json:"tokenizer"`
	WeaviateURL     string                  `json:"weaviate_url"`
	TemporalAddress string                  `json:"temporal_address"`
	S3              s3store.Config          `json:"s3"`
	LogDirectory    string                  `json:"log_directory"`
	DatabaseURL     string                  `json:"database_url"`
	Listen          string                  `json:"listen"`
	ProbeListen     string                  `json:"probe_listen"`
	CursorKey       string                  `json:"cursor_key"`
	Keys            map[string]corpus.Scope `json:"keys"`
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
	if cfg.WeaviateURL == "" || cfg.TemporalAddress == "" || cfg.S3.Endpoint == "" || cfg.S3.Bucket == "" || cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
		return errors.New("Temporal and S3 configuration required")
	}
	blobs := s3store.New(cfg.S3)
	store := postgres.ContentStore{Pool: pool}
	contents := content.Service{Repository: store, Blobs: blobs, Baseline: store, Embeddings: store, BlobSource: store, Relations: store, Extensions: content.BuiltinExtensions{}}
	uploadService := uploads.Service{Store: store, Transfer: blobs}
	projection := weaviate.New(cfg.WeaviateURL)
	encoder := tokenizer.Encoder{Config: cfg.Tokenizer}
	windows := processing.TokenWindows{Tokenizer: encoder}
	embedding := tei.Encoder{Endpoint: cfg.TEIURL}
	search := retrieval.Service{Embedder: embedding, Routing: store, Projection: projection, Content: contents, QueryNormalizer: windows}
	processor := processing.Service{Content: contents, Processor: windows, Retrieval: search, Embedder: embedding, Enrichment: search}
	if command == "migrate" {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err = postgres.Migrate(ctx, pool); err != nil {
			return errors.New("migration failed; check database connectivity and schema")
		}
		for {
			if err = blobs.Bootstrap(ctx); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return errors.New("S3 bootstrap deadline exceeded")
			case <-time.After(200 * time.Millisecond):
			}
		}
		for {
			if err = projection.Bootstrap(ctx, weaviate.InitialCollection); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return errors.New("projection bootstrap deadline exceeded")
			case <-time.After(200 * time.Millisecond):
			}
		}
		if err = store.BootstrapGeneration(ctx, weaviate.InitialCollection, tei.Space().ID); err != nil {
			return errors.New("projection routing bootstrap failed")
		}
		if _, err = encoder.Encode(ctx, []processing.TokenInput{{Text: "tokenizer readiness"}}); err != nil {
			return errors.New("tokenizer preparation required")
		}
		slog.Info("migrations complete")
		return nil
	}
	var runtime atomic.Pointer[orchestration.Runtime]
	schemaReady := func(ctx context.Context) error {
		var exists bool
		err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='008_withdrawal_receipts.sql')").Scan(&exists)
		if err == nil && !exists {
			return errors.New("schema migration missing")
		}
		return err
	}
	ready := func(ctx context.Context) error {
		err := schemaReady(ctx)
		if err == nil && command == "worker" {
			rt := runtime.Load()
			if rt == nil {
				return errors.New("worker dependencies unavailable")
			}
			if _, err = rt.Client.CheckHealth(ctx, nil); err == nil {
				err = blobs.Ready(ctx)
				if err == nil {
					err = projection.Ready(ctx)
				}
			}
		}
		return err
	}
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = schemaReady(startup)
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
		handler, err := httpapi.New(postgres.Store{Pool: pool}, contents, search, uploadService, cfg.Keys, []byte(cfg.CursorKey))
		if err != nil {
			return fmt.Errorf("compile public request schema: %w", err)
		}
		servers = append(servers, &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384})
	} else {
		workerDone := make(chan struct{})
		defer func() {
			stop()
			select {
			case <-workerDone:
			case <-time.After(5 * time.Second):
			}
		}()
		go func() {
			defer close(workerDone)
			for ctx.Err() == nil {
				rt, err := orchestration.Start(ctx, cfg.TemporalAddress, processor, postgres.ContentStore{Pool: pool})
				if err == nil {
					runtime.Store(rt)
					<-ctx.Done()
					rt.Close()
					return
				}
				slog.Warn("worker dependencies unavailable; retrying")
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}()
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
