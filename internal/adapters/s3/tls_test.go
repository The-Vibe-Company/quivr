package s3_test

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	store "github.com/The-Vibe-Company/quivr/internal/adapters/s3"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/testutil/tlsfixture"
)

// The AWS SDK must use the configured transport for signed object-store calls.
func TestS3ReadyOverTLS(t *testing.T) {
	f := tlsfixture.New(t)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" || r.URL.Path != "/bucket" || r.Header.Get("Authorization") == "" {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(200)
	}))
	s.TLS = &tls.Config{Certificates: []tls.Certificate{f.Server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: f.Roots}
	s.StartTLS()
	defer s.Close()
	blobs, err := store.NewWithTLS(store.Config{Endpoint: s.URL, Bucket: "bucket", AccessKey: "test-key", SecretKey: "test-secret"}, outbound.TLS{CAFile: f.CAFile, ServerName: "dependency.test", CertFile: f.CertFile, KeyFile: f.KeyFile})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := blobs.Ready(ctx); err != nil {
		t.Fatalf("S3 signed HEAD over TLS: %v", err)
	}
}
