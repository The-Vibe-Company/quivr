package quivrplugin

import "strings"

// CommonMetadataNamespace is the reserved extension namespace available to
// every connector and normalizer.
const CommonMetadataNamespace = "quivr.metadata"

// CommonMetadataVersion is the first version of the shared metadata contract.
const CommonMetadataVersion = "1"

// CommonMetadata contains source metadata that the engine can expose through
// the same contract for every first-party plugin. Empty values are omitted.
type CommonMetadata struct {
	Language    string
	PublishedAt string
	SourceType  string
	Source      string
	Author      []string
	Subjects    []string
	Tags        []string
	Country     []string
	Place       []string
}

// CommonMetadataExtension builds the reserved extension using only the
// contract's wire keys. Values are bounded before they leave a plugin; date
// syntax is intentionally left for the engine contract validator to reject.
func CommonMetadataExtension(metadata CommonMetadata) Extension {
	data := map[string]any{}
	put := func(key, value string) {
		if value = boundedMetadataString(value); value != "" {
			data[key] = value
		}
	}
	put("language", metadata.Language)
	put("published_at", metadata.PublishedAt)
	put("source_type", metadata.SourceType)
	put("source", metadata.Source)
	putList := func(key string, values []string) {
		bounded := boundedMetadataList(values)
		if len(bounded) > 0 {
			data[key] = bounded
		}
	}
	putList("author", metadata.Author)
	putList("subjects", metadata.Subjects)
	putList("tags", metadata.Tags)
	putList("country", metadata.Country)
	putList("place", metadata.Place)
	return Extension{SchemaVersion: CommonMetadataVersion, Data: data}
}

func boundedMetadataString(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}

func boundedMetadataList(values []string) []string {
	if len(values) > 50 {
		values = values[:50]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = boundedMetadataString(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
