package outbound_test

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/testutil/tlsfixture"
)

// Owns verification and optional client authentication: removing verification,
// ignoring the name/CA, or failing to load a client key breaks a real handshake.
func TestTLSAuthenticatesBothPeers(t *testing.T) {
	f := tlsfixture.New(t)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{f.Server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: f.Roots}
	s.StartTLS()
	defer s.Close()
	for name, tc := range map[string]struct {
		ca, server, cert, key string
		ok                    bool
	}{
		"trusted mutual TLS":      {f.CAFile, "dependency.test", f.CertFile, f.KeyFile, true},
		"untrusted server":        {"", "dependency.test", f.CertFile, f.KeyFile, false},
		"wrong name":              {f.CAFile, "wrong.test", f.CertFile, f.KeyFile, false},
		"missing client identity": {f.CAFile, "dependency.test", "", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			config, err := (outbound.TLS{CAFile: tc.ca, ServerName: tc.server, CertFile: tc.cert, KeyFile: tc.key}).Build(true)
			if err != nil {
				t.Fatal(err)
			}
			transport := outbound.Transport(config)
			defer transport.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
			resp, err := (&http.Client{Transport: transport}).Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			if (err == nil) != tc.ok {
				t.Fatalf("handshake success=%v, want %v: %v", err == nil, tc.ok, err)
			}
		})
	}
}

// Redirects cannot downgrade a call or disclose its client identity to another
// origin; redirects within the dependency's origin still work.
func TestTLSRedirectStaysOnDependency(t *testing.T) {
	f := tlsfixture.New(t)
	var reached atomic.Bool
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer plain.Close()
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	other.TLS = &tls.Config{
		Certificates: []tls.Certificate{f.Server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: f.Roots,
		VerifyConnection: func(tls.ConnectionState) error { reached.Store(true); return nil },
	}
	other.StartTLS()
	defer other.Close()
	secure := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/done" {
			w.WriteHeader(204)
			return
		}
		http.Redirect(w, r, r.URL.Query().Get("target"), http.StatusTemporaryRedirect)
	}))
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{f.Server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: f.Roots}
	secure.StartTLS()
	defer secure.Close()
	config, err := (outbound.TLS{CAFile: f.CAFile, ServerName: "dependency.test", CertFile: f.CertFile, KeyFile: f.KeyFile}).Build(true)
	if err != nil {
		t.Fatal(err)
	}
	transport := outbound.Transport(config)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: outbound.CheckRedirect, Timeout: time.Second}
	for _, tc := range []struct{ name, target, refusal string }{
		{"plaintext", plain.URL, "plaintext"},
		{"different TLS origin", other.URL, "origin"},
		{"same origin", secure.URL + "/done", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached.Store(false)
			response, err := client.Get(secure.URL + "?target=" + url.QueryEscape(tc.target))
			if response != nil {
				response.Body.Close()
			}
			if tc.refusal == "" {
				if err != nil || response.StatusCode != 204 {
					t.Fatalf("same-origin redirect response=%v error=%v", response, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.refusal) {
				t.Fatalf("redirect error=%v, want %s refusal", err, tc.refusal)
			}
			if reached.Load() {
				t.Fatal("redirect reached another origin or sent it the client certificate")
			}
		})
	}
}
