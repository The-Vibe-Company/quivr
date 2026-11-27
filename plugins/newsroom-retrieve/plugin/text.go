package plugin

import (
	"math"
	"strings"
	"unicode"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
	"golang.org/x/text/unicode/norm"
)

var ligatureReplacer = strings.NewReplacer("œ", "oe", "æ", "ae")

// Generic multilingual (French + English) function words excluded from
// content-term coverage scoring. Strictly domain- and corpus-independent.
var stopwords = func() map[string]bool {
	words := strings.Fields(
		"a ai aie aient aies ait alors au aux avec avez aviez avons ayant ayez ayons " +
			"c ce ceci cela ces cet cette ci comme comment " +
			"d dans de des du elle elles en entre est et etaient etais etait etant ete etes etiez etions etre eu eue eues eus eut eux " +
			"fais fait font " +
			"il ils j je l la le les leur leurs lui m ma mais me meme mes moi mon " +
			"n ne ni nos notre nous on ou par pas pendant peu plus pour pourquoi " +
			"qu quand que quel quelle quelles quels qui quoi " +
			"s sa sans se sera seront ses si soit son sont sous sur " +
			"t ta te tes toi ton tous tout toute toutes tres tu " +
			"un une vos votre vous y " +
			"an and are as at be by for from has have how in is it its of on or that the their there these they this to was were what when where which who will with",
	)
	out := make(map[string]bool, len(words))
	for _, w := range words {
		out[w] = true
	}
	return out
}()

// contentTerms folds accents/ligatures, splits on non-alphanumeric boundaries
// (naturally separating French elisions like l' / d' / qu'), drops stopwords
// and 1-character tokens, and applies light suffix normalization.
func contentTerms(text string) []string {
	if text == "" {
		return nil
	}
	lower := ligatureReplacer.Replace(strings.ToLower(text))
	folded := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, norm.NFD.String(lower))

	fields := strings.Fields(folded)
	out := make([]string, 0, len(fields))
	for _, w := range fields {
		if len(w) <= 1 || stopwords[w] {
			continue
		}
		out = append(out, lightNormalize(w))
	}
	return out
}

func lightNormalize(w string) string {
	if len(w) >= 5 && strings.HasSuffix(w, "aux") {
		return w[:len(w)-3] + "al"
	}
	if len(w) >= 4 && (strings.HasSuffix(w, "s") || strings.HasSuffix(w, "x")) {
		return w[:len(w)-1]
	}
	return w
}

func uniqueTerms(text string) []string {
	terms := contentTerms(text)
	if len(terms) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(terms))
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func termSet(text string) map[string]bool {
	terms := contentTerms(text)
	if len(terms) == 0 {
		return nil
	}
	out := make(map[string]bool, len(terms))
	for _, t := range terms {
		out[t] = true
	}
	return out
}

func firstRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || s == "" {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

func isLeadSegment(c quivrplugin.Candidate) bool {
	if c.PartKey == "title" {
		return true
	}
	if len(c.SourceRanges) > 0 {
		for _, r := range c.SourceRanges {
			if r.PartKey == "title" || (r.PartKey == "body" && r.Start == 0) {
				return true
			}
		}
		return false
	}
	return c.Start == 0
}

// extractTitleAndLead extracts headline and opening-lead text from a hydrated
// candidate without extra storage round-trips.
func extractTitleAndLead(c quivrplugin.Candidate, leadChars int) (title, lead string) {
	sep := c.SourceSeparator
	if sep == "" {
		sep = "\n\n"
	}
	if c.PartKey == "title" {
		title = strings.TrimSpace(c.Text)
		if c.PassageText != "" {
			rest := c.PassageText
			if title != "" && strings.HasPrefix(rest, title) {
				rest = strings.TrimPrefix(rest, title)
				rest = strings.TrimPrefix(rest, sep)
				rest = strings.TrimSpace(rest)
			}
			hasLeadBody := len(c.SourceRanges) == 0
			for _, r := range c.SourceRanges {
				if r.PartKey != "title" && r.Start == 0 {
					hasLeadBody = true
					break
				}
			}
			if hasLeadBody && rest != "" {
				lead = firstRunes(rest, leadChars)
			}
		}
		return title, lead
	}
	if isLeadSegment(c) {
		body := c.PassageText
		if body == "" {
			body = c.Text
		}
		lead = firstRunes(strings.TrimSpace(body), leadChars)
	}
	return title, lead
}

// computeInPoolIDF computes smooth IDF weights for query terms across the
// shortlisted candidate texts (O(1) with respect to corpus size).
func computeInPoolIDF(queryTerms []string, docTermSets []map[string]bool) map[string]float64 {
	n := float64(len(docTermSets))
	idf := make(map[string]float64, len(queryTerms))
	for _, qt := range queryTerms {
		df := 0.0
		for _, ds := range docTermSets {
			if ds[qt] {
				df++
			}
		}
		idf[qt] = math.Log(1.0 + (n+1.0)/(df+1.0))
	}
	return idf
}
