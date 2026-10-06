package app

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/buildinfo"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

func override(name, configured string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return configured
}

func (cfg Config) processSettings() (string, time.Duration, error) {
	level := override("QUIVR_LOG_LEVEL", cfg.LogLevel)
	if level == "" {
		level = "info"
	}
	switch level {
	case "debug", "info", "warn", "error":
	default:
		return "", 0, badConfig(configInvalid, "log_level", "log_level must be debug, info, warn or error")
	}
	grace := time.Minute
	if value := override("QUIVR_SHUTDOWN_GRACE", cfg.ShutdownGrace); value != "" {
		var err error
		grace, err = time.ParseDuration(value)
		if err != nil || grace <= 0 {
			return "", 0, badConfig(configInvalid, "shutdown_grace", "shutdown_grace must be a positive Go duration")
		}
	}
	return level, grace, nil
}

func (cfg *Config) configureProcess(command string) (time.Duration, error) {
	level, grace, err := cfg.processSettings()
	if err != nil {
		return 0, err
	}
	cfg.LogLevel = level
	cfg.Instance = override("QUIVR_INSTANCE", cfg.Instance)
	cfg.Environment = override("QUIVR_ENVIRONMENT", cfg.Environment)
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8080"
	}
	if cfg.ProbeListen == "" {
		cfg.ProbeListen = "127.0.0.1:8081"
	}
	var output io.Writer = os.Stdout
	if cfg.LogDirectory != "" {
		if err := os.MkdirAll(cfg.LogDirectory, 0o700); err != nil {
			return 0, badConfig(configInvalid, "log_directory", "create log_directory failed")
		}
		output = io.MultiWriter(os.Stdout, &rotatingLog{path: filepath.Join(cfg.LogDirectory, command+".log")})
	}
	logger, err := logging.New(output, logging.Options{Level: level, Service: "quivr." + command, Version: buildinfo.Version,
		Instance: cfg.Instance, Environment: cfg.Environment, Secrets: cfg.logSecrets()})
	if err != nil {
		return 0, err
	}
	slog.SetDefault(logger)
	return grace, nil
}

func (cfg Config) logSecrets() []string {
	secrets := []string{cfg.CursorKey, cfg.CredentialKey, cfg.DatabaseURL, cfg.S3.AccessKey, cfg.S3.SecretKey}
	for _, value := range cfg.Telemetry.Headers {
		secrets = append(secrets, value)
	}
	secrets = append(secrets, cfg.Telemetry.Endpoint)
	for token := range cfg.Keys {
		secrets = append(secrets, token)
	}
	for _, destination := range cfg.Destinations {
		secrets = append(secrets, destination.Secret, os.Getenv(destination.SecretEnv))
	}
	for _, raw := range []string{cfg.DatabaseURL, cfg.TEIURL, cfg.WeaviateURL, cfg.S3.Endpoint, cfg.PublicURL} {
		if u, err := url.Parse(raw); err == nil && u.User != nil {
			password, _ := u.User.Password()
			secrets = append(secrets, password, u.User.Username())
		}
	}
	// Plugin settings are opaque and may carry arbitrary credentials. Treat all
	// strings as confidential without needing to understand a plugin's schema.
	var collect func(any)
	collect = func(v any) {
		switch value := v.(type) {
		case string:
			secrets = append(secrets, value)
		case []any:
			for _, item := range value {
				collect(item)
			}
		case map[string]any:
			for _, item := range value {
				collect(item)
			}
		}
	}
	pins := append([]plugins.PinConfig(nil), cfg.Plugins...)
	if cfg.Plugin != nil {
		pins = append(pins, *cfg.Plugin)
	}
	for _, pin := range pins {
		raw, _ := json.Marshal(pin.Configuration)
		var v any
		if json.Unmarshal(raw, &v) == nil {
			collect(v)
		}
	}
	// Signing key rings live in the engine environment, independently of pins.
	// Collect their secrets while retaining non-secret plugin and key IDs.
	if raw := os.Getenv(plugins.EnvSigningKeys); raw != "" {
		secrets = append(secrets, raw)
		var rings map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &rings) == nil {
			for _, rawRing := range rings {
				var ring plugins.SigningKeys
				if json.Unmarshal(rawRing, &ring) != nil {
					continue
				}
				for _, key := range ring.Keys {
					secrets = append(secrets, key.Secret)
				}
			}
		}
	}

	return secrets
}

// The startup summary is an allowlist; opaque settings, URLs, credential values
// and API key scope data never enter a log record.
func (cfg Config) processSummary(grace time.Duration) slog.Attr {
	concurrency := cfg.IngestionEvaluationConcurrency
	if concurrency == 0 {
		concurrency = 4
	}
	return slog.Group("effective_config",
		slog.String("log_level", cfg.LogLevel), slog.Duration("shutdown_grace", grace),
		slog.String("listen", cfg.Listen), slog.String("probe_listen", cfg.ProbeListen),
		slog.Bool("tracing", cfg.Telemetry.Endpoint != ""),
		slog.Bool("file_logging", cfg.LogDirectory != ""), slog.Int("key_count", len(cfg.Keys)),
		slog.Bool("credential_deposits", cfg.CredentialKey != ""), slog.Int("destinations", len(cfg.Destinations)),
		slog.Int("ingestion_evaluation_concurrency", concurrency),
		slog.Bool("record_query_text", cfg.Observability.RecordQueryText),
		slog.Bool("allow_private_destinations", cfg.Delivery.AllowPrivateDestinations))
}
