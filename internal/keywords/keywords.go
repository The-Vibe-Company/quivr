// Package keywords holds the keyword analyzers a Corpus field can name. An
// analyzer writes a separate copy of the field's text at ingestion and
// normalizes queries the same way; canonical text and vectors never change.
package keywords

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Analyzer normalizes keyword text for one named field configuration.
type Analyzer struct {
	Name string
	// Property suffixes the index property holding the analyzed copy. It is
	// part of stored indexes: never reuse or change it.
	Property string
	analyze  func(string) string
}

// Analyze returns the space-separated terms the analyzer indexes for text.
func (a Analyzer) Analyze(text string) string { return a.analyze(text) }

// Folded is language-neutral: lowercase, accent-folded words, nothing removed
// or stemmed. Passage selection uses it for fields that name no analyzer.
var Folded = Analyzer{Name: "folded", Property: "folded", analyze: func(text string) string { return strings.Join(fold(text), " ") }}

var analyzers = []Analyzer{
	Folded,
	{Name: "french_light", Property: "fr", analyze: frenchLight},
}

// Lookup returns the registered analyzer with this name.
func Lookup(name string) (Analyzer, bool) {
	for _, a := range analyzers {
		if a.Name == name {
			return a, true
		}
	}
	return Analyzer{}, false
}

var ligatures = strings.NewReplacer("œ", "oe", "æ", "ae")

// fold lowercases, expands ligatures, strips combining marks and splits text
// into letter and digit words.
func fold(text string) []string {
	text = ligatures.Replace(strings.ToLower(text))
	folded := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, norm.NFD.String(text))
	return strings.FieldsFunc(folded, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
