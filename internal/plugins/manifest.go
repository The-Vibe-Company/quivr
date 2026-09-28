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
const PluginAPIVersion = "0.1.0"

// EngineVersion is the engine version plugins declare compatibility with.
// Release builds may override it:
//
//	go build -ldflags "-X github.com/The-Vibe-Company/quivr-v2/internal/plugins.EngineVersion=0.1.1"
var EngineVersion = "0.1.0"

// ManifestFile is the plugin manifest file name inside a plugin directory.
const ManifestFile = "quivr-plugin.yaml"

// ReservedContributions are Contribution names kept for later Plugin API
// versions; Plugin API 0.1 rejects them.
var ReservedContributions = []string{"connector", "enricher", "validator", "projector", "retriever", "subscription"}

// Effective defaults applied when a manifest omits the field.
const (
	DefaultTimeoutMS        = 30000
	DefaultMaxAttempts      = 3
	DefaultMaxResponseBytes = 4 << 20
	DefaultMaxParts         = 256
)

// Stable issue codes.
const (
	CodeUnreadable             = "unreadable_manifest"
	CodeInvalidYAML            = "invalid_yaml"
	CodeSchema                 = "schema_violation"
	CodeInvalidRange           = "invalid_range"
	CodeIncompatibleEngine     = "incompatible_engine"
	CodeIncompatiblePluginAPI  = "incompatible_plugin_api"
	CodeReservedContribution   = "reserved_contribution"
	CodeForeignNamespace       = "foreign_namespace"
	CodeInvalidConfigSchema    = "invalid_config_schema"
	CodeInvalidExtensionSchema = "invalid_extension_schema"
	CodeDuplicateSecret        = "duplicate_secret"
	CodeInvalidManifest        = "invalid_manifest"
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
	Normalizer *Normalizer `json:"normalizer,omitempty"`
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
					Message: fmt.Sprintf("%q is a reserved Contribution name that Plugin API %s does not accept; declare only normalizer", name, PluginAPIVersion)})
			}
		}
	}
	if c, ok := root["compatibility"].(map[string]any); ok {
		for _, target := range []struct {
			field, code, label, version string
			out                         **RangeCheck
		}{
			{"engine", CodeIncompatibleEngine, "engine", EngineVersion, &compat.Engine},
			{"plugin_api", CodeIncompatiblePluginAPI, "Plugin API", PluginAPIVersion, &compat.PluginAPI},
		} {
			raw, ok := c[target.field].(string)
			if !ok {
				continue
			}
			path := "/compatibility/" + target.field
			r, err := ParseRange(raw)
			if err != nil {
				issues = append(issues, Issue{Code: CodeInvalidRange, Path: path, Message: err.Error()})
				continue
			}
			v, verr := ParseVersion(target.version)
			check := &RangeCheck{Range: r.String(), Version: target.version, Compatible: verr == nil && r.Contains(v)}
			*target.out = check
			if verr != nil {
				issues = append(issues, Issue{Code: target.code, Path: path,
					Message: fmt.Sprintf("this build reports %s version %q, which is not SemVer; rebuild with a valid version", target.label, target.version)})
			} else if !check.Compatible {
				issues = append(issues, Issue{Code: target.code, Path: path,
					Message: fmt.Sprintf("%s %s does not satisfy the declared range %q", target.label, target.version, r.String())})
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
				if err := compileUserSchema(versions[version]); err != nil {
					issues = append(issues, Issue{Code: CodeInvalidExtensionSchema, Path: path + "/" + pointerToken(version),
						Message: fmt.Sprintf("schema version %q of %q is not a valid JSON Schema: %v", version, namespace, err)})
				}
			}
		}
	}
	if config, ok := root["configuration"].(map[string]any); ok {
		if schema, present := config["schema"]; present {
			if err := compileUserSchema(schema); err != nil {
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
