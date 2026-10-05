package outbound_test

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
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

// A trusted HTTPS endpoint cannot downgrade a dependency call via a redirect.
func TestTLSRedirectCannotReachPlaintext(t *testing.T) {
	reached := false
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusTemporaryRedirect)
	}))
	defer secure.Close()
	client := secure.Client()
	client.CheckRedirect = outbound.CheckRedirect
	response, err := client.Get(secure.URL)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || reached {
		t.Fatalf("downgrade error=%v plaintext reached=%v", err, reached)
	}
}
