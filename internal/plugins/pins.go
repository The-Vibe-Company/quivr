package plugins

import (
	"fmt"
	"sort"
	"strings"

	"github.com/The-Vibe-Company/quivr-v2/internal/content"
)

// CodePluginConflict is a plugin pinned twice, or a pin that takes an
// evaluator id the engine reserves.
const CodePluginConflict = "plugin_conflict"

// CodeKindConflict is a connector kind declared by two pinned plugins.
const CodeKindConflict = "kind_conflict"

// CodeSpaceConflict is a vector space declared by more than one pinned
// ingestion plugin. A space key has one owner for as long as vectors exist in
// it.
const CodeSpaceConflict = "space_owner_conflict"

// ReservedEvaluatorIDs are evaluator ids the engine installs itself (the
// deterministic test evaluator); no plugin may be pinned under them.
var ReservedEvaluatorIDs = []string{"quivr.fixture"}

// PinSet is every plugin pinned at startup, with its Contribution routing:
// normalizers by accepted Blob media type, subscription evaluators by plugin
// id and version, connectors by kind, and ingestion plugins by source media
// type and declared vector space. A nil set pins nothing.
type PinSet struct {
	pins             []*Pin
	normalizers      map[string]*Pin
	evaluators       map[string]*Pin
	connectors       map[string]*Pin
	ingestion        *Pin
	ingestions       map[string]*Pin
	spaces           map[string]*Pin
	ingestionRouting IngestionRouting
	retrieval        []*Pin
}

// LoadPins validates each pin with LoadPin, then routes the Contributions of
// all of them. A plugin id pinned twice, a media type routed to two plugins,
// a connector kind provided by two plugins and a reserved evaluator id refuse
// startup. Nothing contacts a plugin.
func LoadPins(configs []PinConfig) (*PinSet, error) {
	var issues []Issue
	pins := make([]*Pin, len(configs))
	for i, c := range configs {
		pin, err := LoadPin(c)
		if err != nil {
			if pe, ok := err.(*PinError); ok {
				for _, issue := range pe.Issues {
					issue.Path = fmt.Sprintf("/plugins/%d", i) + issue.Path
					issue.Message = fmt.Sprintf("%s (%s)", issue.Message, pe.Path)
					issues = append(issues, issue)
				}
				continue
			}
			return nil, err
		}
		pins[i] = pin
	}
	if len(issues) > 0 {
		return nil, &PinError{Path: "plugins", Issues: issues}
	}
	return NewPinSet(pins)
}

// NewPinSet routes the Contributions of pins already validated one by one
// (LoadPin, LoadPinManifest), with the rules of LoadPins: a plugin id twice,
// a media type routed to two plugins, a connector kind provided by two, a
// declared vector space owned by two plugins and a reserved evaluator id are
// conflicts. Several ingestion and retrieval plugins are valid; call
// ConfigureIngestion to choose an ingestion default and source-media routes.
// Issue paths name each pin by its position.
func NewPinSet(pins []*Pin) (*PinSet, error) {
	set := &PinSet{normalizers: map[string]*Pin{}, evaluators: map[string]*Pin{}, connectors: map[string]*Pin{}, ingestions: map[string]*Pin{}, spaces: map[string]*Pin{}}
	var issues []Issue
	byID := map[string]int{}
	for i, pin := range pins {
		prefix := fmt.Sprintf("/plugins/%d", i)
		id := pin.Manifest.ID
		for _, reserved := range ReservedEvaluatorIDs {
			if id == reserved {
				issues = append(issues, Issue{Code: CodePluginConflict, Path: prefix + "/manifest", PluginID: id, PluginVersion: pin.Manifest.Version, Message: fmt.Sprintf("plugin id %q is reserved by the engine; choose an id of your own", id)})
			}
		}
		if first, exists := byID[id]; exists {
			issues = append(issues, Issue{Code: CodePluginConflict, Path: prefix + "/manifest", PluginID: id, PluginVersion: pins[first].Manifest.Version, Message: fmt.Sprintf("plugin %q is already pinned at /plugins/%d; pin each plugin once", id, first)})
			continue
		}
		byID[id] = i
		set.pins = append(set.pins, pin)
		mediaTypes := make([]string, 0, len(pin.routes))
		for mediaType := range pin.routes {
			mediaTypes = append(mediaTypes, mediaType)
		}
		sort.Strings(mediaTypes)
		for _, mediaType := range mediaTypes {
			if other, exists := set.normalizers[mediaType]; exists {
				issues = append(issues, Issue{Code: CodeRouteConflict, Path: prefix + "/routes", PluginID: other.Manifest.ID, PluginVersion: other.Manifest.Version, Message: fmt.Sprintf("media type %q is already routed to %s; route each media type to one normalizer", mediaType, other.Manifest.ID)})
				continue
			}
			set.normalizers[mediaType] = pin
		}
		if pin.Manifest.Contributions.Subscription != nil {
			set.evaluators[EvaluatorKey(id, pin.Manifest.Version)] = pin
		}
		if pin.Manifest.Contributions.Ingestion != nil {
			set.ingestions[id] = pin
			spaceIDs := make([]string, 0, len(pin.Manifest.Contributions.Ingestion.Spaces))
			for spaceID := range pin.Manifest.Contributions.Ingestion.Spaces {
				spaceIDs = append(spaceIDs, spaceID)
			}
			sort.Strings(spaceIDs)
			for _, spaceID := range spaceIDs {
				space := pin.Manifest.Contributions.Ingestion.Spaces[spaceID]
				key := SpaceKey(spaceID, space.Version)
				if other, exists := set.spaces[key]; exists && other.Manifest.ID != id {
					issues = append(issues, Issue{Code: CodeSpaceConflict, Path: prefix + "/manifest/contributions/ingestion/spaces/" + pointerToken(spaceID), PluginID: other.Manifest.ID, PluginVersion: other.Manifest.Version, Message: fmt.Sprintf("vector space %q is also declared by %s; each declared space key belongs to one ingestion plugin", key, other.Manifest.ID)})
					continue
				}
				set.spaces[key] = pin
			}
		}
		if pin.Manifest.Contributions.Retrieval != nil {
			set.retrieval = append(set.retrieval, pin)
		}
		if c := pin.Manifest.Contributions.Connector; c != nil {
			kinds := make([]string, 0, len(c.Kinds))
			for kind := range c.Kinds {
				kinds = append(kinds, kind)
			}
			sort.Strings(kinds)
			for _, kind := range kinds {
				if other, exists := set.connectors[kind]; exists {
					issues = append(issues, Issue{Code: CodeKindConflict, Path: prefix + "/manifest", PluginID: other.Manifest.ID, PluginVersion: other.Manifest.Version, Message: fmt.Sprintf("connector kind %q is also provided by %s@%s; each kind resolves to one provider, so pin only one of them", kind, other.Manifest.ID, other.Manifest.Version)})
					continue
				}
				set.connectors[kind] = pin
			}
		}
	}
	if len(issues) > 0 {
		return nil, &PinError{Path: "plugins", Issues: issues}
	}
	// Search Protocol v0 carries at most 16 spaces, including evaluation
	// spaces. Refuse an unservable deployment before its first search.
	enabledSpaces := 0
	for _, pin := range set.ingestions {
		enabledSpaces += len(pin.EnabledSpaces())
	}
	if enabledSpaces > 16 {
		return nil, &PinError{Path: "plugins", Issues: []Issue{{Code: CodeInvalidPin, Path: "/plugins", Message: fmt.Sprintf("%d vector spaces are enabled across ingestion plugins; the search protocol carries at most 16", enabledSpaces)}}}
	}
	// Keep the compatibility accessor useful for the common configurations.
	// An ambiguous set is still valid until the operator supplies an explicit
	// routing object through ConfigureIngestion.
	routing := IngestionRouting{}
	if _, ok := set.ingestions["core.ingest"]; ok {
		routing.Default = "core.ingest"
	} else if len(set.ingestions) == 1 {
		for id := range set.ingestions {
			routing.Default = id
		}
	}
	if routing.Default != "" {
		set.ingestionRouting = routing
		set.ingestion = set.ingestions[routing.Default]
	}
	return set, nil
}

// EvaluatorKey identifies a subscription evaluator by plugin id and version.
func EvaluatorKey(id, version string) string { return id + "@" + version }

// Pins lists the pinned plugins in configuration order.
func (s *PinSet) Pins() []*Pin {
	if s == nil {
		return nil
	}
	return s.pins
}

// Routed reports whether a Blob media type is routed to a pinned normalizer.
func (s *PinSet) Routed(mediaType string) bool {
	_, _, ok := s.Normalizer(mediaType)
	return ok
}

// Normalizer returns the pin and route a Blob media type is routed to.
func (s *PinSet) Normalizer(mediaType string) (*Pin, RouteConfig, bool) {
	if s == nil {
		return nil, RouteConfig{}, false
	}
	pin, ok := s.normalizers[mediaType]
	if !ok {
		return nil, RouteConfig{}, false
	}
	route, _ := pin.Route(mediaType)
	return pin, route, true
}

// Evaluators lists the pinned subscription evaluators, sorted by key.
func (s *PinSet) Evaluators() []*Pin {
	if s == nil {
		return nil
	}
	keys := make([]string, 0, len(s.evaluators))
	for k := range s.evaluators {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*Pin, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.evaluators[k])
	}
	return out
}

// Ingestion returns the pinned ingestion plugin, or nil.
func (s *PinSet) Ingestion() *Pin {
	if s == nil {
		return nil
	}
	return s.ingestion
}

// Retrievals lists the retrieval plugins in configuration order.
func (s *PinSet) Retrievals() []*Pin {
	if s == nil {
		return nil
	}
	return s.retrieval
}

// PinnedKind is one connector kind a pinned plugin provides.
type PinnedKind struct {
	Kind string
	Pin  *Pin
}

// Connectors lists the connector kinds of the pinned plugins, sorted by kind.
func (s *PinSet) Connectors() []PinnedKind {
	if s == nil {
		return nil
	}
	out := make([]PinnedKind, 0, len(s.connectors))
	for kind, pin := range s.connectors {
		out = append(out, PinnedKind{Kind: kind, Pin: pin})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// Describe names the pinned plugins for startup logs.
func (s *PinSet) Describe() string {
	names := []string{}
	for _, p := range s.Pins() {
		names = append(names, p.Manifest.ID+"@"+p.Manifest.Version+" ["+strings.Join(p.Manifest.Contributions.Names(), ",")+"]")
	}
	return strings.Join(names, " ")
}

// PinsExtensionRegistry registers the declared extension namespaces of every
// pinned plugin beside the built-in ones; a nil set registers none.
func PinsExtensionRegistry(s *PinSet) (*content.ExtensionRegistry, error) {
	registry := content.NewExtensionRegistry()
	for _, p := range s.Pins() {
		if err := registry.Own(p.Manifest.ID, namespacesOf(&p.Manifest)...); err != nil {
			return nil, &PinError{Path: p.Path, Issues: []Issue{{Code: CodeNamespaceConflict, Path: "/extensions", Message: err.Error()}}}
		}
	}
	return registry, nil
}
