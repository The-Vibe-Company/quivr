package app

import (
	json "encoding/json/v2"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/The-Vibe-Company/quivr/internal/audit"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/secretinput"
)

func boundSecret(name, field string) (string, error) {
	value, err := secretinput.Read(name)
	switch {
	case errors.Is(err, secretinput.ErrConflict):
		return "", badConfig(configConflict, field, err.Error())
	case err != nil:
		return "", badConfig(configInvalid, field, err.Error())
	default:
		return value, nil
	}
}

func (cfg *Config) loadSecrets(databaseOnly bool) error {
	database, err := boundSecret("DATABASE_URL", "database_url")
	if err != nil {
		return err
	}
	if database != "" {
		cfg.DatabaseURL = database
	}
	if databaseOnly {
		return nil
	}

	for _, binding := range []struct {
		name, field string
		target      *string
	}{
		{"QUIVR_CURSOR_KEY", "cursor_key", &cfg.CursorKey},
		{"QUIVR_CREDENTIAL_KEY", "credential_key", &cfg.CredentialKey},
		{"S3_ACCESS_KEY", "s3.access_key", &cfg.S3.AccessKey},
		{"S3_SECRET_KEY", "s3.secret_key", &cfg.S3.SecretKey},
		{"WEAVIATE_API_KEY", "weaviate_api_key", &cfg.WeaviateAPIKey},
	} {
		value, err := boundSecret(binding.name, binding.field)
		if err != nil {
			return err
		}
		if value != "" {
			*binding.target = value
		}
		if *binding.target != "" && strings.TrimSpace(*binding.target) == "" {
			return badConfig(configInvalid, binding.field, "secret must contain a non-whitespace value")
		}
	}

	if err := loadSecretMap("QUIVR_API_KEYS", "keys", &cfg.Keys); err != nil {
		return err
	}
	if err := loadSecretMap("QUIVR_TELEMETRY_HEADERS", "telemetry.headers", &cfg.Telemetry.Headers); err != nil {
		return err
	}
	return nil
}

func loadSecretMap[T any](name, field string, target *map[string]T) error {
	raw, err := boundSecret(name, field)
	if err != nil || raw == "" {
		return err
	}
	var decoded map[string]T
	if json.Unmarshal([]byte(raw), &decoded, json.RejectUnknownMembers(true)) != nil || decoded == nil {
		return badConfig(configInvalid, field, "secret binding must hold a valid JSON object")
	}
	*target = decoded
	return nil
}

func invalidBearerToken(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

func (cfg Config) validateAccessKeys() error {
	if len(cfg.Keys) == 0 {
		return badConfig(configMissing, "keys", "keys must contain at least one scoped API key")
	}
	for token, scope := range cfg.Keys {
		if scope.ActorID != "" && !audit.ValidActorID(scope.ActorID) {
			return badConfig(configInvalid, "keys.actor_id", "actor_id must be a 32-character lowercase hexadecimal identifier")
		}
		if len(token) < 32 || invalidBearerToken(token) || scope.Organization == "" || len(scope.Actions) == 0 || len(scope.Corpora) == 0 {
			return badConfig(configInvalid, "keys", "invalid scoped credential configuration")
		}
	}
	if invalidBearerToken(cfg.WeaviateAPIKey) {
		return badConfig(configInvalid, "weaviate_api_key", "invalid Weaviate bearer token")
	}
	return nil
}

func validateEngineSigning(pins *plugins.PinSet, now time.Time) error {
	raw, err := boundSecret(plugins.EnvSigningKeys, plugins.EnvSigningKeys)
	if err != nil {
		return err
	}
	var rings map[string]plugins.SigningKeys
	if raw != "" {
		rings, err = plugins.ParseEngineSigningKeys([]byte(raw), now)
		if err != nil {
			return badConfig(configInvalid, plugins.EnvSigningKeys, "each engine signing ring must be valid and have a currently valid active key")
		}
	}
	for _, pin := range pins.Pins() {
		if pin.Speaks(plugins.FeatureSignedCalls) && rings[pin.Manifest.ID].Active == "" {
			return badConfig(configMissing, plugins.EnvSigningKeys, "each plugin that requires signed calls needs its own signing key ring")
		}
	}
	return nil
}
