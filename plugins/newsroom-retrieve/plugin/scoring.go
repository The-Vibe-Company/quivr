package plugin

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// recordBundle aggregates all evidence retrieved for one Record across
// retrieval legs and ungrouped dense passages.
type recordBundle struct {
	RecordID      string
	BestSeg       quivrplugin.Candidate
	HasBestSeg    bool
	LeadSeg         *quivrplugin.Candidate
	DeepBestSeg     *quivrplugin.Candidate
	SecondSeg       *quivrplugin.Candidate
	BodyLeadSeg     *quivrplugin.Candidate
	BodyBestSeg     *quivrplugin.Candidate
	BodySecondSeg   *quivrplugin.Candidate
	BodyDeepBestSeg *quivrplugin.Candidate
	HasBodyDense    bool
	CardSeg         *quivrplugin.Candidate
	DenseScore      float64
	HasDense      bool
	DenseRank     int
	LexicalScore  float64
	HasLexical    bool
	LexicalRank   int
	EntityScore       float64
	HasEntity         bool
	EntityRank        int
	FieldLexicalScore float64
	HasFieldLexical   bool
	FieldLexicalRank  int
	FirstP            float64
	HasFirstP     bool
	MaxP          float64
	HasUngrouped  bool
	PassageCount  int
	Stage1Score   float64
	FinalScore    float64
	LegsMatched   int
	Explanation   string
}

type legStats struct {
	mean float64
	std  float64
	min  float64
	max  float64
	n    int
}

func computeLegStats(cands []quivrplugin.Candidate) legStats {
	n := len(cands)
	if n == 0 {
		return legStats{}
	}
	minV, maxV := cands[0].Score, cands[0].Score
	sum := 0.0
	for _, c := range cands {
		sum += c.Score
		if c.Score < minV {
			minV = c.Score
		}
		if c.Score > maxV {
			maxV = c.Score
		}
	}
	mean := sum / float64(n)
	varSq := 0.0
	for _, c := range cands {
		d := c.Score - mean
		varSq += d * d
	}
	std := math.Sqrt(varSq / float64(n))
	return legStats{mean: mean, std: std, min: minV, max: maxV, n: n}
}

func normalizeLegScore(score float64, rank int, st legStats, fusion string, rrfK float64) float64 {
	switch fusion {
	case "rrf":
		if rrfK <= 0 {
			rrfK = 60
		}
		return 1.0 / (rrfK + float64(rank))
	case "convex":
		if st.n <= 1 || st.max == st.min {
			return 1.0
		}
		v := (score - st.min) / (st.max - st.min)
		return clamp01(v)
	default: // dbsf: 0.5 + (s - mu) / (6 * sigma), clamped [0, 1]
		if st.n <= 1 || st.std == 0 {
			return 0.5
		}
		v := 0.5 + (score-st.mean)/(6.0*st.std)
		return clamp01(v)
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func candidateBodyText(c quivrplugin.Candidate) string {
	if c.PassageText != "" {
		return c.PassageText
	}
	return c.Text
}

func isLeadSegment(c quivrplugin.Candidate) bool {
	if c.PartKey == "title" {
		return true
	}
	if len(c.SourceRanges) > 0 {
		for _, r := range c.SourceRanges {
			if r.PartKey == "title" || r.Start == 0 {
				return true
			}
		}
		return false
	}
	return c.Start == 0
}

// extractZoneTexts splits a record's retrieved segment(s) into structured
// inverted-pyramid zones: title (if present), lead (first leadChars runes at
// offset 0), early body (rest of offset-0 chunk), and deep body (offset > 0).
func extractZoneTexts(b *recordBundle, leadChars int) (title, lead, earlyBody, deepBody, fullText string) {
	if leadChars <= 0 {
		leadChars = 300
	}
	var leadBuf, earlyBuf, deepBuf, titleBuf strings.Builder

	inspectSeg := func(c quivrplugin.Candidate) {
		txt := candidateBodyText(c)
		if txt == "" {
			return
		}
		if c.PartKey == "title" {
			if titleBuf.Len() == 0 {
				titleBuf.WriteString(txt)
			}
			return
		}
		// If SourceRanges contain a title range and a body range separated by SourceSeparator.
		if len(c.SourceRanges) > 1 && c.SourceSeparator != "" && c.PassageText != "" {
			parts := strings.Split(c.PassageText, c.SourceSeparator)
			if len(parts) == len(c.SourceRanges) {
				for i, sr := range c.SourceRanges {
					if sr.PartKey == "title" {
						if titleBuf.Len() == 0 {
							titleBuf.WriteString(parts[i])
						}
					} else if sr.Start == 0 {
						l, e := splitLead(parts[i], leadChars)
						if leadBuf.Len() == 0 {
							leadBuf.WriteString(l)
							earlyBuf.WriteString(e)
						}
					} else {
						if deepBuf.Len() > 0 {
							deepBuf.WriteString("\n")
						}
						deepBuf.WriteString(parts[i])
					}
				}
				return
			}
		}
		if isLeadSegment(c) {
			if leadBuf.Len() == 0 {
				l, e := splitLead(txt, leadChars)
				leadBuf.WriteString(l)
				earlyBuf.WriteString(e)
			}
		} else {
			if deepBuf.Len() > 0 {
				deepBuf.WriteString("\n")
			}
			deepBuf.WriteString(txt)
		}
	}

	if b.LeadSeg != nil {
		inspectSeg(*b.LeadSeg)
	}
	if b.HasBestSeg && (b.LeadSeg == nil || b.BestSeg.SegmentID != b.LeadSeg.SegmentID) {
		inspectSeg(b.BestSeg)
	}
	if b.DeepBestSeg != nil &&
		(!b.HasBestSeg || b.DeepBestSeg.SegmentID != b.BestSeg.SegmentID) &&
		(b.LeadSeg == nil || b.DeepBestSeg.SegmentID != b.LeadSeg.SegmentID) {
		inspectSeg(*b.DeepBestSeg)
	}

	title = titleBuf.String()
	lead = leadBuf.String()
	earlyBody = earlyBuf.String()
	deepBody = deepBuf.String()

	var full strings.Builder
	for _, s := range []string{title, lead, earlyBody, deepBody} {
		if s != "" {
			if full.Len() > 0 {
				full.WriteString(" ")
			}
			full.WriteString(s)
		}
	}
	fullText = full.String()
	return title, lead, earlyBody, deepBody, fullText
}

func splitLead(text string, leadChars int) (string, string) {
	if leadChars <= 0 {
		return "", text
	}
	count := 0
	for idx := range text {
		if count == leadChars {
			return text[:idx], text[idx:]
		}
		count++
	}
	return text, ""
}

func truncateRunes(text string, maxChars int) string {
	if maxChars <= 0 {
		return text
	}
	count := 0
	for idx := range text {
		if count == maxChars {
			return text[:idx]
		}
		count++
	}
	return text
}

// queryAspects extracts distinct folded+stemmed query terms preserving order.
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

// computePoolIDF computes corpus-free local IDF weights over the top-K
// candidate pool so rarer, distinguishing query aspects outweigh common terms.
func computePoolIDF(aspects []string, docTokenSets []map[string]bool) []float64 {
	n := float64(len(docTokenSets))
	idfs := make([]float64, len(aspects))
	for i, a := range aspects {
		df := 0.0
		for _, set := range docTokenSets {
			if set[a] {
				df++
			}
		}
		// Smooth BM25-style pool IDF bounded strictly positive.
		idfs[i] = math.Log((n+1.0)/(df+0.5)) + 0.5
		if idfs[i] < 0.25 {
			idfs[i] = 0.25
		}
	}
	return idfs
}

// scoreAspectCoverage returns the IDF-weighted fraction of distinct query
// aspects present in tokenSet, in [0, 1].
func scoreAspectCoverage(aspects []string, idfs []float64, tokenSet map[string]bool) float64 {
	if len(aspects) == 0 {
		return 0
	}
	matched, total := 0.0, 0.0
	for i, a := range aspects {
		w := idfs[i]
		total += w
		if tokenSet[a] {
			matched += w
		}
	}
	if total == 0 {
		return 0
	}
	return matched / total
}

// scoreProximity computes an IDF-weighted unordered minimum-span bigram
// proximity score (inspired by Metzler & Croft SDM and BM25-TP) over the
// ordered content token stream. Bounded in [0, 1].
func scoreProximity(aspects []string, idfs []float64, tokens []string, window int) float64 {
	if len(aspects) < 2 || len(tokens) == 0 {
		return 0
	}
	if window < 2 {
		window = 25
	}
	aspectIdx := make(map[string]int, len(aspects))
	for i, a := range aspects {
		aspectIdx[a] = i
	}
	positions := make([][]int, len(aspects))
	for pos, tok := range tokens {
		if idx, ok := aspectIdx[tok]; ok {
			positions[idx] = append(positions[idx], pos)
		}
	}

	num, den := 0.0, 0.0
	for i := 0; i < len(aspects); i++ {
		for j := i + 1; j < len(aspects); j++ {
			pairW := idfs[i] * idfs[j]
			den += pairW
			pi, pj := positions[i], positions[j]
			if len(pi) == 0 || len(pj) == 0 {
				continue
			}
			minDist := minTokenDistance(pi, pj)
			if minDist <= window {
				// Smooth logarithmic decay: d=1 -> 1.0, d=2 -> 0.5, d=8 -> 0.25, d=25 -> 0.177
				num += pairW / (1.0 + math.Log2(float64(max(1, minDist))))
			}
		}
	}
	if den == 0 {
		return 0
	}
	return clamp01(num / den)
}

func minTokenDistance(a, b []int) int {
	i, j := 0, 0
	best := math.MaxInt
	for i < len(a) && j < len(b) {
		d := a[i] - b[j]
		if d < 0 {
			d = -d
		}
		if d < best {
			best = d
			if best <= 1 {
				return best
			}
		}
		if a[i] < b[j] {
			i++
		} else {
			j++
		}
	}
	return best
}

// scoreSalience computes Inverted-Pyramid zone-weighted salience in [0, 1].
// Each query aspect receives credit according to the highest-salience zone in
// which it appears: title (1.0), lead (0.85, or 1.0 when no title part is
// present), early body (0.45), deep body (0.15), absent (0.0).
func scoreSalience(aspects []string, idfs []float64, title, lead, earlyBody, deepBody string) float64 {
	if len(aspects) == 0 {
		return 0
	}
	titleSet := tokenSetOf(title)
	leadSet := tokenSetOf(lead)
	earlySet := tokenSetOf(earlyBody)
	deepSet := tokenSetOf(deepBody)

	leadCredit := 0.85
	if title == "" {
		leadCredit = 1.0
	}

	matched, total := 0.0, 0.0
	for i, a := range aspects {
		w := idfs[i]
		total += w
		switch {
		case titleSet[a]:
			matched += w * 1.0
		case leadSet[a]:
			matched += w * leadCredit
		case earlySet[a]:
			matched += w * 0.45
		case deepSet[a]:
			matched += w * 0.15
		}
	}
	if total == 0 {
		return 0
	}
	return clamp01(matched / total)
}

func tokenSetOf(text string) map[string]bool {
	if text == "" {
		return nil
	}
	toks := tokenizeContent(text)
	set := make(map[string]bool, len(toks))
	for _, t := range toks {
		set[t] = true
	}
	return set
}

// scoreSDMSpanProximity computes Clarke et al. shortest-cover span plus
// Metzler & Croft SDM ordered (#od2) and unordered (#uw8) adjacent query
// bigram ratios: 0.50*(|M|/(W_min+4)) + 0.35*S_ord + 0.15*S_unord.
func scoreSDMSpanProximity(querySeq []string, aspects []string, tokens []string) float64 {
	if len(aspects) < 2 || len(tokens) == 0 {
		return 0
	}
	aspectIdx := make(map[string]int, len(aspects))
	for i, a := range aspects {
		aspectIdx[a] = i
	}
	// Shortest cover of all distinct matched query aspects via sliding window.
	present := make([]bool, len(aspects))
	matchedCount := 0
	for _, tok := range tokens {
		if idx, ok := aspectIdx[tok]; ok && !present[idx] {
			present[idx] = true
			matchedCount++
		}
	}
	sSpan := 0.0
	if matchedCount >= 2 {
		counts := make([]int, len(aspects))
		have := 0
		left := 0
		bestSpan := math.MaxInt
		for right, tok := range tokens {
			if idx, ok := aspectIdx[tok]; ok {
				if counts[idx] == 0 {
					have++
				}
				counts[idx]++
			}
			for have == matchedCount && left <= right {
				span := right - left + 1
				if span < bestSpan {
					bestSpan = span
				}
				if idx, ok := aspectIdx[tokens[left]]; ok {
					counts[idx]--
					if counts[idx] == 0 {
						have--
					}
				}
				left++
			}
		}
		if bestSpan < math.MaxInt {
			sSpan = clamp01(float64(matchedCount) / float64(bestSpan+4))
		}
	}

	// Ordered (#od2) and unordered (#uw8) adjacent query bigrams.
	sOrd, sUnord := 0.0, 0.0
	if len(querySeq) >= 2 {
		// Build position map for query terms.
		posMap := map[string][]int{}
		for p, tok := range tokens {
			if _, ok := aspectIdx[tok]; ok {
				posMap[tok] = append(posMap[tok], p)
			}
		}
		totalPairs := 0
		ordHits := 0
		unordHits := 0
		for i := 0; i < len(querySeq)-1; i++ {
			a, b := querySeq[i], querySeq[i+1]
			if a == b {
				continue
			}
			totalPairs++
			pa, pb := posMap[a], posMap[b]
			if len(pa) == 0 || len(pb) == 0 {
				continue
			}
			// Check #od2 (b follows a at distance 1 or 2) and #uw8 (|pa-pb| <= 8).
			foundOrd, foundUnord := false, false
			for _, x := range pa {
				for _, y := range pb {
					if y > x && y-x <= 2 {
						foundOrd = true
					}
					d := y - x
					if d < 0 {
						d = -d
					}
					if d <= 8 {
						foundUnord = true
					}
					if foundOrd && foundUnord {
						break
					}
				}
				if foundOrd && foundUnord {
					break
				}
			}
			if foundOrd {
				ordHits++
			}
			if foundUnord {
				unordHits++
			}
		}
		if totalPairs > 0 {
			sOrd = float64(ordHits) / float64(totalPairs)
			sUnord = float64(unordHits) / float64(totalPairs)
		}
	}
	return clamp01(0.50*sSpan + 0.35*sOrd + 0.15*sUnord)
}

// scoreExpSalience computes continuous earliest-mention exponential decay
// Sal_pos = sum(idf(f) * (beta_title*I[title] + exp(-pos_1/600))) / sum(idf(f))
// and detects single-tail-mention passing mentions (highest-IDF facet appears
// only once and past offset 800 with <=1 retrieved passage).
func scoreExpSalience(aspects []string, idfs []float64, b *recordBundle) (sal float64, tailOnly bool) {
	if len(aspects) == 0 || !b.HasBestSeg {
		return 0, false
	}
	aspectIdx := make(map[string]int, len(aspects))
	for i, a := range aspects {
		aspectIdx[a] = i
	}
	earliest := make([]int, len(aspects))
	inTitle := make([]bool, len(aspects))
	tf := make([]int, len(aspects))
	for i := range earliest {
		earliest[i] = math.MaxInt
	}

	scanSeg := func(c quivrplugin.Candidate) {
		txt := candidateBodyText(c)
		if txt == "" {
			return
		}
		baseOffset := c.Start
		isTitle := c.PartKey == "title"
		// Walk tokens with rune character offset to compute exact pos_1.
		runePos := 0
		wordStart := -1
		var wordBuf strings.Builder
		flush := func() {
			if wordBuf.Len() == 0 {
				return
			}
			folded := foldWord(wordBuf.String())
			wordBuf.Reset()
			if folded == "" || stopwords[folded] {
				return
			}
			stem := frenchLightStem(folded)
			if idx, ok := aspectIdx[stem]; ok {
				tf[idx]++
				if isTitle {
					inTitle[idx] = true
					earliest[idx] = 0
				} else {
					absPos := baseOffset + wordStart
					if absPos < earliest[idx] {
						earliest[idx] = absPos
					}
				}
			}
		}
		for _, r := range txt {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r > 127 {
				if wordBuf.Len() == 0 {
					wordStart = runePos
				}
				wordBuf.WriteRune(r)
			} else {
				flush()
			}
			runePos++
		}
		flush()
	}

	if b.LeadSeg != nil {
		scanSeg(*b.LeadSeg)
	}
	if b.LeadSeg == nil || b.BestSeg.SegmentID != b.LeadSeg.SegmentID {
		scanSeg(b.BestSeg)
	}
	if b.DeepBestSeg != nil && b.DeepBestSeg.SegmentID != b.BestSeg.SegmentID && (b.LeadSeg == nil || b.DeepBestSeg.SegmentID != b.LeadSeg.SegmentID) {
		scanSeg(*b.DeepBestSeg)
	}

	num, den := 0.0, 0.0
	primaryIdx := 0
	for i := range aspects {
		if idfs[i] > idfs[primaryIdx] {
			primaryIdx = i
		}
		w := idfs[i]
		den += w
		if earliest[i] < math.MaxInt {
			bonus := 0.0
			if inTitle[i] {
				bonus = 0.5
			}
			num += w * (bonus + math.Exp(-float64(earliest[i])/600.0))
		}
	}
	if den > 0 {
		sal = clamp01(num / den)
	}
	// Single-tail-mention detector: primary facet absent from first 800 chars, tf <= 1, and not corroborated across multiple passages.
	if b.PassageCount <= 1 && earliest[primaryIdx] > 800 && tf[primaryIdx] <= 1 {
		tailOnly = true
	}
	return sal, tailOnly
}

// anchorRatio estimates the fraction of query content tokens that are named
// entities, acronyms, or numbers (for query-adaptive dense weighting).
func anchorRatio(query string) float64 {
	fields := strings.Fields(query)
	content, anchors := 0, 0
	for i, f := range fields {
		tok := strings.TrimFunc(f, func(r rune) bool { return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r > 127) })
		folded := foldWord(tok)
		if folded == "" || stopwords[folded] {
			continue
		}
		content++
		if hasDigit(tok) || isAllUpperAcronym(tok) || (i > 0 && startsUpper(tok)) {
			anchors++
		}
	}
	if content == 0 {
		return 0
	}
	return float64(anchors) / float64(content)
}

// computeConfidence computes Stage-1 query-level distribution-shape QPP signals
// for adaptive cascade routing:
//   - jaccard10: overlap between Dense top-10 and BM25 top-10 records
//   - scoreMargin: relative drop between rank-1 and rank-10 Stage-1 scores
//   - denseGap / lexGap: (mean(1..5) - mean(25..50)) / std(1..100)
//   - lexNQC: std(1..100) / (|mean(80..100)| + eps)
//   - top5LeadFrac: fraction of Stage-1 top-5 whose winning segment is offset 0
type stage1Confidence struct {
	Jaccard10    float64
	ScoreMargin  float64
	DenseGap     float64
	LexGap       float64
	LexNQC       float64
	Top5LeadFrac float64
}

func distShapeQPP(cands []quivrplugin.Candidate) (gap, nqc float64) {
	n := len(cands)
	if n < 5 {
		return 0, 0
	}
	st := computeLegStats(cands)
	if st.std < 1e-9 {
		return 0, 0
	}
	topMean := 0.0
	for i := 0; i < 5; i++ {
		topMean += cands[i].Score
	}
	topMean /= 5.0
	lo := min(24, n-1)
	hi := min(50, n)
	midMean := 0.0
	for i := lo; i < hi; i++ {
		midMean += cands[i].Score
	}
	midMean /= float64(max(1, hi-lo))
	gap = (topMean - midMean) / (st.std + 1e-9)

	tLo := min(80, n-1)
	tailMean := 0.0
	for i := tLo; i < n; i++ {
		tailMean += cands[i].Score
	}
	tailMean /= float64(max(1, n-tLo))
	nqc = st.std / (math.Abs(tailMean) + 1e-6)
	return gap, nqc
}

func computeStage1Confidence(dense, lexical []quivrplugin.Candidate, ranked []*recordBundle) stage1Confidence {
	conf := stage1Confidence{}
	conf.DenseGap, _ = distShapeQPP(dense)
	conf.LexGap, conf.LexNQC = distShapeQPP(lexical)
	dTop := map[string]bool{}
	for _, c := range dense[:min(10, len(dense))] {
		dTop[recordKey(c)] = true
	}
	lTop := map[string]bool{}
	for _, c := range lexical[:min(10, len(lexical))] {
		lTop[recordKey(c)] = true
	}
	if len(dTop) > 0 && len(lTop) > 0 {
		inter := 0
		union := len(dTop)
		for k := range lTop {
			if dTop[k] {
				inter++
			} else {
				union++
			}
		}
		if union > 0 {
			conf.Jaccard10 = float64(inter) / float64(union)
		}
	}
	if len(ranked) >= 2 {
		top := ranked[0].FinalScore
		refIdx := min(9, len(ranked)-1)
		ref := ranked[refIdx].FinalScore
		if top > 0 {
			conf.ScoreMargin = clamp01((top - ref) / top)
		}
	}
	topK := min(5, len(ranked))
	if topK > 0 {
		leads := 0
		for _, b := range ranked[:topK] {
			if b.HasBestSeg && isLeadSegment(b.BestSeg) {
				leads++
			}
		}
		conf.Top5LeadFrac = float64(leads) / float64(topK)
	}
	return conf
}

func recordKey(c quivrplugin.Candidate) string {
	if c.RecordID != "" {
		return c.RecordID
	}
	return c.SegmentID
}

func isBodyPart(c quivrplugin.Candidate) bool {
	return c.PartKey == "" || c.PartKey == "body" || c.PartKey == "title"
}

// buildSurrogate builds the document text passed to the deep reranker.
// In "inverted_pyramid" mode, it explicitly pairs lead context with the best
// matching passage (or annotates deep-only excerpts whose lead did not match).
// In "inverted_pyramid_verbatim" mode, it decouples Stage-1 structured card
// retrieval from Stage-2 reranking by always preferring verbatim body/title
// segments for the Inverted-Pyramid surrogate (preventing synthetic cards from
// overwriting [LEAD] or hiding verbatim dates/status/passing-mention cues).
func buildSurrogate(b *recordBundle, mode string, maxChars int) string {
	if !b.HasBestSeg {
		return ""
	}
	if mode == "inverted_pyramid_verbatim" || mode == "inverted_pyramid_verbatim_plus_card" {
		if b.BodyBestSeg != nil || b.BodyLeadSeg != nil {
			best := b.BodyBestSeg
			if best == nil {
				best = b.BodyLeadSeg
			}
			vb := &recordBundle{
				RecordID:    b.RecordID,
				BestSeg:     *best,
				HasBestSeg:  true,
				LeadSeg:     b.BodyLeadSeg,
				DeepBestSeg: b.BodyDeepBestSeg,
				SecondSeg:   b.BodySecondSeg,
			}
			if mode == "inverted_pyramid_verbatim_plus_card" && b.CardSeg != nil {
				cardText := strings.TrimSpace(candidateBodyText(*b.CardSeg))
				if cardText != "" {
					bodyBudget := max(1200, maxChars-480)
					base := buildSurrogate(vb, "inverted_pyramid", bodyBudget)
					rem := maxChars - utf8.RuneCountInString(base) - 24
					if rem >= 120 {
						return base + "\n\n[CARD]: " + truncateRunes(cardText, rem)
					}
					return truncateRunes(base, maxChars)
				}
			}
			return buildSurrogate(vb, "inverted_pyramid", maxChars)
		}
		return buildSurrogate(b, "inverted_pyramid", maxChars)
	}
	bestText := strings.TrimSpace(candidateBodyText(b.BestSeg))
	if mode == "raw" {
		return truncateRunes(bestText, maxChars)
	}

	var out strings.Builder
	bestIsLead := isLeadSegment(b.BestSeg)
	if bestIsLead {
		out.WriteString("[LEAD]: ")
		out.WriteString(bestText)
		if b.SecondSeg != nil && b.SecondSeg.SegmentID != b.BestSeg.SegmentID {
			secondText := strings.TrimSpace(candidateBodyText(*b.SecondSeg))
			if secondText != "" {
				out.WriteString("\n\n[SUPPORTING PASSAGE]: ")
				secondLimit := max(400, maxChars/3)
				if b.BestSeg.PartKey != "body" && b.BestSeg.PartKey != "title" {
					rem := maxChars - utf8.RuneCountInString(bestText) - 48
					if rem > secondLimit {
						secondLimit = rem
					}
				}
				out.WriteString(truncateRunes(secondText, secondLimit))
			}
		}
	} else if b.LeadSeg != nil {
		leadText := strings.TrimSpace(candidateBodyText(*b.LeadSeg))
		out.WriteString("[LEAD]: ")
		out.WriteString(truncateRunes(leadText, min(600, maxChars/2)))
		out.WriteString("\n\n[MATCHED DEEP PASSAGE]: ")
		out.WriteString(bestText)
	} else {
		out.WriteString("[DEEP BODY EXCERPT — NOT IN ARTICLE LEAD]: ")
		out.WriteString(bestText)
	}
	res := out.String()
	if utf8.RuneCountInString(res) > maxChars {
		return truncateRunes(res, maxChars)
	}
	return res
}

func sortBundlesDeterministic(bundles []*recordBundle) {
	sort.SliceStable(bundles, func(i, j int) bool {
		if bundles[i].FinalScore != bundles[j].FinalScore {
			return bundles[i].FinalScore > bundles[j].FinalScore
		}
		return bundles[i].BestSeg.SegmentID < bundles[j].BestSeg.SegmentID
	})
}
