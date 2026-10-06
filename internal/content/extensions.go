package content

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/The-Vibe-Company/quivr/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ExtensionValidator validates namespaced extensions against schemas declared by
// the deployment. It rejects undeclared namespaces and schema versions.
type ExtensionValidator interface {
	Validate(context.Context, Extensions) error
}

// CheckExtensions applies the engine's extension rules: a bounded JSON size,
// then validator (BuiltinExtensions when nil). Acceptance uses it for submitted
// and Part extensions, and the Plugin Contract Runner for normalizer output. An
// oversized set is a *ManifestViolation wrapping ErrUnsupported.
func CheckExtensions(ctx context.Context, validator ExtensionValidator, exts Extensions) error {
	if len(exts) == 0 {
		return nil
	}
	if !boundedJSON(exts) {
		return violation(ErrUnsupported, "extensions exceed %d bytes of JSON", maxGenericJSONBytes)
	}
	if validator == nil {
		validator = BuiltinExtensions{}
	}
	return validator.Validate(ctx, exts)
}

// CommonMetadataNamespace is the reserved namespace for source metadata that
// every plugin may emit without declaring ownership in its manifest.
const CommonMetadataNamespace = "quivr.metadata"

// CommonMetadataVersion is the first version of the common metadata contract.
const CommonMetadataVersion = "1"

// BuiltinExtensions is the foundation's declared schema registry.
type BuiltinExtensions struct{}

var declaredExtensionSchemas = map[string]map[string]string{
	CommonMetadataNamespace: {
		CommonMetadataVersion: string(contracts.CommonMetadata()),
	},
	"example.editorial": {
		"1": `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "headline": {"type": "string"},
    "subjects": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {"code": {"type": "string"}, "score": {"type": "number"}},
        "required": ["code"],
        "additionalProperties": true
      }
    },
    "flags": {"type": "object"},
    "extra": {}
  },
  "additionalProperties": true
}`,
	},
}

var (
	extensionOnce    sync.Once
	extensionSchemas map[string]map[string]*jsonschema.Schema
	extensionErr     error
)

func compiledExtensionSchemas() (map[string]map[string]*jsonschema.Schema, error) {
	extensionOnce.Do(func() {
		extensionSchemas = map[string]map[string]*jsonschema.Schema{}
		for namespace, versions := range declaredExtensionSchemas {
			compiled := map[string]*jsonschema.Schema{}
			for version, raw := range versions {
				var doc any
				if err := json.Unmarshal([]byte(raw), &doc); err != nil {
					extensionErr = err
					return
				}
				compiler := jsonschema.NewCompiler()
				compiler.AssertFormat()
				url := "https://quivr.invalid/extensions/" + namespace + "/" + version
				if err := compiler.AddResource(url, doc); err != nil {
					extensionErr = err
					return
				}
				schema, err := compiler.Compile(url)
				if err != nil {
					extensionErr = err
					return
				}
				compiled[version] = schema
			}
			extensionSchemas[namespace] = compiled
		}
	})
	return extensionSchemas, extensionErr
}

// DeclaredExtension reports whether the deployment declares the namespace, so
// retrieval mappings may address its source data.
func DeclaredExtension(namespace string) bool {
	_, ok := declaredExtensionSchemas[namespace]
	return ok
}

func (BuiltinExtensions) Validate(_ context.Context, exts Extensions) error {
	if len(exts) == 0 {
		return nil
	}
	schemas, err := compiledExtensionSchemas()
	if err != nil {
		return err
	}
	for namespace, ext := range exts {
		versions, ok := schemas[namespace]
		if !ok {
			return ErrUnsupported
		}
		schema, ok := versions[ext.SchemaVersion]
		if !ok {
			return ErrUnsupported
		}
		data := ext.Data
		if data == nil {
			data = map[string]any{}
		}
		if err := schema.Validate(data); err != nil {
			return ErrInvalid
		}
	}
	return nil
}
