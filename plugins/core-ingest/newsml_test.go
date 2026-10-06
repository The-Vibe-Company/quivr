package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// Run by plugin_sdk.sh after installing NewsML-G2. This composition owns the
// regression: real normalizer output must fit Go's extension budget and the
// ingestion recipe. Only the tokenizer dependency is replaced by words.
func TestNewsMLMessagesFitIngestion(t *testing.T) {
	python := os.Getenv("QUIVR_NEWSML_TEST_PYTHON")
	if python == "" {
		t.Skip("run through scripts/plugin_sdk.sh with the NewsML-G2 environment")
	}
	// Bodies are deliberately short and trimmed: one window each with words.
	// Long-window splitting is owned by the ingestion recipe/parity tests.
	for _, tc := range []struct {
		fixture string
		title   string
		bodies  []string
	}{
		{"multi-item", "Harbour reopened", []string{"Morning transport", "Ferry service restored", "The morning ferry leaves at nine.", "Le port accueille les voyageurs", "Transport maritime", "Le prochain départ est prévu à midi."}},
		{"oversized-header", "Harbour weather update", []string{"A calm afternoon is forecast for the harbour."}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			cmd := exec.Command(python, "-c", `
import json, sys
from pathlib import Path
from newsml_g2.normalizer import plugin
from quivr_plugin.testing import build_request, expect_response, invoke_fixture
fixture = Path('../newsml-g2/fixtures') / (sys.argv[1] + '.json')
first = expect_response(invoke_fixture(plugin, fixture)).to_dict()
second = expect_response(invoke_fixture(plugin, fixture)).to_dict()
assert first == second, 'normalization must replay deterministically'
source = first['manifest']['parts'][-1]
assert source['role'] == 'source'
assert source['content']['blob_id'] == build_request(fixture).input.blob_id
print(json.dumps(first))
`, tc.fixture)
			raw, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("normalizer: %v\n%s", err, raw)
			}
			var response struct {
				Manifest struct {
					Parts []struct {
						Key, Role  string
						Content    struct{ Kind, Text string }
						Extensions map[string]any
					}
				}
				Extensions map[string]struct {
					Data          map[string]any `json:"data"`
					SchemaVersion string         `json:"schema_version"`
				}
				Warnings []struct{ Code string }
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			// Go encoding/json escapes HTML and U+2028/U+2029, unlike the
			// Python wire encoding. This is the engine's exact size rule.
			extensions, err := json.Marshal(response.Extensions)
			if err != nil || len(extensions) > 65536 {
				t.Fatalf("extension JSON: %d bytes, %v", len(extensions), err)
			}
			if tc.fixture == "oversized-header" {
				for _, namespace := range []string{"newsml-g2.document", "newsml-g2.xml", "newsml-g2.headers"} {
					if response.Extensions[namespace].Data["truncated"] != true {
						t.Errorf("%s has no truncation marker", namespace)
					}
				}
				if len(response.Warnings) != 1 || response.Warnings[0].Code != "extensions_truncated" {
					t.Errorf("missing truncation diagnostic: %+v", response.Warnings)
				}
			}
			if tc.fixture == "oversized-header" {
				if response.Extensions["quivr.metadata"].Data["source_type"] != "news_item" {
					t.Fatal("bounded shared metadata lost required source_type")
				}
				if _, ok := response.Extensions["quivr.metadata"].Data["tags"]; ok {
					t.Fatal("oversize shared tags were not bounded")
				}
			}
			var parts []Part
			var titles, bodies []string
			seen := map[string]bool{}
			for _, p := range response.Manifest.Parts {
				if seen[p.Key] {
					t.Errorf("duplicate Part key %s", p.Key)
				}
				seen[p.Key] = true
				partExtensions, err := json.Marshal(p.Extensions)
				if err != nil || len(partExtensions) > 65536 {
					t.Fatalf("Part %s extension JSON: %d bytes, %v", p.Key, len(partExtensions), err)
				}
				if p.Content.Kind != "text" {
					continue
				}
				parts = append(parts, Part{Key: p.Key, Role: p.Role, Text: p.Content.Text})
				if p.Role == "title" {
					titles = append(titles, p.Content.Text)
				} else if p.Role == "body" {
					bodies = append(bodies, p.Content.Text)
				}
			}
			if !reflect.DeepEqual(titles, []string{tc.title}) || !reflect.DeepEqual(bodies, tc.bodies) {
				t.Fatalf("text mapping: titles %q, bodies %q", titles, bodies)
			}
			windows, err := (TokenWindows{Tokenizer: words{}}).Process(context.Background(), parts)
			if err != nil {
				t.Fatalf("core.ingest refused normalized message: %v", err)
			}
			var excerpts []string
			for _, w := range windows {
				for _, p := range parts {
					if p.Key == w.PartKey {
						excerpts = append(excerpts, string([]rune(p.Text)[w.Start:w.End]))
					}
				}
				if !strings.Contains(w.Derivation.ModelInput, tc.title) {
					t.Errorf("segment lost title: %q", w.Derivation.ModelInput)
				}
			}
			if !reflect.DeepEqual(excerpts, tc.bodies) {
				t.Fatalf("searchable excerpts %q, want all bodies %q", excerpts, tc.bodies)
			}
		})
	}
}
