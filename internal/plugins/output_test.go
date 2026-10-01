package plugins_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

const outputManifest = `id: acme.pages
version: 1.0.0
compatibility:
  engine: ">=0.1.0 <0.2.0"
  plugin_api: ">=0.1.0 <0.2.0"
contributions:
  normalizer:
    media_types: [application/pdf]
    limits:
      max_parts: 3
extensions:
  acme.pages.stats:
    "1":
      type: object
      additionalProperties: false
      required: [pages]
      properties:
        pages: {type: integer, minimum: 0}
  acme.pages.notes:
    "1": {type: object}
`

func outputContext(t *testing.T) plugins.OutputContext {
	t.Helper()
	report := plugins.Validate([]byte(outputManifest))
	if !report.Valid {
		t.Fatalf("manifest: %+v", report.Errors)
	}
	sum := strings.Repeat("a", 64)
	return plugins.OutputContext{
		Manifest: report.Manifest,
		Input:    plugins.InputBlob{BlobID: "blob-1", MediaType: "application/pdf", SHA256: sum},
		VerifyBlob: func(_ context.Context, id string) (plugins.VerifiedBlob, error) {
			if id != "blob-1" {
				return plugins.VerifiedBlob{}, errors.New("unknown")
			}
			return plugins.VerifiedBlob{ID: id, MediaType: "application/pdf", SHA256: sum}, nil
		},
	}
}

func TestCheckNormalizerOutput(t *testing.T) {
	cases := []struct {
		name, body string
		code, want string
	}{
		{"valid with input Blob Part and declared extension", `{"manifest":{"kind":"manifest","parts":[
			{"key":"pdf","role":"original","content":{"kind":"blob","blob_id":"blob-1","media_type":"application/pdf"}},
			{"key":"p1","parent_key":"pdf","role":"page","content":{"kind":"text","text":"one"},
			 "extensions":{"acme.pages.stats":{"schema_version":"1","data":{"pages":1}}}}]},
			"extensions":{"acme.pages.stats":{"schema_version":"1","data":{"pages":1}}}}`, "", ""},
		{"unknown response field", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"text","text":"x"}}]},"extra":1}`,
			plugins.CodeSchema, "extra"},
		{"engine Manifest rule", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"text","text":"x\u0000"}}]}}`,
			plugins.CodeInvalidManifest, "without NUL"},
		{"declared max_parts", `{"manifest":{"kind":"manifest","parts":[
			{"key":"a","role":"r","content":{"kind":"text","text":"x"}},{"key":"b","role":"r","content":{"kind":"text","text":"x"}},
			{"key":"c","role":"r","content":{"kind":"text","text":"x"}},{"key":"d","role":"r","content":{"kind":"text","text":"x"}}]}}`,
			plugins.CodeTooManyParts, "max_parts 3"},
		{"foreign Blob", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"blob","blob_id":"blob-2","media_type":"application/pdf"}}]}}`,
			plugins.CodeForeignBlob, "only the input Blob blob-1"},
		{"input Blob with another media type", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"blob","blob_id":"blob-1","media_type":"text/plain"}}]}}`,
			plugins.CodeUnverifiedBlob, "text/plain"},
		{"undeclared namespace", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"text","text":"x"}}]},
			"extensions":{"acme.other":{"schema_version":"1","data":{}}}}`, plugins.CodeUndeclaredNamespace, `"acme.other"`},
		{"undeclared namespace on a Part", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"text","text":"x"},
			"extensions":{"example.editorial":{"schema_version":"1","data":{}}}}]}}`, plugins.CodeUndeclaredNamespace, `"example.editorial"`},
		{"undeclared schema version", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"text","text":"x"}}]},
			"extensions":{"acme.pages.stats":{"schema_version":"2","data":{"pages":1}}}}`, plugins.CodeUndeclaredSchemaVersion, `"2"`},
		{"extension data invalid", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"text","text":"x"}}]},
			"extensions":{"acme.pages.stats":{"schema_version":"1","data":{"pages":-1}}}}`, plugins.CodeInvalidExtension, "pages"},
		// Extension data is stored as JSON text, which cannot hold NUL: such
		// output is invalid, never a storage failure retried.
		{"NUL in extension data", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"text","text":"x"}}]},
			"extensions":{"acme.pages.notes":{"schema_version":"1","data":{"title":"a\u0000b"}}}}`, plugins.CodeInvalidExtension, "NUL"},
		{"NUL in nested extension data on a Part", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"text","text":"x"},
			"extensions":{"acme.pages.notes":{"schema_version":"1","data":{"list":[{"x":"\u0000"}]}}}}]}}`, plugins.CodeInvalidExtension, "NUL"},
		{"NUL in an extension data key", `{"manifest":{"kind":"manifest","parts":[{"key":"a","role":"r","content":{"kind":"text","text":"x"}}]},
			"extensions":{"acme.pages.notes":{"schema_version":"1","data":{"a\u0000":1}}}}`, plugins.CodeInvalidExtension, "NUL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := plugins.CheckNormalizerOutput(context.Background(), []byte(tc.body), outputContext(t))
			if tc.code == "" {
				if len(issues) > 0 {
					t.Fatalf("unexpected issues: %+v", issues)
				}
				return
			}
			if len(issues) != 1 || issues[0].Code != tc.code || !strings.Contains(issues[0].Message, tc.want) {
				t.Fatalf("want one %s issue mentioning %q, got %+v", tc.code, tc.want, issues)
			}
		})
	}
}

func TestMaxResponseBytesIsBoundedByTheEngine(t *testing.T) {
	m := outputContext(t).Manifest
	if got := plugins.MaxResponseBytes(m); got != plugins.DefaultMaxResponseBytes {
		t.Fatalf("default: %d", got)
	}
	m.Contributions.Normalizer.Limits.MaxResponseBytes = 64 << 20
	if got := plugins.MaxResponseBytes(m); got != plugins.EngineMaxResponseBytes {
		t.Fatalf("capped: %d", got)
	}
}
