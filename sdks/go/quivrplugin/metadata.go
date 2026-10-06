package quivrplugin

import (
	"encoding/json"
	"strings"
)

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
// The serialized common extension is kept below the helper's 8 KiB budget;
// callers still own the combined size of all extensions in one item.
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
	boundCommonMetadataJSON(data)
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
	out := make([]string, 0, min(len(values), 50))
	for _, value := range values {
		if value = boundedMetadataString(value); value != "" {
			out = append(out, value)
			if len(out) == 50 {
				break
			}
		}
	}
	return out
}

// The engine bounds the combined extension JSON at 64 KiB. Keep this common
// extension below 8 KiB so a connector's own extension can use the remaining
// space (RSS, for example, allows 48 KiB for its owned extension). This budget
// applies to the common extension alone; callers with other large extensions
// still own the combined-payload check. Trimming list tails preserves the
// first values and makes the helper safe to use without a second common-only
// size check in every connector.
const commonMetadataJSONBudget = 8 << 10

var commonMetadataListKeys = [...]string{"author", "subjects", "tags", "country", "place"}

func boundCommonMetadataJSON(data map[string]any) {
	for commonMetadataJSONSize(data) > commonMetadataJSONBudget {
		removed := false
		for _, key := range commonMetadataListKeys {
			values, ok := data[key].([]string)
			if !ok || len(values) == 0 {
				continue
			}
			if len(values) == 1 {
				delete(data, key)
			} else {
				data[key] = values[:len(values)-1]
			}
			removed = true
			break
		}
		if !removed {
			return
		}
	}
}

func commonMetadataJSONSize(data map[string]any) int {
	b, err := json.Marshal(map[string]Extension{
		CommonMetadataNamespace: {SchemaVersion: CommonMetadataVersion, Data: data},
	})
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return len(b)
}
