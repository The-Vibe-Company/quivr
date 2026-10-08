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

// Folded is language-neutral: lowercase words with accents, ligatures and
// compatibility forms (ﬁ, full-width letters) folded, nothing removed or
// stemmed. Passage selection uses it for fields that name no analyzer.
var Folded = Analyzer{Name: "folded", Property: "folded", analyze: func(text string) string {
	// Symbols separate words before compatibility decomposition, so ², ½ or ™
	// never join a neighbouring word; decomposed capitals such as 𝐀 lowercase.
	text = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			return r
		}
		return ' '
	}, text)
	return strings.ToLower(strings.Join(fold(text, norm.NFKD), " "))
}}

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

// fold lowercases, expands ligatures, decomposes with form, strips combining
// marks and splits text into letter and digit words.
func fold(text string, form norm.Form) []string {
	text = ligatures.Replace(strings.ToLower(text))
	folded := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, form.String(text))
	return strings.FieldsFunc(folded, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
