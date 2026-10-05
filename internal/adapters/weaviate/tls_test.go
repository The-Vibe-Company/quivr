package weaviate_test

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/weaviate"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/testutil/tlsfixture"
)

// The adapter must actually use configured trust for its readiness request.
func TestReadyOverTLS(t *testing.T) {
	f := tlsfixture.New(t)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/.well-known/ready" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(204)
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{f.Server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: f.Roots}
	s.StartTLS()
	defer s.Close()
	for _, tc := range []struct {
		name, ca, server string
		ok               bool
	}{
		{"configured CA", f.CAFile, "dependency.test", true},
		{"system roots reject private CA", "", "dependency.test", false},
		{"hostname verified", f.CAFile, "wrong.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := weaviate.NewWithTLS(s.URL, outbound.TLS{CAFile: tc.ca, ServerName: tc.server, CertFile: f.CertFile, KeyFile: f.KeyFile})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Client.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = store.Ready(ctx)
			if (err == nil) != tc.ok {
				t.Fatalf("Ready error=%v, want success=%v", err, tc.ok)
			}
		})
	}
}
