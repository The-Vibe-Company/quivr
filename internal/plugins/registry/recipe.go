package registry

import (
	"bytes"
	"encoding/json"
	"reflect"
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
		before := old[r.PluginID]
		if before != nil && (sameIngestionInputs(before, pin) ||
			// Legacy rollback targets predate execution declarations. Preserve
			// their established tuning lineage, without allowing a forward
			// deployment to remove declarations and hide semantic changes.
			next.Source == SourceRollback && len(executionKeys(pin)) == 0 && sameIngestionInputs(pin, before)) {
			pin = before
		} else if next.Source == SourceRollback {
			// A semantic rollback restores the target's historical derivation.
			// Equivalent tuning above instead keeps the currently served lineage.
			pin.IngestionDerivation = a.IngestionDerivation
		}
		if before != nil && pin != before && next.Source != SourceRollback && nativeIngestionRecipe(pin) == nativeIngestionRecipe(before) {
			// A rejected declaration change can hide a semantic field in a
			// composed schema without changing the native hash. Fence that
			// unproven transition with its full build identity; normal proven
			// execution tuning never enters this recipe branch.
			pin.IngestionDerivation = &plugins.IngestionDerivation{
				Recipe:     "plugin:" + pin.Manifest.ID + "@" + pin.Manifest.Version + "#" + content.StableID("ingestion", IngestionRecipe(before), pin.ManifestDigest, string(SettingsOf(pin).Configuration)),
				Provenance: IngestionProvenance(pin),
			}
		}
		out[i].IngestionDerivation = &plugins.IngestionDerivation{Recipe: IngestionRecipe(pin), Provenance: IngestionProvenance(pin)}
	}
	return out, nil
}

func sameIngestionInputs(before, next *plugins.Pin) bool {
	if before == nil || next == nil {
		return false
	}
	if before.Manifest.ID != next.Manifest.ID || before.Manifest.Version != next.Manifest.Version {
		return false
	}
	a, b := executionKeys(before), executionKeys(next)
	if len(a) > 0 {
		for _, key := range a {
			if !slices.Contains(b, key) {
				return false
			}
		}
	}
	for _, key := range b {
		// The first declaration may adopt explicitly supplied legacy tuning
		// settings. It cannot reclassify a field described by the old schema.
		if !slices.Contains(a, key) && (schemaSemanticSetting(before, key) || len(a) > 0 && configuredSetting(before, key)) {
			return false
		}
	}
	keys := b
	if len(keys) == 0 {
		keys = a
	}
	if len(keys) == 0 {
		return before.ManifestDigest == next.ManifestDigest && bytes.Equal(SettingsOf(before).Configuration, SettingsOf(next).Configuration)
	}
	return bytes.Equal(semanticInputs(before, keys), semanticInputs(next, keys))
}

func configuredSetting(pin *plugins.Pin, key string) bool {
	var configuration map[string]json.RawMessage
	_ = json.Unmarshal(pin.Configuration, &configuration)
	_, exists := configuration[key]
	return exists
}

func hasSemanticSetting(pin *plugins.Pin, key string) bool {
	return configuredSetting(pin, key) || schemaSemanticSetting(pin, key)
}

func schemaSemanticSetting(pin *plugins.Pin, key string) bool {
	if pin.Manifest.Configuration != nil {
		var keywords map[string]json.RawMessage
		if err := json.Unmarshal(pin.Manifest.Configuration.Schema, &keywords); err != nil {
			return true
		}
		// Only a simple object schema proves that an absent setting was not
		// already semantic. References, composition, wildcard constraints and
		// unknown keywords may describe it elsewhere; refuse reclassification.
		for keyword, value := range keywords {
			switch keyword {
			case "$schema", "$id", "$anchor", "$comment", "$defs", "definitions",
				"title", "description", "type", "properties", "required", "minProperties", "maxProperties":
			case "additionalProperties":
				if v := string(bytes.TrimSpace(value)); v != "true" && v != "false" {
					return true
				}
			default:
				return true
			}
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		_ = json.Unmarshal(pin.Manifest.Configuration.Schema, &schema)
		_, exists := schema.Properties[key]
		return exists || slices.Contains(schema.Required, key)
	}
	return false
}

// IngestionReplacement uses an active build only when its declared execution
// changes cannot affect the historical pin's results. Its serving manifest and
// settings change together; work ownership, stop checks and invocation identity
// remain attached to the original registration.
func IngestionReplacement(before, next *plugins.Pin) *plugins.Pin {
	if before == next {
		return before
	}
	if before == nil || next == nil || before.Manifest.Contributions.Ingestion == nil || next.Manifest.Contributions.Ingestion == nil ||
		!reflect.DeepEqual(before.Spaces, next.Spaces) || !sameIngestionInputs(before, next) {
		return before
	}
	a, b := executionKeys(before), executionKeys(next)
	for _, key := range a {
		if !slices.Contains(b, key) {
			return before
		}
	}
	for _, key := range b {
		if !slices.Contains(a, key) && hasSemanticSetting(before, key) {
			return before
		}
	}
	return &plugins.Pin{
		Manifest: next.Manifest, ManifestDigest: next.ManifestDigest, Source: next.Source,
		Path: next.Path, Endpoint: next.Endpoint, Configuration: next.Configuration,
		Kinds: next.Kinds, Spaces: next.Spaces, Registration: before.Registration, KeyIdentity: before.Generation(),
		IngestionDerivation: &plugins.IngestionDerivation{Recipe: IngestionRecipe(before), Provenance: IngestionProvenance(before)},
	}
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
