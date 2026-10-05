package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/logging"
	"github.com/The-Vibe-Company/quivr/internal/retrieval"
	"github.com/The-Vibe-Company/quivr/internal/transport/httpapi"
	"github.com/The-Vibe-Company/quivr/internal/uploads"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Owns inbound W3C/caller identity through the real API/error/log boundary.
// Metrics/access completeness tests cannot detect a regenerated trace or an
// error whose correlation fields disagree with logs. No test-only seams.
func TestRequestTraceIdentityMatchesErrorAndLog(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(oldProvider); _ = provider.Shutdown(context.Background()) })
	var logs bytes.Buffer
	logger, err := logging.New(&logs, logging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	oldLogger := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	handler, err := httpapi.New(knownCorpora{}, content.Service{}, retrieval.Service{}, uploads.Service{}, nil, catalogCursorKey)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v0/corpora/private-corpus?query=private-query", nil)
	req.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	req.Header.Set("X-Request-ID", "caller-request-123")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	var body, event map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"request_id": "caller-request-123", "trace_id": "11111111111111111111111111111111"} {
		if body[key] != want || event[key] != want {
			t.Fatalf("%s: body %v log %v want %s", key, body[key], event[key], want)
		}
	}
	if body["span_id"] == "" || body["span_id"] != event["span_id"] {
		t.Fatalf("span mismatch: body %v log %v", body, event)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans want one HTTP span", len(spans))
	}
	if spans[0].Name != "GET /v0/corpora/{corpus_id}" || spans[0].Parent.SpanID().String() != "2222222222222222" {
		t.Fatalf("wrong route/parent: %+v", spans[0])
	}
	raw, _ := json.Marshal(spans)
	for _, secret := range []string{"private-corpus", "private-query"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("span leaked %s", secret)
		}
	}
}
