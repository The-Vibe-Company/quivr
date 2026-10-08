package app

import (
	"fmt"
	"sort"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// VectorIndexSetting is how the search index stores a space's vectors. An
// omitted quantization or rescore limit takes the deployment's, except that
// a space set to "none" has nothing to rescore. A rescore limit of 0 keeps
// the index's own default.
type VectorIndexSetting struct {
	Quantization string `json:"quantization,omitempty"`
	RescoreLimit *int   `json:"rescore_limit,omitempty"`
}

// VectorIndexConfig is the deployment's vector index setting, rq-8 when
// omitted, with overrides by space id ("<id>" or "<id>@<version>"). A change
// applies to generations built afterwards: new Corpora, and existing ones
// once rebuilt.
type VectorIndexConfig struct {
	VectorIndexSetting
	Spaces map[string]VectorIndexSetting `json:"spaces,omitempty"`
}

// For is the index setting of a space, by its registry key or declared id.
func (c VectorIndexConfig) For(key, id string) content.VectorIndex {
	out := content.DefaultVectorIndex
	override := c.override(key, id)
	for _, setting := range []VectorIndexSetting{c.VectorIndexSetting, override} {
		if setting.Quantization != "" {
			out.Quantization = setting.Quantization
		}
		if setting.RescoreLimit != nil {
			out.RescoreLimit = *setting.RescoreLimit
		}
	}
	if override.Quantization == content.QuantizationNone && override.RescoreLimit == nil {
		out.RescoreLimit = 0
	}
	return out
}

func (c VectorIndexConfig) override(key, id string) VectorIndexSetting {
	if setting, ok := c.Spaces[key]; ok {
		return setting
	}
	return c.Spaces[id]
}

// Validate refuses a deployment setting or override that resolves to an
// invalid index.
func (c VectorIndexConfig) Validate() error {
	if err := c.For("", "").Validate(); err != nil {
		return err
	}
	spaces := make([]string, 0, len(c.Spaces))
	for space := range c.Spaces {
		spaces = append(spaces, space)
	}
	sort.Strings(spaces)
	for _, space := range spaces {
		if err := c.For(space, space).Validate(); err != nil {
			return fmt.Errorf("space %s: %w", space, err)
		}
	}
	return nil
}
