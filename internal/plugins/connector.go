package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// Connector output bounds the schema cannot express. They mirror what the
// core stores: an Acquisition Checkpoint and Connector Health diagnostics. A
// manifest may raise the checkpoint bound up to MaxDeclaredCheckpointBytes
// with limits.max_checkpoint_bytes (since Plugin API 0.3.1).
const (
	DefaultMaxCheckpointBytes  = 64 << 10
	MaxDeclaredCheckpointBytes = 1 << 20
	MaxDiagnosticsBytes        = 16 << 10
)

// Connector error classes (error envelope class, since Plugin API 0.3). They
// map one to one to the core connector error classes.
const (
	ClassAccess    = "access"
	ClassTransient = "transient"
	ClassSource    = "source"
)

// Issue codes for connector requests and output.
const (
	CodeInvalidItem         = "invalid_item"
	CodeDuplicateRecordKey  = "duplicate_record_key"
	CodeBlobPartNotAllowed  = "blob_part_not_allowed"
	CodeTooManyItems        = "too_many_items"
	CodeCheckpointTooLarge  = "checkpoint_too_large"
	CodeDiagnosticsTooLarge = "diagnostics_too_large"
	CodeInvalidNotDue       = "invalid_not_due"
	CodeUnknownKind         = "unknown_kind"
	CodeInvalidConfig       = "invalid_config"
	CodeInvalidCredential   = "invalid_credential"
	CodeWrongErrorClass     = "wrong_error_class"
	// CodeAttachmentsUnsupported: items carry attachments but the manifest
	// declares no contributions.connector.attachments (Plugin API 0.4).
	CodeAttachmentsUnsupported           = "attachments_unsupported"
	CodeSubmissionConcurrencyUnsupported = "submission_concurrency_unsupported"
	CodeAttachmentOnlyUnsupported        = "attachment_only_unsupported"
	// CodeAttachmentTooLarge: an exact attachment size above the effective
	// attachments.max_bytes.
	CodeAttachmentTooLarge = "attachment_too_large"
)

// AttachmentMaxBytes is the effective attachment size cap: the declared
// attachments.max_bytes, capped by the engine's MaxAttachmentBytes. It is 0
// when the manifest declares no attachments.
func AttachmentMaxBytes(m *Manifest) int64 {
	if m == nil || m.Contributions.Connector == nil || m.Contributions.Connector.Attachments == nil {
		return 0
	}
	if limit := m.Contributions.Connector.Attachments.MaxBytes; limit > 0 {
		return min(limit, MaxAttachmentBytes)
	}
	return MaxAttachmentBytes
}

// AttachmentTimeoutMS is the effective deadline of one attachment invocation
// in milliseconds: the declared attachments.timeout_ms, capped by the engine.
func AttachmentTimeoutMS(m *Manifest) int {
	if m == nil || m.Contributions.Connector == nil || m.Contributions.Connector.Attachments == nil || m.Contributions.Connector.Attachments.TimeoutMS <= 0 {
		return DefaultAttachmentTimeoutMS
	}
	return min(m.Contributions.Connector.Attachments.TimeoutMS, DefaultAttachmentTimeoutMS)
}

// ConnectorMaxResponseBytes is the response bound for a connector: the declared
// max_response_bytes (or its default), capped by EngineMaxResponseBytes.
func ConnectorMaxResponseBytes(m *Manifest) int {
	limit := DefaultMaxResponseBytes
	if m != nil && m.Contributions.Connector != nil && m.Contributions.Connector.Limits.MaxResponseBytes > 0 {
		limit = m.Contributions.Connector.Limits.MaxResponseBytes
	}
	return min(limit, EngineMaxResponseBytes)
}

// ConnectorMaxItems is the declared max_items, or its default.
func ConnectorMaxItems(m *Manifest) int {
	if m != nil && m.Contributions.Connector != nil && m.Contributions.Connector.Limits.MaxItems > 0 {
		return m.Contributions.Connector.Limits.MaxItems
	}
	return DefaultMaxItems
}

// ConnectorMaxCheckpointBytes is the declared max_checkpoint_bytes, or its
// default.
func ConnectorMaxCheckpointBytes(m *Manifest) int {
	if m != nil && m.Contributions.Connector != nil && m.Contributions.Connector.Limits.MaxCheckpointBytes > 0 {
		return min(m.Contributions.Connector.Limits.MaxCheckpointBytes, MaxDeclaredCheckpointBytes)
	}
	return DefaultMaxCheckpointBytes
}

// ConnectorItem is one decoded item of a valid fetch response.
type ConnectorItem struct {
	RecordKey      string                `json:"record_key"`
	Revision       string                `json:"revision,omitempty"`
	SourcePosition string                `json:"source_position,omitempty"`
	Content        json.RawMessage       `json:"content,omitempty"`
	Extensions     content.Extensions    `json:"extensions,omitempty"`
	Withdraw       bool                  `json:"withdraw,omitempty"`
	Attachments    []ConnectorAttachment `json:"attachments,omitempty"`
}

// ConnectorAttachment is an attachment descriptor: a binary Part whose bytes
// the core asks for later.
type ConnectorAttachment struct {
	Key        string             `json:"key"`
	ParentKey  string             `json:"parent_key,omitempty"`
	Role       string             `json:"role"`
	MediaType  string             `json:"media_type"`
	SizeBytes  *int64             `json:"size_bytes,omitempty"`
	SHA256     string             `json:"sha256,omitempty"`
	Extensions content.Extensions `json:"extensions,omitempty"`
	Ref        string             `json:"ref"`
}

// ConnectorPage is a decoded valid fetch response.
type ConnectorPage struct {
	SubmissionConcurrency int             `json:"submission_concurrency,omitempty"`
	Items                 []ConnectorItem `json:"items"`
	Checkpoint            json.RawMessage `json:"checkpoint"`
	More                  bool            `json:"more"`
	Reads                 int64           `json:"reads,omitempty"`
	Diagnostics           json.RawMessage `json:"diagnostics,omitempty"`
	Notice                string          `json:"notice,omitempty"`
	NotDue                bool            `json:"not_due,omitempty"`
	// Push is a push kind's report on its push channel (Plugin API 0.5).
	Push *ConnectorPushStatus `json:"push,omitempty"`
}

// CheckConnectorOutput judges a 200 fetch response for the API the peer serves,
// exactly as the engine does
// before it accepts any item: the response bound, the response schema
// (unknown fields are rejected), the declared max_items and
// max_checkpoint_bytes, the diagnostics bound, not_due coherence against the request's checkpoint,
// then per item: exactly one of content and withdraw, unique Record Keys,
// attachments only beside a Manifest, the engine's structural Manifest rules
// with the attachments as Parts, no Blob Parts, and extensions only in
// namespaces and schema versions the manifest declares.
func CheckConnectorOutput(ctx context.Context, raw []byte, requestCheckpoint json.RawMessage, m *Manifest, api string) []Issue {
	if limit := ConnectorMaxResponseBytes(m); len(raw) > limit {
		return []Issue{{Code: CodeResponseTooLarge, Message: fmt.Sprintf("the response is %d bytes; the limit is %d (declared max_response_bytes, capped by the engine at %d)", len(raw), limit, EngineMaxResponseBytes)}}
	}
	if issues := ValidateDocument("connector-fetch-response.schema.json", raw); len(issues) > 0 {
		for index := range issues {
			if issues[index].Path == "/submission_concurrency" {
				issues[index].Message = "submission_concurrency must be an integer from 1 to 32; omit it for serial submission: " + issues[index].Message
			}
		}
		return issues
	}
	var page ConnectorPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return []Issue{{Code: CodeSchema, Message: err.Error()}}
	}
	var issues []Issue
	if page.SubmissionConcurrency != 0 && !ResolveAPI(api).Speaks(FeatureConnectorSubmissionConcurrency) {
		issues = append(issues, Issue{Code: CodeSubmissionConcurrencyUnsupported, Path: "/submission_concurrency", Message: "submission_concurrency requires Plugin API " + FeatureSince(FeatureConnectorSubmissionConcurrency)})
	}
	if limit := ConnectorMaxItems(m); len(page.Items) > limit {
		issues = append(issues, Issue{Code: CodeTooManyItems, Path: "/items",
			Message: fmt.Sprintf("%d items exceed the declared max_items %d; answer more: true and return the rest on the next page", len(page.Items), limit)})
	}
	if limit, n := ConnectorMaxCheckpointBytes(m), compactLen(page.Checkpoint); n > limit {
		issues = append(issues, Issue{Code: CodeCheckpointTooLarge, Path: "/checkpoint",
			Message: fmt.Sprintf("the checkpoint serializes to %d bytes; the limit is %d (declared max_checkpoint_bytes, at most %d)", n, limit, MaxDeclaredCheckpointBytes)})
	}
	if n := compactLen(page.Diagnostics); n > MaxDiagnosticsBytes {
		issues = append(issues, Issue{Code: CodeDiagnosticsTooLarge, Path: "/diagnostics",
			Message: fmt.Sprintf("diagnostics serialize to %d bytes; the core stores at most %d", n, MaxDiagnosticsBytes)})
	}
	if page.NotDue && (len(page.Items) > 0 || page.More || !SameJSON(page.Checkpoint, requestCheckpoint)) {
		issues = append(issues, Issue{Code: CodeInvalidNotDue, Path: "/not_due",
			Message: "not_due: true skips the run: answer no items, more: false and the request's checkpoint unchanged"})
	}
	for index, item := range page.Items {
		var manifest content.Manifest
		if len(item.Attachments) > 0 && json.Unmarshal(item.Content, &manifest) == nil && manifest.Kind == "manifest" && len(manifest.Parts) == 0 && !ResolveAPI(api).Speaks(FeatureConnectorAttachmentOnly) {
			issues = append(issues, Issue{Code: CodeAttachmentOnlyUnsupported, Path: fmt.Sprintf("/items/%d/content/parts", index), Message: "attachment-only Manifests require Plugin API " + FeatureSince(FeatureConnectorAttachmentOnly)})
		}
	}
	issues = append(issues, pushStatusIssues(page.Push, m)...)
	return append(issues, checkItems(ctx, page.Items, m)...)
}

// checkItems judges the items of a fetch page or a delivery: unique Record
// Keys, attachments only when the manifest declares them and within their
// size cap, then each item's content and extensions.
func checkItems(ctx context.Context, items []ConnectorItem, m *Manifest) []Issue {
	var issues []Issue
	validator := newDeclaredExtensions(m)
	seen := map[string]int{}
	for i, item := range items {
		path := fmt.Sprintf("/items/%d", i)
		if first, dup := seen[item.RecordKey]; dup {
			issues = append(issues, Issue{Code: CodeDuplicateRecordKey, Path: path + "/record_key",
				Message: fmt.Sprintf("Record Key %q is also item %d of this page; return each item once per page", item.RecordKey, first)})
			continue
		}
		seen[item.RecordKey] = i
		if len(item.Attachments) > 0 && !item.Withdraw {
			if limit := AttachmentMaxBytes(m); limit == 0 {
				issues = append(issues, Issue{Code: CodeAttachmentsUnsupported, Path: path + "/attachments",
					Message: fmt.Sprintf("item %q carries attachments, which need contributions.connector.attachments in the manifest (Plugin API 0.4)", item.RecordKey)})
				continue
			} else if issue := attachmentSizeIssue(item.Attachments, limit); issue != nil {
				issue.Path = path + issue.Path
				issues = append(issues, *issue)
				continue
			}
		}
		if issue := checkConnectorItem(ctx, item, validator); issue != nil {
			issue.Path = path + issue.Path
			issues = append(issues, *issue)
		}
	}
	return issues
}

func checkConnectorItem(ctx context.Context, item ConnectorItem, validator *declaredExtensions) *Issue {
	hasContent := len(item.Content) > 0 && string(item.Content) != "null"
	switch {
	case item.Withdraw && hasContent:
		return &Issue{Code: CodeInvalidItem, Message: fmt.Sprintf("item %q has content and withdraw: true; a withdrawal carries no content", item.RecordKey)}
	case item.Withdraw && (len(item.Attachments) > 0 || len(item.Extensions) > 0):
		return &Issue{Code: CodeInvalidItem, Message: fmt.Sprintf("item %q is withdrawn and carries attachments or extensions; a withdrawal carries only its Record Key", item.RecordKey)}
	case item.Withdraw:
		return nil
	case !hasContent:
		return &Issue{Code: CodeInvalidItem, Message: fmt.Sprintf("item %q has neither content nor withdraw: true", item.RecordKey)}
	}
	var text content.Text
	if err := json.Unmarshal(item.Content, &text); err != nil {
		return &Issue{Code: CodeSchema, Path: "/content", Message: err.Error()}
	}
	if text.Kind == "text" {
		if len(item.Attachments) > 0 {
			return &Issue{Code: CodeInvalidItem, Path: "/attachments",
				Message: fmt.Sprintf("item %q has attachments beside text content; attachments are Parts of a Manifest, so answer content kind manifest", item.RecordKey)}
		}
		if !content.ValidText(text.Text) {
			return &Issue{Code: CodeInvalidItem, Path: "/content/text", Message: fmt.Sprintf("item %q text must be valid UTF-8 without NUL", item.RecordKey)}
		}
	} else {
		var manifest content.Manifest
		if err := json.Unmarshal(item.Content, &manifest); err != nil {
			return &Issue{Code: CodeSchema, Path: "/content", Message: err.Error()}
		}
		if len(manifest.Parts) == 0 && len(item.Attachments) > 0 {
			at := item.Attachments[0]
			if len(item.Attachments) != 1 || len(manifest.Relations) != 0 || at.Key != "source" || at.Role != "source" || at.ParentKey != "" || len(at.Extensions) != 0 {
				return &Issue{Code: CodeInvalidItem, Path: "/attachments", Message: "attachment-only input requires exactly one attachment with key and role source, no parent or Part extensions, and no relations"}
			}
		}
		declared := len(manifest.Parts)
		// The core appends each attachment as a Blob Part once its bytes are
		// stored, so the combined Manifest must hold: unique keys, known and
		// acyclic parents, the Part count bound.
		for _, at := range item.Attachments {
			manifest.Parts = append(manifest.Parts, content.Part{Key: at.Key, ParentKey: at.ParentKey, Role: at.Role,
				Content: content.Text{Kind: "blob", BlobID: "attachment", MediaType: at.MediaType}, Extensions: at.Extensions})
		}
		err := content.CheckManifest(&manifest, func(i int) error {
			p := manifest.Parts[i]
			prefix := fmt.Sprintf("/content/parts/%d", i)
			if i >= declared {
				prefix = fmt.Sprintf("/attachments/%d", i-declared)
			} else if p.Content.Kind == "blob" {
				return &Violation{Kind: content.ErrUnsupported, Code: CodeBlobPartNotAllowed, Path: prefix + "/content",
					Detail: fmt.Sprintf("Part %q is a Blob Part; a connector has no Blob to reference, so return binary Parts as attachments", p.Key)}
			}
			return withPrefix(content.CheckExtensions(ctx, validator, p.Extensions), prefix+"/extensions")
		})
		if err != nil {
			issue := issueFrom(err)
			if issue.Path == "/manifest" {
				issue.Path = "/content"
			}
			return &issue
		}
	}
	if err := withPrefix(content.CheckExtensions(ctx, validator, item.Extensions), "/extensions"); err != nil {
		issue := issueFrom(err)
		return &issue
	}
	return nil
}

func attachmentSizeIssue(attachments []ConnectorAttachment, limit int64) *Issue {
	for i, at := range attachments {
		if at.SHA256 != "" && at.SizeBytes != nil && *at.SizeBytes > limit {
			return &Issue{Code: CodeAttachmentTooLarge, Path: fmt.Sprintf("/attachments/%d/size_bytes", i),
				Message: fmt.Sprintf("attachment %q is %d bytes; attachments.max_bytes allows %d, so leave it out of the item", at.Key, *at.SizeBytes, limit)}
		}
	}
	return nil
}

// AttachmentAnswer is a decoded valid describe_attachment response.
type AttachmentAnswer struct {
	SizeBytes      int64              `json:"size_bytes,omitempty"`
	SHA256         string             `json:"sha256,omitempty"`
	Skip           string             `json:"skip,omitempty"`
	ItemExtensions content.Extensions `json:"item_extensions,omitempty"`
}

// CheckAttachmentAnswer judges a 200 describe_attachment response exactly as
// the engine does before it issues an upload grant: the response schema, the
// effective attachments.max_bytes, and item_extensions only in namespaces
// and schema versions the manifest declares.
func CheckAttachmentAnswer(ctx context.Context, raw []byte, m *Manifest) []Issue {
	if len(raw) > EngineMaxResponseBytes {
		return []Issue{{Code: CodeResponseTooLarge, Message: fmt.Sprintf("the response is %d bytes; the limit is %d", len(raw), EngineMaxResponseBytes)}}
	}
	if issues := ValidateDocument("connector-describe-attachment-response.schema.json", raw); len(issues) > 0 {
		return issues
	}
	var answer AttachmentAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		return []Issue{{Code: CodeSchema, Message: err.Error()}}
	}
	if limit := AttachmentMaxBytes(m); answer.Skip == "" && answer.SizeBytes > limit {
		return []Issue{{Code: CodeAttachmentTooLarge, Path: "/size_bytes",
			Message: fmt.Sprintf("the attachment is %d bytes; attachments.max_bytes allows %d, so answer {\"skip\": \"too_large\"}", answer.SizeBytes, limit)}}
	}
	if err := withPrefix(content.CheckExtensions(ctx, newDeclaredExtensions(m), answer.ItemExtensions), "/item_extensions"); err != nil {
		return []Issue{issueFrom(err)}
	}
	return nil
}

// CheckUploadAnswer judges a 200 upload_attachment response.
func CheckUploadAnswer(raw []byte) []Issue {
	return ValidateDocument("connector-upload-attachment-response.schema.json", raw)
}

// CheckCredentialOutput judges a 200 check_credential response.
func CheckCredentialOutput(raw []byte) []Issue {
	return ValidateDocument("connector-check-credential-response.schema.json", raw)
}

// ConnectorErrorIssues judges a connector error envelope: a connector error
// carries a class, and retryable agrees with it (transient is retryable;
// access and source are terminal). The core maps the class to Connector
// Health, so a missing or contradictory class cannot be interpreted.
func ConnectorErrorIssues(class string, retryable bool) []Issue {
	switch class {
	case "":
		return []Issue{{Code: CodeWrongErrorClass, Path: "/error_class",
			Message: "a connector error envelope carries error_class access, transient or source, which the core maps to Connector Health"}}
	case ClassTransient:
		if !retryable {
			return []Issue{{Code: CodeWrongErrorClass, Path: "/retryable", Message: "class transient is retryable: answer retryable true"}}
		}
	case ClassAccess, ClassSource:
		if retryable {
			return []Issue{{Code: CodeWrongErrorClass, Path: "/retryable", Message: fmt.Sprintf("class %s is terminal until the source or the operator changes something: answer retryable false", class)}}
		}
	}
	return nil
}

// ValidateConnectorInstance validates a Connector Instance configuration and
// credential (JSON objects, the credential possibly null) against the schemas
// the manifest declares for kind, as the core does before it invokes the
// plugin. Issue paths start with /connector/config or /credential.
func ValidateConnectorInstance(m *Manifest, kind string, config, credential []byte) []Issue {
	var c *Connector
	if m != nil {
		c = m.Contributions.Connector
	}
	if c == nil {
		return []Issue{{Code: CodeInvalidManifest, Path: "/contributions/connector", Message: "the manifest declares no connector Contribution"}}
	}
	declared, ok := c.Kinds[kind]
	if !ok {
		return []Issue{{Code: CodeUnknownKind, Path: "/connector/kind", Message: fmt.Sprintf("kind %q is not declared under contributions.connector.kinds", kind)}}
	}
	issues := validateObject("connector/config", CodeInvalidConfig, config, declared.ConfigSchema)
	nullCredential := len(credential) == 0 || string(credential) == "null"
	switch {
	case declared.CredentialSchema == nil && !nullCredential:
		issues = append(issues, Issue{Code: CodeInvalidCredential, Path: "/credential", Message: fmt.Sprintf("kind %q takes no credential; send null", kind)})
	case declared.NeedsCredential() && nullCredential:
		issues = append(issues, Issue{Code: CodeInvalidCredential, Path: "/credential", Message: fmt.Sprintf("kind %q needs a credential", kind)})
	case declared.CredentialSchema != nil && !nullCredential:
		issues = append(issues, validateObject("credential", CodeInvalidCredential, credential, declared.CredentialSchema)...)
	}
	return issues
}

// ValidateConnectorAPIBody checks a parsed route delivery against its declared
// request schema. A nil schema accepts any JSON value; external references are
// forbidden, as for other plugin-declared schemas. Issue paths start at /body.
func ValidateConnectorAPIBody(requestSchema, body []byte) []Issue {
	instance, err := decodeInstance(body)
	if err == nil && len(requestSchema) > 0 {
		var value any
		value, err = decodeInstance(requestSchema)
		if err == nil {
			if schema, compileErr := compileUserSchema(value); compileErr != nil {
				err = compileErr
			} else {
				err = schema.Validate(instance)
			}
		}
	}
	if err != nil {
		return []Issue{{Code: CodeInvalidConfig, Path: "/body", Message: err.Error()}}
	}
	return nil
}

// SameJSON reports whether two JSON documents are equal values; empty is null.
func SameJSON(a, b json.RawMessage) bool {
	var av, bv any
	if len(a) > 0 && json.Unmarshal(a, &av) != nil {
		return false
	}
	if len(b) > 0 && json.Unmarshal(b, &bv) != nil {
		return false
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}

func compactLen(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return len(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return len(raw)
	}
	return len(b)
}

// MinSecretLength is the shortest credential string looked for in plugin
// answers and output: shorter strings (a region, a flag) are not secrets and
// would match by chance.
const MinSecretLength = 8

// CredentialSecrets lists the string leaves of a credential JSON document of
// at least MinSecretLength characters.
func CredentialSecrets(credential json.RawMessage) []string {
	var v any
	if len(credential) == 0 || json.Unmarshal(credential, &v) != nil {
		return nil
	}
	seen := map[string]bool{}
	collectSecrets(v, seen)
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out
}

// ContainsSecret reports whether body holds one of the secrets, raw or JSON
// escaped. An empty string is in every body and holds no secret.
func ContainsSecret(body []byte, secrets []string) bool {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if bytes.Contains(body, []byte(secret)) || bytes.Contains(body, jsonEscaped(secret)) {
			return true
		}
	}
	return false
}

func collectSecrets(v any, into map[string]bool) {
	switch v := v.(type) {
	case string:
		if len(v) >= MinSecretLength {
			into[v] = true
		}
	case map[string]any:
		for _, e := range v {
			collectSecrets(e, into)
		}
	case []any:
		for _, e := range v {
			collectSecrets(e, into)
		}
	}
}

func jsonEscaped(s string) []byte {
	b, _ := json.Marshal(s)
	return bytes.Trim(b, `"`)
}
