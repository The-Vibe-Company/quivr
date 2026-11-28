package plugin

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// structuredSeparators are generic punctuation delimiters separating a headline
// or subject phrase from an elaborative question/clause in newsroom queries.
var structuredSeparators = []string{"\n", " — ", " – ", " | ", " -- "}

// genericQueryPrefixes are strictly domain-neutral French and English
// interrogative and meta-search framing prefixes stripped when constructing
// focused query views. Ordered longest-first so multi-word prefixes match
// before single-word prefixes.
var genericQueryPrefixes = []string{
	// French meta-search and interrogative prefixes
	"trouver les articles concernant ",
	"trouver les depeches concernant ",
	"rechercher les articles concernant ",
	"rechercher les depeches concernant ",
	"chercher les articles concernant ",
	"trouver des articles sur ",
	"trouver les articles sur ",
	"trouver les depeches sur ",
	"rechercher des articles sur ",
	"rechercher les articles sur ",
	"quels articles parlent de ",
	"quels articles parlent du ",
	"quels articles parlent des ",
	"quels articles portent sur ",
	"quels articles concernent ",
	"quelles depeches parlent de ",
	"quelles depeches parlent du ",
	"quelles depeches parlent des ",
	"quelles depeches portent sur ",
	"quelles depeches concernent ",
	"quels documents portent sur ",
	"quels documents concernent ",
	"articles concernant ",
	"depeches concernant ",
	"articles portant sur ",
	"depeches portant sur ",
	"articles sur ",
	"depeches sur ",
	"informations sur ",
	"quelles sont les ",
	"quels sont les ",
	"quelle est la ",
	"quelle est l'",
	"quel est le ",
	"quel est l'",
	"qu'est-ce que ",
	"qu'est-ce qui ",
	"qu'est ce que ",
	"qu'est ce qui ",
	"est-ce que ",
	"est-ce qu'",
	"y a-t-il ",
	"comment se ",
	"comment ",
	"pourquoi ",
	"combien ",
	"quand ",
	"quelles ",
	"quels ",
	"quelle ",
	"quel ",
	// English meta-search and interrogative prefixes
	"find articles about ",
	"search articles about ",
	"which articles discuss ",
	"which articles cover ",
	"which articles mention ",
	"which documents about ",
	"what are the ",
	"what is the ",
	"what were the ",
	"what was the ",
	"how does the ",
	"how did the ",
	"how have the ",
	"how has the ",
	"why did the ",
	"why does the ",
	"how does ",
	"how did ",
	"how have ",
	"how has ",
	"why did ",
	"why does ",
	"when did ",
	"where did ",
	"what ",
	"which ",
	"how ",
	"why ",
	"who ",
}

var leadingArticles = []string{"les ", "des ", "la ", "le ", "du ", "l'", "d'", "the "}

// splitQueryClauses splits a structured query into (subject, question, true)
// along standard headline/clause delimiters.
func splitQueryClauses(query string) (string, string, bool) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "", "", false
	}
	for _, sep := range structuredSeparators {
		if idx := strings.Index(trimmed, sep); idx > 0 {
			left := strings.TrimSpace(trimmed[:idx])
			right := strings.TrimSpace(trimmed[idx+len(sep):])
			if left != "" && right != "" {
				return left, right, true
			}
		}
	}
	return "", "", false
}

// foldWithOffsets folds raw once into a lowercase, diacritic-free string (with
// curly apostrophes normalized to '\'') and returns byteEnd[i] = byte offset in
// raw immediately after the character that emitted folded byte i. NFD-safe.
func foldWithOffsets(raw string) (string, []int) {
	var b strings.Builder
	b.Grow(len(raw))
	byteEnd := make([]int, 0, len(raw))

	for pos := 0; pos < len(raw); {
		r, sz := utf8.DecodeRuneInString(raw[pos:])
		nextPos := pos + sz
		// Consume any trailing combining marks attached to this base rune (NFD input).
		for nextPos < len(raw) {
			nr, nsz := utf8.DecodeRuneInString(raw[nextPos:])
			if !unicode.Is(unicode.Mn, nr) {
				break
			}
			nextPos += nsz
		}
		if unicode.Is(unicode.Mn, r) {
			pos = nextPos
			continue
		}
		if r == '’' {
			r = '\''
		}
		if r < utf8.RuneSelf {
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			b.WriteByte(byte(r))
			byteEnd = append(byteEnd, nextPos)
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			folded := ligatures.Replace(strings.ToLower(string(r)))
			for _, fr := range norm.NFD.String(folded) {
				if unicode.Is(unicode.Mn, fr) {
					continue
				}
				startLen := b.Len()
				b.WriteRune(fr)
				for b.Len() > len(byteEnd) {
					byteEnd = append(byteEnd, nextPos)
				}
				_ = startLen
			}
		} else {
			b.WriteRune(r)
			for b.Len() > len(byteEnd) {
				byteEnd = append(byteEnd, nextPos)
			}
		}
		pos = nextPos
	}
	return b.String(), byteEnd
}

// stripQueryBoilerplate removes generic French/English interrogative and
// meta-search framing prefixes and trailing question punctuation while
// preserving original casing of entities and topical terms.
func stripQueryBoilerplate(text string) string {
	s := strings.TrimSpace(text)
	if s == "" {
		return ""
	}
	s = strings.TrimRight(s, " \t\r\n?!.")
	for pass := 0; pass < 2; pass++ {
		folded, byteEnd := foldWithOffsets(s)
		changed := false
		for _, pref := range genericQueryPrefixes {
			if strings.HasPrefix(folded, pref) && len(pref) > 0 && len(pref) <= len(byteEnd) {
				rest := strings.TrimSpace(s[byteEnd[len(pref)-1]:])
				if len(tokenizeContent(rest)) >= 2 {
					s = rest
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
	folded, byteEnd := foldWithOffsets(s)
	for _, art := range leadingArticles {
		if strings.HasPrefix(folded, art) && len(art) > 0 && len(art) <= len(byteEnd) {
			rest := strings.TrimSpace(s[byteEnd[len(art)-1]:])
			if len(tokenizeContent(rest)) >= 2 {
				s = rest
				break
			}
		}
	}
	s = strings.Trim(s, " \t\r\n?!.:;-–—")
	if s == "" {
		return strings.TrimSpace(text)
	}
	return s
}

// extractCoreQuery constructs a secondary query view stripped of interrogative
// framing, returning ("", false) when it does not meaningfully differ from full.
func extractCoreQuery(query, mode string) (string, bool) {
	full := strings.TrimSpace(query)
	if full == "" {
		return "", false
	}
	subj, quest, hasSplit := splitQueryClauses(full)
	var view string
	switch mode {
	case "subject":
		if hasSplit {
			view = stripQueryBoilerplate(subj)
		} else {
			view = stripQueryBoilerplate(full)
		}
	case "question":
		if hasSplit {
			view = stripQueryBoilerplate(quest)
		} else {
			view = stripQueryBoilerplate(full)
		}
	case "full_clean":
		if hasSplit {
			view = strings.TrimSpace(stripQueryBoilerplate(subj) + " — " + stripQueryBoilerplate(quest))
		} else {
			view = stripQueryBoilerplate(full)
		}
	default:
		return "", false
	}
	view = strings.TrimSpace(view)
	if view == "" || strings.EqualFold(view, full) {
		return "", false
	}
	if len(tokenizeContent(view)) < 2 {
		return "", false
	}
	return view, true
}

func denseViewQuery(query string, cfg configuration) (string, bool) {
	if cfg.DenseViewMode == "" || cfg.DenseViewMode == "none" || cfg.DenseViewWeight <= 0 {
		return "", false
	}
	return extractCoreQuery(query, cfg.DenseViewMode)
}

func lexicalViewQuery(query string, cfg configuration) (string, bool) {
	if cfg.LexicalViewMode == "" || cfg.LexicalViewMode == "none" || cfg.LexicalViewWeight <= 0 {
		return "", false
	}
	core, ok := extractCoreQuery(query, cfg.LexicalViewMode)
	if !ok {
		return "", false
	}
	view := strings.TrimSpace(cleanLexicalQuery(core))
	primary := strings.TrimSpace(query)
	if cfg.CleanLexicalQuery {
		primary = strings.TrimSpace(cleanLexicalQuery(query))
	}
	if view == "" || strings.EqualFold(view, primary) {
		return "", false
	}
	return view, true
}
