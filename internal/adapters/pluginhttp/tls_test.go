package pluginhttp_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/The-Vibe-Company/quivr/internal/adapters/pluginhttp"
	"github.com/The-Vibe-Company/quivr/internal/outbound"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/plugins/devhost"
	"github.com/The-Vibe-Company/quivr/internal/testutil/tlsfixture"
)

// Deployment trust must reach discovery and POST through the shared protocol
// client, including a pin resolved after the transport was configured.
func TestPluginDiscoveryAndCallOverTLS(t *testing.T) {
	f := tlsfixture.New(t)
	config, err := (outbound.TLS{CAFile: f.CAFile, ServerName: "dependency.test"}).Build(true)
	if err != nil {
		t.Fatal(err)
	}
	transport := outbound.Transport(config)
	defer transport.CloseIdleConnections()
	devhost.SetTransport(transport)
	defer devhost.SetTransport(http.DefaultTransport)
	var pin *plugins.Pin
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDiscovery(w, r, pin) {
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v0/contributions/ingestion/embed_query" {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"vector": []float64{1, 0}})
	}))
	s.TLS = &tls.Config{Certificates: []tls.Certificate{f.Server}}
	s.StartTLS()
	defer s.Close()
	pin, err = plugins.LoadPinManifest([]byte(embedderManifest), "TLS test", plugins.PinConfig{Endpoint: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	vector, err := (pluginhttp.Ingestor{Pin: pin}).EncodeQuery(ctx, "org", plugins.SpaceKey("acme.embedder.small", "1"), "a query")
	if err != nil || len(vector) != 2 || vector[0] != 1 {
		t.Fatalf("plugin query over TLS vector=%v error=%v", vector, err)
	}
}
