// Package contracts embeds the authoritative public contracts and compiles them
// as one set of JSON Schema resources. The shared Manifest schema is the single
// source of the Manifest, Part, Extensions, Relation, SourceIdentity and
// Provenance shapes; the HTTP OpenAPI document and the Plugin Protocol schemas
// both reference it.
package contracts

import (
	"bytes"
	"embed"
	"io/fs"
	"path"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

//go:embed http/v0/openapi.yaml shared/v0/manifest.schema.json plugins/v0/*.schema.json
var files embed.FS

const (
	// BaseURL mirrors the repository layout so relative $refs between contract
	// files resolve exactly as they do on disk.
	BaseURL      = "https://quivr.invalid/contracts/"
	openAPIPath  = "http/v0/openapi.yaml"
	sharedPath   = "shared/v0/manifest.schema.json"
	pluginSchema = "plugins/v0"
	// SharedManifestRef is how openapi.yaml and plugin schemas reference the
	// shared Manifest schema, relative to their own directory.
	SharedManifestRef = "../../" + sharedPath
)

func mustRead(name string) []byte {
	b, err := files.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return b
}

// OpenAPI returns the authoritative HTTP contract document.
func OpenAPI() []byte { return mustRead(openAPIPath) }

// SharedManifest returns the shared Manifest JSON Schema.
func SharedManifest() []byte { return mustRead(sharedPath) }

// PluginSchemas returns the Plugin Protocol v0 schemas keyed by file name.
func PluginSchemas() map[string][]byte {
	out := map[string][]byte{}
	entries, _ := fs.ReadDir(files, pluginSchema)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".schema.json") {
			out[e.Name()] = mustRead(path.Join(pluginSchema, e.Name()))
		}
	}
	return out
}

// HTTPSchema is the compiler URL of an OpenAPI component schema.
func HTTPSchema(name string) string {
	return BaseURL + openAPIPath + "#/components/schemas/" + name
}

// PluginSchema is the compiler URL of a Plugin Protocol schema file.
func PluginSchema(file string) string { return BaseURL + path.Join(pluginSchema, file) }

// NewCompiler registers every contract document as a JSON Schema resource.
func NewCompiler() (*jsonschema.Compiler, error) {
	compiler := jsonschema.NewCompiler()
	var doc map[string]any
	if err := yaml.Unmarshal(OpenAPI(), &doc); err != nil {
		return nil, err
	}
	if err := compiler.AddResource(BaseURL+openAPIPath, doc); err != nil {
		return nil, err
	}
	add := func(name string, raw []byte) error {
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		return compiler.AddResource(BaseURL+name, v)
	}
	if err := add(sharedPath, SharedManifest()); err != nil {
		return nil, err
	}
	for file, raw := range PluginSchemas() {
		if err := add(path.Join(pluginSchema, file), raw); err != nil {
			return nil, err
		}
	}
	return compiler, nil
}
