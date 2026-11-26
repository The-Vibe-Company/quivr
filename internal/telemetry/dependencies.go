package telemetry

import "io"

// Fixed dependency, protocol and operation label values for DependencyMetrics.
// Only these declared tuples are accepted so identifiers, paths or query text
// can never become metric labels.
const (
	DependencyWeaviate = "weaviate"
	DependencyS3       = "s3"
	DependencyPostgres = "postgres"

	ProtocolHTTP = "http"
	ProtocolGRPC = "grpc"
	ProtocolWire = "wire"

	OpQuery      = "query"
	OpWrite      = "write"
	OpRead       = "read"
	OpDelete     = "delete"
	OpSchema     = "schema"
	OpReady      = "ready"
	OpOther      = "other"
	OpGetObject  = "get_object"
	OpPutObject  = "put_object"
	OpHeadObject = "head_object"
	OpBucket     = "bucket"
	OpBatch      = "batch"
	OpBatchQuery = "batch_query"
)

// DependencyMetrics counts process-local dependency calls by bounded
// (dependency, protocol, operation) tuples. Nil receivers ignore calls so
// standalone adapter callers and tests need no setup.
type DependencyMetrics struct {
	calls *Counter
}

// NewDependencyMetrics declares all supported dependency call series with
// explicit zero values so before/after scrapes can compute exact deltas.
func NewDependencyMetrics() *DependencyMetrics {
	series := [][]string{
		{DependencyWeaviate, ProtocolHTTP, OpQuery},
		{DependencyWeaviate, ProtocolHTTP, OpWrite},
		{DependencyWeaviate, ProtocolHTTP, OpRead},
		{DependencyWeaviate, ProtocolHTTP, OpDelete},
		{DependencyWeaviate, ProtocolHTTP, OpSchema},
		{DependencyWeaviate, ProtocolHTTP, OpReady},
		{DependencyWeaviate, ProtocolHTTP, OpOther},
		{DependencyWeaviate, ProtocolGRPC, OpQuery},
		{DependencyWeaviate, ProtocolGRPC, OpWrite},
		{DependencyWeaviate, ProtocolGRPC, OpRead},
		{DependencyWeaviate, ProtocolGRPC, OpDelete},
		{DependencyWeaviate, ProtocolGRPC, OpSchema},
		{DependencyWeaviate, ProtocolGRPC, OpReady},
		{DependencyWeaviate, ProtocolGRPC, OpOther},
		{DependencyS3, ProtocolHTTP, OpGetObject},
		{DependencyS3, ProtocolHTTP, OpPutObject},
		{DependencyS3, ProtocolHTTP, OpHeadObject},
		{DependencyS3, ProtocolHTTP, OpBucket},
		{DependencyPostgres, ProtocolWire, OpQuery},
		{DependencyPostgres, ProtocolWire, OpBatch},
		{DependencyPostgres, ProtocolWire, OpBatchQuery},
	}
	return &DependencyMetrics{
		calls: NewCounter(
			"quivr_dependency_calls_total",
			"Dependency calls issued by this process, by dependency, protocol and operation.",
			[]string{"dependency", "protocol", "operation"},
			series...,
		),
	}
}

// Record increments the counter for one declared dependency call.
func (m *DependencyMetrics) Record(dependency, protocol, operation string) {
	if m != nil {
		m.calls.Inc(dependency, protocol, operation)
	}
}

// Add increments the counter by n for a declared dependency call.
func (m *DependencyMetrics) Add(n int, dependency, protocol, operation string) {
	if m != nil {
		m.calls.Add(n, dependency, protocol, operation)
	}
}

// Write renders the dependency counter in Prometheus text exposition format.
func (m *DependencyMetrics) Write(w io.Writer) {
	if m != nil {
		m.calls.Write(w)
	}
}
