package telemetry_test

import (
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/telemetry"
)

// Owns the bounded dependency-call counter contract: only declared
// (dependency, protocol, operation) tuples are exposed, unknown values are
// dropped, and nil receivers are safe no-ops.
func TestDependencyMetricsKeepDeclaredSeriesOnly(t *testing.T) {
	m := telemetry.NewDependencyMetrics()
	m.Record(telemetry.DependencyWeaviate, telemetry.ProtocolHTTP, telemetry.OpQuery)
	m.Add(2, telemetry.DependencyWeaviate, telemetry.ProtocolGRPC, telemetry.OpWrite)
	m.Record(telemetry.DependencyS3, telemetry.ProtocolHTTP, telemetry.OpGetObject)
	m.Record(telemetry.DependencyS3, telemetry.ProtocolHTTP, telemetry.OpPutObject)
	m.Record(telemetry.DependencyPostgres, telemetry.ProtocolWire, telemetry.OpQuery)
	m.Add(3, telemetry.DependencyPostgres, telemetry.ProtocolWire, telemetry.OpBatchQuery)
	// Undeclared tuples (e.g. arbitrary paths or tenant IDs) are dropped.
	m.Record("weaviate", "http", "/v1/objects/secret-id")
	m.Record("unknown-dep", "http", "query")

	var nilMetrics *telemetry.DependencyMetrics
	nilMetrics.Record(telemetry.DependencyS3, telemetry.ProtocolHTTP, telemetry.OpGetObject)
	nilMetrics.Add(4, telemetry.DependencyPostgres, telemetry.ProtocolWire, telemetry.OpQuery)

	var b strings.Builder
	nilMetrics.Write(&b)
	m.Write(&b)
	text := b.String()

	for _, want := range []string{
		"# TYPE quivr_dependency_calls_total counter",
		`quivr_dependency_calls_total{dependency="weaviate",protocol="http",operation="query"} 1`,
		`quivr_dependency_calls_total{dependency="weaviate",protocol="grpc",operation="write"} 2`,
		`quivr_dependency_calls_total{dependency="s3",protocol="http",operation="get_object"} 1`,
		`quivr_dependency_calls_total{dependency="s3",protocol="http",operation="put_object"} 1`,
		`quivr_dependency_calls_total{dependency="postgres",protocol="wire",operation="query"} 1`,
		`quivr_dependency_calls_total{dependency="postgres",protocol="wire",operation="batch"} 0`,
		`quivr_dependency_calls_total{dependency="postgres",protocol="wire",operation="batch_query"} 3`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"secret-id", "unknown-dep"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unexpected undeclared label %q in\n%s", forbidden, text)
		}
	}
}
