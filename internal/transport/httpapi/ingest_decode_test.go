package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/publicerr"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

// unionKind reads a discriminated union's `kind` without assuming its variant.
func unionKind(in any) string {
	b, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	var discriminator struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(b, &discriminator) != nil {
		return ""
	}
	return discriminator.Kind
}

func extensionsFromTransport(in transport.Extensions) content.Extensions {
	out := make(content.Extensions, len(in))
	for namespace, extension := range in {
		out[namespace] = content.Extension{SchemaVersion: extension.SchemaVersion, Data: extension.Data}
	}
	return out
}

func relationFromTransport(in transport.RelationInput) content.Relation {
	out := content.Relation{Type: in.Type, Target: content.Source{CorpusID: in.Target.CorpusId, Namespace: in.Target.Namespace, RecordKey: in.Target.RecordKey}}
	if in.SourceTargetRevision != nil {
		out.SourceTargetRevision = *in.SourceTargetRevision
	}
	return out
}

func partFromTransport(in transport.Part) (content.Part, error) {
	out := content.Part{Key: in.Key, Role: in.Role}
	if in.ParentKey != nil {
		out.ParentKey = *in.ParentKey
	}
	switch unionKind(in.Content) {
	case "text":
		text, err := in.Content.AsTextContent()
		if err != nil {
			return out, err
		}
		out.Content = content.Text{Kind: "text", Text: text.Text}
	case "blob":
		blob, err := in.Content.AsBlobContent()
		if err != nil {
			return out, err
		}
		out.Content = content.Text{Kind: "blob", BlobID: blob.BlobId, MediaType: blob.MediaType}
	default:
		return out, content.ErrUnsupported
	}
	if in.Extensions != nil {
		out.Extensions = extensionsFromTransport(*in.Extensions)
	}
	return out, nil
}

func commandFromTransport(in transport.IngestCommand) (content.Command, error) {
	c := content.Command{Key: in.IdempotencyKey, Source: content.Source{CorpusID: in.Source.CorpusId, Namespace: in.Source.Namespace, RecordKey: in.Source.RecordKey}}
	if in.RecordAcl != nil {
		c.ACL = *in.RecordAcl
	}

	switch unionKind(in.Content) {
	case "text":
		text, err := in.Content.AsTextContent()
		if err != nil {
			return content.Command{}, err
		}
		c.Content = content.Text{Kind: string(text.Kind), Text: text.Text}
	case "blob":
		blob, err := in.Content.AsBlobContent()
		if err != nil {
			return content.Command{}, err
		}
		c.Content = content.Text{Kind: "blob", BlobID: blob.BlobId, MediaType: blob.MediaType}
	case "manifest":
		manifest, err := in.Content.AsManifestContent()
		if err != nil {
			return content.Command{}, err
		}
		parts := make([]content.Part, 0, len(manifest.Parts))
		for _, p := range manifest.Parts {
			part, err := partFromTransport(p)
			if err != nil {
				return content.Command{}, err
			}
			parts = append(parts, part)
		}
		relations := []content.Relation{}
		if manifest.Relations != nil {
			for _, r := range *manifest.Relations {
				relations = append(relations, relationFromTransport(r))
			}
		}
		c.Content = content.Text{Kind: "manifest"}
		c.Manifest = &content.Manifest{Kind: "manifest", Parts: parts, Relations: relations}
	default:
		return content.Command{}, content.ErrUnsupported
	}
	if in.SourceRevision != nil {
		c.Revision = *in.SourceRevision
	}
	if in.SourcePosition != nil {
		c.Position = *in.SourcePosition
	}
	if in.Extensions != nil {
		c.Extensions = extensionsFromTransport(*in.Extensions)
	}
	if in.Provenance != nil {
		if in.Provenance.Normalization != nil {
			// Engine-owned: only an external normalization publishes it.
			return content.Command{}, content.ErrInvalid
		}
		c.Provenance = map[string]any{}
		if in.Provenance.Producer != nil {
			c.Provenance["producer"] = *in.Provenance.Producer
		}
		if in.Provenance.ProducerVersion != nil {
			c.Provenance["producer_version"] = *in.Provenance.ProducerVersion
		}
		if in.Provenance.SourceBlobIds != nil {
			ids := make([]any, 0, len(*in.Provenance.SourceBlobIds))
			for _, id := range *in.Provenance.SourceBlobIds {
				ids = append(ids, id)
			}
			c.Provenance["source_blob_ids"] = ids
		}
	}
	return c, nil
}

// referenceDecodeIngestCommand mirrors the legacy multi-pass decode + JSON Schema
// validation + commandFromTransport pipeline so equivalence tests can verify
// byte-identical contract verdicts without maintaining a second rule table.
func referenceDecodeIngestCommand(t *testing.T, raw []byte, checkSyntax bool) (content.Command, error) {
	t.Helper()
	contract, err := loadContract()
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		if checkSyntax {
			return content.Command{}, publicerr.MalformedJson
		}
		return content.Command{}, publicerr.InvalidSchema
	}
	if err := dec.Decode(new(any)); err != nil {
		if err.Error() != "EOF" {
			if checkSyntax {
				return content.Command{}, publicerr.MalformedJson
			}
			return content.Command{}, publicerr.InvalidSchema
		}
	} else {
		if checkSyntax {
			return content.Command{}, publicerr.MalformedJson
		}
		return content.Command{}, publicerr.InvalidSchema
	}
	if contract.schemas["IngestCommand"].Validate(parsed) != nil {
		return content.Command{}, publicerr.InvalidSchema
	}
	var wire transport.IngestCommand
	if err := json.Unmarshal(raw, &wire); err != nil {
		return content.Command{}, publicerr.InvalidSchema
	}
	cmd, err := commandFromTransport(wire)
	if err != nil {
		return content.Command{}, publicerr.InvalidSchema
	}
	return cmd, nil
}

// Owns 1-to-1 contract equivalence between the single-pass IngestCommand
// decoder/validator and the authoritative OpenAPI JSON Schema + wire mapping.
func TestDecodeIngestCommandMatchesJSONSchemaAndWireMapping(t *testing.T) {
	validACL64 := make([]string, 64)
	for i := range validACL64 {
		validACL64[i] = "user:member-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
	}
	invalidACL65 := append(append([]string{}, validACL64...), "user:extra")
	acl64JSON, _ := json.Marshal(validACL64)
	acl65JSON, _ := json.Marshal(invalidACL65)

	maxLeft := strings.Repeat("a", 32)
	maxRight := strings.Repeat("b", 128)
	tooLongLeft := strings.Repeat("a", 33)
	tooLongRight := strings.Repeat("b", 129)

	cases := []struct {
		name string
		raw  string
	}{
		// Valid cases covering every IngestCommand branch and optional field.
		{
			name: "inline text minimal",
			raw:  `{"idempotency_key":"k1","source":{"corpus_id":"c1","namespace":"ns","record_key":"r1"},"content":{"kind":"text","text":"hello world"}}`,
		},
		{
			name: "inline text reversed keys and whitespace",
			raw:  " \n\t{ \"content\" : { \"text\" : \"line1\\nline2\\t\\\"quoted\\\"\\u00e9\\uD83D\\uDE00\\u0000\" , \"kind\" : \"text\" } , \"source\" : { \"record_key\" : \"r2\" , \"namespace\" : \"ns\" , \"corpus_id\" : \"c1\" } , \"idempotency_key\" : \"k2\" } \r\n",
		},
		{
			name: "escaped object key",
			raw:  `{"\u0069dempotency_key":"k3","source":{"corpus_id":"c1","namespace":"ns","record_key":"r3"},"content":{"kind":"text","text":"ok"}}`,
		},
		{
			name: "blob content with all optional fields",
			raw:  `{"idempotency_key":"k4","source":{"corpus_id":"c1","namespace":"ns","record_key":"r4"},"source_revision":"rev-1","source_position":"0012345678901234567890","record_acl":["user:alice","group:eng_1.2-3"],"content":{"blob_id":"blob_1","media_type":"text/plain","kind":"blob"},"extensions":{"ext.ns":{"schema_version":"1.0","data":{"flag":true,"count":42,"nested":{"x":[1,"two",null]}}}},"provenance":{"producer":"crawler","producer_version":"2.1","source_blob_ids":["b1","b2"]}}`,
		},
		{
			name: "manifest content with text and blob parts and relations",
			raw:  `{"idempotency_key":"k5","source":{"corpus_id":"c1","namespace":"ns","record_key":"r5"},"content":{"kind":"manifest","parts":[{"key":"p1","role":"body","content":{"kind":"text","text":"part one"}},{"key":"p2","parent_key":"p1","role":"attachment","content":{"kind":"blob","blob_id":"b_2","media_type":"application/pdf"},"extensions":{"ext.part":{"schema_version":"1","data":{}}}}],"relations":[{"type":"replies_to","target":{"corpus_id":"c1","namespace":"ns","record_key":"r0"},"source_target_revision":"rev-0"}]}}`,
		},
		{
			name: "manifest without relations and empty extensions/provenance",
			raw:  `{"idempotency_key":"k6","source":{"corpus_id":"c1","namespace":"ns","record_key":"r6"},"content":{"kind":"manifest","parts":[{"key":"p1","role":"body","content":{"kind":"text","text":"x"}}]},"extensions":{},"provenance":{}}`,
		},
		{
			name: "manifest with empty relations and empty source_blob_ids",
			raw:  `{"idempotency_key":"k7","source":{"corpus_id":"c1","namespace":"ns","record_key":"r7"},"content":{"kind":"manifest","parts":[{"key":"p1","role":"body","content":{"kind":"text","text":"x"}}],"relations":[]},"provenance":{"source_blob_ids":[]}}`,
		},
		{
			name: "record_acl boundary 64 items and 32:128 principal length",
			raw:  `{"idempotency_key":"k8","source":{"corpus_id":"c1","namespace":"ns","record_key":"r8"},"content":{"kind":"text","text":"x"},"record_acl":` + string(acl64JSON) + `}`,
		},
		{
			name: "record_acl exact max left and right lengths",
			raw:  `{"idempotency_key":"k9","source":{"corpus_id":"c1","namespace":"ns","record_key":"r9"},"content":{"kind":"text","text":"x"},"record_acl":["` + maxLeft + `:` + maxRight + `"]}`,
		},

		// Schema violations (must return InvalidSchema).
		{name: "empty object", raw: `{}`},
		{name: "bare array", raw: `[]`},
		{name: "bare number", raw: `42`},
		{name: "bare null", raw: `null`},
		{name: "missing content", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"}}`},
		{name: "missing source", raw: `{"idempotency_key":"k","content":{"kind":"text","text":"x"}}`},
		{name: "missing idempotency_key", raw: `{"source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"}}`},
		{name: "empty idempotency_key", raw: `{"idempotency_key":"","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"}}`},
		{name: "null idempotency_key", raw: `{"idempotency_key":null,"source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"}}`},
		{name: "unknown top-level field", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"extra":1}`},
		{name: "null optional source_revision", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"source_revision":null}`},
		{name: "empty source_revision", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"source_revision":""}`},
		{name: "non-digit source_position", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"source_position":"-1"}`},
		{name: "empty source_position", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"source_position":""}`},
		{name: "source missing record_key", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n"},"content":{"kind":"text","text":"x"}}`},
		{name: "source extra field", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r","x":"y"},"content":{"kind":"text","text":"x"}}`},
		{name: "text empty", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":""}}`},
		{name: "text with extra blob_id", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x","blob_id":"b"}}`},
		{name: "blob missing media_type", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"blob","blob_id":"b"}}`},
		{name: "unknown content kind", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"video","text":"x"}}`},
		{name: "manifest empty parts", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"manifest","parts":[]}}`},
		{name: "manifest nested inside part", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"manifest","parts":[{"key":"p1","role":"body","content":{"kind":"manifest","parts":[{"key":"p2","role":"body","content":{"kind":"text","text":"x"}}]}}]}}`},
		{name: "part missing role", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"manifest","parts":[{"key":"p1","content":{"kind":"text","text":"x"}}]}}`},
		{name: "relation missing target", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"manifest","parts":[{"key":"p1","role":"body","content":{"kind":"text","text":"x"}}],"relations":[{"type":"ref"}]}}`},
		{name: "extensions data is array", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"extensions":{"ns":{"schema_version":"1","data":[]}}}`},
		{name: "extensions data is null", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"extensions":{"ns":{"schema_version":"1","data":null}}}`},
		{name: "extensions missing schema_version", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"extensions":{"ns":{"data":{}}}}`},
		{name: "provenance normalization rejected", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"provenance":{"normalization":{"plugin_id":"p","plugin_version":"1","plugin_api":"0.1","contribution":"normalizer","idempotency_key":"k","input_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}}`},
		{name: "provenance empty source_blob_id element", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"provenance":{"source_blob_ids":[""]}}`},
		{name: "record_acl empty", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"record_acl":[]}`},
		{name: "record_acl duplicate", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"record_acl":["user:a","user:a"]}`},
		{name: "record_acl 65 items", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"record_acl":` + string(acl65JSON) + `}`},
		{name: "record_acl bad principal no colon", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"record_acl":["useronly"]}`},
		{name: "record_acl bad principal two colons", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"record_acl":["a:b:c"]}`},
		{name: "record_acl left too long", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"record_acl":["` + tooLongLeft + `:ok"]}`},
		{name: "record_acl right too long", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"record_acl":["ok:` + tooLongRight + `"]}`},

		// Duplicate top-level and nested command keys (last-value-wins parity
		// with legacy JSON decode: invalid-before-valid and valid-before-invalid
		// across every scalar and object field).
		{
			name: "duplicate idempotency_key invalid before valid",
			raw:  `{"idempotency_key":"","idempotency_key":"k-ok","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"}}`,
		},
		{
			name: "duplicate idempotency_key valid before invalid",
			raw:  `{"idempotency_key":"k-ok","idempotency_key":123,"source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"}}`,
		},
		{
			name: "duplicate source invalid before valid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c"},"source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"}}`,
		},
		{
			name: "duplicate source valid before invalid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"source":{"corpus_id":"c"},"content":{"kind":"text","text":"x"}}`,
		},
		{
			name: "duplicate content invalid before valid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":""},"content":{"kind":"text","text":"ok"}}`,
		},
		{
			name: "duplicate content valid before invalid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"ok"},"content":{"kind":"text","text":42}}`,
		},
		{
			name: "duplicate source_revision invalid before valid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"source_revision":null,"source_revision":"rev-2"}`,
		},
		{
			name: "duplicate source_revision valid before invalid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"source_revision":"rev-2","source_revision":null}`,
		},
		{
			name: "duplicate source_position invalid before valid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"source_position":"bad","source_position":"42"}`,
		},
		{
			name: "duplicate source_position valid before invalid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"source_position":"42","source_position":"bad"}`,
		},
		{
			name: "duplicate record_acl invalid before valid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"record_acl":[],"record_acl":["user:alice"]}`,
		},
		{
			name: "duplicate record_acl valid before invalid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"record_acl":["user:alice"],"record_acl":[]}`,
		},
		{
			name: "duplicate extensions invalid before valid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"extensions":null,"extensions":{"ns":{"schema_version":"1","data":{"ok":true}}}}`,
		},
		{
			name: "duplicate extensions valid before invalid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"extensions":{"ns":{"schema_version":"1","data":{"ok":true}}},"extensions":null}`,
		},
		{
			name: "duplicate provenance invalid before valid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"provenance":null,"provenance":{"producer":"p"}}`,
		},
		{
			name: "duplicate provenance valid before invalid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"provenance":{"producer":"p"},"provenance":123}`,
		},
		{
			name: "duplicate nested source and content fields invalid before valid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"","corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"","text":"ok"}}`,
		},
		{
			name: "duplicate extension namespace invalid before valid",
			raw:  `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"extensions":{"ns":null,"ns":{"schema_version":"1","data":{"a":1}}}}`,
		},

		// Syntax errors (must return MalformedJson when checkSyntax=true, InvalidSchema when false).
		{name: "truncated object", raw: `{"idempotency_key":"k"`},
		{name: "trailing document", raw: `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"}} {}`},
		{name: "schema error before syntax error", raw: `{"unknown":1, broken`},
		{name: "unescaped control char", raw: "{\"idempotency_key\":\"k\",\"source\":{\"corpus_id\":\"c\",\"namespace\":\"n\",\"record_key\":\"r\"},\"content\":{\"kind\":\"text\",\"text\":\"a\nb\"}}"},
	}

	for _, tc := range cases {
		for _, checkSyntax := range []bool{false, true} {
			mode := "batch"
			if checkSyntax {
				mode = "single"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				wantCmd, wantErr := referenceDecodeIngestCommand(t, []byte(tc.raw), checkSyntax)
				gotCmd, gotErr := decodeIngestCommand([]byte(tc.raw), checkSyntax)
				if !errors.Is(gotErr, wantErr) {
					t.Fatalf("err=%v, want %v (raw=%s)", gotErr, wantErr, tc.raw)
				}
				if wantErr == nil && !reflect.DeepEqual(gotCmd, wantCmd) {
					t.Fatalf("cmd mismatch:\ngot:  %#v\nwant: %#v", gotCmd, wantCmd)
				}
			})
		}
	}
}

// Owns consistent last-value-wins duplicate-key resolution even when an earlier
// duplicate carried an incompatible JSON type (and confirms valid-before-invalid
// still rejects cleanly).
func TestDecodeIngestCommandDuplicateKeysConsistentLastValueWins(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr error
		wantKey string
	}{
		{
			name:    "scalar wrong JSON type overridden by valid string",
			raw:     `{"idempotency_key":123,"idempotency_key":"k-final","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"}}`,
			wantKey: "k-final",
		},
		{
			name:    "object wrong JSON type overridden by valid provenance",
			raw:     `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"},"provenance":123,"provenance":{"producer":"p"}}`,
			wantKey: "k",
		},
		{
			name:    "nested scalar wrong JSON types overridden by valid values",
			raw:     `{"idempotency_key":"k","source":{"corpus_id":1,"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":false,"text":"ok"}}`,
			wantKey: "k",
		},
		{
			name:    "manifest content overwritten by inline text clears Manifest",
			raw:     `{"idempotency_key":"k","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"manifest","parts":[{"key":"p1","role":"body","content":{"kind":"text","text":"m"}}]},"content":{"kind":"text","text":"inline"}}`,
			wantKey: "k",
		},
		{
			name:    "valid scalar overwritten by wrong JSON type rejects",
			raw:     `{"idempotency_key":"k-first","idempotency_key":123,"source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"}}`,
			wantErr: publicerr.InvalidSchema,
		},
		{
			name:    "malformed syntax in overridden duplicate still rejects as malformed_json",
			raw:     `{"idempotency_key":broken,"idempotency_key":"k-final","source":{"corpus_id":"c","namespace":"n","record_key":"r"},"content":{"kind":"text","text":"x"}}`,
			wantErr: publicerr.MalformedJson,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := decodeIngestCommand([]byte(tc.raw), true)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil {
				if cmd.Key != tc.wantKey {
					t.Fatalf("Key=%q, want %q", cmd.Key, tc.wantKey)
				}
				if tc.name == "manifest content overwritten by inline text clears Manifest" && (cmd.Manifest != nil || cmd.Content.Kind != "text" || cmd.Content.Text != "inline") {
					t.Fatalf("expected clean inline text command, got content=%+v manifest=%+v", cmd.Content, cmd.Manifest)
				}
			}
		})
	}
}

// Owns zero-copy BatchRequest envelope scanning edge cases (escaped keys,
// duplicate keys, error precedence, and exact raw-byte slicing).
func TestParseBatchEnvelopeEdgeCases(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantErr   error
		wantItems []string
	}{
		{
			name:      "zero-copy trimmed slices",
			raw:       ` { "items" : [  {"a":1} , 42 , "hello" ] } `,
			wantItems: []string{`{"a":1}`, `42`, `"hello"`},
		},
		{
			name:      "escaped items key",
			raw:       `{"\u0069tems":[{"a":1}]}`,
			wantItems: []string{`{"a":1}`},
		},
		{
			name:    "duplicate escaped items key",
			raw:     `{"items":[1],"\u0069tems":[2]}`,
			wantErr: publicerr.MalformedJson,
		},
		{
			name:    "duplicate extra key",
			raw:     `{"a":1,"a":2,"items":[1]}`,
			wantErr: publicerr.MalformedJson,
		},
		{
			name:    "malformed syntax after 101 items keeps malformed_json",
			raw:     `{"items":[` + strings.Repeat("1,", 101) + `]`,
			wantErr: publicerr.MalformedJson,
		},
		{
			name:    "batch_too_large takes precedence over extra key",
			raw:     `{"extra":true,"items":[` + strings.TrimSuffix(strings.Repeat("1,", 101), ",") + `]}`,
			wantErr: publicerr.BatchTooLarge,
		},
		{
			name:    "bare valid non-object is invalid_schema",
			raw:     `[1,2,3]`,
			wantErr: publicerr.InvalidSchema,
		},
		{
			name:    "bare malformed array is malformed_json",
			raw:     `[1,2,`,
			wantErr: publicerr.MalformedJson,
		},
		{
			name:    "deeply nested input rejected cleanly",
			raw:     `{"items":[` + strings.Repeat("[", maxJSONNestingDepth+5) + strings.Repeat("]", maxJSONNestingDepth+5) + `]}`,
			wantErr: publicerr.MalformedJson,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.raw)
			items, err := parseBatchEnvelope(payload)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if len(items) != len(tc.wantItems) {
				t.Fatalf("len(items)=%d, want %d", len(items), len(tc.wantItems))
			}
			for i, want := range tc.wantItems {
				if string(items[i]) != want {
					t.Fatalf("item[%d]=%q, want %q", i, items[i], want)
				}
				// Verify zero-copy slicing directly into payload backing array.
				if idx := bytes.Index(payload, items[i]); idx < 0 || (len(items[i]) > 0 && &items[i][0] != &payload[idx]) {
					t.Fatalf("item[%d] was copied instead of sliced from payload", i)
				}
			}
		})
	}
}
