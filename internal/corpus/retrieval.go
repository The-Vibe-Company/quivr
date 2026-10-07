package corpus

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Field is one logical, typed source-field retrieval mapping. SourcePointer is
// an RFC 6901 JSON Pointer into a Version's canonical source view (its
// manifest, declared extensions and provenance), never a search-engine field.
type Field struct {
	Name          string   `json:"name"`
	SourcePointer string   `json:"source_pointer,omitempty"`
	Type          string   `json:"type"`
	Roles         []string `json:"roles"`
	Boost         *int     `json:"boost,omitempty"`
	Analyzer      string   `json:"analyzer,omitempty"`
	PartRole      string   `json:"part_role,omitempty"`
	PartKeyPrefix string   `json:"part_key_prefix,omitempty"`
	ValuePointer  string   `json:"value_pointer,omitempty"`
}

// Retrieval is a resolved Corpus retrieval configuration: the pinned profile
// and the effective fields after explicit overrides by logical name.
type Retrieval struct {
	PluginProfile string  `json:"plugin_profile,omitempty"`
	Fields        []Field `json:"fields"`
}

// Search-role field names with projection meaning. TitleField replaces the
// projected title; every other search field contributes additional text.
const TitleField = "title"

// profiles are the built-in plugin profiles. example.editorial is purely
// illustrative: it pairs with the example extension namespace so profile
// resolution and overrides can be exercised without a plugin platform. It is
// not a product default and carries no vertical vocabulary.
var profiles = map[string][]Field{
	"example.editorial": {{Name: TitleField, SourcePointer: "/extensions/example.editorial/data/headline", Type: "string", Roles: []string{"search"}}},
}

var (
	logicalName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	fieldTypes  = map[string]bool{"string": true, "number": true, "boolean": true, "datetime": true, "string_array": true}
	// sourceRoots are the canonical source-view members a mapping may address.
	sourceRoots = map[string]bool{"manifest": true, "extensions": true, "provenance": true}
)

// ResolveRetrieval validates a requested configuration and resolves it against
// its profile. declared reports whether an extension namespace is declared by
// the deployment; nil declares none. Invalid mappings, including pointers into
// undeclared namespaces or engine-shaped names, return ErrInvalidMapping.
func ResolveRetrieval(raw map[string]any, declared func(string) bool) (Retrieval, error) {
	body, err := json.Marshal(raw)
	if err != nil {
		return Retrieval{}, ErrInvalidMapping
	}
	var requested Retrieval
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&requested); err != nil {
		return Retrieval{}, ErrInvalidMapping
	}
	out := Retrieval{PluginProfile: requested.PluginProfile, Fields: []Field{}}
	if _, ok := raw["plugin_profile"]; ok {
		defaults, ok := profiles[requested.PluginProfile]
		if !ok {
			return Retrieval{}, ErrUnsupportedProfile
		}
		out.Fields = append(out.Fields, defaults...)
	}
	seen := map[string]bool{}
	for _, f := range requested.Fields {
		if seen[f.Name] || !validField(f, declared) {
			return Retrieval{}, ErrInvalidMapping
		}
		seen[f.Name] = true
		replaced := false
		for i := range out.Fields {
			if out.Fields[i].Name == f.Name {
				out.Fields[i], replaced = f, true
			}
		}
		if !replaced {
			out.Fields = append(out.Fields, f)
		}
	}
	return out, nil
}

func validField(f Field, declared func(string) bool) bool {
	if !logicalName.MatchString(f.Name) || !fieldTypes[f.Type] || len(f.Roles) == 0 {
		return false
	}
	roles := map[string]bool{}
	for _, role := range f.Roles {
		if roles[role] || (role != "search" && role != "filter") {
			return false
		}
		if role == "search" && f.Type != "string" && f.Type != "string_array" {
			return false
		}
		roles[role] = true
	}
	if f.Boost != nil && (!f.Searchable() || *f.Boost < 1 || *f.Boost > 100) {
		return false
	}
	if f.Analyzer != "" && (f.Analyzer != "french_light" || !f.Searchable()) {
		return false
	}
	if f.ValuePointer != "" && (f.Type != "string_array" || !validPointer(f.ValuePointer)) {
		return false
	}
	if f.PartRole != "" {
		return f.SourcePointer == "" && f.ValuePointer == "" && f.Searchable() && (f.PartRole == "title" || f.PartRole == "body" || f.PartRole == "caption" || f.PartRole == "transcript") && utf8.RuneCountInString(f.PartKeyPrefix) <= 200
	}
	if f.PartKeyPrefix != "" {
		return false
	}
	tokens, ok := PointerTokens(f.SourcePointer)
	if !ok || len(tokens) < 2 || !sourceRoots[tokens[0]] {
		return false
	}
	if tokens[0] == "extensions" && (declared == nil || !declared(tokens[1])) {
		return false
	}
	return true
}

// PointerTokens splits and unescapes an RFC 6901 JSON Pointer.
func PointerTokens(p string) ([]string, bool) {
	if !validPointer(p) {
		return nil, false
	}
	parts := strings.Split(p[1:], "/")
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts, true
}

// Searchable reports whether the field contributes projected search text.
func (f Field) Searchable() bool {
	for _, role := range f.Roles {
		if role == "search" {
			return true
		}
	}
	return false
}

// EffectiveBoost is the integer BM25F weight; omitted configuration means one.
func (f Field) EffectiveBoost() int {
	if f.Boost == nil {
		return 1
	}
	return *f.Boost
}
