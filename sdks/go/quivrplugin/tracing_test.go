package quivrplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"go.opentelemetry.io/otel/trace"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Owns SDK continuation at its real protocol handler. Removing extraction
// silently detaches plugin/provider work; engine tests cannot see this process.
func TestPluginContinuesIncomingTraceInHandlerContext(t *testing.T) {
	p, err := New(filepath.Join(fixtures, "manifests/valid/minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var got trace.SpanContext
	if err := p.Normalizer(normalizerFunc(func(ctx context.Context, _ *NormalizerRequest) (*NormalizerResponse, error) {
		got = trace.SpanContextFromContext(ctx)
		return nil, TerminalError("observed", "observed")
	})); err != nil {
		t.Fatal(err)
	}
	h, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(fixtures, "requests/signed-url.json"))
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(body, &input); err != nil {
		t.Fatal(err)
	}
	input["input"].(map[string]any)["media_type"] = "application/x-example"
	body, _ = json.Marshal(input)
	req := httptest.NewRequest("POST", "/v0/contributions/normalizer", bytes.NewReader(body))
	req.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if !got.IsValid() || got.TraceID().String() != "11111111111111111111111111111111" {
		t.Fatalf("trace lost: %v, response %s", got, response.Body.String())
	}
}
