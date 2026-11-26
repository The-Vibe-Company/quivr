package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresPreflightDeadlineLogsOperatorGuidance(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://fixture@127.0.0.1:1/quivr?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// The elapsed child deadline exercises the startup probe failure without a
	// network connection, a sleep, or cancellation of the parent context.
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	LogFailure(postgres.CheckPrerequisites(ctx, pool, false))
	var event map[string]string
	if err := json.Unmarshal(log.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["error_code"] != "postgres_prerequisite" || !strings.Contains(event["problem"], "TLS, and CONNECT access") {
		t.Fatalf("preflight deadline must log PostgreSQL connection guidance: %v", event)
	}
	if _, exists := event["field"]; exists {
		t.Fatalf("preflight diagnostics must omit the configuration field: %v", event)
	}
}
