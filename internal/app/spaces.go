package app

import (
	"encoding/json"

	"github.com/The-Vibe-Company/quivr-v2/internal/adapters/tei"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"github.com/The-Vibe-Company/quivr-v2/internal/plugins"
)

// DeploymentSpaces are the vector spaces this deployment registers: the
// enabled spaces of all pinned ingestion plugins with their roles. Without
// one (a bare `quivr migrate`), it is the legacy E5 space the engine served
// itself before the core.ingest plugin (THE-777). A registered space the
// list leaves out is retired: generations built later no longer carry it,
// while generations that serve it keep serving it until rebuilt.
func DeploymentSpaces(pins *plugins.PinSet) []content.RegisteredSpace {
	if len(pins.Ingestions()) > 0 {
		out := []content.RegisteredSpace{}
		for _, pin := range pins.Ingestions() {
			for _, s := range pin.EnabledSpaces() {
				out = append(out, content.RegisteredSpace{
					VectorSpace: content.VectorSpace{ID: s.Key, Manifest: plugins.SpaceManifest(pin.Manifest.ID, s.ID, s.Space), Dimensions: s.Space.Dimensions},
					Name:        s.ID, Version: s.Space.Version, OwnerPluginID: pin.Manifest.ID, OwnerPluginVersion: pin.Manifest.Version,
					Model: s.Space.Model, Metric: s.Space.Metric, Indexes: s.Space.Indexes, QueryModalities: s.Space.QueryModalities, Role: s.Role,
				})
			}
		}
		return out
	}
	return []content.RegisteredSpace{BuiltinSpace()}
}

// BuiltinSpace is the registry entry of the legacy E5 space, owned by the
// engine: generations built before the core.ingest plugin serve it until
// their Corpus is rebuilt.
func BuiltinSpace() content.RegisteredSpace {
	space := tei.Space()
	var m struct {
		Model    string `json:"model_repository"`
		Revision string `json:"model_revision"`
	}
	_ = json.Unmarshal(space.Manifest, &m)
	return content.RegisteredSpace{VectorSpace: space, Name: "quivr.e5_small", Version: m.Revision, Model: m.Model, Metric: "cosine",
		Indexes: []string{"text"}, QueryModalities: []string{"text"}, Role: content.SpaceServed}
}
