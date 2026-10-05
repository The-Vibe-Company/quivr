package postgres

import (
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/The-Vibe-Company/quivr/internal/telemetry"

	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig keeps native verify-full connection-string options when no TLS
// override is set. Overrides apply to every HA host and remove SSL fallback.
func PoolConfig(dsn string, settings outbound.TLS) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Parse errors may include connection-string contents. Keep credentials
		// out of startup logs even when the supplied DSN is malformed.
		return nil, errors.New("postgres TLS/configuration is invalid; check database_url and certificate paths")
	}
	if telemetry.Enabled() {
		config.ConnConfig.Tracer = queryTracer{}
	}
	native := settings.Enabled == nil && !settings.HasSettings()
	if native {
		if config.ConnConfig.TLSConfig != nil {
			if config.ConnConfig.TLSConfig.InsecureSkipVerify {
				return nil, errors.New("postgres TLS: unverified modes, including the default sslmode=prefer, are refused; use sslmode=verify-full, sslmode=disable for plaintext, or explicit tls.postgres settings")
			}
			config.ConnConfig.TLSConfig.MinVersion = tls.VersionTLS12
		}
		for _, fallback := range config.ConnConfig.Fallbacks {
			if (fallback.TLSConfig == nil) != (config.ConnConfig.TLSConfig == nil) || (fallback.TLSConfig != nil && fallback.TLSConfig.InsecureSkipVerify) {
				return nil, errors.New("postgres TLS: plaintext or unverified SSL fallback refused; use sslmode=verify-full, sslmode=disable for plaintext, or explicit tls.postgres settings")
			}
			if fallback.TLSConfig != nil {
				fallback.TLSConfig.MinVersion = tls.VersionTLS12
			}
		}
		return config, nil
	}
	tlsConfig, err := settings.Build(true)
	if err != nil {
		return nil, fmt.Errorf("postgres TLS: %w", err)
	}
	if tlsConfig != nil && config.ConnConfig.TLSConfig != nil {
		tlsConfig.NextProtos = config.ConnConfig.TLSConfig.NextProtos
	}
	config.ConnConfig.TLSConfig = forHost(tlsConfig, config.ConnConfig.Host)
	// The parsed fallback list can mix plaintext and TLS for the same host.
	// Preserve distinct HA hosts only; each gets the same strict TLS policy.
	seen := map[string]bool{fmt.Sprintf("%s:%d", config.ConnConfig.Host, config.ConnConfig.Port): true}
	fallbacks := config.ConnConfig.Fallbacks[:0]
	for _, fallback := range config.ConnConfig.Fallbacks {
		address := fmt.Sprintf("%s:%d", fallback.Host, fallback.Port)
		if seen[address] {
			continue
		}
		seen[address] = true
		fallback.TLSConfig = forHost(tlsConfig, fallback.Host)
		fallbacks = append(fallbacks, fallback)
	}
	config.ConnConfig.Fallbacks = fallbacks
	return config, nil
}

func forHost(config *tls.Config, host string) *tls.Config {
	if config == nil {
		return nil
	}
	config = config.Clone()
	if config.ServerName == "" {
		config.ServerName = host
	}
	return config
}
