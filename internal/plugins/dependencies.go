package plugins

import (
	"fmt"
	"slices"
)

// CodePluginDependency reports a missing, incompatible or looping dependency.
const CodePluginDependency = "plugin_dependency"

// MaxProfileDepth includes the outer search and its dependency.
const MaxProfileDepth = 2

// RequiresProfile reports whether a manifest declares the full profile name.
func (m *Manifest) RequiresProfile(name string) bool {
	for _, r := range m.Requires {
		for _, p := range r.Profiles {
			if name == r.Plugin+"/"+p {
				return true
			}
		}
	}
	return false
}

// SupportsSearchBudget reports whether new budget fields are safe on the wire.
// Declared dependencies require feature-capable discovery. Without them, a wide
// compatibility range may still describe an older running implementation.
func (m *Manifest) SupportsSearchBudget() bool {
	r, err := ParseRange(m.Compatibility.PluginAPI)
	if err != nil {
		return false
	}
	version, ok := NegotiatePluginAPI(r)
	if !ok || !ResolveAPI(version).Speaks(FeatureProfileCandidates) {
		return false
	}
	if len(m.Requires) > 0 {
		return true
	}
	for _, version := range SupportedPluginAPIVersions {
		v, _ := ParseVersion(version)
		if r.Contains(v) && !ResolveAPI(version).Speaks(FeatureProfileCandidates) {
			return false
		}
	}
	return true
}

// SupportsCoverageSnapshots requires the declared range to exclude older
// implementations whose request schemas reject snapshot metadata.
func (m *Manifest) SupportsCoverageSnapshots() bool {
	r, err := ParseRange(m.Compatibility.PluginAPI)
	if err != nil {
		return false
	}
	version, ok := NegotiatePluginAPI(r)
	if !ok || !ResolveAPI(version).Speaks(FeatureCoverageSnapshots) {
		return false
	}
	for _, version := range SupportedPluginAPIVersions {
		v, _ := ParseVersion(version)
		if r.Contains(v) && !ResolveAPI(version).Speaks(FeatureCoverageSnapshots) {
			return false
		}
	}
	return true
}

// SupportsMultiPartSegments requires the declared range to exclude older
// implementations whose ingestion response schemas reject source ranges.
func (m *Manifest) SupportsMultiPartSegments() bool {
	r, err := ParseRange(m.Compatibility.PluginAPI)
	if err != nil {
		return false
	}
	version, ok := NegotiatePluginAPI(r)
	if !ok || !ResolveAPI(version).Speaks(FeatureMultiPartSegments) {
		return false
	}
	for _, version := range SupportedPluginAPIVersions {
		v, _ := ParseVersion(version)
		if r.Contains(v) && !ResolveAPI(version).Speaks(FeatureMultiPartSegments) {
			return false
		}
	}
	return true
}

func dependencyIssues(pins []*Pin) []Issue {
	byID := map[string]*Pin{}
	for _, p := range pins {
		byID[p.Manifest.ID] = p
	}
	var issues []Issue
	for i, p := range pins {
		for j, r := range p.Manifest.Requires {
			path := fmt.Sprintf("/plugins/%d/manifest/requires/%d", i, j)
			add := func(message string) {
				issues = append(issues, Issue{Code: CodePluginDependency, Path: path, PluginID: p.Manifest.ID, PluginVersion: p.Manifest.Version, Message: message})
			}
			dep := byID[r.Plugin]
			if dep == nil {
				add(fmt.Sprintf("%s requires %s; install it before activating this plugin", p.Manifest.ID, r.Plugin))
				continue
			}
			v, ve := ParseVersion(dep.Manifest.Version)
			ran, re := ParseRange(r.Version)
			if ve != nil || re != nil || !ran.Contains(v) {
				add(fmt.Sprintf("%s requires %s %s; installed version is %s", p.Manifest.ID, r.Plugin, r.Version, dep.Manifest.Version))
			}
			for _, name := range r.Profiles {
				profiles := retrievalOf(&dep.Manifest)
				if profiles == nil {
					add(fmt.Sprintf("%s provides no retrieval profiles", r.Plugin))
					break
				}
				if _, ok := profiles.Profiles[name]; !ok {
					add(fmt.Sprintf("%s does not declare required profile %q", r.Plugin, name))
				}
			}
		}
	}
	if len(issues) > 0 {
		return issues
	}
	// A bounded walk from every root also checks shared subgraphs at their
	// deepest occurrence, independent of installation order.
	var walk func(string, []string) string
	walk = func(id string, path []string) string {
		if slices.Contains(path, id) {
			return fmt.Sprintf("dependency cycle: %v -> %s", path, id)
		}
		if len(path) >= MaxProfileDepth {
			return fmt.Sprintf("dependency depth exceeds %d profile levels: %v -> %s", MaxProfileDepth, path, id)
		}
		for _, r := range byID[id].Manifest.Requires {
			if issue := walk(r.Plugin, append(slices.Clone(path), id)); issue != "" {
				return issue
			}
		}
		return ""
	}
	for i, p := range pins {
		if issue := walk(p.Manifest.ID, nil); issue != "" {
			issues = append(issues, Issue{Code: CodePluginDependency, Path: fmt.Sprintf("/plugins/%d/manifest/requires", i), PluginID: p.Manifest.ID, PluginVersion: p.Manifest.Version, Message: issue})
		}
	}
	return issues
}
