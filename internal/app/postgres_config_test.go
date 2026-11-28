package app

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Literal operator keys and the resulting driver config own pool sizing.
func TestPostgresPoolConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input, dsn string
		timeout    string
		want       int32
		valid      bool
	}{
		{`{}`, "pool_max_conns=7", "30000", 7, true},
		{`{"postgres":{"max_connections":0}}`, "pool_max_conns=7", "30000", 7, true},
		{`{"postgres":{"max_connections":16}}`, "pool_max_conns=7", "30000", 16, true},
		{`{"postgres":{"max_connections":1}}`, "", "30000", 1, true},
		{`{"postgres":{"max_connections":-1}}`, "", "30000", 0, false},
		{`{"postgres":{"max_connections":2147483648}}`, "", "30000", 0, false},
		{`{"postgres":{"max_connections":2}}`, "pool_min_conns=3", "30000", 0, false},
		{`{"postgres":{"max_connections":2}}`, "pool_min_idle_conns=3", "30000", 0, false},
		{`{"postgres":{"transaction_timeout":"2s","max_connections":4}}`, "transaction_timeout=9000", "2000", 4, true},
		{`{"postgres":{"transaction_timeout":"0s"}}`, "", "", 0, false},
		{`{"postgres":{"transaction_timeout":"invalid"}}`, "", "", 0, false},
		{`{"postgres":{"transaction_timeout":"0.1ms"}}`, "", "", 0, false},
	} {
		var cfg Config
		err := json.Unmarshal([]byte(tc.input), &cfg)
		if err != nil {
			if tc.valid {
				t.Fatal(err)
			}
			continue
		}
		cfg.DatabaseURL = "host=localhost sslmode=disable " + tc.dsn
		got, err := cfg.poolConfig()
		if err == nil && got.ConnConfig.RuntimeParams["transaction_timeout"] != tc.timeout {
			t.Fatalf("transaction_timeout=%q, want %s ms", got.ConnConfig.RuntimeParams["transaction_timeout"], tc.timeout)
		}
		if (err == nil) != tc.valid || (tc.valid && got.MaxConns != tc.want) {
			t.Fatalf("config %s: got=%v err=%v want=%d valid=%v", tc.input, got, err, tc.want, tc.valid)
		}
	}
}

// Literal database_url keys and their omission own the pool lifecycle: a
// supplied parameter keeps its value, including zero, and an omitted one gets
// the engine default instead of pgx's.
func TestPostgresPoolLifecycleDefaults(t *testing.T) {
	type lifecycle struct{ lifetime, jitter, idle, health, ping, connect time.Duration }
	defaults := lifecycle{30 * time.Minute, 3 * time.Minute, 5 * time.Minute, 30 * time.Second, 5 * time.Second, 10 * time.Second}
	for _, tc := range []struct {
		name, dsn, env string
		want           lifecycle
	}{
		{"omitted", "host=localhost sslmode=disable", "", defaults},
		{"keyword_literals", "host=localhost sslmode=disable pool_max_conn_lifetime=2h pool_max_conn_idle_time=1m pool_health_check_period=10s pool_ping_timeout=1s connect_timeout=3",
			"", lifecycle{2 * time.Hour, 12 * time.Minute, time.Minute, 10 * time.Second, time.Second, 3 * time.Second}},
		{"keyword_spaced", "host=localhost sslmode=disable pool_max_conn_lifetime = 1h",
			"", lifecycle{time.Hour, 6 * time.Minute, defaults.idle, defaults.health, defaults.ping, defaults.connect}},
		{"url_zero_literals", "postgres://quivr@localhost/quivr?sslmode=disable&pool_max_conn_lifetime_jitter=0s&connect_timeout=0",
			"", lifecycle{defaults.lifetime, 0, defaults.idle, defaults.health, defaults.ping, 0}},
		{"url_jitter_is_not_lifetime", "postgres://quivr@localhost/quivr?sslmode=disable&pool_max_conn_lifetime_jitter=1m",
			"", lifecycle{defaults.lifetime, time.Minute, defaults.idle, defaults.health, defaults.ping, defaults.connect}},
		{"keyword_quoted_value_is_not_a_key", `host=localhost sslmode=disable password='a pool_ping_timeout=1s' application_name=x\ pool_max_conn_idle_time=1m`,
			"", defaults},
		{"keyword_escaped_quote", `host=localhost sslmode=disable password='a\' pool_ping_timeout=1s' pool_health_check_period=10s`,
			"", lifecycle{defaults.lifetime, defaults.jitter, defaults.idle, 10 * time.Second, defaults.ping, defaults.connect}},
		{"url_encoded_key", "postgres://quivr@localhost/quivr?sslmode=disable&pool%5Fping%5Ftimeout=1s&application_name=pool_max_conn_idle_time%3D1m",
			"", lifecycle{defaults.lifetime, defaults.jitter, defaults.idle, defaults.health, time.Second, defaults.connect}},
		{"connect_timeout_environment", "host=localhost sslmode=disable", "7",
			lifecycle{defaults.lifetime, defaults.jitter, defaults.idle, defaults.health, defaults.ping, 7 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PGCONNECT_TIMEOUT", tc.env)
			if tc.env == "" {
				os.Unsetenv("PGCONNECT_TIMEOUT") // t.Setenv restores the runner's value.
			}
			pool, err := Config{DatabaseURL: tc.dsn}.poolConfig()
			if err != nil {
				t.Fatal(err)
			}
			got := lifecycle{pool.MaxConnLifetime, pool.MaxConnLifetimeJitter, pool.MaxConnIdleTime, pool.HealthCheckPeriod, pool.PingTimeout, pool.ConnConfig.ConnectTimeout}
			if got != tc.want {
				t.Fatalf("%s:\ngot  %+v\nwant %+v", tc.dsn, got, tc.want)
			}
		})
	}
}

// Literal worker and pool keys, and their omission, own how many activity
// slots a process may run against its pool: slots that fit are kept, larger
// requests are fitted below max_connections with headroom.
func TestWorkerSlotsFitThePostgresPool(t *testing.T) {
	for _, tc := range []struct {
		raw, dsn   string
		live, bulk int
		valid      bool
	}{
		{`{"worker":{"slots":{"live":32,"bulk":32}},"postgres":{"max_connections":4}}`, "", 2, 1, true},
		{`{"postgres":{"max_connections":4}}`, "", 2, 1, true},
		{`{"worker":{"queues":["bulk"],"slots":{"bulk":64}},"postgres":{"max_connections":4}}`, "", 4, 3, true},
		{`{}`, "pool_max_conns=16", 4, 4, true},
		{`{"worker":{"slots":{"live":12,"bulk":12}}}`, "pool_max_conns=16", 6, 6, true},
		{`{"worker":{"slots":{"live":-1}},"postgres":{"max_connections":4}}`, "", 0, 0, false},
	} {
		var cfg Config
		if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
			t.Fatal(err)
		}
		cfg.DatabaseURL = "host=localhost sslmode=disable " + tc.dsn
		pool, err := cfg.poolConfig()
		if err != nil {
			t.Fatal(err)
		}
		got, err := cfg.workerConfig(pool)
		if (err == nil) != tc.valid || (tc.valid && (got.Slots.Live != tc.live || got.Slots.Bulk != tc.bulk)) {
			t.Fatalf("%s %s: got %+v err=%v, want live=%d bulk=%d valid=%v", tc.raw, tc.dsn, got.Slots, err, tc.live, tc.bulk, tc.valid)
		}
	}
}
