package quivrplugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommonMetadataExtensionFiltersBeforeCappingLists(t *testing.T) {
	values := make([]string, 0, 102)
	for i := 0; i < 50; i++ {
		values = append(values, "")
	}
	values = append(values, "late value")
	for i := 0; i < 51; i++ {
		values = append(values, "value")
	}

	ext := CommonMetadataExtension(CommonMetadata{Tags: values})
	got, ok := ext.Data["tags"].([]string)
	if !ok {
		t.Fatalf("tags has type %T, want []string", ext.Data["tags"])
	}
	if len(got) != 50 || got[0] != "late value" {
		t.Fatalf("tags = %v, want 50 non-empty values beginning with the late value", got)
	}
	for _, value := range got {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("blank tag survived filtering: %q", value)
		}
	}
}

func TestCommonMetadataExtensionStaysWithinGenericJSONLimit(t *testing.T) {
	values := make([]string, 50)
	for i := range values {
		values[i] = strings.Repeat("界", 200)
	}
	ext := CommonMetadataExtension(CommonMetadata{
		Language:   "en",
		SourceType: "document",
		Source:     strings.Repeat("source", 200),
		Author:     values,
		Subjects:   values,
		Tags:       values,
		Country:    values,
		Place:      values,
	})
	commonOnly, err := json.Marshal(map[string]Extension{CommonMetadataNamespace: ext})
	if err != nil {
		t.Fatal(err)
	}
	const documentedCommonBudget = 8 << 10
	if len(commonOnly) > documentedCommonBudget {
		t.Fatalf("common metadata is %d bytes, exceeds the documented 8 KiB helper budget", len(commonOnly))
	}
	combined, err := json.Marshal(map[string]Extension{
		CommonMetadataNamespace: ext,
		"connector.rss":         {SchemaVersion: "1", Data: map[string]any{"payload": strings.Repeat("x", 48<<10)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(combined) > 64<<10 {
		t.Fatalf("common and 48 KiB connector metadata are %d bytes, exceeds the engine's 64 KiB generic JSON limit", len(combined))
	}
}
