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

// storageError contains only engine-owned codes, never a store's diagnostic.
type storageError struct{ code string }

func (e *storageError) Error() string { return e.code }

func compactionFailure(ctx context.Context, phase string, err error) error {
	code := "compaction_unit_failed"
	switch phase {
	case "vectors":
		code = "compaction_vectors_failed"
	case "receipts":
		code = "compaction_receipts_failed"
	case "normalizations":
		code = "compaction_normalizations_failed"
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		code = "compaction_unit_deadline"
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		code = "compaction_unit_canceled"
	}
	return &storageError{code: code}
}

// runStorage connects only to durable stores. It does not boot plugins, a
// tokenizer, Temporal or a search index, and never invokes an embedder.
func runStorage(cfg Config, args []string, out io.Writer) error {
	if len(args) == 0 {
		return &storageError{code: "storage_usage"}
	}
	flags := flag.NewFlagSet("storage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "durable compaction identifier")
	batch := flags.Int("batch", 100, "maximum units to process before returning")
	retire := flags.Bool("retire-audit-detail", false, "explicitly remove captured historical import audit detail")
	drained := flags.Bool("writers-drained", false, "acknowledge that older API and workers have stopped")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *batch < 1 || *batch > 10000 {
		return &storageError{code: "storage_invalid_arguments"}
	}
	command := args[0]
	if command != "status" && command != "activate" && command != "compact" {
		return &storageError{code: "storage_unknown_command"}
	}
	if command == "activate" && !*drained {
		return &storageError{code: "storage_writers_not_drained"}
	}
	if command == "compact" && *id == "" {
		return &storageError{code: "compaction_id_required"}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	poolConfig, err := cfg.poolConfig()
	if err != nil {
		return &storageError{code: "storage_invalid_database_settings"}
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return &storageError{code: "storage_database_unavailable"}
	}
	defer pool.Close()
	if command == "activate" {
		if err = postgres.ActivateCompactStorage(ctx, pool); err != nil {
			return &storageError{code: "storage_activation_failed"}
		}
		return json.NewEncoder(out).Encode(map[string]bool{"compact": true})
	}
	runner := postgres.StorageCompaction{Pool: pool}
	if command == "status" {
		if *id != "" {
			status, err := runner.Status(ctx, *id)
			if err != nil {
				return &storageError{code: "compaction_status_failed"}
			}
			return json.NewEncoder(out).Encode(status)
		}
		active, err := (postgres.EmbeddingStore{Pool: pool}).CompactStorage(ctx)
		if err != nil {
			return &storageError{code: "storage_status_failed"}
		}
		return json.NewEncoder(out).Encode(map[string]bool{"compact": active})
	}
	if cfg.S3.Endpoint == "" || cfg.S3.Bucket == "" || cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
		return &storageError{code: "compaction_object_settings_required"}
	}
	blobs, err := s3store.NewWithTLS(cfg.S3, cfg.TLS.S3)
	if err != nil {
		return &storageError{code: "storage_invalid_object_settings"}
	}
	runner.Blobs = blobs
	status, err := runner.Start(ctx, *id, *retire)
	if err != nil {
		return &storageError{code: "compaction_start_failed"}
	}
	for n := 0; n < *batch && status.Phase != "done"; n++ {
		unit, cancel := context.WithTimeout(ctx, 30*time.Second)
		phase := status.Phase
		status, err = runner.Step(unit, *id)
		if err != nil {
			// Classify before cleanup cancellation can obscure the real cause.
			failure := compactionFailure(unit, phase, err)
			cancel()
			return failure
		}
		cancel()
	}
	return json.NewEncoder(out).Encode(status)
}
