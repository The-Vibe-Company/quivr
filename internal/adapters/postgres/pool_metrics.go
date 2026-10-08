package postgres

import (
	"context"
	"io"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolMetrics exposes a process's local PostgreSQL pool without acquiring a
// connection. Even a saturated or disconnected pool remains observable.
type PoolMetrics struct{ pool *pgxpool.Pool }

var poolGauges = []telemetry.GaugeDefinition{
	{Name: "quivr_postgres_pool_max_connections", Help: "Maximum connections in this process's PostgreSQL pool.", Unit: "{connection}"},
	{Name: "quivr_postgres_pool_total_connections", Help: "Total connections in this process's PostgreSQL pool, including connections being constructed.", Unit: "{connection}"},
	{Name: "quivr_postgres_pool_acquired_connections", Help: "Connections currently acquired from this process's PostgreSQL pool.", Unit: "{connection}"},
	{Name: "quivr_postgres_pool_idle_connections", Help: "Idle connections in this process's PostgreSQL pool.", Unit: "{connection}"},
	{Name: "quivr_postgres_pool_saturation_ratio", Help: "Acquired connections divided by this process's maximum pool connections.", Unit: "1"},
}

func NewPoolMetrics(pool *pgxpool.Pool) *PoolMetrics {
	m := &PoolMetrics{pool: pool}
	telemetry.RegisterGauges(poolGauges, func(context.Context) ([]float64, error) { return m.values(), nil })
	return m
}

func (m *PoolMetrics) values() []float64 {
	s := m.pool.Stat()
	ratio := 0.0
	if s.MaxConns() > 0 {
		ratio = float64(s.AcquiredConns()) / float64(s.MaxConns())
	}
	return []float64{float64(s.MaxConns()), float64(s.TotalConns()), float64(s.AcquiredConns()), float64(s.IdleConns()), ratio}
}

func (m *PoolMetrics) Write(w io.Writer) {
	values := m.values()
	for i, g := range poolGauges {
		telemetry.Gauge(w, g.Name, g.Help, values[i])
	}
}
