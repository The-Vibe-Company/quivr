package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	s3store "github.com/The-Vibe-Company/quivr/internal/adapters/s3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// runStorage connects only to durable stores. It does not boot plugins, a
// tokenizer, Temporal or a search index, and never invokes an embedder.
func runStorage(cfg Config, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: quivr storage status|activate|compact [flags]")
	}
	flags := flag.NewFlagSet("storage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "durable compaction identifier")
	batch := flags.Int("batch", 100, "maximum units to process before returning")
	retire := flags.Bool("retire-audit-detail", false, "explicitly remove captured historical import audit detail")
	drained := flags.Bool("writers-drained", false, "acknowledge that older API and workers have stopped")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *batch < 1 || *batch > 10000 {
		return errors.New("invalid storage command arguments")
	}
	command := args[0]
	if command != "status" && command != "activate" && command != "compact" {
		return errors.New("unknown storage command")
	}
	if command == "activate" && !*drained {
		return errors.New("activation requires --writers-drained after retiring older binaries")
	}
	if command == "compact" && *id == "" {
		return errors.New("compaction requires --id")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	poolConfig, err := postgres.PoolConfig(cfg.DatabaseURL, cfg.TLS.Postgres)
	if err != nil {
		return errors.New("invalid storage database settings")
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("storage database unavailable")
	}
	defer pool.Close()
	if command == "activate" {
		if err = postgres.ActivateCompactStorage(ctx, pool); err != nil {
			return errors.New("storage activation failed")
		}
		return json.NewEncoder(out).Encode(map[string]bool{"compact": true})
	}
	runner := postgres.StorageCompaction{Pool: pool}
	if command == "status" {
		if *id != "" {
			status, err := runner.Status(ctx, *id)
			if err != nil {
				return errors.New("storage compaction status unavailable")
			}
			return json.NewEncoder(out).Encode(status)
		}
		active, err := (postgres.EmbeddingStore{Pool: pool}).CompactStorage(ctx)
		if err != nil {
			return errors.New("storage status unavailable")
		}
		return json.NewEncoder(out).Encode(map[string]bool{"compact": active})
	}
	if cfg.S3.Endpoint == "" || cfg.S3.Bucket == "" || cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
		return errors.New("compaction requires object storage settings")
	}
	blobs, err := s3store.NewWithTLS(cfg.S3, cfg.TLS.S3)
	if err != nil {
		return errors.New("invalid storage object settings")
	}
	runner.Blobs = blobs
	status, err := runner.Start(ctx, *id, *retire)
	if err != nil {
		return errors.New("storage compaction start failed; inspect activation and operation options")
	}
	for n := 0; n < *batch && status.Phase != "done"; n++ {
		unit, cancel := context.WithTimeout(ctx, 30*time.Second)
		status, err = runner.Step(unit, *id)
		cancel()
		if err != nil {
			return errors.New("storage compaction stopped; checkpoint retained, inspect durable artifacts before resuming")
		}
	}
	return json.NewEncoder(out).Encode(status)
}
