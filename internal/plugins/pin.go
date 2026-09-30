package plugins

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// Issue codes of a startup pin, beside the manifest codes.
const (
	CodeInvalidPin    = "invalid_pin"
	CodeRouteConflict = "route_conflict"
	// CodeNamespaceConflict is a declared extension namespace that clashes
	// with one the engine already owns.
	CodeNamespaceConflict = "namespace_conflict"
)

// Route modes. A required route quarantines a Version whose normalization
// fails; an optional route falls back to the built-in text path, so it is
// allowed only for media types that path handles (text/*).
const (
	RouteRequired = "required"
	RouteOptional = "optional"
)

// PinConfig is the startup configuration that pins one external plugin (an
// item of `plugins` in QUIVR_CONFIG, or the single `plugin`): its manifest
// file, its endpoint, its configuration and the media types routed to its
// normalizer. A plugin that contributes only an alert rule has no routes.
type PinConfig struct {
	Manifest      string          `json:"manifest"`
	Endpoint      string          `json:"endpoint"`
	Configuration json.RawMessage `json:"configuration,omitempty"`
	Routes        []RouteConfig   `json:"routes"`
	// Kinds lists the alert kinds this installation offers, a subset of those
	// the subscription expression schema declares (ExpressionKinds). A Saved
	// Query of another kind is refused when a Subscription pins it. Absent
	// offers every declared kind. The operator sets it, for example to leave
	// out a kind whose backend the plugin has no credentials for: the core
	// never sees the plugin's environment.
	Kinds []string `json:"kinds,omitempty"`
	// Spaces enables vector spaces of an ingestion Contribution, by space
	// id: "served" (search uses it) or "evaluation" (indexed and compared,
	// never served). Exactly one is served. Absent enables the only declared
	// space as served.
	Spaces map[string]string `json:"spaces,omitempty"`
}

// Space roles of a deployment.
const (
	SpaceServed     = "served"
	SpaceEvaluation = "evaluation"
)

// RouteConfig maps one accepted Blob media type to the pinned normalizer.
type RouteConfig struct {
	MediaType string `json:"media_type"`
	// Mode is "required" (the default) or "optional" (text/* only).
	Mode string `json:"mode,omitempty"`
}

// Pin is a validated startup pin. It is loaded without contacting the plugin,
// so an unreachable plugin never prevents startup.
type Pin struct {
	Manifest       Manifest
	ManifestDigest string
	// Path is the manifest file the pin was loaded from.
	Path     string
	Endpoint string
	// Configuration is the validated plugin configuration JSON object.
	Configuration json.RawMessage
	// Kinds is the offered subset of the declared alert kinds; nil offers all.
	Kinds []string
	// Spaces are the enabled vector spaces by space id, with their role.
	Spaces map[string]string
	routes map[string]RouteConfig
}

// PinError lists every actionable issue of a refused pin.
type PinError struct {
	Path   string
	Issues []Issue
}

func (e *PinError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid plugin pin (%s):", e.Path)
	for _, issue := range e.Issues {
		fmt.Fprintf(&b, "\n  %s %s: %s", issue.Code, issue.Path, issue.Message)
	}
	return b.String()
}

// LoadPin reads and validates a startup pin: the manifest (schema, engine and
// Plugin API ranges), the configuration against the manifest's configuration
// schema, the endpoint, and the routes (declared by the normalizer, no
// duplicate, a supported mode).
func LoadPin(c PinConfig) (*Pin, error) {
	report := Inspect(c.Manifest)
	refuse := func(issues []Issue) error { return &PinError{Path: report.Path, Issues: issues} }
	if !report.Valid || report.Manifest == nil {
		return nil, refuse(report.Errors)
	}
	m := report.Manifest
	var issues []Issue
	config := c.Configuration
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	issues = append(issues, ValidateConfiguration(m, config)...)
	if u, err := url.Parse(c.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		issues = append(issues, Issue{Code: CodeInvalidPin, Path: "/endpoint", Message: fmt.Sprintf("endpoint %q must be an http(s) base URL such as http://127.0.0.1:9900", c.Endpoint)})
	} else if m.Contributions.Connector != nil && u.Scheme != "https" && !loopbackHost(u.Hostname()) {
		// A connector receives Deposited Credentials in its request bodies.
		issues = append(issues, Issue{Code: CodeInvalidPin, Path: "/endpoint", Message: fmt.Sprintf("endpoint %q: a connector plugin receives credentials; use https:// or a loopback address (127.0.0.1, ::1, localhost)", c.Endpoint)})
	}
	// Routes feed the normalizer. A plugin that also, or only, contributes an
	// alert rule (subscription) is useful without routes.
	switch {
	case m.Contributions.Normalizer == nil && len(c.Routes) > 0:
		issues = append(issues, Issue{Code: CodeInvalidPin, Path: "/routes", Message: "the manifest declares no normalizer Contribution to route to; remove the routes of this pin"})
	case m.Contributions.Normalizer != nil && m.Contributions.Subscription == nil && m.Contributions.Ingestion == nil && m.Contributions.Retrieval == nil && len(c.Routes) == 0:
		issues = append(issues, Issue{Code: CodeInvalidPin, Path: "/routes", Message: "a pin needs at least one media type route"})
	}
	for _, namespace := range namespacesOf(m) {
		if content.DeclaredExtension(namespace) {
			issues = append(issues, Issue{Code: CodeNamespaceConflict, Path: "/extensions/" + pointerToken(namespace), Message: fmt.Sprintf("extension namespace %q is a built-in namespace of the engine; declare a namespace of your own under %q", namespace, m.ID+".")})
		}
	}
	issues = append(issues, checkKinds(m, c.Kinds)...)
	spaces, spaceIssues := checkSpaces(m, c.Spaces)
	issues = append(issues, spaceIssues...)
	declared := map[string]bool{}
	if m.Contributions.Normalizer != nil {
		for _, mediaType := range m.Contributions.Normalizer.MediaTypes {
			declared[mediaType] = true
		}
	}
	routes := map[string]RouteConfig{}
	for i, r := range c.Routes {
		path := fmt.Sprintf("/routes/%d", i)
		if r.Mode == "" {
			r.Mode = RouteRequired
		}
		switch r.Mode {
		case RouteRequired:
		case RouteOptional:
			if !BuiltinTextMediaType(r.MediaType) {
				issues = append(issues, Issue{Code: CodeInvalidPin, Path: path + "/mode", Message: fmt.Sprintf("media type %q cannot be optional: the built-in text path it would fall back to handles text/* only; use mode \"required\"", r.MediaType)})
			}
		default:
			issues = append(issues, Issue{Code: CodeInvalidPin, Path: path + "/mode", Message: fmt.Sprintf("unknown route mode %q; use \"required\" or \"optional\"", r.Mode)})
		}
		if !declared[r.MediaType] {
			issues = append(issues, Issue{Code: CodeRouteConflict, Path: path + "/media_type", Message: fmt.Sprintf("media type %q is not declared by the normalizer of %s (declared: %v)", r.MediaType, m.ID, keysOf(declared))})
			continue
		}
		if _, exists := routes[r.MediaType]; exists {
			issues = append(issues, Issue{Code: CodeRouteConflict, Path: path + "/media_type", Message: fmt.Sprintf("media type %q is routed more than once", r.MediaType)})
			continue
		}
		routes[r.MediaType] = r
	}
	if len(issues) > 0 {
		return nil, refuse(issues)
	}
	return &Pin{Manifest: *m, ManifestDigest: report.ManifestDigest, Path: report.Path, Endpoint: strings.TrimRight(c.Endpoint, "/"), Configuration: config, Kinds: c.Kinds, Spaces: spaces, routes: routes}, nil
}

// checkSpaces resolves the vector spaces a pin enables: declared ones, each
// served or evaluation, exactly one served. Without a spaces map the only
// declared space is served.
func checkSpaces(m *Manifest, spaces map[string]string) (map[string]string, []Issue) {
	in := m.Contributions.Ingestion
	if in == nil {
		if spaces != nil {
			return nil, []Issue{{Code: CodeInvalidPin, Path: "/spaces", Message: "the manifest declares no ingestion Contribution; remove spaces from this pin"}}
		}
		return nil, nil
	}
	declared := make([]string, 0, len(in.Spaces))
	for id := range in.Spaces {
		declared = append(declared, id)
	}
	sort.Strings(declared)
	if spaces == nil {
		if len(declared) != 1 {
			return nil, []Issue{{Code: CodeInvalidPin, Path: "/spaces", Message: fmt.Sprintf("%s declares %d vector spaces (%s); say which one is served and which are for evaluation, for example {\"%s\": \"served\"}", m.ID, len(declared), strings.Join(declared, ", "), declared[0])}}
		}
		return map[string]string{declared[0]: SpaceServed}, nil
	}
	var issues []Issue
	served := 0
	for _, id := range sortedStringKeys(spaces) {
		path := "/spaces/" + pointerToken(id)
		switch role := spaces[id]; {
		case !contains(declared, id):
			issues = append(issues, Issue{Code: CodeInvalidPin, Path: path, Message: fmt.Sprintf("vector space %q is not declared by %s (declared: %s)", id, m.ID, strings.Join(declared, ", "))})
		case role == SpaceServed:
			served++
		case role != SpaceEvaluation:
			issues = append(issues, Issue{Code: CodeInvalidPin, Path: path, Message: fmt.Sprintf("unknown role %q; use \"served\" or \"evaluation\"", role)})
		}
	}
	if served != 1 {
		issues = append(issues, Issue{Code: CodeInvalidPin, Path: "/spaces", Message: fmt.Sprintf("%d served vector spaces; exactly one is served, the others are for evaluation", served)})
	}
	return spaces, issues
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// EnabledSpace is one vector space a pin enables.
type EnabledSpace struct {
	// ID is the declared space id; Key is its identity with the version.
	ID, Key string
	Role    string
	Space   VectorSpace
}

// EnabledSpaces lists the pin's enabled vector spaces, served first, then by id.
func (p *Pin) EnabledSpaces() []EnabledSpace {
	if p == nil || p.Manifest.Contributions.Ingestion == nil {
		return nil
	}
	out := []EnabledSpace{}
	for _, id := range sortedStringKeys(p.Spaces) {
		space := p.Manifest.Contributions.Ingestion.Spaces[id]
		out = append(out, EnabledSpace{ID: id, Key: SpaceKey(id, space.Version), Role: p.Spaces[id], Space: space})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Role == SpaceServed && out[j].Role != SpaceServed })
	return out
}

// ExpressionKinds lists the alert kinds of a subscription expression schema
// that discriminates them with a oneOf over a constant `kind` property, in
// declaration order. It returns nil for any other schema.
func ExpressionKinds(raw json.RawMessage) []string {
	var schema struct {
		OneOf []struct {
			Properties struct {
				Kind struct {
					Const *string `json:"const"`
				} `json:"kind"`
			} `json:"properties"`
		} `json:"oneOf"`
	}
	if json.Unmarshal(raw, &schema) != nil || len(schema.OneOf) == 0 {
		return nil
	}
	kinds := make([]string, 0, len(schema.OneOf))
	for _, branch := range schema.OneOf {
		if branch.Properties.Kind.Const == nil {
			return nil
		}
		kinds = append(kinds, *branch.Properties.Kind.Const)
	}
	return kinds
}

// checkKinds validates a pin's offered kinds against the declared ones.
func checkKinds(m *Manifest, kinds []string) []Issue {
	if kinds == nil {
		return nil
	}
	var declared []string
	if m.Contributions.Subscription != nil {
		declared = ExpressionKinds(m.Contributions.Subscription.ExpressionSchema)
	}
	if declared == nil {
		return []Issue{{Code: CodeInvalidPin, Path: "/kinds", Message: "kinds applies to a subscription Contribution whose expression schema discriminates kinds with a oneOf over a constant kind property; remove kinds from this pin"}}
	}
	if len(kinds) == 0 {
		return []Issue{{Code: CodeInvalidPin, Path: "/kinds", Message: fmt.Sprintf("list at least one kind (declared: %s), or omit kinds to offer them all", strings.Join(declared, ", "))}}
	}
	var issues []Issue
	seen := map[string]bool{}
	for i, kind := range kinds {
		path := fmt.Sprintf("/kinds/%d", i)
		switch {
		case seen[kind]:
			issues = append(issues, Issue{Code: CodeInvalidPin, Path: path, Message: fmt.Sprintf("kind %q is listed more than once", kind)})
		case !contains(declared, kind):
			issues = append(issues, Issue{Code: CodeInvalidPin, Path: path, Message: fmt.Sprintf("kind %q is not declared by %s (declared: %s)", kind, m.ID, strings.Join(declared, ", "))})
		}
		seen[kind] = true
	}
	return issues
}

// Offers reports whether the pin offers an expression's alert kind: always
// when the pin restricts no kinds.
func (p *Pin) Offers(expression map[string]any) bool {
	if p == nil || p.Kinds == nil {
		return true
	}
	kind, _ := expression["kind"].(string)
	return contains(p.Kinds, kind)
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func namespacesOf(m *Manifest) []string {
	namespaces := make([]string, 0, len(m.Extensions))
	for ns := range m.Extensions {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	return namespaces
}

// ExtensionRegistry registers the pinned plugin's declared extension
// namespaces beside the built-in ones; a nil pin registers none. API and
// worker install it as the content.Service validator at startup, so clients
// cannot write a plugin-owned namespace and retrieval mappings may address it.
// The plugin's own output is validated by DeclaredExtensions of the same
// manifest (CheckNormalizerOutput).
func ExtensionRegistry(p *Pin) (*content.ExtensionRegistry, error) {
	registry := content.NewExtensionRegistry()
	if p == nil {
		return registry, nil
	}
	if err := registry.Own(p.Manifest.ID, namespacesOf(&p.Manifest)...); err != nil {
		return nil, &PinError{Path: p.Path, Issues: []Issue{{Code: CodeNamespaceConflict, Path: "/extensions", Message: err.Error()}}}
	}
	return registry, nil
}

// loopbackHost reports whether an endpoint host is a loopback literal or
// localhost, where plain HTTP never leaves the machine.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// BuiltinTextMediaType reports whether the built-in text path can take a Blob
// of this media type.
func BuiltinTextMediaType(mediaType string) bool {
	return strings.HasPrefix(strings.ToLower(mediaType), "text/")
}

func keysOf(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Routed reports whether a Blob media type is routed to the pinned normalizer.
// Media types match exactly, as declared in the manifest.
func (p *Pin) Routed(mediaType string) bool {
	if p == nil {
		return false
	}
	_, ok := p.routes[mediaType]
	return ok
}

// Route returns the route of a media type.
func (p *Pin) Route(mediaType string) (RouteConfig, bool) {
	if p == nil {
		return RouteConfig{}, false
	}
	r, ok := p.routes[mediaType]
	return r, ok
}

// Generation is the Plugin Generation placeholder of a startup pin, the first
// component of the invocation idempotency key. Spec 5 substitutes the Plugin
// Generation id without changing the key's shape.
func (p *Pin) Generation() string {
	return "startup:" + p.Manifest.ID + "@" + p.Manifest.Version + "#" + p.ManifestDigest
}

// PluginAPI is the Plugin API version the engine speaks to the pinned plugin:
// the highest supported version its plugin_api range admits.
func (p *Pin) PluginAPI() string {
	if r, err := ParseRange(p.Manifest.Compatibility.PluginAPI); err == nil {
		if v, ok := NegotiatePluginAPI(r); ok {
			return v
		}
	}
	return PluginAPIVersion
}

// Report is the inspection report the discovery check compares against.
func (p *Pin) Report() Report {
	m := p.Manifest
	return Report{Valid: true, Path: p.Path, ManifestDigest: p.ManifestDigest, Manifest: &m}
}

// Normalizer returns this pin and the route of a media type routed to it, so
// a single pin serves where a PinSet is expected.
func (p *Pin) Normalizer(mediaType string) (*Pin, RouteConfig, bool) {
	r, ok := p.Route(mediaType)
	if !ok {
		return nil, RouteConfig{}, false
	}
	return p, r, true
}
