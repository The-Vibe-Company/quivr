package postgres

import (
	"maps"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// Apply only formats changed by the installation plan. Existing corpora can
// deliberately retain a different owner after a configuration reload.
func changedIngestionRouting(current, previous, next content.IngestionRouting) content.IngestionRouting {
	result := content.IngestionRouting{Default: current.Default, Routes: maps.Clone(current.Routes)}
	if result.Routes == nil {
		result.Routes = map[string]string{}
	}
	if previous.Default != next.Default {
		result.Default = next.Default
	}
	formats := map[string]bool{}
	for media := range current.Routes {
		formats[media] = true
	}
	for media := range previous.Routes {
		formats[media] = true
	}
	for media := range next.Routes {
		formats[media] = true
	}
	for media := range formats {
		owner := current.For(media)
		if previous.For(media) != next.For(media) {
			owner = next.For(media)
		}
		if owner == result.Default {
			delete(result.Routes, media)
		} else {
			result.Routes[media] = owner
		}
	}
	return result
}
