package plugins

import (
	"encoding/json"
	"fmt"
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
}

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
	routes        map[string]RouteConfig
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
	}
	// Routes feed the normalizer. A plugin that also, or only, contributes an
	// alert rule (subscription) is useful without routes.
	switch {
	case m.Contributions.Normalizer == nil && len(c.Routes) > 0:
		issues = append(issues, Issue{Code: CodeInvalidPin, Path: "/routes", Message: "the manifest declares no normalizer Contribution to route to; remove the routes of this pin"})
	case m.Contributions.Normalizer != nil && m.Contributions.Subscription == nil && len(c.Routes) == 0:
		issues = append(issues, Issue{Code: CodeInvalidPin, Path: "/routes", Message: "a pin needs at least one media type route"})
	}
	for _, namespace := range namespacesOf(m) {
		if content.DeclaredExtension(namespace) {
			issues = append(issues, Issue{Code: CodeNamespaceConflict, Path: "/extensions/" + pointerToken(namespace), Message: fmt.Sprintf("extension namespace %q is a built-in namespace of the engine; declare a namespace of your own under %q", namespace, m.ID+".")})
		}
	}
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
	return &Pin{Manifest: *m, ManifestDigest: report.ManifestDigest, Path: report.Path, Endpoint: strings.TrimRight(c.Endpoint, "/"), Configuration: config, routes: routes}, nil
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
