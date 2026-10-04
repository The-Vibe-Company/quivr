package contracts

import (
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// HTTPRegistry is the shared schema cache for HTTP requests and responses.
// The registry owns one compiler; compiled schemas are immutable and safe to
// validate concurrently. References may name components or inline responses.
type HTTPRegistry struct {
	mu       sync.Mutex
	compiler *jsonschema.Compiler
	schemas  map[string]*jsonschema.Schema
}

var httpRegistry = sync.OnceValues(func() (*HTTPRegistry, error) {
	compiler, err := NewCompiler()
	if err != nil {
		return nil, err
	}
	return &HTTPRegistry{compiler: compiler, schemas: map[string]*jsonschema.Schema{}}, nil
})

// HTTP returns the registry for the embedded contract, compiled once per process.
func HTTP() (*HTTPRegistry, error) { return httpRegistry() }

// Schema returns a cached compiled schema at an absolute contract reference.
func (r *HTTPRegistry) Schema(ref string) (*jsonschema.Schema, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if schema := r.schemas[ref]; schema != nil {
		return schema, nil
	}
	schema, err := r.compiler.Compile(ref)
	if err != nil {
		return nil, fmt.Errorf("HTTP schema %s: %w", ref, err)
	}
	r.schemas[ref] = schema
	return schema, nil
}
