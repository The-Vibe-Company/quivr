package quivrplugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PluginAPIVersion is the newest Plugin API version this SDK implements.
const PluginAPIVersion = "0.3.1"

// SupportedPluginAPIVersions are the Plugin API versions this SDK can serve,
// oldest first. Discovery reports the highest one the manifest range admits.
var SupportedPluginAPIVersions = []string{"0.1.0", "0.2.0", "0.3.0", "0.3.1"}

// Manifest is what the SDK reads from quivr-plugin.yaml: identity, the
// Plugin API range and the connector Contribution. The engine validates the
// whole manifest with `quivr plugin inspect`.
type Manifest struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	Compatibility struct {
		PluginAPI string `json:"plugin_api"`
	} `json:"compatibility"`
	Contributions map[string]json.RawMessage `json:"contributions"`
	Configuration *struct {
		Schema json.RawMessage `json:"schema"`
	} `json:"configuration,omitempty"`

	// Connector is the decoded connector Contribution, with defaults.
	Connector *ConnectorContribution `json:"-"`
}

// ConnectorContribution is the connector Contribution of a manifest.
type ConnectorContribution struct {
	Kinds     map[string]ConnectorKind `json:"kinds"`
	TimeoutMS int                      `json:"timeout_ms"`
	Limits    struct {
		MaxResponseBytes   int `json:"max_response_bytes"`
		MaxItems           int `json:"max_items"`
		MaxCheckpointBytes int `json:"max_checkpoint_bytes"`
	} `json:"limits"`
}

// ConnectorKind is one declared connector kind.
type ConnectorKind struct {
	Description            string          `json:"description,omitempty"`
	ConfigSchema           json.RawMessage `json:"config_schema"`
	CredentialSchema       json.RawMessage `json:"credential_schema,omitempty"`
	// CredentialRequired false makes the declared credential optional: an
	// instance without one is invoked with a null credential (default true).
	CredentialRequired *bool `json:"credential_required,omitempty"`
	DefaultIntervalSeconds int             `json:"default_interval_seconds"`
	Modes                  []string        `json:"modes,omitempty"`
}

// Output bounds, as in the contract.
const (
	DefaultMaxResponseBytes = 4 << 20
	EngineMaxResponseBytes  = 16 << 20
	DefaultMaxItems         = 100
	MaxCheckpointBytes      = 64 << 10 // default; limits.max_checkpoint_bytes raises it
	MaxDeclaredCheckpoint   = 1 << 20
	MaxDiagnosticsBytes     = 16 << 10
)

type loadedManifest struct {
	Manifest
	raw        []byte
	doc        []byte // JSON equivalent of the YAML
	digest     string
	pluginAPI  string
	maxBytes   int
	maxItems   int
	maxCheckpt int
	timeoutDur time.Duration
}

func loadManifest(path string) (*loadedManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var parsed any
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	doc, err := json.Marshal(normalizeYAML(parsed))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := validate("plugins/v0/plugin-manifest.schema.json", doc); err != nil {
		return nil, fmt.Errorf("%s does not match the manifest schema (run quivr plugin inspect): %w", path, err)
	}
	sum := sha256.Sum256(raw)
	m := &loadedManifest{raw: raw, doc: doc, digest: "sha256:" + hex.EncodeToString(sum[:])}
	if err := json.Unmarshal(doc, &m.Manifest); err != nil {
		return nil, err
	}
	for name := range m.Contributions {
		if name != "connector" {
			return nil, fmt.Errorf("%s declares the %s Contribution; this SDK serves connector Contributions only", path, name)
		}
	}
	raw0, ok := m.Contributions["connector"]
	if !ok {
		return nil, fmt.Errorf("%s declares no connector Contribution", path)
	}
	m.Connector = &ConnectorContribution{}
	if err := json.Unmarshal(raw0, m.Connector); err != nil {
		return nil, err
	}
	c := m.Connector
	if c.TimeoutMS == 0 {
		c.TimeoutMS = 30000
	}
	m.timeoutDur = time.Duration(c.TimeoutMS) * time.Millisecond
	m.maxBytes = min(DefaultMaxResponseBytes, EngineMaxResponseBytes)
	if c.Limits.MaxResponseBytes > 0 {
		m.maxBytes = min(c.Limits.MaxResponseBytes, EngineMaxResponseBytes)
	}
	m.maxItems = DefaultMaxItems
	if c.Limits.MaxItems > 0 {
		m.maxItems = c.Limits.MaxItems
	}
	m.maxCheckpt = MaxCheckpointBytes
	if c.Limits.MaxCheckpointBytes > 0 {
		m.maxCheckpt = min(c.Limits.MaxCheckpointBytes, MaxDeclaredCheckpoint)
	}
	api, ok, err := negotiate(m.Compatibility.PluginAPI)
	if err != nil {
		return nil, fmt.Errorf("%s: compatibility.plugin_api: %w", path, err)
	}
	if !ok || compareVersions(api, "0.3.0") < 0 {
		return nil, fmt.Errorf("%s: the plugin_api range %q must admit Plugin API 0.3.0, which introduced connectors", path, m.Compatibility.PluginAPI)
	}
	m.pluginAPI = api
	return m, nil
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

var comparator = regexp.MustCompile(`^(>=|<=|>|<|=)?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// negotiate returns the highest supported Plugin API version the range
// admits (see "Version ranges" in contracts/plugins/v0/README.md).
func negotiate(r string) (string, bool, error) {
	fields := strings.Fields(r)
	if len(fields) == 0 {
		return "", false, fmt.Errorf("empty range")
	}
	type cmp struct{ op, version string }
	var cmps []cmp
	for _, f := range fields {
		m := comparator.FindStringSubmatch(f)
		if m == nil {
			return "", false, fmt.Errorf("%q is not a comparator", f)
		}
		cmps = append(cmps, cmp{m[1], m[2] + "." + m[3] + "." + m[4]})
	}
	for i := len(SupportedPluginAPIVersions) - 1; i >= 0; i-- {
		v := SupportedPluginAPIVersions[i]
		ok := true
		for _, c := range cmps {
			d := compareVersions(v, c.version)
			switch c.op {
			case ">=":
				ok = ok && d >= 0
			case ">":
				ok = ok && d > 0
			case "<=":
				ok = ok && d <= 0
			case "<":
				ok = ok && d < 0
			default:
				ok = ok && d == 0
			}
		}
		if ok {
			return v, true, nil
		}
	}
	return "", false, nil
}

// compareVersions orders MAJOR.MINOR.PATCH release versions.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// compact returns the length of a JSON value once compacted.
func compactLen(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return len(raw)
	}
	return buf.Len()
}
