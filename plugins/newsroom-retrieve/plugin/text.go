package plugin

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var ligatures = strings.NewReplacer("œ", "oe", "æ", "ae", "Œ", "oe", "Æ", "ae")

// stopwords contains generic French and English functional and interrogative
// words (in folded, hyphen-free form) removed when building aspect sets and
// cleaning BM25 queries. Strictly domain-neutral and topic-agnostic.
var stopwords = func() map[string]bool {
	words := strings.Fields(
		// French articles, prepositions, pronouns, auxiliaries, conjunctions
		"a ai aie aient aies ait alors au aux avec avez aviez avons ayant " +
			"ce cela ces cet cette ci comme contre " +
			"d dans de des du dont donc " +
			"elle elles en encore entre est et etaient etais etait etant ete etes etre eu eue eues eus eut eux " +
			"faire fait faites font " +
			"il ils j je l la le les leur leurs lui " +
			"m ma mais me meme mes moi mon " +
			"n ne ni nos notre nous " +
			"on ont ou " +
			"par parce pas pendant peut peux peuvent plus pour pourquoi " +
			"qu quand que quel quelle quelles quels qui quoi " +
			"comment combien depuis apres avant selon face chez ainsi aussi bien " +
			// Folded forms of French hyphenated inverted-verb interrogatives (a-t-il, est-ce, peut-on, ...)
			"atil atelle estil estelle sontils sontelles ontils ontelles peutil peutelle peuton estce " +
			"s sa sans se sera seront ses si soit son sont sous sur " +
			"t ta te tes toi ton tous tout toute toutes tres tu " +
			"un une " +
			"va vers vos votre vous y " +
			// Generic French query-framing verbs/nouns (non-topical)
			"article articles depeche depeches document documents information informations " +
			"concernant concerne concernent portant porte portent sujet sujets " +
			"trouver cherche recherche liste lister montrer " +
			// English common functional and interrogative words
			"the and for that this with from have has had are was were will would could should " +
			"about into over after before between under what which who whom whose where when why how " +
			"not but can all any each few more most other some such than too very just",
	)
	out := make(map[string]bool, len(words))
	for _, w := range words {
		out[w] = true
	}
	return out
}()

// foldWord normalizes a single word: expands ligatures, strips diacritics and
// non-alphanumeric punctuation, and lowercases. Uses a zero-NFD ASCII fast path.
func foldWord(s string) string {
	allASCII := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			allASCII = false
			break
		}
	}
	if allASCII {
		var b strings.Builder
		b.Grow(len(s))
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case c >= 'A' && c <= 'Z':
				b.WriteByte(c + ('a' - 'A'))
			case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
				b.WriteByte(c)
			}
		}
		return b.String()
	}

	s = ligatures.Replace(strings.ToLower(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hasASCIISuffix compares the tail of s[:n] against an ASCII suffix with zero allocations.
func hasASCIISuffix(s []rune, n int, suffix string) bool {
	m := len(suffix)
	if n < m {
		return false
	}
	base := n - m
	for i := 0; i < m; i++ {
		if s[base+i] != rune(suffix[i]) {
			return false
		}
	}
	return true
}

// frenchLightStem applies the Apache Lucene UniNE French light stemmer
// (Jacques Savoy, 2005) to a folded lowercase word.
func frenchLightStem(word string) string {
	s := []rune(word)
	n := len(s)
	if n <= 3 {
		return word
	}
	// Preserve purely numeric tokens untouched.
	allDigits := true
	for _, r := range s {
		if !unicode.IsDigit(r) {
			allDigits = false
			break
		}
	}
	if allDigits {
		return word
	}
	ends := func(suffix string) bool { return hasASCIISuffix(s, n, suffix) }
	if n > 5 && s[n-1] == 'x' {
		if s[n-3] == 'a' && s[n-2] == 'u' && s[n-4] != 'e' {
			s[n-2] = 'l'
		}
		n--
	}
	if n > 3 && s[n-1] == 'x' {
		n--
	}
	if n > 3 && s[n-1] == 's' {
		n--
	}
	switch {
	case n > 9 && ends("issement"):
		n -= 6
		s[n-1] = 'r'
	case n > 8 && ends("issant"):
		n -= 4
		s[n-1] = 'r'
	case n > 6 && ends("ement"):
		n -= 4
		if n > 3 && ends("ive") {
			n--
			s[n-1] = 'f'
		}
	case n > 11 && ends("ficatrice"):
		n -= 5
		s[n-2], s[n-1] = 'e', 'r'
	case n > 10 && ends("ficateur"):
		n -= 4
		s[n-2], s[n-1] = 'e', 'r'
	case n > 9 && ends("catrice"):
		n -= 3
		s[n-4], s[n-3], s[n-2] = 'q', 'u', 'e'
	case n > 8 && ends("cateur"):
		n -= 2
		s[n-4], s[n-3], s[n-2], s[n-1] = 'q', 'u', 'e', 'r'
	case n > 8 && ends("atrice"):
		n -= 4
		s[n-2], s[n-1] = 'e', 'r'
	case n > 7 && ends("ateur"):
		n -= 3
		s[n-2], s[n-1] = 'e', 'r'
	default:
		if n > 6 && ends("trice") {
			n--
			s[n-3], s[n-2], s[n-1] = 'e', 'u', 'r'
		}
		switch {
		case n > 5 && ends("ieme"):
			n -= 4
		case n > 7 && ends("teuse"):
			n -= 2
			s[n-1] = 'r'
		case n > 6 && ends("teur"):
			n--
			s[n-1] = 'r'
		case n > 5 && ends("euse"):
			n -= 2
		case n > 8 && ends("ere"):
			n--
			s[n-2] = 'e'
		case n > 7 && ends("ive"):
			n--
			s[n-1] = 'f'
		case n > 4 && (ends("folle") || ends("molle")):
			n -= 2
			s[n-1] = 'u'
		case n > 9 && ends("nnelle"):
			n -= 5
		case n > 9 && ends("nnel"):
			n -= 3
		default:
			if n > 4 && ends("ete") {
				n--
				s[n-2] = 'e'
			}
			if n > 8 && ends("ique") {
				n -= 4
			}
			switch {
			case n > 8 && ends("esse"):
				n -= 3
			case n > 7 && ends("inage"):
				n -= 3
			case n > 9 && ends("isation"):
				n -= 7
				if n > 5 && ends("ual") {
					s[n-2] = 'e'
				}
			case n > 9 && ends("isateur"):
				n -= 7
			case n > 8 && (ends("ation") || ends("ition")):
				n -= 5
			}
		}
	}
	s = s[:n]
	if len(s) > 4 {
		out := s[:1]
		for _, r := range s[1:] {
			if r != out[len(out)-1] || !unicode.IsLetter(r) {
				out = append(out, r)
			}
		}
		s = out
	}
	if len(s) > 4 && hasASCIISuffix(s, len(s), "ie") {
		s = s[:len(s)-2]
	}
	if len(s) > 4 {
		if s[len(s)-1] == 'r' {
			s = s[:len(s)-1]
		}
		if s[len(s)-1] == 'e' {
			s = s[:len(s)-1]
		}
		if s[len(s)-1] == 'e' {
			s = s[:len(s)-1]
		}
		if s[len(s)-1] == s[len(s)-2] && unicode.IsLetter(s[len(s)-1]) {
			s = s[:len(s)-1]
		}
	}
	return string(s)
}

// tokenizeContent splits text into folded, lightly-stemmed content tokens
// preserving order. Stopwords and 1-char tokens (except digits) are filtered out.
func tokenizeContent(text string) []string {
	raw := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(raw))
	for _, tok := range raw {
		folded := foldWord(tok)
		if folded == "" || stopwords[folded] {
			continue
		}
		if utf8.RuneCountInString(folded) < 2 && !isAllDigits(folded) {
			continue
		}
		out = append(out, frenchLightStem(folded))
	}
	return out
}

func tokenSetOf(text string) map[string]bool {
	toks := tokenizeContent(text)
	set := make(map[string]bool, len(toks))
	for _, t := range toks {
		set[t] = true
	}
	return set
}

func queryAspects(query string) []string {
	toks := tokenizeContent(query)
	seen := make(map[string]bool, len(toks))
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// cleanLexicalQuery removes generic multilingual stopwords and French elision
// prefixes (l', d', qu', etc.) from a BM25 query while preserving original
// surface forms, accents, digits, and deduplicating repeated tokens.
// Falls back to the original query if no content tokens remain.
func cleanLexicalQuery(query string) string {
	raw := strings.TrimSpace(query)
	if raw == "" {
		return raw
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, tok := range parts {
		tok = strings.Trim(tok, "-–—")
		if tok == "" {
			continue
		}
		folded := foldWord(tok)
		if folded == "" || stopwords[folded] {
			continue
		}
		if utf8.RuneCountInString(folded) < 2 && !isAllDigits(folded) {
			continue
		}
		if seen[folded] {
			continue
		}
		seen[folded] = true
		out = append(out, tok)
	}
	if len(out) == 0 {
		return raw
	}
	return strings.Join(out, " ")
}
