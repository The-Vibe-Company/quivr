package registry

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
)

// IngestionProvenance identifies the immutable derivation's original inputs.
// Invocation configuration and discovery still use the current installed build.
func IngestionProvenance(pin *plugins.Pin) json.RawMessage {
	if pin.IngestionDerivation != nil {
		return canonicalJSON(pin.IngestionDerivation.Provenance)
	}
	raw, _ := json.Marshal(map[string]string{"plugin_id": pin.Manifest.ID, "plugin_version": pin.Manifest.Version,
		"manifest_digest": pin.ManifestDigest, "configuration_digest": content.Hash(SettingsOf(pin).Configuration)})
	return raw
}

// BindIngestionRecipes binds a new plan to the recipes it replaces. Callers
// serialize it with plan activation, not registration, so delayed activation
// and rollback inherit the currently served lineage. Historical plans stay exact.
func BindIngestionRecipes(previous, next Plan, registrations map[string]Registration) ([]Assignment, error) {
	old := map[string]*plugins.Pin{}
	for _, a := range canonicalRoles(previous.Roles, registrations) {
		r := registrations[a.RegistrationID]
		if a.Role != ingestionMembershipRole(r.PluginID) {
			continue
		}
		pin, err := r.Pin()
		if err != nil {
			// An unresolvable retired build cannot supply an anchor.
			continue
		}
		pin.IngestionDerivation = a.IngestionDerivation
		old[r.PluginID] = pin
	}
	out := slices.Clone(next.Roles)
	for i, a := range out {
		out[i].IngestionDerivation = nil
		r := registrations[a.RegistrationID]
		if a.Role != ingestionMembershipRole(r.PluginID) {
			continue
		}
		pin, err := r.Pin()
		if err != nil {
			return nil, err
		}
		if before := old[r.PluginID]; before != nil && sameIngestionInputs(before, pin) {
			pin = before
		} else if next.Source == SourceRollback {
			// A semantic rollback restores the target's historical derivation.
			// Equivalent tuning above instead keeps the currently served lineage.
			pin.IngestionDerivation = a.IngestionDerivation
		}
		out[i].IngestionDerivation = &plugins.IngestionDerivation{Recipe: IngestionRecipe(pin), Provenance: IngestionProvenance(pin)}
	}
	return out, nil
}

func sameIngestionInputs(before, next *plugins.Pin) bool {
	if before.Manifest.ID != next.Manifest.ID || before.Manifest.Version != next.Manifest.Version {
		return false
	}
	a, b := executionKeys(before), executionKeys(next)
	if len(a) > 0 && len(b) > 0 && !slices.Equal(a, b) {
		return false
	}
	keys := a
	if len(keys) == 0 {
		keys = b
	}
	if len(keys) == 0 {
		return before.ManifestDigest == next.ManifestDigest && bytes.Equal(SettingsOf(before).Configuration, SettingsOf(next).Configuration)
	}
	return bytes.Equal(semanticInputs(before, keys), semanticInputs(next, keys))
}

func executionKeys(pin *plugins.Pin) []string {
	if pin.Manifest.Configuration == nil {
		return nil
	}
	keys := slices.Clone(pin.Manifest.Configuration.ExecutionKeys)
	sort.Strings(keys)
	return keys
}

// semanticInputs excludes only the declared top-level execution settings and
// their schema constraints. All other manifest content remains recipe input;
// raw manifest digests still identify builds for discovery and registration.
func semanticInputs(pin *plugins.Pin, keys []string) json.RawMessage {
	var manifest map[string]any
	// Source has already passed admission; use that identical YAML conversion.
	document, _ := plugins.ManifestJSON(pin.Source)
	manifestDecoder := json.NewDecoder(bytes.NewReader(document))
	manifestDecoder.UseNumber()
	_ = manifestDecoder.Decode(&manifest)
	if config, ok := manifest["configuration"].(map[string]any); ok {
		delete(config, "execution_keys")
		if schema, ok := config["schema"].(map[string]any); ok {
			if props, ok := schema["properties"].(map[string]any); ok {
				for _, key := range keys {
					delete(props, key)
				}
			}
			if required, ok := schema["required"].([]any); ok {
				filtered := []any{}
				for _, key := range required {
					name, _ := key.(string)
					if !slices.Contains(keys, name) {
						filtered = append(filtered, key)
					}
				}
				schema["required"] = filtered
			}
		}
	}
	var configuration map[string]any
	decoder := json.NewDecoder(bytes.NewReader(SettingsOf(pin).Configuration))
	decoder.UseNumber()
	_ = decoder.Decode(&configuration)
	for _, key := range keys {
		delete(configuration, key)
	}
	raw, _ := json.Marshal([]any{manifest, configuration})
	return canonicalJSON(raw)
}
