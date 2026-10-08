package corpus_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

func declared(ns string) bool { return ns == "example.editorial" }

func raw(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestResolveRetrievalRejectsInvalidAndRawEngineMappings(t *testing.T) {
	field := func(name, pointer, typ, roles string) string {
		return `{"fields":[{"name":"` + name + `","source_pointer":"` + pointer + `","type":"` + typ + `","roles":` + roles + `}]}`
	}
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"engine property name", field("corpusId", "/provenance/a", "string", `["search"]`), corpus.ErrInvalidMapping},
		{"engine boost syntax", field("title^2", "/provenance/a", "string", `["search"]`), corpus.ErrInvalidMapping},
		{"engine internal name", field("_additional", "/provenance/a", "string", `["search"]`), corpus.ErrInvalidMapping},
		{"undeclared root", field("headline", "/metadata/headline", "string", `["search"]`), corpus.ErrInvalidMapping},
		{"raw engine path", field("headline", "/properties/title", "string", `["search"]`), corpus.ErrInvalidMapping},
		{"bare root", field("headline", "/provenance", "string", `["search"]`), corpus.ErrInvalidMapping},
		{"undeclared extension namespace", field("headline", "/extensions/other.ns/data/headline", "string", `["search"]`), corpus.ErrInvalidMapping},
		{"bad escape", field("headline", "/provenance/~2bad", "string", `["search"]`), corpus.ErrInvalidMapping},
		{"relative pointer", field("headline", "provenance/a", "string", `["search"]`), corpus.ErrInvalidMapping},
		{"search on number", field("score", "/provenance/score", "number", `["search"]`), corpus.ErrInvalidMapping},
		{"unknown type", field("score", "/provenance/score", "integer", `["filter"]`), corpus.ErrInvalidMapping},
		{"unknown role", field("score", "/provenance/score", "number", `["sort"]`), corpus.ErrInvalidMapping},
		{"no roles", field("score", "/provenance/score", "number", `[]`), corpus.ErrInvalidMapping},
		{"duplicate role", field("score", "/provenance/score", "number", `["filter","filter"]`), corpus.ErrInvalidMapping},
		{"unknown field key", `{"fields":[{"name":"a","source_pointer":"/provenance/a","type":"string","roles":["search"],"engine_field":"title"}]}`, corpus.ErrInvalidMapping},
		{"unknown config key", `{"engine":{"title":"x"}}`, corpus.ErrInvalidMapping},
		{"duplicate names", `{"fields":[{"name":"a","source_pointer":"/provenance/a","type":"string","roles":["search"]},{"name":"a","source_pointer":"/provenance/b","type":"string","roles":["search"]}]}`, corpus.ErrInvalidMapping},
		{"unknown profile", `{"plugin_profile":"missing.profile"}`, corpus.ErrUnsupportedProfile},
	} {
		if _, err := corpus.ResolveRetrieval(raw(t, tc.body), declared); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// Without a declared namespace registry no extension pointer is accepted.
	if _, err := corpus.ResolveRetrieval(raw(t, `{"fields":[{"name":"t","source_pointer":"/extensions/example.editorial/data/headline","type":"string","roles":["search"]}]}`), nil); !errors.Is(err, corpus.ErrInvalidMapping) {
		t.Fatalf("extension without registry: %v", err)
	}
}

func TestResolveRetrievalAcceptsTypedFieldsAndProfileOverrides(t *testing.T) {
	got, err := corpus.ResolveRetrieval(raw(t, `{}`), declared)
	if err != nil || got.PluginProfile != "" || got.Fields == nil || len(got.Fields) != 0 {
		t.Fatalf("empty config: %+v %v", got, err)
	}
	got, err = corpus.ResolveRetrieval(raw(t, `{"fields":[
		{"name":"title","source_pointer":"/extensions/example.editorial/data/headline","type":"string","roles":["search"]},
		{"name":"tags","source_pointer":"/manifest/parts/0/role","type":"string_array","roles":["search","filter"]},
		{"name":"published","source_pointer":"/provenance/published","type":"datetime","roles":["filter"]},
		{"name":"urgent","source_pointer":"/provenance/~0tilde/~1slash","type":"boolean","roles":["filter"]}]}`), declared)
	if err != nil || len(got.Fields) != 4 || got.Fields[1].Roles[1] != "filter" {
		t.Fatalf("typed fields: %+v %v", got, err)
	}
	// The illustrative profile supplies a default title; an explicit field of
	// the same logical name overrides it and new names are appended.
	got, err = corpus.ResolveRetrieval(raw(t, `{"plugin_profile":"example.editorial"}`), declared)
	if err != nil || got.PluginProfile != "example.editorial" || len(got.Fields) != 1 || got.Fields[0].Name != "title" || got.Fields[0].SourcePointer != "/extensions/example.editorial/data/headline" {
		t.Fatalf("profile defaults: %+v %v", got, err)
	}
	got, err = corpus.ResolveRetrieval(raw(t, `{"plugin_profile":"example.editorial","fields":[
		{"name":"summary","source_pointer":"/provenance/summary","type":"string","roles":["search"]},
		{"name":"title","source_pointer":"/provenance/title","type":"string","roles":["search"]}]}`), declared)
	if err != nil || len(got.Fields) != 2 || got.Fields[0].Name != "title" || got.Fields[0].SourcePointer != "/provenance/title" || got.Fields[1].Name != "summary" {
		t.Fatalf("profile override: %+v %v", got, err)
	}
	// Resolution is deterministic, so equal requests share canonical bytes.
	again, _ := corpus.ResolveRetrieval(raw(t, `{"plugin_profile":"example.editorial","fields":[
		{"name":"summary","source_pointer":"/provenance/summary","type":"string","roles":["search"]},
		{"name":"title","source_pointer":"/provenance/title","type":"string","roles":["search"]}]}`), declared)
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatalf("non-deterministic resolution %s %s", a, b)
	}
}

// Owns literal configuration keys and omitted-option compatibility for item fields.
func TestItemFieldConfiguration(t *testing.T) {
	for _, n := range []int{200, 201} {
		body := `{"fields":[{"name":"body","part_role":"body","part_key_prefix":"` + strings.Repeat("é", n) + `","type":"string","roles":["search"]}]}`
		_, err := corpus.ResolveRetrieval(raw(t, body), declared)
		if (err == nil) != (n == 200) {
			t.Fatalf("Unicode prefix length %d: %v", n, err)
		}
	}
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"fields":[{"name":"headline","part_role":"title","type":"string","roles":["search"],"boost":6}]}`, true},
		{`{"fields":[{"name":"slugline","part_role":"body","part_key_prefix":"slugline-","type":"string","roles":["search"],"boost":4,"analyzer":"french_light"}]}`, true},
		{`{"fields":[{"name":"body","part_role":"body","type":"string","roles":["search"],"analyzer":"folded"}]}`, true},
		{`{"fields":[{"name":"labels","source_pointer":"/provenance/subjects","value_pointer":"/names","type":"string_array","roles":["search"],"boost":3}]}`, true},
		{`{"fields":[{"name":"body","source_pointer":"/provenance/body","type":"string","roles":["search"]}]}`, true},
		{`{"fields":[{"name":"body","part_role":"body","type":"string","roles":["search"],"boost":1.5}]}`, false},
		{`{"fields":[{"name":"body","part_role":"body","type":"string","roles":["search"],"boost":0}]}`, false},
		{`{"fields":[{"name":"body","part_role":"body","source_pointer":"/provenance/body","type":"string","roles":["search"]}]}`, false},
		{`{"fields":[{"name":"body","part_key_prefix":"p-","type":"string","roles":["search"]}]}`, false},
		{`{"fields":[{"name":"code","source_pointer":"/provenance/code","type":"string","roles":["filter"],"boost":2}]}`, false},
		{`{"fields":[{"name":"body","part_role":"body","type":"string","roles":["search"],"analyzer":"unknown"}]}`, false},
	} {
		_, err := corpus.ResolveRetrieval(raw(t, tc.body), declared)
		if (err == nil) != tc.valid {
			t.Errorf("config %s: %v, valid=%v", tc.body, err, tc.valid)
		}
	}
}
