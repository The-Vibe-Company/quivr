package postgres_test

import (
	"crypto/tls"
	"slices"
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
		hosts      []string
	}{
		{"native verified TLS", "host=first.test,second.test sslmode=verify-full", outbound.TLS{}, true, true, []string{"first.test", "second.test"}},
		{"native plaintext", "host=localhost sslmode=disable", outbound.TLS{}, true, false, []string{"localhost"}},
		{"opportunistic TLS", "host=localhost sslmode=prefer", outbound.TLS{}, false, false, nil},
		{"encryption without authentication", "host=localhost sslmode=require", outbound.TLS{}, false, false, nil},
		{"chain without hostname", "host=localhost sslmode=verify-ca", outbound.TLS{}, false, false, nil},
		{"explicit verified TLS overrides fallback", "host=first.test,second.test sslmode=prefer", outbound.TLS{Enabled: &enabled}, true, true, []string{"first.test", "second.test"}},
		{"explicit plaintext", "host=localhost sslmode=prefer", outbound.TLS{Enabled: &disabled}, true, false, []string{"localhost"}},
		{"explicit server name", "host=first.test,second.test sslmode=disable", outbound.TLS{Enabled: &enabled, ServerName: "dependency.test"}, true, true, []string{"first.test", "second.test"}},
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
			hosts := []string{config.ConnConfig.Host}
			check(config.ConnConfig.Host, config.ConnConfig.TLSConfig)
			for _, fallback := range config.ConnConfig.Fallbacks {
				hosts = append(hosts, fallback.Host)
				check(fallback.Host, fallback.TLSConfig)
			}
			if !slices.Equal(hosts, tc.hosts) {
				t.Fatalf("dial hosts=%v, want %v", hosts, tc.hosts)
			}
		})
	}
}
