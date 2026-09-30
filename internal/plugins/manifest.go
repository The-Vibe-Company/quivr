// Package plugins validates Plugin Protocol v0 documents: the plugin manifest
// (quivr-plugin.yaml) and the protocol payloads defined by the JSON Schemas in
// contracts/plugins/v0. It performs no network or engine I/O.
package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PluginAPIVersion is the Plugin API this engine implements.
const PluginAPIVersion = "0.8.0"

// SupportedPluginAPIVersions are the Plugin API versions this engine serves,
// oldest first. A minor version only adds to the previous one, so a plugin
// built for Plugin API 0.1 keeps working unchanged: a manifest is compatible
// when its plugin_api range admits any of these versions.
var SupportedPluginAPIVersions = []string{"0.1.0", "0.2.0", "0.3.0", "0.3.1", "0.4.0", "0.5.0", "0.6.0", "0.7.0", "0.8.0"}

// ContributionSince is the Plugin API version that introduced each accepted
// Contribution. A manifest that declares one needs a plugin_api range that
// admits that version or a later supported one.
var ContributionSince = map[string]string{"normalizer": "0.1.0", "subscription": "0.2.0", "connector": "0.3.0", "ingestion": "0.6.0", "retrieval": RetrievalSince}

// RetrievalSince is the Plugin API version that introduced the retrieval
// Contribution.
const RetrievalSince = "0.7.0"

// FieldSince is the Plugin API version that introduced a manifest field
// inside a Contribution (a JSON Pointer). A manifest that declares it needs a
// plugin_api range that admits that version or a later supported one.
var FieldSince = map[string]string{"/contributions/connector/attachments": "0.4.0"}

// PushSince is the Plugin API version that introduced the connector push
// mode: the core relays webhook deliveries to receive.
const PushSince = "0.5.0"

// SegmentOnlySince is the Plugin API version that lets segment_and_embed ask
// for no space: the segments only, without vectors. The core then segments a
// Version before, and independently of, embedding it.
const SegmentOnlySince = "0.8.0"

// EngineVersion is the engine version plugins declare compatibility with.
// Release builds may override it:
//
//	go build -ldflags "-X github.com/The-Vibe-Company/quivr-v2/internal/plugins.EngineVersion=0.1.1"
var EngineVersion = "0.1.0"

// ManifestFile is the plugin manifest file name inside a plugin directory.
const ManifestFile = "quivr-plugin.yaml"

// ReservedContributions are Contribution names kept for later Plugin API
// versions; Plugin API 0.3 rejects them.
var ReservedContributions = []string{"enricher", "validator", "projector", "retriever"}

// reservedFields are manifest fields (JSON Pointers) kept for a later Plugin
// API version.
var reservedFields = []struct{ path, message string }{
	{"/contributions/subscription/vectors", "Part and query vectors for subscription rules are reserved for a later Plugin API version; remove vectors"},
}

// Effective defaults applied when a manifest omits the field.
const (
	DefaultTimeoutMS        = 30000
	DefaultMaxAttempts      = 3
	DefaultMaxResponseBytes = 4 << 20
	DefaultMaxParts         = 256
	DefaultMaxBatchSize     = 32
	DefaultMaxItems         = 100
	// MaxAttachmentBytes is the engine's cap on one connector attachment and
	// the default of attachments.max_bytes.
	MaxAttachmentBytes int64 = 25 << 20
	// DefaultAttachmentTimeoutMS is the default and the engine cap of
	// attachments.timeout_ms.
	DefaultAttachmentTimeoutMS = 120000
	// DefaultQueryTimeoutMS is the default deadline of one embed_query.
	DefaultQueryTimeoutMS = 2000
	// DefaultMaxSegments is the default segment bound of one Record Version.
	DefaultMaxSegments = 256
)

// Stable issue codes.
const (
	CodeUnreadable              = "unreadable_manifest"
	CodeInvalidYAML             = "invalid_yaml"
	CodeSchema                  = "schema_violation"
	CodeInvalidRange            = "invalid_range"
	CodeIncompatibleEngine      = "incompatible_engine"
	CodeIncompatiblePluginAPI   = "incompatible_plugin_api"
	CodeReservedContribution    = "reserved_contribution"
	CodeForeignNamespace        = "foreign_namespace"
	CodeInvalidConfigSchema     = "invalid_config_schema"
	CodeInvalidExtensionSchema  = "invalid_extension_schema"
	CodeDuplicateSecret         = "duplicate_secret"
	CodeInvalidManifest         = "invalid_manifest"
	CodeInvalidConfiguration    = "invalid_configuration"
	CodeInvalidExpressionSchema = "invalid_expression_schema"
	CodeReservedField           = "reserved_field"
	CodeInvalidCredentialSchema = "invalid_credential_schema"
	CodeInvalidModes            = "invalid_modes"
	CodeForeignSpace            = "foreign_space"
)

// Issue is one actionable validation failure. Path is a JSON Pointer into the
// validated document.
type Issue struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Manifest is the effective plugin manifest: declared values plus defaults.
type Manifest struct {
	ID            string                                `json:"id"`
	Version       string                                `json:"version"`
	Description   string                                `json:"description,omitempty"`
	Compatibility Compatibility                         `json:"compatibility"`
	Contributions Contributions                         `json:"contributions"`
	Configuration *Configuration                        `json:"configuration,omitempty"`
	Secrets       []Secret                              `json:"secrets,omitempty"`
	Extensions    map[string]map[string]json.RawMessage `json:"extensions,omitempty"`
	Run           *Run                                  `json:"run,omitempty"`
}

type Compatibility struct {
	Engine    string `json:"engine"`
	PluginAPI string `json:"plugin_api"`
}

type Contributions struct {
	Normalizer   *Normalizer   `json:"normalizer,omitempty"`
	Subscription *Subscription `json:"subscription,omitempty"`
	Connector    *Connector    `json:"connector,omitempty"`
	Ingestion    *Ingestion    `json:"ingestion,omitempty"`
	Retrieval    *Retrieval    `json:"retrieval,omitempty"`
}

// Names lists the declared Contributions in protocol order, as discovery
// lists them.
func (c Contributions) Names() []string {
	names := []string{}
	if c.Normalizer != nil {
		names = append(names, "normalizer")
	}
	if c.Subscription != nil {
		names = append(names, "subscription")
	}
	if c.Connector != nil {
		names = append(names, "connector")
	}
	if c.Ingestion != nil {
		names = append(names, "ingestion")
	}
	if c.Retrieval != nil {
		names = append(names, "retrieval")
	}
	return names
}

// Subscription is an alert rule (Plugin API 0.2): it decides whether one
// Record Version matches each Saved Query expression of a batch.
type Subscription struct {
	// ExpressionSchema is the JSON Schema of the Saved Query expression.
	ExpressionSchema json.RawMessage `json:"expression_schema"`
	// ConfigurationSchema is the JSON Schema of the per-Subscription
	// evaluator configuration; nil accepts any object.
	ConfigurationSchema json.RawMessage    `json:"configuration_schema,omitempty"`
	MaxBatchSize        int                `json:"max_batch_size"`
	TimeoutMS           int                `json:"timeout_ms"`
	Retry               Retry              `json:"retry"`
	Limits              SubscriptionLimits `json:"limits"`
}

// Ingestion segments and embeds Record Versions (Plugin API 0.6): it cuts the
// text Parts of one Version into segments, embeds each in the vector spaces
// the plugin owns, and encodes queries into one of them.
type Ingestion struct {
	// Spaces are the vector spaces the plugin owns, by space id.
	Spaces         map[string]VectorSpace `json:"spaces"`
	TimeoutMS      int                    `json:"timeout_ms"`
	QueryTimeoutMS int                    `json:"query_timeout_ms"`
	Limits         IngestionLimits        `json:"limits"`
}

// VectorSpace is one declared vector space. Its identity is the id and the
// version together (SpaceKey).
type VectorSpace struct {
	Version         string   `json:"version"`
	Model           string   `json:"model"`
	Dimensions      int      `json:"dimensions"`
	Metric          string   `json:"metric"`
	Indexes         []string `json:"indexes"`
	QueryModalities []string `json:"query_modalities"`
	Description     string   `json:"description,omitempty"`
}

// IngestionLimits bound one segment_and_embed answer.
type IngestionLimits struct {
	MaxSegments      int `json:"max_segments"`
	MaxResponseBytes int `json:"max_response_bytes"`
}

// Connector is a source collector (Plugin API 0.3): it fetches pages of new or
// changed items after an opaque checkpoint, for one or more connector kinds.
type Connector struct {
	Kinds     map[string]ConnectorKind `json:"kinds"`
	TimeoutMS int                      `json:"timeout_ms"`
	Limits    ConnectorLimits          `json:"limits"`
	// Attachments, since Plugin API 0.4, lets items carry attachments whose
	// bytes the plugin uploads through core-issued grants; nil refuses them.
	Attachments *ConnectorAttachments `json:"attachments,omitempty"`
}

// ConnectorAttachments bounds the attachment exchange (Plugin API 0.4).
type ConnectorAttachments struct {
	// MaxBytes lowers the engine's attachment size cap.
	MaxBytes int64 `json:"max_bytes"`
	// TimeoutMS is the deadline of one describe_attachment or
	// upload_attachment invocation.
	TimeoutMS int `json:"timeout_ms"`
}

// ConnectorKind is one connector kind a plugin provides.
type ConnectorKind struct {
	Description string `json:"description,omitempty"`
	// ConfigSchema is the JSON Schema of a Connector Instance configuration.
	ConfigSchema json.RawMessage `json:"config_schema"`
	// CredentialSchema is the JSON Schema of the Deposited Credential; nil
	// means the kind takes no credential.
	CredentialSchema json.RawMessage `json:"credential_schema,omitempty"`
	// CredentialRequired false makes the credential optional (default true).
	CredentialRequired     *bool    `json:"credential_required,omitempty"`
	DefaultIntervalSeconds int      `json:"default_interval_seconds"`
	Modes                  []string `json:"modes"`
}

// NeedsCredential reports whether an instance of the kind runs only with a
// Deposited Credential: it declares one and does not make it optional.
func (k ConnectorKind) NeedsCredential() bool {
	return k.CredentialSchema != nil && (k.CredentialRequired == nil || *k.CredentialRequired)
}

type ConnectorLimits struct {
	MaxResponseBytes int `json:"max_response_bytes"`
	MaxItems         int `json:"max_items"`
	// MaxCheckpointBytes bounds the compact JSON checkpoint (since Plugin API
	// 0.3.1).
	MaxCheckpointBytes int `json:"max_checkpoint_bytes"`
}

type SubscriptionLimits struct {
	MaxResponseBytes int `json:"max_response_bytes"`
}

type Normalizer struct {
	MediaTypes []string `json:"media_types"`
	TimeoutMS  int      `json:"timeout_ms"`
	Retry      Retry    `json:"retry"`
	Limits     Limits   `json:"limits"`
}

type Retry struct {
	MaxAttempts int `json:"max_attempts"`
}

type Limits struct {
	MaxResponseBytes int `json:"max_response_bytes"`
	MaxParts         int `json:"max_parts"`
}

type Configuration struct {
	Schema json.RawMessage `json:"schema"`
}

type Secret struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    *bool  `json:"required,omitempty"`
}

type Run struct {
	Command []string `json:"command"`
}

// RangeCheck reports a declared range against the version this engine offers.
type RangeCheck struct {
	Range      string `json:"range"`
	Version    string `json:"version"`
	Compatible bool   `json:"compatible"`
}

type CompatibilityReport struct {
	Engine    *RangeCheck `json:"engine,omitempty"`
	PluginAPI *RangeCheck `json:"plugin_api,omitempty"`
}

// Report is the result of inspecting one manifest.
type Report struct {
	Valid            bool                `json:"valid"`
	Path             string              `json:"path,omitempty"`
	ManifestDigest   string              `json:"manifest_digest,omitempty"`
	EngineVersion    string              `json:"engine_version"`
	PluginAPIVersion string              `json:"plugin_api_version"`
	Compatibility    CompatibilityReport `json:"compatibility"`
	Manifest         *Manifest           `json:"manifest,omitempty"`
	Errors           []Issue             `json:"errors"`
}

// Inspect reads a manifest file, or quivr-plugin.yaml inside a directory, and
// validates it.
func Inspect(target string) Report {
	path := target
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		path = filepath.Join(target, ManifestFile)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return finish(Report{Path: path, Errors: []Issue{{Code: CodeUnreadable, Message: fmt.Sprintf("cannot read %s: %v", path, err)}}})
	}
	report := Validate(raw)
	report.Path = path
	return report
}

// Validate checks manifest bytes: YAML syntax, the manifest JSON Schema, then
// the semantic rules the schema cannot express.
func Validate(raw []byte) Report {
	sum := sha256.Sum256(raw)
	report := Report{ManifestDigest: "sha256:" + hex.EncodeToString(sum[:])}
	var parsed any
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		report.Errors = []Issue{{Code: CodeInvalidYAML, Message: err.Error()}}
		return finish(report)
	}
	doc, err := toJSON(parsed)
	if err != nil {
		report.Errors = []Issue{{Code: CodeInvalidYAML, Message: err.Error()}}
		return finish(report)
	}
	instance, err := decodeInstance(doc)
	if err != nil {
		report.Errors = []Issue{{Code: CodeInvalidYAML, Message: err.Error()}}
		return finish(report)
	}
	semantic := checkManifest(instance, &report.Compatibility)
	schemaIssues, err := validateAgainst("plugin-manifest.schema.json", instance)
	if err != nil {
		report.Errors = []Issue{{Code: CodeSchema, Message: err.Error()}}
		return finish(report)
	}
	report.Errors = append(semantic, withoutCovered(schemaIssues, semantic)...)
	if len(schemaIssues) == 0 {
		var m Manifest
		if err := json.Unmarshal(doc, &m); err == nil {
			applyDefaults(&m)
			report.Manifest = &m
		}
	}
	return finish(report)
}

func finish(r Report) Report {
	r.EngineVersion = EngineVersion
	r.PluginAPIVersion = PluginAPIVersion
	if r.Errors == nil {
		r.Errors = []Issue{}
	}
	sort.SliceStable(r.Errors, func(i, j int) bool {
		if r.Errors[i].Path != r.Errors[j].Path {
			return r.Errors[i].Path < r.Errors[j].Path
		}
		return r.Errors[i].Code < r.Errors[j].Code
	})
	r.Valid = len(r.Errors) == 0
	return r
}

func applyDefaults(m *Manifest) {
	if n := m.Contributions.Normalizer; n != nil {
		if n.TimeoutMS == 0 {
			n.TimeoutMS = DefaultTimeoutMS
		}
		if n.Retry.MaxAttempts == 0 {
			n.Retry.MaxAttempts = DefaultMaxAttempts
		}
		if n.Limits.MaxResponseBytes == 0 {
			n.Limits.MaxResponseBytes = DefaultMaxResponseBytes
		}
		if n.Limits.MaxParts == 0 {
			n.Limits.MaxParts = DefaultMaxParts
		}
	}
	if s := m.Contributions.Subscription; s != nil {
		if s.MaxBatchSize == 0 {
			s.MaxBatchSize = DefaultMaxBatchSize
		}
		if s.TimeoutMS == 0 {
			s.TimeoutMS = DefaultTimeoutMS
		}
		if s.Retry.MaxAttempts == 0 {
			s.Retry.MaxAttempts = DefaultMaxAttempts
		}
		if s.Limits.MaxResponseBytes == 0 {
			s.Limits.MaxResponseBytes = DefaultMaxResponseBytes
		}
	}
	if c := m.Contributions.Connector; c != nil {
		if c.TimeoutMS == 0 {
			c.TimeoutMS = DefaultTimeoutMS
		}
		if c.Limits.MaxResponseBytes == 0 {
			c.Limits.MaxResponseBytes = DefaultMaxResponseBytes
		}
		if c.Limits.MaxItems == 0 {
			c.Limits.MaxItems = DefaultMaxItems
		}
		if c.Limits.MaxCheckpointBytes == 0 {
			c.Limits.MaxCheckpointBytes = DefaultMaxCheckpointBytes
		}
		if a := c.Attachments; a != nil {
			if a.MaxBytes == 0 {
				a.MaxBytes = MaxAttachmentBytes
			}
			if a.TimeoutMS == 0 {
				a.TimeoutMS = DefaultAttachmentTimeoutMS
			}
		}
		for name, kind := range c.Kinds {
			if len(kind.Modes) == 0 {
				kind.Modes = []string{"pull"}
				c.Kinds[name] = kind
			}
		}
	}
	if in := m.Contributions.Ingestion; in != nil {
		if in.TimeoutMS == 0 {
			in.TimeoutMS = DefaultTimeoutMS
		}
		if in.QueryTimeoutMS == 0 {
			in.QueryTimeoutMS = DefaultQueryTimeoutMS
		}
		if in.Limits.MaxSegments == 0 {
			in.Limits.MaxSegments = DefaultMaxSegments
		}
		if in.Limits.MaxResponseBytes == 0 {
			in.Limits.MaxResponseBytes = EngineMaxResponseBytes
		}
	}
	if r := m.Contributions.Retrieval; r != nil {
		if r.Limits.MaxRounds == 0 {
			r.Limits.MaxRounds = MaxRetrievalRounds
		}
		if r.Limits.MaxRequests == 0 {
			r.Limits.MaxRequests = DefaultMaxRetrievalRequests
		}
		if r.Limits.MaxCandidates == 0 {
			r.Limits.MaxCandidates = DefaultMaxRetrievalCandidates
		}
		if r.Limits.MaxResponseBytes == 0 {
			r.Limits.MaxResponseBytes = DefaultRetrievalMaxResponseBytes
		}
	}
	for i := range m.Secrets {
		if m.Secrets[i].Required == nil {
			required := true
			m.Secrets[i].Required = &required
		}
	}
}

// withoutCovered drops schema issues located at or below a path that already
// carries a targeted semantic issue, so each problem is reported once.
func withoutCovered(schema, semantic []Issue) []Issue {
	var out []Issue
	for _, issue := range schema {
		covered := false
		for _, s := range semantic {
			if issue.Path == s.Path || strings.HasPrefix(issue.Path, s.Path+"/") {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, issue)
		}
	}
	return out
}

// checkManifest applies rules JSON Schema cannot express. It reads the generic
// document defensively because it runs before, and independently of, schema
// validation.
func checkManifest(doc any, compat *CompatibilityReport) []Issue {
	root, _ := doc.(map[string]any)
	var issues []Issue
	if contributions, ok := root["contributions"].(map[string]any); ok {
		for _, name := range ReservedContributions {
			if _, declared := contributions[name]; declared {
				issues = append(issues, Issue{Code: CodeReservedContribution, Path: "/contributions/" + name,
					Message: fmt.Sprintf("%q is a reserved Contribution name that Plugin API %s does not accept; declare only normalizer, subscription, connector, ingestion or retrieval", name, PluginAPIVersion)})
			}
		}
		if sub, ok := contributions["subscription"].(map[string]any); ok {
			for _, field := range []struct{ name, code, label string }{
				{"expression_schema", CodeInvalidExpressionSchema, "expression"},
				{"configuration_schema", CodeInvalidConfigSchema, "Subscription configuration"},
			} {
				if schema, present := sub[field.name]; present {
					if _, err := compileUserSchema(schema); err != nil {
						issues = append(issues, Issue{Code: field.code, Path: "/contributions/subscription/" + field.name,
							Message: fmt.Sprintf("the %s schema is not a valid JSON Schema: %v", field.label, err)})
					}
				}
			}
		}
		if connector, ok := contributions["connector"].(map[string]any); ok {
			issues = append(issues, connectorKindIssues(connector)...)
		}
		if ingestion, ok := contributions["ingestion"].(map[string]any); ok {
			id, _ := root["id"].(string)
			issues = append(issues, spaceIssues(id, ingestion)...)
		}
		for _, field := range reservedFields {
			if pointerPresent(root, field.path) {
				issues = append(issues, Issue{Code: CodeReservedField, Path: field.path, Message: field.message})
			}
		}
	}
	if c, ok := root["compatibility"].(map[string]any); ok {
		if raw, ok := c["engine"].(string); ok {
			path := "/compatibility/engine"
			r, err := ParseRange(raw)
			if err != nil {
				issues = append(issues, Issue{Code: CodeInvalidRange, Path: path, Message: err.Error()})
			} else {
				v, verr := ParseVersion(EngineVersion)
				compat.Engine = &RangeCheck{Range: r.String(), Version: EngineVersion, Compatible: verr == nil && r.Contains(v)}
				if verr != nil {
					issues = append(issues, Issue{Code: CodeIncompatibleEngine, Path: path,
						Message: fmt.Sprintf("this build reports engine version %q, which is not SemVer; rebuild with a valid version", EngineVersion)})
				} else if !compat.Engine.Compatible {
					issues = append(issues, Issue{Code: CodeIncompatibleEngine, Path: path,
						Message: fmt.Sprintf("engine %s does not satisfy the declared range %q", EngineVersion, r.String())})
				}
			}
		}
		if raw, ok := c["plugin_api"].(string); ok {
			path := "/compatibility/plugin_api"
			r, err := ParseRange(raw)
			if err != nil {
				issues = append(issues, Issue{Code: CodeInvalidRange, Path: path, Message: err.Error()})
			} else {
				negotiated, ok := NegotiatePluginAPI(r)
				compat.PluginAPI = &RangeCheck{Range: r.String(), Version: PluginAPIVersion, Compatible: ok}
				if ok {
					compat.PluginAPI.Version = negotiated
				} else {
					issues = append(issues, Issue{Code: CodeIncompatiblePluginAPI, Path: path,
						Message: fmt.Sprintf("no Plugin API version this engine supports (%s) satisfies the declared range %q", strings.Join(SupportedPluginAPIVersions, ", "), r.String())})
				}
				issues = append(issues, contributionVersionIssues(root, r)...)
			}
		}
	}
	id, _ := root["id"].(string)
	if exts, ok := root["extensions"].(map[string]any); ok {
		for _, namespace := range sortedKeys(exts) {
			path := "/extensions/" + pointerToken(namespace)
			if id != "" && namespace != id && !strings.HasPrefix(namespace, id+".") {
				issues = append(issues, Issue{Code: CodeForeignNamespace, Path: path,
					Message: fmt.Sprintf("extension namespace %q must be %q or start with %q", namespace, id, id+".")})
				continue
			}
			versions, _ := exts[namespace].(map[string]any)
			for _, version := range sortedKeys(versions) {
				if _, err := compileUserSchema(versions[version]); err != nil {
					issues = append(issues, Issue{Code: CodeInvalidExtensionSchema, Path: path + "/" + pointerToken(version),
						Message: fmt.Sprintf("schema version %q of %q is not a valid JSON Schema: %v", version, namespace, err)})
				}
			}
		}
	}
	if config, ok := root["configuration"].(map[string]any); ok {
		if schema, present := config["schema"]; present {
			if _, err := compileUserSchema(schema); err != nil {
				issues = append(issues, Issue{Code: CodeInvalidConfigSchema, Path: "/configuration/schema",
					Message: fmt.Sprintf("configuration schema is not a valid JSON Schema: %v", err)})
			}
		}
	}
	if secrets, ok := root["secrets"].([]any); ok {
		seen := map[string]bool{}
		for i, s := range secrets {
			secret, _ := s.(map[string]any)
			name, _ := secret["name"].(string)
			if name == "" {
				continue
			}
			if seen[name] {
				issues = append(issues, Issue{Code: CodeDuplicateSecret, Path: fmt.Sprintf("/secrets/%d/name", i),
					Message: fmt.Sprintf("secret %q is declared more than once", name)})
			}
			seen[name] = true
		}
	}
	return issues
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func pointerToken(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// toJSON converts a decoded YAML value into JSON bytes. YAML-only shapes that
// JSON cannot carry (non-string keys, timestamps) become strings.
func toJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalizeYAML(v)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func normalizeYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeYAML(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalizeYAML(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeYAML(val)
		}
		return out
	case time.Time:
		return t.Format(time.RFC3339Nano)
	}
	return v
}

// NegotiatePluginAPI returns the highest supported Plugin API version the
// range admits: the version the engine speaks to that plugin.
func NegotiatePluginAPI(r Range) (string, bool) {
	for i := len(SupportedPluginAPIVersions) - 1; i >= 0; i-- {
		if v, err := ParseVersion(SupportedPluginAPIVersions[i]); err == nil && r.Contains(v) {
			return SupportedPluginAPIVersions[i], true
		}
	}
	return "", false
}

// contributionVersionIssues reports a declared Contribution that none of the
// supported Plugin API versions admitted by the range provides.
func contributionVersionIssues(root map[string]any, r Range) []Issue {
	contributions, _ := root["contributions"].(map[string]any)
	var issues []Issue
	for _, name := range sortedKeys(contributions) {
		since, known := ContributionSince[name]
		if !known {
			continue
		}
		if minimum, admitted := admits(r, since); !admitted {
			issues = append(issues, Issue{Code: CodeIncompatiblePluginAPI, Path: "/contributions/" + name,
				Message: fmt.Sprintf("the %s Contribution exists since Plugin API %s, which the declared plugin_api range %q excludes; widen it, for example to \">=%s <%d.%d.0\"", name, since, r.String(), since, minimum.Major, minimum.Minor+1)})
		}
	}
	pointers := make([]string, 0, len(FieldSince))
	for pointer := range FieldSince {
		pointers = append(pointers, pointer)
	}
	sort.Strings(pointers)
	for _, pointer := range pointers {
		if !pointerPresent(root, pointer) {
			continue
		}
		since := FieldSince[pointer]
		if minimum, admitted := admits(r, since); !admitted {
			issues = append(issues, Issue{Code: CodeIncompatiblePluginAPI, Path: pointer,
				Message: fmt.Sprintf("%s exists since Plugin API %s, which the declared plugin_api range %q excludes; widen it, for example to \">=%s <%d.%d.0\"", pointer, since, r.String(), since, minimum.Major, minimum.Minor+1)})
		}
	}
	connector, _ := contributions["connector"].(map[string]any)
	kinds, _ := connector["kinds"].(map[string]any)
	for _, name := range sortedKeys(kinds) {
		kind, _ := kinds[name].(map[string]any)
		if !declaresMode(kind, "push") {
			continue
		}
		if minimum, admitted := admits(r, PushSince); !admitted {
			issues = append(issues, Issue{Code: CodeIncompatiblePluginAPI, Path: "/contributions/connector/kinds/" + pointerToken(name) + "/modes",
				Message: fmt.Sprintf("kind %q declares the push mode, which exists since Plugin API %s and the declared plugin_api range %q excludes; widen it, for example to \">=%s <%d.%d.0\"", name, PushSince, r.String(), PushSince, minimum.Major, minimum.Minor+1)})
		}
	}
	return issues
}

// declaresMode reports whether a decoded connector kind lists mode.
func declaresMode(kind map[string]any, mode string) bool {
	modes, _ := kind["modes"].([]any)
	for _, m := range modes {
		if m == mode {
			return true
		}
	}
	return false
}

// admits reports whether the range admits a supported Plugin API version at
// least since.
func admits(r Range, since string) (Version, bool) {
	minimum, _ := ParseVersion(since)
	for _, supported := range SupportedPluginAPIVersions {
		if v, err := ParseVersion(supported); err == nil && v.Compare(minimum) >= 0 && r.Contains(v) {
			return minimum, true
		}
	}
	return minimum, false
}

// pointerPresent reports whether a JSON Pointer made of object keys resolves
// in doc.
func pointerPresent(doc any, pointer string) bool {
	node := doc
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		object, ok := node.(map[string]any)
		if !ok {
			return false
		}
		if node, ok = object[token]; !ok {
			return false
		}
	}
	return true
}

// spaceIssues checks that every declared vector space id is the plugin's own:
// the plugin id or prefixed by "<id>.", so two plugins never claim one space.
func spaceIssues(id string, ingestion map[string]any) []Issue {
	spaces, _ := ingestion["spaces"].(map[string]any)
	var issues []Issue
	for _, name := range sortedKeys(spaces) {
		if id != "" && name != id && !strings.HasPrefix(name, id+".") {
			issues = append(issues, Issue{Code: CodeForeignSpace, Path: "/contributions/ingestion/spaces/" + pointerToken(name),
				Message: fmt.Sprintf("vector space %q must be %q or start with %q: a space has exactly one owner plugin", name, id, id+".")})
		}
	}
	return issues
}

// connectorKindIssues checks what the schema cannot for each declared
// connector kind: config and credential schemas that compile, and pull beside
// push (the push mode's Plugin API version is checked with the range).
func connectorKindIssues(connector map[string]any) []Issue {
	kinds, _ := connector["kinds"].(map[string]any)
	var issues []Issue
	for _, name := range sortedKeys(kinds) {
		kind, _ := kinds[name].(map[string]any)
		path := "/contributions/connector/kinds/" + pointerToken(name)
		for _, field := range []struct{ name, code, label string }{
			{"config_schema", CodeInvalidConfigSchema, "configuration"},
			{"credential_schema", CodeInvalidCredentialSchema, "credential"},
		} {
			if schema, present := kind[field.name]; present {
				if _, err := compileUserSchema(schema); err != nil {
					issues = append(issues, Issue{Code: field.code, Path: path + "/" + field.name,
						Message: fmt.Sprintf("the %s schema of kind %q is not a valid JSON Schema: %v", field.label, name, err)})
				}
			}
		}
		if declaresMode(kind, "push") && !declaresMode(kind, "pull") {
			issues = append(issues, Issue{Code: CodeInvalidModes, Path: path + "/modes",
				Message: fmt.Sprintf("kind %q declares push without pull; declare modes: [pull, push], because polling is the fallback when deliveries stop", name)})
		}
	}
	return issues
}
