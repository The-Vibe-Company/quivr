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
const PluginAPIVersion = "0.10.0"

// SupportedPluginAPIVersions are the Plugin API versions this SDK can serve,
// oldest first. Discovery reports the highest one the manifest range admits.
var SupportedPluginAPIVersions = []string{"0.1.0", "0.2.0", "0.3.0", "0.3.1", "0.4.0", "0.5.0", "0.6.0", "0.7.0", "0.8.0", "0.9.0", "0.10.0"}

// Manifest is what the SDK reads from quivr-plugin.yaml: identity, the
// Plugin API range and the connector, ingestion and retrieval Contributions. The engine validates the
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

	// Connector is the decoded connector Contribution, with defaults; nil
	// when the manifest declares none.
	Connector *ConnectorContribution `json:"-"`
	// Ingestion is the decoded ingestion Contribution (Plugin API 0.6), with
	// defaults; nil when the manifest declares none.
	Ingestion *IngestionContribution `json:"-"`
	// Retrieval is the decoded retrieval Contribution (Plugin API 0.7), with
	// defaults; nil when the manifest declares none.
	Retrieval *RetrievalContribution `json:"-"`
}

// RetrievalContribution is the retrieval Contribution of a manifest.
type RetrievalContribution struct {
	Profiles map[string]RetrievalProfile `json:"profiles"`
	Limits   struct {
		MaxRounds        int `json:"max_rounds"`
		MaxRequests      int `json:"max_requests"`
		MaxCandidates    int `json:"max_candidates"`
		MaxResponseBytes int `json:"max_response_bytes"`
	} `json:"limits"`
}

// RetrievalProfile is one declared search profile and its budgets.
type RetrievalProfile struct {
	Description  string  `json:"description,omitempty"`
	MaxLatencyMS int     `json:"max_latency_ms"`
	MaxCostCents float64 `json:"max_cost_cents"`
}

// IngestionContribution is the ingestion Contribution of a manifest.
type IngestionContribution struct {
	Spaces         map[string]Space `json:"spaces"`
	TimeoutMS      int              `json:"timeout_ms"`
	QueryTimeoutMS int              `json:"query_timeout_ms"`
	Limits         struct {
		MaxSegments      int `json:"max_segments"`
		MaxResponseBytes int `json:"max_response_bytes"`
	} `json:"limits"`
}

// Space is one vector space the plugin declares.
type Space struct {
	Version         string   `json:"version"`
	Model           string   `json:"model"`
	Dimensions      int      `json:"dimensions"`
	Metric          string   `json:"metric"`
	Indexes         []string `json:"indexes"`
	QueryModalities []string `json:"query_modalities"`
	// InputPrice is what embedding text in the space costs (Plugin API 0.9),
	// for the estimate a backfill shows before it starts.
	InputPrice *InputPrice `json:"input_price,omitempty"`
}

// InputPrice is the declared price of embedding text in a vector space.
type InputPrice struct {
	USDPerMillionTokens float64 `json:"usd_per_million_tokens"`
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
	// Attachments, since Plugin API 0.4, lets items carry attachments; the
	// connector then implements AttachmentSource.
	Attachments *AttachmentLimits `json:"attachments,omitempty"`
}

// AttachmentLimits is contributions.connector.attachments, with defaults.
type AttachmentLimits struct {
	MaxBytes  int64 `json:"max_bytes"`
	TimeoutMS int   `json:"timeout_ms"`
}

// ConnectorKind is one declared connector kind.
type ConnectorKind struct {
	Description      string          `json:"description,omitempty"`
	ConfigSchema     json.RawMessage `json:"config_schema"`
	CredentialSchema json.RawMessage `json:"credential_schema,omitempty"`
	// CredentialRequired false makes the declared credential optional: an
	// instance without one is invoked with a null credential (default true).
	CredentialRequired     *bool    `json:"credential_required,omitempty"`
	DefaultIntervalSeconds int      `json:"default_interval_seconds"`
	Modes                  []string `json:"modes,omitempty"`
}

// Pushes reports whether the kind declares the push mode (Plugin API 0.5):
// its implementation then also implements Receiver.
func (k ConnectorKind) Pushes() bool {
	for _, mode := range k.Modes {
		if mode == "push" {
			return true
		}
	}
	return false
}

// Output bounds, as in the contract.
const (
	DefaultMaxResponseBytes = 4 << 20
	EngineMaxResponseBytes  = 16 << 20
	DefaultMaxItems         = 100
	MaxCheckpointBytes      = 64 << 10 // default; limits.max_checkpoint_bytes raises it
	MaxDeclaredCheckpoint   = 1 << 20
	MaxDiagnosticsBytes     = 16 << 10
	// MaxAttachmentBytes is the engine's cap on one attachment.
	MaxAttachmentBytes int64 = 25 << 20
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
		if name != "connector" && name != "ingestion" && name != "retrieval" {
			return nil, fmt.Errorf("%s declares the %s Contribution; this SDK serves connector, ingestion and retrieval Contributions only", path, name)
		}
	}
	api, ok, err := negotiate(m.Compatibility.PluginAPI)
	if err != nil {
		return nil, fmt.Errorf("%s: compatibility.plugin_api: %w", path, err)
	}
	if !ok {
		return nil, fmt.Errorf("%s: no Plugin API version this SDK serves (%s) satisfies the range %q", path, strings.Join(SupportedPluginAPIVersions, ", "), m.Compatibility.PluginAPI)
	}
	m.pluginAPI = api
	if raw, ok := m.Contributions["ingestion"]; ok {
		if compareVersions(api, "0.6.0") < 0 {
			return nil, fmt.Errorf("%s: the plugin_api range %q must admit Plugin API 0.6.0, which introduced ingestion", path, m.Compatibility.PluginAPI)
		}
		in := &IngestionContribution{}
		if err := json.Unmarshal(raw, in); err != nil {
			return nil, err
		}
		if in.TimeoutMS == 0 {
			in.TimeoutMS = 30000
		}
		if in.QueryTimeoutMS == 0 {
			in.QueryTimeoutMS = 2000
		}
		if in.Limits.MaxSegments == 0 {
			in.Limits.MaxSegments = 256
		}
		if in.Limits.MaxResponseBytes == 0 || in.Limits.MaxResponseBytes > EngineMaxResponseBytes {
			in.Limits.MaxResponseBytes = EngineMaxResponseBytes
		}
		m.Ingestion = in
	}
	if raw, ok := m.Contributions["retrieval"]; ok {
		if compareVersions(api, "0.7.0") < 0 {
			return nil, fmt.Errorf("%s: the plugin_api range %q must admit Plugin API 0.7.0, which introduced retrieval", path, m.Compatibility.PluginAPI)
		}
		rc := &RetrievalContribution{}
		if err := json.Unmarshal(raw, rc); err != nil {
			return nil, err
		}
		if rc.Limits.MaxRounds == 0 {
			rc.Limits.MaxRounds = 3
		}
		if rc.Limits.MaxRequests == 0 {
			rc.Limits.MaxRequests = 4
		}
		if rc.Limits.MaxCandidates == 0 {
			rc.Limits.MaxCandidates = 50
		}
		if rc.Limits.MaxResponseBytes == 0 {
			rc.Limits.MaxResponseBytes = 1 << 20
		}
		m.Retrieval = rc
	}
	raw0, ok := m.Contributions["connector"]
	if !ok {
		if m.Ingestion == nil && m.Retrieval == nil {
			return nil, fmt.Errorf("%s declares no connector, ingestion or retrieval Contribution", path)
		}
		return m, nil
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
	if a := c.Attachments; a != nil {
		if a.MaxBytes <= 0 || a.MaxBytes > MaxAttachmentBytes {
			a.MaxBytes = MaxAttachmentBytes
		}
		if a.TimeoutMS <= 0 || a.TimeoutMS > 120000 {
			a.TimeoutMS = 120000
		}
	}
	if compareVersions(api, "0.3.0") < 0 {
		return nil, fmt.Errorf("%s: the plugin_api range %q must admit Plugin API 0.3.0, which introduced connectors", path, m.Compatibility.PluginAPI)
	}
	if c.Attachments != nil && compareVersions(api, "0.4.0") < 0 {
		return nil, fmt.Errorf("%s: contributions.connector.attachments needs a plugin_api range that admits Plugin API 0.4.0", path)
	}
	for name, kind := range c.Kinds {
		if kind.Pushes() && compareVersions(api, "0.5.0") < 0 {
			return nil, fmt.Errorf("%s: kind %s declares the push mode, which needs a plugin_api range that admits Plugin API 0.5.0", path, name)
		}
	}
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
