package app

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/outbound"
)

// TLSConfig is deployment trust, independent of plugin registrations and plans.
type TLSConfig struct {
	Temporal outbound.TLS `json:"temporal"`
	Weaviate outbound.TLS `json:"weaviate"`
	Postgres outbound.TLS `json:"postgres"`
	S3       outbound.TLS `json:"s3"`
	Plugins  outbound.TLS `json:"plugins"`
}

type dependencyTLS struct {
	temporal *tls.Config
	plugins  http.RoundTripper
}

func (cfg Config) validateTLS() (dependencyTLS, error) {
	var result dependencyTLS
	for _, dep := range []struct {
		name           string
		settings       outbound.TLS
		defaultEnabled bool
		endpoint       string
	}{
		{"temporal", cfg.TLS.Temporal, false, ""},
		{"weaviate", cfg.TLS.Weaviate, strings.HasPrefix(cfg.WeaviateURL, "https://"), cfg.WeaviateURL},
		{"postgres", cfg.TLS.Postgres, true, ""},
		{"s3", cfg.TLS.S3, strings.HasPrefix(cfg.S3.Endpoint, "https://"), cfg.S3.Endpoint},
		{"plugins", cfg.TLS.Plugins, true, ""},
	} {
		config, err := dep.settings.Build(dep.defaultEnabled)
		if err != nil {
			code := configInvalid
			enabled := dep.defaultEnabled
			if dep.settings.Enabled != nil {
				enabled = *dep.settings.Enabled
			}
			if (!enabled && dep.settings.HasSettings()) || (dep.settings.CertFile == "") != (dep.settings.KeyFile == "") {
				code = configConflict
			}
			return result, &configError{code: code, field: "tls." + dep.name, problem: dep.name + " TLS settings are invalid", cause: err}
		}
		if dep.endpoint != "" {
			if _, err = dep.settings.ForURL(dep.endpoint); err != nil {
				code := configInvalid
				if u, parseErr := url.Parse(dep.endpoint); parseErr == nil && (u.Scheme == "http" || u.Scheme == "https") && dep.settings.Enabled != nil && *dep.settings.Enabled != (u.Scheme == "https") {
					code = configConflict
				}
				return result, &configError{code: code, field: "tls." + dep.name, problem: dep.name + " TLS settings conflict with the endpoint or are invalid", cause: err}
			}
		}
		switch dep.name {
		case "temporal":
			result.temporal = config
		case "plugins":
			if dep.settings.CertFile != "" || dep.settings.KeyFile != "" {
				return result, badConfig(configInvalid, "tls.plugins", "plugins TLS: client certificates are not supported")
			}
			result.plugins = pluginTransport{next: outbound.Transport(config), enabled: dep.settings.Enabled}
		}
	}
	pins := cfg.Plugins
	if cfg.Plugin != nil {
		pins = append(append(pins[:0:0], pins...), *cfg.Plugin)
	}
	for _, pin := range pins {
		if cfg.TLS.Plugins.Enabled != nil && pin.Endpoint != "" {
			if _, err := cfg.TLS.Plugins.ForURL(pin.Endpoint); err != nil {
				code := configInvalid
				if u, parseErr := url.Parse(pin.Endpoint); parseErr == nil && (u.Scheme == "http" || u.Scheme == "https") && *cfg.TLS.Plugins.Enabled != (u.Scheme == "https") {
					code = configConflict
				}
				return result, &configError{code: code, field: "tls.plugins", problem: "plugins TLS settings conflict with the endpoint or are invalid", cause: err}
			}
		}
	}
	return result, nil
}

// Enforce the explicit switch on new registrations as well as startup pins.
// With no switch, each plugin URL chooses HTTP or verified HTTPS.
type pluginTransport struct {
	next    http.RoundTripper
	enabled *bool
}

func (p pluginTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if p.enabled != nil && *p.enabled != (req.URL.Scheme == "https") {
		return nil, fmt.Errorf("plugins TLS: endpoint scheme conflicts with enabled")
	}
	return p.next.RoundTrip(req)
}
