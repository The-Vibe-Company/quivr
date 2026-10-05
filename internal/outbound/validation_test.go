package outbound_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/testutil/tlsfixture"
)

func TestMalformedTLSSettingsAreRefused(t *testing.T) {
	f := tlsfixture.New(t)
	other := tlsfixture.New(t)
	invalid := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(invalid, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	disabled := false
	for name, tc := range map[string]struct {
		settings outbound.TLS
		field    string
	}{
		"disabled trust":          {outbound.TLS{Enabled: &disabled, CAFile: f.CAFile}, "enabled"},
		"unreadable roots":        {outbound.TLS{CAFile: invalid + ".missing"}, "ca_file"},
		"invalid roots":           {outbound.TLS{CAFile: invalid}, "ca_file"},
		"certificate without key": {outbound.TLS{CertFile: f.CertFile}, "key_file"},
		"key without certificate": {outbound.TLS{KeyFile: f.KeyFile}, "cert_file"},
		"mismatched identity":     {outbound.TLS{CertFile: f.CertFile, KeyFile: other.KeyFile}, "cert_file"},
		"URL as server name":      {outbound.TLS{ServerName: "https://dependency.test"}, "server_name"},
		"invalid name character":  {outbound.TLS{ServerName: "db?.internal"}, "server_name"},
		"empty DNS label":         {outbound.TLS{ServerName: "db..internal"}, "server_name"},
		"leading DNS hyphen":      {outbound.TLS{ServerName: "-db.internal"}, "server_name"},
		"trailing DNS hyphen":     {outbound.TLS{ServerName: "db-.internal"}, "server_name"},
		"oversized DNS label":     {outbound.TLS{ServerName: strings.Repeat("a", 64) + ".internal"}, "server_name"},
		"oversized DNS name":      {outbound.TLS{ServerName: strings.Repeat(strings.Repeat("a", 63)+".", 4)}, "server_name"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.settings.Build(true)
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error=%v, want %s refusal", err, tc.field)
			}
		})
	}
}

func TestTLSSwitchCannotDisagreeWithEndpoint(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		endpoint string
		enabled  *bool
		ok       bool
	}{
		{"https://dependency.test", nil, true},
		{"http://localhost", nil, true},
		{"https://dependency.test", &enabled, true},
		{"http://localhost", &disabled, true},
		{"https://dependency.test", &disabled, false},
		{"http://localhost", &enabled, false},
		{"ftp://dependency.test", nil, false},
	} {
		config, err := (outbound.TLS{Enabled: tc.enabled}).ForURL(tc.endpoint)
		if (err == nil) != tc.ok {
			t.Fatalf("endpoint %s enabled=%v error=%v want valid=%v", tc.endpoint, tc.enabled, err, tc.ok)
		}
		if tc.ok && (config != nil) != strings.HasPrefix(tc.endpoint, "https://") {
			t.Fatalf("endpoint %s TLS config present=%v", tc.endpoint, config != nil)
		}
	}
}

func TestTLSServerNamesAcceptDNSAndIPAddresses(t *testing.T) {
	for _, name := range []string{"", "localhost", "db.internal", "DB.internal.", "127.0.0.1", "::1", "2001:db8::1", strings.Repeat("a", 63) + ".internal"} {
		if _, err := (outbound.TLS{ServerName: name}).Build(true); err != nil {
			t.Fatalf("valid server name %q refused: %v", name, err)
		}
	}
}
