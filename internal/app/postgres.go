package app

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/workqueue"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresConfig bounds connections per process; workerConfig fits the
// process's worker activity slots into the resulting pool.
type PostgresConfig struct {
	MaxConnections     int32  `json:"max_connections"`
	TransactionTimeout string `json:"transaction_timeout"`
}

// Pool lifecycle defaults. pgx's own defaults keep idle connections for 30
// minutes, health-check once a minute, reconnect all connections in the same
// instant after an hour and wait on an unreachable server for as long as the
// caller's context allows. A database_url parameter always wins.
const (
	poolMaxConnLifetime   = 30 * time.Minute // pool_max_conn_lifetime: follow failovers and rebalanced replicas
	poolMaxConnIdleTime   = 5 * time.Minute  // pool_max_conn_idle_time: return burst connections to the server
	poolHealthCheckPeriod = 30 * time.Second // pool_health_check_period: find dead idle connections sooner
	poolPingTimeout       = 5 * time.Second  // pool_ping_timeout: a stalled liveness ping replaces the connection
	poolConnectTimeout    = 10 * time.Second // connect_timeout: bound an acquisition that has to dial
)

// poolJitterShare spreads lifetime closes (pool_max_conn_lifetime_jitter)
// over a tenth of the effective lifetime, so a pool never reconnects at once.
const poolJitterShare = 10

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
	timeout := 30 * time.Second
	if cfg.Postgres.TransactionTimeout != "" {
		timeout, err = time.ParseDuration(cfg.Postgres.TransactionTimeout)
		if err != nil || timeout.Milliseconds() < 1 || timeout.Milliseconds() > 2147483647 {
			return nil, badConfig(configInvalid, "postgres.transaction_timeout", "postgres.transaction_timeout must be a positive duration in PostgreSQL's millisecond range")
		}
	}
	// Bound all engine transactions, including read-only snapshots, from BEGIN.
	// A transaction-local SET after allocating an event cannot bound the time
	// already spent in its transaction. PostgreSQL 17 enforces this at the server.
	pool.ConnConfig.RuntimeParams["transaction_timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	applyPoolDefaults(pool, cfg.DatabaseURL)
	return pool, nil
}

// applyPoolDefaults sets each lifecycle default the database_url leaves out.
// pgxpool consumes its pool_* parameters while parsing, so presence is read
// from the URL itself; PGCONNECT_TIMEOUT counts as a supplied connect_timeout.
func applyPoolDefaults(pool *pgxpool.Config, dsn string) {
	set := dsnKeys(dsn)
	if !set["pool_max_conn_lifetime"] {
		pool.MaxConnLifetime = poolMaxConnLifetime
	}
	if !set["pool_max_conn_lifetime_jitter"] {
		pool.MaxConnLifetimeJitter = pool.MaxConnLifetime / poolJitterShare
	}
	if !set["pool_max_conn_idle_time"] {
		pool.MaxConnIdleTime = poolMaxConnIdleTime
	}
	if !set["pool_health_check_period"] {
		pool.HealthCheckPeriod = poolHealthCheckPeriod
	}
	if !set["pool_ping_timeout"] {
		pool.PingTimeout = poolPingTimeout
	}
	if _, env := os.LookupEnv("PGCONNECT_TIMEOUT"); !env && !set["connect_timeout"] {
		pool.ConnConfig.ConnectTimeout = poolConnectTimeout
	}
}

const dsnSpace = " \t\n\r\v\f"

// dsnKeys returns the parameter names a database_url supplies, tokenized as
// pgconn does: decoded query keys of a postgres:// or postgresql:// URL, or the
// keywords of a keyword/value string, skipping plain and quoted values with
// their backslash escapes. It runs after pgx accepted the URL; a malformed
// remainder only ends the scan.
func dsnKeys(dsn string) map[string]bool {
	keys := map[string]bool{}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		if u, err := url.Parse(dsn); err == nil {
			for key := range u.Query() {
				keys[key] = true
			}
		}
		return keys
	}
	for s := strings.TrimLeft(dsn, dsnSpace); s != ""; s = strings.TrimLeft(s, dsnSpace) {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		keys[strings.Trim(s[:eq], dsnSpace)] = true
		s = strings.TrimLeft(s[eq+1:], dsnSpace)
		quoted := strings.HasPrefix(s, "'")
		if quoted {
			s = s[1:]
		}
		end := 0
		for ; end < len(s); end++ {
			if s[end] == '\\' {
				end++
				continue
			}
			if quoted && s[end] == '\'' || !quoted && strings.IndexByte(dsnSpace, s[end]) >= 0 {
				break
			}
		}
		if quoted && end < len(s) {
			end++ // closing quote
		}
		s = s[min(end, len(s)):]
	}
	return keys
}

// workerConfig resolves worker queues and slots and fits the served activity
// slots into the process pool, keeping workqueue.PoolHeadroom free.
func (cfg Config) workerConfig(pool *pgxpool.Config) (workqueue.Config, error) {
	worker, err := cfg.Worker.Resolve()
	if err != nil {
		return worker, err
	}
	return worker.Bound(pool.MaxConns), nil
}
