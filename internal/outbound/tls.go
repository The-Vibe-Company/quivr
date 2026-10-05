// Package outbound builds deployment-owned, verified dependency transports.
package outbound

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// TLS leaves protocol selection to the dependency when Enabled is omitted.
// Files are PEM paths readable by the engine process, never inline key material.
type TLS struct {
	Enabled    *bool  `json:"enabled"`
	CAFile     string `json:"ca_file"`
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
	ServerName string `json:"server_name"`
}

func (c TLS) HasSettings() bool {
	return c.CAFile != "" || c.CertFile != "" || c.KeyFile != "" || c.ServerName != ""
}

// Build refuses contradictory settings before any connection is opened.
// A nil result selects plaintext; a non-nil result always verifies the peer.
func (c TLS) Build(defaultEnabled bool) (*tls.Config, error) {
	enabled := defaultEnabled
	if c.Enabled != nil {
		enabled = *c.Enabled
	}
	if !enabled {
		if c.HasSettings() {
			return nil, errors.New("TLS settings require enabled TLS")
		}
		return nil, nil
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return nil, errors.New("cert_file and key_file must be configured together")
	}
	if !validServerName(c.ServerName) {
		return nil, errors.New("server_name must be a hostname or IP address")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName}
	if c.CAFile != "" {
		data, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("ca_file cannot be read: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("system CA roots cannot be loaded: %w", err)
		}
		if !roots.AppendCertsFromPEM(data) {
			return nil, errors.New("ca_file contains no PEM certificates")
		}
		config.RootCAs = roots
	}
	if c.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("cert_file and key_file must contain a matching PEM certificate and private key: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}
	}
	return config, nil
}

func validServerName(name string) bool {
	if name == "" || net.ParseIP(name) != nil {
		return true
	}
	name = strings.TrimSuffix(name, ".")
	if len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// ForURL keeps the endpoint's scheme authoritative. A switch cannot silently
// downgrade HTTPS or claim encryption for an HTTP connection.
func (c TLS) ForURL(endpoint string) (*tls.Config, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("endpoint must be an http or https URL")
	}
	secure := u.Scheme == "https"
	if c.Enabled != nil && *c.Enabled != secure {
		return nil, errors.New("enabled must match the endpoint URL scheme (https for TLS, http for plaintext)")
	}
	return c.Build(secure)
}

// Transport clones the standard transport, retaining its proxy and dial policy.
// HTTP clients use CheckRedirect to keep credentials on the dependency origin.
func Transport(config *tls.Config) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = config
	return transport
}

func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("TLS redirect to plaintext refused")
	}
	if len(via) > 0 && origin(req.URL) != origin(via[0].URL) {
		return errors.New("redirect to another dependency origin refused")
	}
	return nil
}

func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}
