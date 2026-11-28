package plugin

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var ligatures = strings.NewReplacer("œ", "oe", "æ", "ae", "Œ", "oe", "Æ", "ae")

// stopwords contains generic French and English functional and interrogative
// words removed when building aspect sets, proximity streams, and focused
// entity sub-queries. Strictly domain-neutral and topic-agnostic.
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
			"a-t-il a-t-elle est-il est-elle sont-ils sont-elles ont-ils ont-elles peut-il peut-elle peut-on est-ce " +
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

// foldWord normalizes a single word: expands ligatures, strips diacritics,
// and lowercases.
func foldWord(s string) string {
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
	ends := func(suffix string) bool { return strings.HasSuffix(string(s[:n]), suffix) }
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
	if len(s) > 4 && strings.HasSuffix(string(s), "ie") {
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
// preserving order (for proximity scoring). Stopwords and 1-char tokens
// (except digits) are filtered out.
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

func hasDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func isAllUpperAcronym(s string) bool {
	runes := []rune(s)
	if len(runes) < 2 || len(runes) > 8 {
		return false
	}
	letters := 0
	for _, r := range runes {
		if unicode.IsLetter(r) {
			if !unicode.IsUpper(r) {
				return false
			}
			letters++
		} else if !unicode.IsDigit(r) {
			return false
		}
	}
	return letters >= 2
}

func startsUpper(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsUpper(r)
}

// extractEntityQuery extracts a focused, high-specificity lexical sub-query
// from a natural-language or "headline\nquestion" query without any LLM.
//
// Specificity signals (strictly domain-neutral):
//   - Acronyms (all-caps 2..8 chars) and numbers/years
//   - Mid-sentence capitalized proper nouns (and consecutive proper-noun phrases)
//   - Headline-line terms (when query has a title/question newline split)
//   - Longest non-stopword content tokens as rare-term proxies
//
// Returns the focused sub-query string and true if it differs meaningfully
// from the full query.
func extractEntityQuery(query string, maxTerms int) (string, bool) {
	if maxTerms <= 0 {
		maxTerms = 6
	}
	type candidateTerm struct {
		surface  string
		stem     string
		score    float64
		firstPos int
	}

	lines := strings.Split(strings.TrimSpace(query), "\n")
	byStem := map[string]*candidateTerm{}
	pos := 0

	for lineIdx, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Split on whitespace first to track sentence starts & hyphenated compounds.
		fields := strings.Fields(line)
		sentenceStart := true
		for _, field := range fields {
			trimmed := strings.TrimFunc(field, func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsDigit(r)
			})
			endsSentence := strings.HasSuffix(field, ".") || strings.HasSuffix(field, "?") || strings.HasSuffix(field, "!") || strings.HasSuffix(field, ":")

			// Split elisions (l', d', qu', etc.) while keeping internal hyphens intact when useful.
			subTokens := strings.FieldsFunc(trimmed, func(r rune) bool {
				return r == '\'' || r == '’' || r == '/' || r == ',' || r == ';' || r == '(' || r == ')' || r == '"' || r == '«' || r == '»'
			})
			for subIdx, tok := range subTokens {
				tok = strings.Trim(tok, "-–—.")
				if tok == "" {
					continue
				}
				folded := foldWord(tok)
				if folded == "" || stopwords[folded] {
					continue
				}
				rlen := utf8.RuneCountInString(folded)
				if rlen < 2 && !isAllDigits(folded) {
					continue
				}
				stem := frenchLightStem(folded)

				score := 0.0
				// 1. Acronyms (e.g., ONU, UE, OTAN, PIB, BCE)
				if isAllUpperAcronym(tok) {
					score += 4.0
				}
				// 2. Numbers / years / identifiers (e.g., 2024, G20, COP29)
				if hasDigit(tok) {
					score += 3.5
				}
				// 3. Capitalized proper nouns (stronger when mid-sentence)
				if startsUpper(tok) {
					if !sentenceStart || subIdx > 0 {
						score += 3.2
					} else {
						score += 1.2
					}
				}
				// 4. Hyphenated compound (often named entities or technical terms)
				if strings.ContainsRune(tok, '-') {
					score += 1.5
				}
				// 5. First-line headline salience in multi-line title+question queries
				if len(lines) > 1 && lineIdx == 0 {
					score += 1.4
				}
				// 6. Morphological specificity proxy: longer content words have higher IDF
				if rlen >= 8 {
					score += 1.6
				} else if rlen >= 6 {
					score += 0.9
				} else {
					score += 0.3
				}

				if existing, ok := byStem[stem]; ok {
					// Repeated across title and question => core subject!
					existing.score += score*0.5 + 1.5
					if startsUpper(tok) && !startsUpper(existing.surface) {
						existing.surface = tok
					}
				} else {
					byStem[stem] = &candidateTerm{
						surface:  tok,
						stem:     stem,
						score:    score,
						firstPos: pos,
					}
				}
				pos++
			}
			sentenceStart = endsSentence
		}
	}

	if len(byStem) == 0 {
		return "", false
	}
	terms := make([]*candidateTerm, 0, len(byStem))
	for _, t := range byStem {
		terms = append(terms, t)
	}
	sort.SliceStable(terms, func(i, j int) bool {
		if terms[i].score != terms[j].score {
			return terms[i].score > terms[j].score
		}
		return terms[i].firstPos < terms[j].firstPos
	})
	if len(terms) > maxTerms {
		terms = terms[:maxTerms]
	}
	// Restore original query order for readability and deterministic BM25 queries.
	sort.SliceStable(terms, func(i, j int) bool {
		return terms[i].firstPos < terms[j].firstPos
	})
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = t.surface
	}
	subQuery := strings.Join(parts, " ")
	if strings.EqualFold(strings.TrimSpace(subQuery), strings.TrimSpace(query)) {
		return subQuery, false
	}
	return subQuery, true
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
