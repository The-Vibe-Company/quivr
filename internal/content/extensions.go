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
	// Source headers of a mail collected by the m365_mail connector.
	"connector.m365_mail": {
		"1": `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "$defs": {"address": {"type": "object", "properties": {"name": {"type": "string"}, "address": {"type": "string"}}, "additionalProperties": false}},
  "properties": {
    "graph_id": {"type": "string"},
    "internet_message_id": {"type": "string"},
    "conversation_id": {"type": "string"},
    "subject": {"type": "string"},
    "from": {"$ref": "#/$defs/address"},
    "sender": {"$ref": "#/$defs/address"},
    "to": {"type": "array", "items": {"$ref": "#/$defs/address"}},
    "cc": {"type": "array", "items": {"$ref": "#/$defs/address"}},
    "to_count": {"type": "integer", "minimum": 0},
    "cc_count": {"type": "integer", "minimum": 0},
    "sent_at": {"type": "string"},
    "received_at": {"type": "string"},
    "folder": {"type": "string"},
    "attachments_skipped": {"type": "array", "items": {"type": "object", "required": ["reason"], "properties": {
      "name": {"type": "string"}, "media_type": {"type": "string"}, "size": {"type": "integer"},
      "reason": {"enum": ["too_large", "reference_attachment", "empty"]}}, "additionalProperties": false}}
  },
  "required": ["graph_id"],
  "additionalProperties": false
}`,
	},
	// Attachment metadata of a Blob Part collected by the m365_mail connector.
	"connector.m365_mail.attachment": {
		"1": `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "name": {"type": "string"},
    "size": {"type": "integer"},
    "is_inline": {"type": "boolean"},
    "attachment_type": {"enum": ["file", "item"]}
  },
  "additionalProperties": false
}`,
	},
	// Item and feed metadata recorded by the built-in rss connector kind.
	"connector.rss": {"1": rssExtensionSchema},
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

const rssExtensionSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["item", "feed"],
  "additionalProperties": false,
  "properties": {
    "item": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "guid": {"type": "string"},
        "link": {"type": "string"},
        "links": {"type": "array", "maxItems": 20, "items": {"type": "string"}},
        "authors": {"type": "array", "maxItems": 20, "items": {"type": "object", "additionalProperties": false, "properties": {"name": {"type": "string"}, "email": {"type": "string"}}}},
        "published": {"type": "string", "format": "date-time"},
        "updated": {"type": "string", "format": "date-time"},
        "categories": {"type": "array", "maxItems": 50, "items": {"type": "string"}},
        "enclosures": {"type": "array", "maxItems": 20, "items": {"type": "object", "required": ["url"], "additionalProperties": false, "properties": {"url": {"type": "string"}, "type": {"type": "string"}, "length": {"type": "string"}}}},
        "truncated": {"type": "boolean"}
      }
    },
    "feed": {
      "type": "object",
      "required": ["format"],
      "additionalProperties": false,
      "properties": {
        "format": {"type": "string"},
        "version": {"type": "string"},
        "title": {"type": "string"},
        "link": {"type": "string"},
        "language": {"type": "string"}
      }
    }
  }
}`
