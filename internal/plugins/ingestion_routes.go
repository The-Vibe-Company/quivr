package plugins

import (
	"fmt"
	"sort"
)

// IngestionRouting selects the ingestion plugin for a whole Version. Routes
// are keyed by the source Blob media type; the normalized media type does not
// change this choice.
type IngestionRouting struct {
	Default string            `json:"default,omitempty"`
	Routes  map[string]string `json:"routes,omitempty"`
}

// ConfigureIngestion validates and installs the source-media routing for the
// set. An empty default uses core.ingest when pinned, or the only ingestion
// plugin. A set with several ingestion plugins must name a default explicitly.
func (s *PinSet) ConfigureIngestion(c IngestionRouting) error {
	if s == nil {
		if c.Default != "" || len(c.Routes) > 0 {
			return ingestionRoutingError("/ingestion", "no ingestion plugins are pinned")
		}
		return nil
	}
	defaultID := c.Default
	if defaultID == "" {
		switch {
		case s.ingestions["core.ingest"] != nil:
			defaultID = "core.ingest"
		case len(s.ingestions) == 1:
			for id := range s.ingestions {
				defaultID = id
			}
		case len(s.ingestions) > 1:
			return ingestionRoutingError("/ingestion/default", "several ingestion plugins are pinned; choose one as the default")
		}
	}
	if defaultID != "" {
		if _, ok := s.ingestions[defaultID]; !ok {
			return ingestionRoutingError("/ingestion/default", fmt.Sprintf("plugin %q is not a pinned ingestion plugin", defaultID))
		}
	} else if len(c.Routes) > 0 {
		return ingestionRoutingError("/ingestion/routes", "routes require a pinned ingestion plugin")
	}
	var routes map[string]string
	if len(c.Routes) > 0 {
		routes = make(map[string]string, len(c.Routes))
	}
	mediaTypes := make([]string, 0, len(c.Routes))
	for mediaType := range c.Routes {
		mediaTypes = append(mediaTypes, mediaType)
	}
	sort.Strings(mediaTypes)
	for _, mediaType := range mediaTypes {
		pluginID := c.Routes[mediaType]
		if mediaType == "" {
			return ingestionRoutingError("/ingestion/routes", "a source media type cannot be empty")
		}
		if _, ok := s.ingestions[pluginID]; !ok {
			return ingestionRoutingError("/ingestion/routes/"+pointerToken(mediaType), fmt.Sprintf("plugin %q is not a pinned ingestion plugin", pluginID))
		}
		routes[mediaType] = pluginID
	}
	s.ingestionRouting = IngestionRouting{Default: defaultID, Routes: routes}
	s.ingestion = s.ingestions[defaultID]
	return nil
}

func ingestionRoutingError(path, message string) error {
	return &PinError{Path: "ingestion", Issues: []Issue{{Code: CodeInvalidPin, Path: path, Message: message}}}
}

// IngestionFor returns the plugin selected by source media type. An unmapped
// source uses the configured default.
func (s *PinSet) IngestionFor(mediaType string) *Pin {
	if s == nil {
		return nil
	}
	if mediaType == "" {
		mediaType = "text/plain"
	}
	if id := s.ingestionRouting.Routes[mediaType]; id != "" {
		return s.ingestions[id]
	}
	return s.ingestion
}

// Ingestions lists the pinned ingestion plugins sorted by plugin id.
func (s *PinSet) Ingestions() []*Pin {
	if s == nil {
		return nil
	}
	ids := make([]string, 0, len(s.ingestions))
	for id := range s.ingestions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*Pin, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.ingestions[id])
	}
	return out
}

// SpaceOwner returns the pinned ingestion plugin that declares a space key.
// Declared spaces are considered even when the pin does not enable that space
// for serving or evaluation.
func (s *PinSet) SpaceOwner(key string) *Pin {
	if s == nil {
		return nil
	}
	return s.spaces[key]
}

// IngestionRouting returns a copy of the configured routing object.
func (s *PinSet) IngestionRouting() IngestionRouting {
	if s == nil {
		return IngestionRouting{}
	}
	var routes map[string]string
	if len(s.ingestionRouting.Routes) > 0 {
		routes = make(map[string]string, len(s.ingestionRouting.Routes))
		for mediaType, pluginID := range s.ingestionRouting.Routes {
			routes[mediaType] = pluginID
		}
	}
	return IngestionRouting{Default: s.ingestionRouting.Default, Routes: routes}
}
