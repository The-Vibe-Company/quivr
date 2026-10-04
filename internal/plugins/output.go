package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// EngineMaxResponseBytes caps any declared max_response_bytes: the engine never
// reads a larger normalizer response.
const EngineMaxResponseBytes = 16 << 20

// Issue codes for normalizer output judged in its invocation context.
const (
	CodeResponseTooLarge        = "response_too_large"
	CodeTooManyParts            = "too_many_parts"
	CodeForeignBlob             = "foreign_blob"
	CodeUnverifiedBlob          = "unverified_blob"
	CodeUndeclaredNamespace     = "undeclared_namespace"
	CodeUndeclaredSchemaVersion = "undeclared_schema_version"
	CodeInvalidExtension        = "invalid_extension"
)

// MaxResponseBytes is the response bound for a normalizer: the declared
// max_response_bytes (or its default), capped by EngineMaxResponseBytes.
func MaxResponseBytes(m *Manifest) int {
	limit := DefaultMaxResponseBytes
	if m != nil && m.Contributions.Normalizer != nil && m.Contributions.Normalizer.Limits.MaxResponseBytes > 0 {
		limit = m.Contributions.Normalizer.Limits.MaxResponseBytes
	}
	return min(limit, EngineMaxResponseBytes)
}

// InputBlob is the input Blob named by a normalizer request.
type InputBlob struct {
	BlobID    string `json:"blob_id"`
	MediaType string `json:"media_type"`
	SHA256    string `json:"sha256"`
}

// VerifiedBlob is a Blob whose bytes the caller has verified: the engine's Blob
// store, or the file the Contract Runner served.
type VerifiedBlob struct {
	ID, MediaType, SHA256 string
}

// OutputContext is what the engine knows about one invocation when it judges
// the plugin's answer.
type OutputContext struct {
	Manifest *Manifest
	Input    InputBlob
	// VerifyBlob resolves a Blob id to its verified identity. Nil means only
	// the input Blob is known, with the request's media type and checksum.
	VerifyBlob func(ctx context.Context, id string) (VerifiedBlob, error)
}

// InputFromRequest reads the input Blob of a normalizer request body.
func InputFromRequest(request []byte) (InputBlob, error) {
	var r struct {
		Input InputBlob `json:"input"`
	}
	err := json.Unmarshal(request, &r)
	return r.Input, err
}

// Violation is a rejection of plugin output with a stable issue code. Its
// error resolves through Kind (content.ErrInvalid or content.ErrUnsupported)
// like content.ManifestViolation, so engine callers map it unchanged.
type Violation struct {
	Kind   error
	Code   string
	Path   string
	Detail string
}

func (v *Violation) Error() string { return v.Kind.Error() }
func (v *Violation) Unwrap() error { return v.Kind }

// CheckNormalizerOutput judges a 200 normalizer response exactly as the engine
// does before anything is committed: the response bound, the response schema
// (unknown fields are rejected), the declared max_parts, the engine's
// structural Manifest rules (content.CheckManifest), Blob Parts that may
// reference only the verified input Blob, and extensions (top-level and on
// Parts) only in namespaces and schema versions the manifest declares, with
// data valid against the declared schema.
func CheckNormalizerOutput(ctx context.Context, raw []byte, oc OutputContext) []Issue {
	if limit := MaxResponseBytes(oc.Manifest); len(raw) > limit {
		return []Issue{{Code: CodeResponseTooLarge, Message: fmt.Sprintf("the response is %d bytes; the limit is %d (declared max_response_bytes, capped by the engine at %d)", len(raw), limit, EngineMaxResponseBytes)}}
	}
	if issues := ValidateDocument("normalizer-response.schema.json", raw); len(issues) > 0 {
		return issues
	}
	var response struct {
		Manifest   content.Manifest   `json:"manifest"`
		Extensions content.Extensions `json:"extensions"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return []Issue{{Code: CodeSchema, Path: "/manifest", Message: err.Error()}}
	}
	m := &response.Manifest
	if oc.Manifest != nil && oc.Manifest.Contributions.Normalizer != nil {
		if limit := oc.Manifest.Contributions.Normalizer.Limits.MaxParts; limit > 0 && len(m.Parts) > limit {
			return []Issue{{Code: CodeTooManyParts, Path: "/manifest/parts",
				Message: fmt.Sprintf("%d Parts exceed the declared max_parts %d", len(m.Parts), limit)}}
		}
	}
	validator := newDeclaredExtensions(oc.Manifest)
	err := content.CheckManifest(m, func(i int) error {
		p := m.Parts[i]
		prefix := fmt.Sprintf("/manifest/parts/%d", i)
		if p.Content.Kind == "blob" {
			if err := checkBlobPart(ctx, oc, p); err != nil {
				return withPrefix(err, prefix+"/content")
			}
		}
		return withPrefix(content.CheckExtensions(ctx, validator, p.Extensions), prefix+"/extensions")
	})
	if err == nil {
		err = withPrefix(content.CheckExtensions(ctx, validator, response.Extensions), "/extensions")
	}
	if err == nil {
		return nil
	}
	return []Issue{issueFrom(err)}
}

func checkBlobPart(ctx context.Context, oc OutputContext, p content.Part) error {
	ref := p.Content
	if ref.BlobID != oc.Input.BlobID {
		return &Violation{Kind: content.ErrUnverifiedBlob, Code: CodeForeignBlob, Path: "/blob_id",
			Detail: fmt.Sprintf("Part %q references Blob %s; Plugin API 0.1 Blob Parts may reference only the input Blob %s", p.Key, ref.BlobID, oc.Input.BlobID)}
	}
	verified := VerifiedBlob{ID: oc.Input.BlobID, MediaType: oc.Input.MediaType, SHA256: oc.Input.SHA256}
	if oc.VerifyBlob != nil {
		var err error
		if verified, err = oc.VerifyBlob(ctx, ref.BlobID); err != nil {
			return &Violation{Kind: content.ErrUnverifiedBlob, Code: CodeUnverifiedBlob, Path: "/blob_id",
				Detail: fmt.Sprintf("Part %q references the input Blob %s, which cannot be verified: %v", p.Key, ref.BlobID, err)}
		}
	}
	switch {
	case verified.MediaType != ref.MediaType:
		return &Violation{Kind: content.ErrUnverifiedBlob, Code: CodeUnverifiedBlob, Path: "/media_type",
			Detail: fmt.Sprintf("Part %q references the input Blob %s as %s; the verified Blob is %s", p.Key, ref.BlobID, ref.MediaType, verified.MediaType)}
	case verified.SHA256 != oc.Input.SHA256:
		return &Violation{Kind: content.ErrUnverifiedBlob, Code: CodeUnverifiedBlob, Path: "/blob_id",
			Detail: fmt.Sprintf("Part %q references Blob %s whose verified sha256 %s differs from the input sha256 %s", p.Key, ref.BlobID, verified.SHA256, oc.Input.SHA256)}
	}
	return nil
}

func withPrefix(err error, prefix string) error {
	var v *Violation
	if errors.As(err, &v) {
		v.Path = prefix + v.Path
	}
	return err
}

func issueFrom(err error) Issue {
	var v *Violation
	if errors.As(err, &v) {
		return Issue{Code: v.Code, Path: v.Path, Message: v.Detail}
	}
	var mv *content.ManifestViolation
	if errors.As(err, &mv) {
		return Issue{Code: CodeInvalidManifest, Path: "/manifest", Message: mv.Detail + " (engine code " + mv.Error() + ")"}
	}
	return Issue{Code: CodeInvalidManifest, Path: "/manifest", Message: err.Error()}
}

// declaredExtensions validates extensions against the namespaces a plugin
// manifest declares. It implements content.ExtensionValidator, the seam the
// engine uses to register plugin-owned namespaces. Errors are *Violation.
type declaredExtensions struct {
	plugin  string
	schemas map[string]map[string]*jsonschema.Schema
	errs    map[string]error
}

// newDeclaredExtensions compiles the manifest's declared extension schemas. A
// nil manifest declares nothing.
func newDeclaredExtensions(m *Manifest) *declaredExtensions {
	d := &declaredExtensions{schemas: map[string]map[string]*jsonschema.Schema{}, errs: map[string]error{}}
	if m == nil {
		return d
	}
	d.plugin = m.ID
	for namespace, versions := range m.Extensions {
		d.schemas[namespace] = map[string]*jsonschema.Schema{}
		for version, raw := range versions {
			value, err := decodeInstance(raw)
			var schema *jsonschema.Schema
			if err == nil {
				schema, err = compileUserSchema(value)
			}
			if err != nil {
				d.errs[namespace+"\x00"+version] = err
				continue
			}
			d.schemas[namespace][version] = schema
		}
	}
	return d
}

// Validate implements content.ExtensionValidator.
func (d *declaredExtensions) Validate(_ context.Context, exts content.Extensions) error {
	namespaces := make([]string, 0, len(exts))
	for ns := range exts {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	for _, ns := range namespaces {
		ext := exts[ns]
		path := "/" + pointerToken(ns)
		versions, ok := d.schemas[ns]
		if !ok {
			return &Violation{Kind: content.ErrUnsupported, Code: CodeUndeclaredNamespace, Path: path,
				Detail: fmt.Sprintf("extension namespace %q is not declared by plugin %q; declare it under extensions in quivr-plugin.yaml", ns, d.plugin)}
		}
		schema, ok := versions[ext.SchemaVersion]
		if !ok {
			detail := fmt.Sprintf("schema version %q of %q is not declared", ext.SchemaVersion, ns)
			if err := d.errs[ns+"\x00"+ext.SchemaVersion]; err != nil {
				detail = fmt.Sprintf("schema version %q of %q does not compile: %v", ext.SchemaVersion, ns, err)
			}
			return &Violation{Kind: content.ErrUnsupported, Code: CodeUndeclaredSchemaVersion, Path: path + "/schema_version", Detail: detail}
		}
		data, err := json.Marshal(ext.Data)
		if err != nil {
			return &Violation{Kind: content.ErrInvalid, Code: CodeInvalidExtension, Path: path + "/data", Detail: err.Error()}
		}
		if string(data) == "null" {
			data = []byte("{}")
		}
		// The engine stores extension data as JSON text, which cannot hold
		// NUL; like Part text, such output is invalid, not retryable.
		if containsNUL(ext.Data) {
			return &Violation{Kind: content.ErrInvalid, Code: CodeInvalidExtension, Path: path + "/data",
				Detail: fmt.Sprintf("%q data contains a NUL character, which the engine cannot store", ns)}
		}
		instance, err := decodeInstance(data)
		if err == nil {
			err = schema.Validate(instance)
		}
		if err != nil {
			detail := err.Error()
			var verr *jsonschema.ValidationError
			if errors.As(err, &verr) {
				if leaves := leafIssues(verr, CodeInvalidExtension, ""); len(leaves) > 0 {
					detail = fmt.Sprintf("at %s: %s", orRoot(leaves[0].Path), leaves[0].Message)
				}
			}
			return &Violation{Kind: content.ErrInvalid, Code: CodeInvalidExtension, Path: path + "/data",
				Detail: fmt.Sprintf("%q data does not match its declared schema version %q: %s", ns, ext.SchemaVersion, detail)}
		}
	}
	return nil
}

// containsNUL reports whether a decoded JSON value holds a NUL character in a
// string or an object key.
func containsNUL(v any) bool {
	switch v := v.(type) {
	case string:
		return strings.ContainsRune(v, 0)
	case map[string]any:
		for k, e := range v {
			if strings.ContainsRune(k, 0) || containsNUL(e) {
				return true
			}
		}
	case []any:
		for _, e := range v {
			if containsNUL(e) {
				return true
			}
		}
	}
	return false
}

func orRoot(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
