package postgres_test

import (
	"crypto/tls"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/adapters/postgres"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
)

func TestPostgresTLSPolicyCoversEveryHost(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		name, dsn  string
		settings   outbound.TLS
		ok, secure bool
		hosts      int
	}{
		{"native verified TLS", "host=first.test,second.test sslmode=verify-full", outbound.TLS{}, true, true, 2},
		{"native plaintext", "host=localhost sslmode=disable", outbound.TLS{}, true, false, 1},
		{"opportunistic TLS", "host=localhost sslmode=prefer", outbound.TLS{}, false, false, 0},
		{"encryption without authentication", "host=localhost sslmode=require", outbound.TLS{}, false, false, 0},
		{"chain without hostname", "host=localhost sslmode=verify-ca", outbound.TLS{}, false, false, 0},
		{"explicit verified TLS overrides fallback", "host=first.test,second.test sslmode=prefer", outbound.TLS{Enabled: &enabled}, true, true, 2},
		{"explicit plaintext", "host=localhost sslmode=prefer", outbound.TLS{Enabled: &disabled}, true, false, 1},
		{"explicit server name", "host=first.test,second.test sslmode=disable", outbound.TLS{Enabled: &enabled, ServerName: "dependency.test"}, true, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := postgres.PoolConfig(tc.dsn, tc.settings)
			if !tc.ok {
				if err == nil {
					t.Fatal("accepted unverified/fallback TLS")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			check := func(host string, c *tls.Config) {
				if (c != nil) != tc.secure {
					t.Fatalf("host %s secure=%v want %v", host, c != nil, tc.secure)
				}
				if c == nil {
					return
				}
				name := tc.settings.ServerName
				if name == "" {
					name = host
				}
				if c.InsecureSkipVerify || c.ServerName != name || c.MinVersion < tls.VersionTLS12 {
					t.Fatalf("host %s has unsafe TLS config: %+v", host, c)
				}
			}
			check(config.ConnConfig.Host, config.ConnConfig.TLSConfig)
			for _, fallback := range config.ConnConfig.Fallbacks {
				check(fallback.Host, fallback.TLSConfig)
			}
			if got := 1 + len(config.ConnConfig.Fallbacks); got != tc.hosts {
				t.Fatalf("got %d dial choices, want %d distinct hosts", got, tc.hosts)
			}
		})
	}
}
