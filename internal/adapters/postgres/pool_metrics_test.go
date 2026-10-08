package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Owns saturation semantics on a real pool. Empty-pool rendering cannot catch
// a swapped idle/acquired statistic, a stuck occupancy or a wrong denominator.
func TestPoolMetricsFollowAcquisitionAndRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixture := rebuildAdapterPool(t, ctx)
	cfg := fixture.Config()
	cfg.MaxConns = 2
	cfg.MinConns = 0
	cfg.MinIdleConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	metrics := postgres.NewPoolMetrics(pool)
	check := func(wants ...string) {
		t.Helper()
		var b strings.Builder
		metrics.Write(&b)
		for _, want := range wants {
			if !strings.Contains(b.String(), want+"\n") {
				t.Fatalf("missing %q in\n%s", want, b.String())
			}
		}
	}
	check("quivr_postgres_pool_max_connections 2", "quivr_postgres_pool_acquired_connections 0", "quivr_postgres_pool_saturation_ratio 0")
	first, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	check("quivr_postgres_pool_acquired_connections 1", "quivr_postgres_pool_saturation_ratio 0.5")
	second, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	check("quivr_postgres_pool_total_connections 2", "quivr_postgres_pool_acquired_connections 2", "quivr_postgres_pool_idle_connections 0", "quivr_postgres_pool_saturation_ratio 1")
	first.Release()
	second.Release()
	check("quivr_postgres_pool_total_connections 2", "quivr_postgres_pool_acquired_connections 0", "quivr_postgres_pool_idle_connections 2", "quivr_postgres_pool_saturation_ratio 0")
}
