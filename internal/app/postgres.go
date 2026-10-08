package app

import (
	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresConfig bounds connections per process, independent of worker slots.
type PostgresConfig struct {
	MaxConnections int32 `json:"max_connections"`
}

func (cfg Config) poolConfig() (*pgxpool.Config, error) {
	if cfg.Postgres.MaxConnections < 0 {
		return nil, badConfig(configInvalid, "postgres.max_connections", "postgres.max_connections must be positive, or zero for URL/driver sizing")
	}
	pool, err := postgres.PoolConfig(cfg.DatabaseURL, cfg.TLS.Postgres)
	if err != nil {
		return nil, invalidConfig("database_url", "invalid database_url or PostgreSQL TLS settings", err)
	}
	if limit := cfg.Postgres.MaxConnections; limit > 0 {
		if pool.MinConns > limit || pool.MinIdleConns > limit {
			return nil, badConfig(configConflict, "postgres.max_connections", "postgres.max_connections is smaller than the URL's minimum pool size")
		}
		pool.MaxConns = limit
	}
	return pool, nil
}
