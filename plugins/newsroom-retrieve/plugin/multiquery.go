package plugin

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// structuredSeparators are generic punctuation delimiters separating a headline
// or subject phrase from an elaborative question/clause in newsroom queries.
var structuredSeparators = []string{"\n", " — ", " – ", " | ", " -- "}

// genericQueryPrefixes are strictly domain-neutral French and English
// interrogative and meta-search framing prefixes stripped when constructing
// focused dense query views. Ordered longest-first so multi-word prefixes match
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

// foldPrefixMatch returns the byte length in raw corresponding to matching a
// folded lowercase ASCII prefix, or -1 if raw does not start with that prefix.
func foldPrefixMatch(raw, foldedPrefix string) int {
	// Walk raw rune-by-rune, folding letters while tracking byte offsets.
	// Normalize apostrophe variants so l’ matches l'.
	pRunes := []rune(foldedPrefix)
	pi := 0
	bytePos := 0
	for bytePos < len(raw) && pi < len(pRunes) {
		r, sz := utf8.DecodeRuneInString(raw[bytePos:])
		bytePos += sz
		if r == '’' {
			r = '\''
		}
		var folded string
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			folded = foldWord(string(r))
		} else {
			folded = string(r)
		}
		for _, fr := range folded {
			if pi >= len(pRunes) {
				return -1
			}
			if unicode.ToLower(fr) != pRunes[pi] {
				return -1
			}
			pi++
		}
	}
	if pi == len(pRunes) {
		return bytePos
	}
	return -1
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
	// Iteratively strip up to 2 nested boilerplate prefixes (e.g. "Quels articles sur ...").
	for pass := 0; pass < 2; pass++ {
		changed := false
		for _, pref := range genericQueryPrefixes {
			if n := foldPrefixMatch(s, pref); n > 0 {
				rest := strings.TrimSpace(s[n:])
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
	// Strip leading French articles left behind after prefix removal only if substantial content remains.
	for _, art := range []string{"les ", "des ", "la ", "le ", "du ", "l'", "d'", "the "} {
		if n := foldPrefixMatch(s, art); n > 0 {
			rest := strings.TrimSpace(s[n:])
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

// extractCoreQuery constructs View 2 (core subject / stripped question /
// boilerplate-stripped full query) for multi-query dense fan-out.
func extractCoreQuery(query, mode string) (string, bool) {
	full := strings.TrimSpace(query)
	if full == "" {
		return "", false
	}
	subj, quest, hasSplit := splitQueryClauses(full)
	var out string
	switch mode {
	case "question":
		if hasSplit {
			out = stripQueryBoilerplate(quest)
		} else {
			out = stripQueryBoilerplate(full)
		}
	case "stripped":
		if hasSplit {
			a := stripQueryBoilerplate(subj)
			b := stripQueryBoilerplate(quest)
			if a != "" && b != "" && !strings.EqualFold(a, b) {
				out = a + " " + b
			} else if a != "" {
				out = a
			} else {
				out = b
			}
		} else {
			out = stripQueryBoilerplate(full)
		}
	default: // "subject"
		if hasSplit {
			out = stripQueryBoilerplate(subj)
		} else {
			out = stripQueryBoilerplate(full)
		}
	}
	out = strings.TrimSpace(out)
	if out == "" || strings.EqualFold(out, full) {
		return out, false
	}
	return out, true
}

// extractClausePair decomposes a query into two complementary semantic clauses
// (subject/headline vs stripped question/second facet) for aspect-conjunction
// dense view fusion.
func extractClausePair(query string) (string, string, bool) {
	full := strings.TrimSpace(query)
	if full == "" {
		return "", "", false
	}
	if subj, quest, ok := splitQueryClauses(full); ok {
		a := stripQueryBoilerplate(subj)
		b := stripQueryBoilerplate(quest)
		if a != "" && b != "" && !strings.EqualFold(a, b) {
			return a, b, true
		}
	}
	// Fallback for unstructured multi-clause queries: split on colon, semicolon, or comma near middle.
	cleaned := stripQueryBoilerplate(full)
	for _, sep := range []string{": ", "; ", ", "} {
		if idx := strings.Index(cleaned, sep); idx > 0 {
			a := strings.TrimSpace(cleaned[:idx])
			b := strings.TrimSpace(cleaned[idx+len(sep):])
			if len(tokenizeContent(a)) >= 2 && len(tokenizeContent(b)) >= 2 {
				return a, b, true
			}
		}
	}
	return "", "", false
}

// extractDenseEntityQuery builds View 3: a compact noun-phrase dense query of
// capitalized named entities, acronyms, years/numbers, and top high-specificity
// content terms. Unlike extractEntityQuery, it recognizes em-dash/en-dash/pipe
// headline delimiters in addition to newlines.
func extractDenseEntityQuery(query string, maxTerms int) (string, bool) {
	if maxTerms <= 0 {
		maxTerms = 8
	}
	normalized := query
	for _, sep := range []string{" — ", " – ", " | ", " -- "} {
		normalized = strings.ReplaceAll(normalized, sep, "\n")
	}
	return extractEntityQuery(normalized, maxTerms)
}

// buildRound2ConsensusQuery implements conservative, zero-LLM Round-2 pseudo-
// relevance dense expansion:
//  1. Intersect Round-1 full dense top-N and full BM25 top-N to obtain high-
//     consensus seed documents (requires >= 2 distinct consensus records).
//  2. Extract capitalized entities, acronyms, and substantial content terms
//     from the lead/opening 700 chars of each seed doc that are NOT already in
//     the query.
//  3. Keep ONLY terms shared by >= 2 seed docs and prioritize those co-occurring
//     near query aspects in the seed docs.
//  4. Append top expansion terms to the anchored base query ("core", "full", or "entity").
func buildRound2ConsensusQuery(
	query string,
	cfg configuration,
	denseGrouped, lexicalFull []quivrplugin.Candidate,
) (string, []string, bool) {
	topN := cfg.Round2ConsensusTopN
	if topN <= 0 {
		topN = 10
	}
	maxDocs := cfg.Round2ConsensusDocs
	if maxDocs < 2 {
		maxDocs = 2
	}
	maxTerms := cfg.Round2MaxTerms
	if maxTerms <= 0 {
		maxTerms = 3
	}

	type seedDoc struct {
		recID    string
		rankSum  int
		cand     quivrplugin.Candidate
	}
	denseRank := make(map[string]int, topN)
	denseCand := make(map[string]quivrplugin.Candidate, topN)
	for i, c := range denseGrouped[:min(topN, len(denseGrouped))] {
		k := recordKey(c)
		if _, ok := denseRank[k]; !ok {
			denseRank[k] = i + 1
			denseCand[k] = c
		}
	}
	var seeds []seedDoc
	seenSeed := map[string]bool{}
	for i, c := range lexicalFull[:min(topN, len(lexicalFull))] {
		k := recordKey(c)
		if dr, ok := denseRank[k]; ok && !seenSeed[k] {
			seenSeed[k] = true
			// Prefer the dense candidate text if it is a lead segment; else lexical candidate.
			chosen := denseCand[k]
			if !isLeadSegment(chosen) && isLeadSegment(c) {
				chosen = c
			}
			seeds = append(seeds, seedDoc{recID: k, rankSum: dr + (i + 1), cand: chosen})
		}
	}
	if len(seeds) < 2 {
		return "", nil, false
	}
	sort.SliceStable(seeds, func(i, j int) bool {
		if seeds[i].rankSum != seeds[j].rankSum {
			return seeds[i].rankSum < seeds[j].rankSum
		}
		return seeds[i].recID < seeds[j].recID
	})
	if len(seeds) > maxDocs {
		seeds = seeds[:maxDocs]
	}

	qAspectSet := map[string]bool{}
	for _, a := range queryAspects(query) {
		qAspectSet[a] = true
	}

	type expTerm struct {
		stem       string
		surface    string
		docCount   int
		score      float64
		coOccurs   bool
		isProper   bool
	}
	byStem := map[string]*expTerm{}

	for _, sd := range seeds {
		txt := truncateRunes(strings.TrimSpace(candidateBodyText(sd.cand)), 800)
		if txt == "" {
			continue
		}
		// Track positions of query aspects inside this seed doc for co-occurrence filtering.
		fields := strings.Fields(txt)
		qPositions := make([]int, 0, 8)
		type fieldInfo struct {
			tok           string
			stem          string
			folded        string
			pos           int
			sentenceStart bool
			isAcronym     bool
			isProper      bool
		}
		var extracted []fieldInfo
		sentenceStart := true
		tokPos := 0
		for _, field := range fields {
			endsSentence := strings.HasSuffix(field, ".") || strings.HasSuffix(field, "?") || strings.HasSuffix(field, "!") || strings.HasSuffix(field, ":")
			trimmed := strings.TrimFunc(field, func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsDigit(r)
			})
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
				if rlen < 3 && !isAllUpperAcronym(tok) {
					continue
				}
				stem := frenchLightStem(folded)
				if qAspectSet[stem] {
					qPositions = append(qPositions, tokPos)
					tokPos++
					continue
				}
				isAcr := isAllUpperAcronym(tok)
				isProp := startsUpper(tok) && (!sentenceStart || subIdx > 0) && !isAllDigits(folded)
				// Skip generic all-caps dateline/agency headers at very beginning (first 3 tokens) unless mixed-case proper noun.
				if tokPos < 3 && isAcr {
					tokPos++
					continue
				}
				if isAcr || isProp || rlen >= 6 {
					extracted = append(extracted, fieldInfo{
						tok:           tok,
						stem:          stem,
						folded:        folded,
						pos:           tokPos,
						sentenceStart: sentenceStart,
						isAcronym:     isAcr,
						isProper:      isProp,
					})
				}
				tokPos++
			}
			sentenceStart = endsSentence
		}

		seenInDoc := map[string]bool{}
		for _, fi := range extracted {
			if seenInDoc[fi.stem] {
				continue
			}
			seenInDoc[fi.stem] = true
			nearQ := false
			for _, qp := range qPositions {
				d := fi.pos - qp
				if d < 0 {
					d = -d
				}
				if d <= 25 {
					nearQ = true
					break
				}
			}
			termScore := 0.5
			if fi.isAcronym {
				termScore += 3.0
			} else if fi.isProper {
				termScore += 2.5
			} else if utf8.RuneCountInString(fi.folded) >= 7 {
				termScore += 1.2
			}
			if nearQ {
				termScore += 2.0
			}
			if existing, ok := byStem[fi.stem]; ok {
				existing.docCount++
				existing.score += termScore
				if nearQ {
					existing.coOccurs = true
				}
				if fi.isProper {
					existing.isProper = true
					existing.surface = fi.tok
				}
			} else {
				byStem[fi.stem] = &expTerm{
					stem:     fi.stem,
					surface:  fi.tok,
					docCount: 1,
					score:    termScore,
					coOccurs: nearQ,
					isProper: fi.isProper || fi.isAcronym,
				}
			}
		}
	}

	// Strict consensus filter: must appear in >= 2 distinct seed docs AND co-occur near query terms.
	var candidates []*expTerm
	for _, t := range byStem {
		if t.docCount >= 2 && (t.coOccurs || t.isProper) {
			candidates = append(candidates, t)
		}
	}
	if len(candidates) == 0 {
		return "", nil, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].docCount != candidates[j].docCount {
			return candidates[i].docCount > candidates[j].docCount
		}
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].stem < candidates[j].stem
	})
	if len(candidates) > maxTerms {
		candidates = candidates[:maxTerms]
	}
	added := make([]string, len(candidates))
	for i, t := range candidates {
		added[i] = t.surface
	}

	base := strings.TrimSpace(query)
	switch cfg.Round2QueryBase {
	case "core":
		if c, ok := extractCoreQuery(query, "stripped"); ok && c != "" {
			base = c
		}
	case "entity":
		if e, ok := extractDenseEntityQuery(query, cfg.DenseEntityMaxTerms); ok && e != "" {
			base = e
		}
	}
	r2Query := strings.TrimSpace(base + " " + strings.Join(added, " "))
	if r2Query == "" || strings.EqualFold(r2Query, strings.TrimSpace(query)) {
		return "", nil, false
	}
	return r2Query, added, true
}

// extractYearsAndNumbers extracts explicit 4-digit years (1900..2099) and
// significant numeric/alphanumeric tokens (length >= 2 containing digits) in
// deterministic sorted order.
func extractYearsAndNumbers(text string) (years []string, nums []string) {
	raw := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	yearSet := map[string]bool{}
	numSet := map[string]bool{}
	for _, tok := range raw {
		folded := foldWord(tok)
		if len(folded) < 2 || !hasDigit(folded) {
			continue
		}
		if len(folded) == 4 && isAllDigits(folded) && (strings.HasPrefix(folded, "19") || strings.HasPrefix(folded, "20")) {
			yearSet[folded] = true
		} else {
			numSet[folded] = true
		}
	}
	years = make([]string, 0, len(yearSet))
	for y := range yearSet {
		years = append(years, y)
	}
	sort.Strings(years)
	nums = make([]string, 0, len(numSet))
	for n := range numSet {
		nums = append(nums, n)
	}
	sort.Strings(nums)
	return years, nums
}

// scoreTemporalAlignment scores exact year/number alignment and conflicting-
// year penalties for a candidate record. Returns (align, conflict) in [0, 1].
// Returns (0, 0) whenever the query mentions no explicit years or numbers.
func scoreTemporalAlignment(qYears, qNums []string, b *recordBundle, leadChars int) (align float64, conflict float64) {
	if len(qYears) == 0 && len(qNums) == 0 {
		return 0, 0
	}
	title, lead, early, deep, full := extractZoneTexts(b, leadChars)
	headText := strings.TrimSpace(title + " " + lead)
	docYearsAll, docNumsAll := extractYearsAndNumbers(full)
	docYearsHead, docNumsHead := extractYearsAndNumbers(headText)

	docYearSet := make(map[string]bool, len(docYearsAll))
	for _, y := range docYearsAll {
		docYearSet[y] = true
	}
	docYearHeadSet := make(map[string]bool, len(docYearsHead))
	for _, y := range docYearsHead {
		docYearHeadSet[y] = true
	}
	docNumSet := make(map[string]bool, len(docNumsAll))
	for _, n := range docNumsAll {
		docNumSet[n] = true
	}
	docNumHeadSet := make(map[string]bool, len(docNumsHead))
	for _, n := range docNumsHead {
		docNumHeadSet[n] = true
	}
	_ = early
	_ = deep

	// Score alignment over explicit years (weight 1.5) and numeric identifiers (weight 1.0).
	matchedW, totalW := 0.0, 0.0
	anyYearMatch := false
	for _, y := range qYears {
		totalW += 1.5
		if docYearHeadSet[y] {
			matchedW += 1.5
			anyYearMatch = true
		} else if docYearSet[y] {
			matchedW += 1.05
			anyYearMatch = true
		}
	}
	for _, n := range qNums {
		totalW += 1.0
		if docNumHeadSet[n] {
			matchedW += 1.0
		} else if docNumSet[n] {
			matchedW += 0.70
		}
	}
	if totalW > 0 {
		align = clamp01(matchedW / totalW)
	}

	// Conflicting year penalty: query names explicit year(s), doc names explicit
	// year(s), and NONE of the query's years appear in the doc.
	if len(qYears) > 0 && !anyYearMatch && len(docYearsAll) > 0 {
		if len(docYearsHead) > 0 {
			conflict = 1.0
		} else {
			conflict = 0.65
		}
	}
	return align, conflict
}

// extractQueryEntityStems extracts distinct folded+stemmed named entities,
// acronyms, and proper-noun tokens from a query (100% generic, deterministic).
func extractQueryEntityStems(query string) []string {
	normalized := query
	for _, sep := range []string{" — ", " – ", " | ", " -- "} {
		normalized = strings.ReplaceAll(normalized, sep, "\n")
	}
	lines := strings.Split(strings.TrimSpace(normalized), "\n")
	seen := map[string]bool{}
	var out []string
	for lineIdx, line := range lines {
		fields := strings.Fields(line)
		sentenceStart := true
		for _, field := range fields {
			endsSentence := strings.HasSuffix(field, ".") || strings.HasSuffix(field, "?") || strings.HasSuffix(field, "!") || strings.HasSuffix(field, ":")
			trimmed := strings.TrimFunc(field, func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsDigit(r)
			})
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
				if utf8.RuneCountInString(folded) < 2 && !isAllDigits(folded) {
					continue
				}
				isEntity := isAllUpperAcronym(tok) ||
					(startsUpper(tok) && (!sentenceStart || subIdx > 0 || (len(lines) > 1 && lineIdx == 0 && utf8.RuneCountInString(folded) >= 4))) ||
					(strings.ContainsRune(tok, '-') && startsUpper(tok))
				if !isEntity {
					continue
				}
				stem := frenchLightStem(folded)
				if !seen[stem] {
					seen[stem] = true
					out = append(out, stem)
				}
			}
			sentenceStart = endsSentence
		}
	}
	return out
}

// extractWireOpeningHeader returns the inline wire dateline / headline /
// opening sentence from a candidate's title and offset-0 lead segment.
func extractWireOpeningHeader(b *recordBundle) (dateline string, opening string) {
	title, lead, _, _, _ := extractZoneTexts(b, 220)
	if lead == "" && title == "" {
		return "", ""
	}
	// Detect inline wire dateline prefix within first 90 chars: e.g. "CITY (Agency) - ..." or "CITY, 12 mai - ..."
	head := truncateRunes(strings.TrimSpace(lead), 90)
	for _, sep := range []string{" - ", " — ", " – ", ") -", ") —"} {
		if idx := strings.Index(head, sep); idx > 0 && idx <= 70 {
			candPrefix := strings.TrimSpace(head[:idx])
			// Verify dateline looks like a wire header (contains an uppercase place/word).
			if hasUppercaseWord(candPrefix) {
				dateline = candPrefix
				break
			}
		}
	}
	// Opening header = title + first ~180 runes of lead (headline + wire dateline + first sentence).
	var buf strings.Builder
	if title != "" {
		buf.WriteString(title)
		buf.WriteString(" ")
	}
	if lead != "" {
		buf.WriteString(truncateRunes(strings.TrimSpace(lead), 180))
	}
	return dateline, buf.String()
}

func hasUppercaseWord(s string) bool {
	for _, f := range strings.Fields(s) {
		clean := strings.TrimFunc(f, func(r rune) bool { return !unicode.IsLetter(r) })
		if utf8.RuneCountInString(clean) >= 2 && startsUpper(clean) {
			return true
		}
	}
	return false
}

// scoreDatelineEntityBoost returns [0, 1] measuring how strongly the query's
// named entities/acronyms appear in the wire dateline or opening headline/lede.
func scoreDatelineEntityBoost(queryEntityStems []string, b *recordBundle) float64 {
	if len(queryEntityStems) == 0 {
		return 0
	}
	dateline, opening := extractWireOpeningHeader(b)
	if opening == "" {
		return 0
	}
	openSet := tokenSetOf(opening)
	dateSet := tokenSetOf(dateline)
	matched := 0.0
	for _, e := range queryEntityStems {
		if dateSet[e] {
			matched += 1.15
		} else if openSet[e] {
			matched += 1.0
		}
	}
	return clamp01(matched / float64(len(queryEntityStems)))
}
