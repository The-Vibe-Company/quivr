package contracts_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// TestHTTPContractAliasesSharedManifestSchemas fails when openapi.yaml
// redefines a shape owned by the shared Manifest schema instead of aliasing it.
func TestHTTPContractAliasesSharedManifestSchemas(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(contracts.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	shared := sharedDefs(t)
	if len(shared) == 0 {
		t.Fatal("shared Manifest schema declares no $defs")
	}
	for name := range shared {
		got, ok := doc.Components.Schemas[name]
		if !ok {
			t.Errorf("openapi.yaml lacks component %s aliasing the shared schema", name)
			continue
		}
		want := contracts.SharedManifestRef + "#/$defs/" + name
		if len(got) != 1 || got["$ref"] != want {
			t.Errorf("openapi.yaml component %s = %v; want only $ref %s (edit contracts/shared/v0/manifest.schema.json instead)", name, got, want)
		}
	}
}

// TestPluginSchemasDoNotRedefineSharedShapes fails when a Plugin Protocol schema
// declares its own copy of a shared shape.
func TestPluginSchemasDoNotRedefineSharedShapes(t *testing.T) {
	shared := sharedDefs(t)
	schemas := contracts.PluginSchemas()
	if len(schemas) == 0 {
		t.Fatal("no Plugin Protocol schemas embedded")
	}
	for file, raw := range schemas {
		var doc struct {
			Defs map[string]any `json:"$defs"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for name := range doc.Defs {
			if _, owned := shared[name]; owned {
				t.Errorf("%s redefines shared shape %s; reference the shared schema instead", file, name)
			}
		}
	}
}

// TestCompilerResolvesSharedReferences proves both contracts compile against the
// one shared Manifest source.
func TestCompilerResolvesSharedReferences(t *testing.T) {
	compiler, err := contracts.NewCompiler()
	if err != nil {
		t.Fatal(err)
	}
	ingest, err := compiler.Compile(contracts.HTTPSchema("IngestCommand"))
	if err != nil {
		t.Fatal(err)
	}
	command := `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},
	  "content":{"kind":"manifest","parts":[{"key":"a","role":"body","content":{"kind":"text","text":"x"}}]}}`
	if err := ingest.Validate(decode(t, command)); err != nil {
		t.Fatalf("valid manifest command rejected: %v", err)
	}
	if err := ingest.Validate(decode(t, strings.Replace(command, `"role":"body",`, ``, 1))); err == nil {
		t.Fatal("Part without role accepted through the shared schema")
	}
	for file := range contracts.PluginSchemas() {
		if _, err := compiler.Compile(contracts.PluginSchema(file)); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
}

// TestCommonMetadataSchema keeps the reserved source metadata contract
// executable at its owner boundary. The inputs use literal wire keys so a
// renamed Go field cannot make the test pass by serializing the same type.
func TestCommonMetadataSchema(t *testing.T) {
	compiler, err := contracts.NewCompiler()
	if err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(contracts.CommonMetadataSchema())
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"all fields":         `{"language":"en","published_at":"2026-09-28T10:00:00Z","source_type":"rss","source":"https://example.org/feed","author":["Ada"],"subjects":["science"],"tags":["news"],"country":["FR"],"place":["Paris"]}`,
		"all fields omitted": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := schema.Validate(decode(t, raw)); err != nil {
				t.Fatalf("valid common metadata rejected: %v", err)
			}
		})
	}
	for name, raw := range map[string]string{
		"unknown key":   `{"unknown":"value"}`,
		"invalid date":  `{"published_at":"not-a-date"}`,
		"long string":   `{"source":"` + strings.Repeat("x", 201) + `"}`,
		"too many tags": `{"tags":["x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x","x"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := schema.Validate(decode(t, raw)); err == nil {
				t.Fatal("invalid common metadata accepted")
			}
		})
	}
}

func sharedDefs(t *testing.T) map[string]any {
	t.Helper()
	var shared struct {
		Defs map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(contracts.SharedManifest(), &shared); err != nil {
		t.Fatal(err)
	}
	return shared.Defs
}

func decode(t *testing.T, s string) any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
