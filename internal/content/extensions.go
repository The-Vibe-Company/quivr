package content

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ExtensionValidator validates namespaced extensions against schemas declared by
// the deployment. It rejects undeclared namespaces and schema versions.
type ExtensionValidator interface {
	Validate(context.Context, Extensions) error
}

// BuiltinExtensions is the foundation's declared schema registry. It ships one
// generic example namespace so structured source data can be exercised without a
// plugin platform; installations install real plugin schemas later. It
// deliberately contains no vertical vocabulary.
type BuiltinExtensions struct{}

var declaredExtensionSchemas = map[string]map[string]string{
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
