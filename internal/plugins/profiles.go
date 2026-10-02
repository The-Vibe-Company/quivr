package plugins

import (
	"fmt"
	"sort"
	"strings"
)

// PinnedProfile is a plugin-local profile and its deployment names.
type PinnedProfile struct {
	Pin            *Pin
	Name, FullName string
	Aliases        []string
}

// ResolveRetrievalProfile selects a pin and plugin-local profile using only this
// pin set and the deployment aliases. Callers retain the returned pin for every
// round; replacing the active plan cannot change this selection.
func (s *PinSet) ResolveRetrievalProfile(profile string, aliases map[string]string) (PinnedProfile, bool) {
	if profile == "" || profile == "balanced" {
		profile = DefaultProfile
	}
	profiles, err := s.RetrievalProfiles(aliases)
	if err != nil {
		return PinnedProfile{}, false
	}
	for _, p := range profiles {
		if p.FullName == profile {
			return p, true
		}
		for _, alias := range p.Aliases {
			if alias == profile {
				return p, true
			}
		}
	}
	return PinnedProfile{}, false
}

// RetrievalProfiles resolves deployment aliases against all installed profiles.
// A single plugin without a map retains its profile names as aliases. Multiple
// plugins require an explicit map containing default. Full names always resolve.
func (s *PinSet) RetrievalProfiles(aliases map[string]string) ([]PinnedProfile, error) {
	pins := s.Retrievals()
	if aliases == nil && len(pins) > 1 {
		return nil, fmt.Errorf("retrieval.profiles must map default to plugin/profile when several retrieval plugins are pinned")
	}
	if aliases != nil && aliases["default"] == "" {
		return nil, fmt.Errorf("retrieval.profiles must contain default")
	}
	out := []PinnedProfile{}
	byName := map[string]int{}
	for _, pin := range pins {
		for _, name := range pin.Manifest.Contributions.Retrieval.ProfileNames() {
			full := pin.Manifest.ID + "/" + name
			byName[full] = len(out)
			out = append(out, PinnedProfile{Pin: pin, Name: name, FullName: full, Aliases: []string{}})
		}
	}
	if aliases == nil && len(pins) == 1 {
		if _, ok := pins[0].Manifest.Contributions.Retrieval.Profiles[DefaultProfile]; !ok {
			return nil, fmt.Errorf("retrieval.profiles must map default to plugin/profile when the retrieval plugin declares no default profile")
		}
		aliases = map[string]string{}
		for _, p := range out {
			// balanced has always selected default, even if a plugin declares it.
			// Keep such a profile installed and reachable by its full name.
			if p.Name == "balanced" {
				continue
			}
			aliases[p.Name] = p.FullName
		}
	}
	for _, alias := range sortedProfileAliases(aliases) {
		if strings.TrimSpace(alias) != alias || alias == "" || strings.Contains(alias, "/") || alias == "balanced" {
			return nil, fmt.Errorf("retrieval.profiles alias %q must be a nonempty short name without a slash; balanced is reserved for default", alias)
		}
		target := aliases[alias]
		i, ok := byName[target]
		if !ok {
			return nil, fmt.Errorf("retrieval.profiles[%q] names unknown profile %q", alias, target)
		}
		out[i].Aliases = append(out[i].Aliases, alias)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := false, false
		for _, a := range out[i].Aliases {
			left = left || a == "default"
		}
		for _, a := range out[j].Aliases {
			right = right || a == "default"
		}
		if left != right {
			return left
		}
		return out[i].FullName < out[j].FullName
	})
	return out, nil
}

func sortedProfileAliases(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
