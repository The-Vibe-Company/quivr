package plugin

import (
	"math"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

// recordBundle aggregates all evidence retrieved for one Record across
// retrieval legs and ungrouped dense passages.
type recordBundle struct {
	RecordID             string
	Title                string
	CommonMetadata       *quivrplugin.CommonMetadata
	TotalPassages        int
	LeadPassage          string
	LeadPassageFromDense bool
	BestSeg              quivrplugin.Candidate
	HasBestSeg           bool
	LeadSeg              *quivrplugin.Candidate
	LeadFromDense        bool
	DeepBestSeg          *quivrplugin.Candidate
	DeepFromDense        bool
	SecondSeg            *quivrplugin.Candidate
	DenseScore           float64
	HasDense             bool
	DenseRank            int
	LexicalScore         float64
	HasLexical           bool
	LexicalRank          int
	FirstP               float64
	HasFirstP            bool
	MaxP                 float64
	HasUngrouped         bool
	PassageCount         int
	Extra                map[string]legHit
	Stage1Score          float64
	FinalScore           float64
	LegsMatched          int
	Explanation          string
}

func (b *recordBundle) isSinglePassage() bool {
	return b.TotalPassages == 1
}

type legHit struct {
	Score float64
	Rank  int
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
		if st.n <= 1 || st.max-st.min < 1e-12 {
			return 1.0
		}
		return clamp01((score - st.min) / (st.max - st.min))
	default: // dbsf: 0.5 + (s - mu) / (6 * sigma), clamped [0, 1]
		if st.n <= 1 || st.std < 1e-9 {
			return 0.5
		}
		return clamp01(0.5 + (score-st.mean)/(6.0*st.std))
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

// isLeadSegment reports whether c is the opening/lead segment of its Record.
// Prefers exact Plugin API 0.21 passage_ordinal/total_passages when populated,
// falling back gracefully to PartKey/SourceRanges/Start heuristics when nil/0.
func isLeadSegment(c quivrplugin.Candidate) bool {
	if c.PassageOrdinal != nil {
		return *c.PassageOrdinal == 0
	}
	if c.TotalPassages == 1 {
		return true
	}
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

// passageDepthFactor returns normalized relative depth in (0, 1] for a deep
// passage when exact passage_ordinal (> 0) and total_passages (> 1) are known.
func passageDepthFactor(c quivrplugin.Candidate, totalPassages int) (float64, bool) {
	if c.PassageOrdinal == nil || *c.PassageOrdinal <= 0 {
		return 0, false
	}
	total := totalPassages
	if c.TotalPassages > total {
		total = c.TotalPassages
	}
	if total <= 1 {
		return 0, false
	}
	return clamp01(float64(*c.PassageOrdinal) / float64(total-1)), true
}

// stripLeadingTitle removes a duplicated headline line from the start of a
// packed lead passage when explicit Candidate.Title is already emitted in
// [TITLE], avoiding repeated headline text without altering inline prose.
func stripLeadingTitle(title, lead string) string {
	title = strings.TrimSpace(title)
	lead = strings.TrimSpace(lead)
	if title == "" || lead == "" || !strings.HasPrefix(lead, title) {
		return lead
	}
	rest := lead[len(title):]
	if rest == "" {
		return ""
	}
	if rest[0] == '\n' || rest[0] == '\r' {
		return strings.TrimSpace(rest)
	}
	return lead
}

// sliceSourceRanges extracts each SourceRange's exact rune span from PassageText
// using sr.End - sr.Start + separator rune length, so body text containing "\n\n"
// never breaks range splitting.
func sliceSourceRanges(c quivrplugin.Candidate) ([]string, bool) {
	if len(c.SourceRanges) <= 1 || c.PassageText == "" {
		return nil, false
	}
	runes := []rune(c.PassageText)
	sepLen := utf8.RuneCountInString(c.SourceSeparator)
	expected := 0
	for i, sr := range c.SourceRanges {
		w := sr.End - sr.Start
		if w < 0 {
			return nil, false
		}
		expected += w
		if i > 0 {
			expected += sepLen
		}
	}
	if expected != len(runes) {
		return nil, false
	}
	parts := make([]string, len(c.SourceRanges))
	pos := 0
	for i, sr := range c.SourceRanges {
		if i > 0 {
			pos += sepLen
		}
		w := sr.End - sr.Start
		parts[i] = string(runes[pos : pos+w])
		pos += w
	}
	return parts, true
}

// extractTitleAndLead returns the headline (preferring explicit Candidate.Title
// over PartKey=="title"/SourceRanges heuristics) and the opening lead excerpt
// (first leadChars runes of the opening passage or hydrated Candidate.LeadPassage).
func extractTitleAndLead(b *recordBundle, leadChars int) (title, lead string) {
	if leadChars <= 0 {
		leadChars = 250
	}
	title = strings.TrimSpace(b.Title)
	inspect := func(c quivrplugin.Candidate) {
		if title == "" {
			title = strings.TrimSpace(c.Title)
		}
		if parts, ok := sliceSourceRanges(c); ok {
			leadSeg := isLeadSegment(c)
			for i, sr := range c.SourceRanges {
				if sr.PartKey == "title" {
					if title == "" {
						title = strings.TrimSpace(parts[i])
					}
				} else if lead == "" && (sr.Start == 0 || leadSeg) {
					lead = truncateRunes(strings.TrimSpace(parts[i]), leadChars)
				}
			}
			return
		}
		txt := strings.TrimSpace(candidateBodyText(c))
		if txt == "" {
			return
		}
		if c.PartKey == "title" && len(c.SourceRanges) <= 1 {
			if title == "" {
				title = txt
			}
			return
		}
		if isLeadSegment(c) && lead == "" {
			lead = truncateRunes(stripLeadingTitle(title, txt), leadChars)
		}
	}
	if b.LeadSeg != nil {
		inspect(*b.LeadSeg)
	}
	if b.HasBestSeg && (b.LeadSeg == nil || b.BestSeg.SegmentID != b.LeadSeg.SegmentID) {
		inspect(b.BestSeg)
	}
	if lead == "" && b.LeadPassage != "" {
		if leadBody := stripLeadingTitle(title, b.LeadPassage); leadBody != "" {
			lead = truncateRunes(leadBody, leadChars)
		}
	}
	return title, lead
}

// extractOpeningParts separates a lead segment into (title, openingBody) cleanly
// whether the segment is a single body chunk, a title-only chunk, or a packed
// [title, body@0] segment. Prefers explicitTitle / Candidate.Title when present.
func extractOpeningParts(c quivrplugin.Candidate, explicitTitle string) (title, openingBody string) {
	title = strings.TrimSpace(explicitTitle)
	if title == "" {
		title = strings.TrimSpace(c.Title)
	}
	if parts, ok := sliceSourceRanges(c); ok {
		var bodyBuf strings.Builder
		for i, sr := range c.SourceRanges {
			seg := strings.TrimSpace(parts[i])
			if seg == "" {
				continue
			}
			if sr.PartKey == "title" {
				if title == "" {
					title = seg
				}
				continue
			}
			if bodyBuf.Len() > 0 {
				bodyBuf.WriteString("\n\n")
			}
			bodyBuf.WriteString(seg)
		}
		return title, bodyBuf.String()
	}
	txt := strings.TrimSpace(candidateBodyText(c))
	if c.PartKey == "title" && len(c.SourceRanges) <= 1 {
		if title == "" {
			title = txt
		}
		return title, ""
	}
	return title, stripLeadingTitle(title, txt)
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
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

// computePoolIDF computes smoothed BM25-style IDF weights for each query aspect
// across the candidate pool so rarer, higher-specificity aspects dominate.
func computePoolIDF(aspects []string, docSets []map[string]bool) []float64 {
	n := float64(len(docSets))
	idfs := make([]float64, len(aspects))
	for i, asp := range aspects {
		df := 0.0
		for _, set := range docSets {
			if set[asp] {
				df++
			}
		}
		idfs[i] = math.Log((n+1.0)/(df+0.5)) + 0.5
	}
	return idfs
}

func scoreAspectCoverage(aspects []string, idfs []float64, docSet map[string]bool) float64 {
	if len(aspects) == 0 || len(docSet) == 0 {
		return 0
	}
	matched, total := 0.0, 0.0
	for i, asp := range aspects {
		total += idfs[i]
		if docSet[asp] {
			matched += idfs[i]
		}
	}
	if total == 0 {
		return 0
	}
	return matched / total
}

type stage1Confidence struct {
	Jaccard10   float64
	ScoreMargin float64
	DenseGap    float64
}

func distShapeQPP(cands []quivrplugin.Candidate) float64 {
	n := len(cands)
	if n < 6 {
		return 0
	}
	st := computeLegStats(cands)
	if st.std < 1e-9 {
		return 0
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
	return (topMean - midMean) / (st.std + 1e-9)
}

func computeStage1Confidence(dense, lexical []quivrplugin.Candidate, ranked []*recordBundle) stage1Confidence {
	conf := stage1Confidence{
		DenseGap: distShapeQPP(dense),
	}
	dTop := make(map[string]bool, 10)
	for _, c := range dense[:min(10, len(dense))] {
		dTop[recordKey(c)] = true
	}
	lTop := make(map[string]bool, 10)
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
		ref := ranked[min(9, len(ranked)-1)].FinalScore
		if top > 0 {
			conf.ScoreMargin = clamp01((top - ref) / top)
		}
	}
	return conf
}

func recordKey(c quivrplugin.Candidate) string {
	if c.RecordID != "" {
		return c.RecordID
	}
	return c.SegmentID
}

// buildSurrogate builds the inverted-pyramid document text passed to the deep
// reranker, strictly capped at maxChars runes including zone labels.
func buildSurrogate(b *recordBundle, maxChars int) string {
	if !b.HasBestSeg || maxChars <= 0 {
		return ""
	}
	var out strings.Builder
	explicitTitle := strings.TrimSpace(b.Title)
	if explicitTitle == "" {
		explicitTitle = strings.TrimSpace(b.BestSeg.Title)
	}
	bestIsLead := isLeadSegment(b.BestSeg)
	if bestIsLead {
		title, body := extractOpeningParts(b.BestSeg, explicitTitle)
		if title != "" {
			out.WriteString("[TITLE]: ")
			out.WriteString(truncateRunes(title, min(300, maxChars)))
		}
		if body != "" {
			if out.Len() > 0 {
				out.WriteString("\n\n")
			}
			out.WriteString("[LEAD]: ")
			out.WriteString(body)
		}
		if b.SecondSeg != nil && b.SecondSeg.SegmentID != b.BestSeg.SegmentID {
			secondText := strings.TrimSpace(candidateBodyText(*b.SecondSeg))
			if secondText != "" {
				out.WriteString("\n\n[SUPPORTING PASSAGE]: ")
				out.WriteString(truncateRunes(secondText, max(400, maxChars/3)))
			}
		}
	} else if b.LeadSeg != nil {
		title, leadBody := extractOpeningParts(*b.LeadSeg, explicitTitle)
		if title != "" {
			out.WriteString("[TITLE]: ")
			out.WriteString(truncateRunes(title, min(300, maxChars/4)))
		}
		if leadBody != "" {
			if out.Len() > 0 {
				out.WriteString("\n\n")
			}
			out.WriteString("[LEAD]: ")
			out.WriteString(truncateRunes(leadBody, min(600, maxChars/2)))
		}
		bestText := strings.TrimSpace(candidateBodyText(b.BestSeg))
		if bestText != "" {
			if out.Len() > 0 {
				out.WriteString("\n\n")
			}
			out.WriteString("[MATCHED DEEP PASSAGE]: ")
			out.WriteString(bestText)
		}
	} else if leadText := strings.TrimSpace(b.LeadPassage); leadText != "" {
		title := explicitTitle
		leadBody := stripLeadingTitle(title, leadText)
		if title != "" {
			out.WriteString("[TITLE]: ")
			out.WriteString(truncateRunes(title, min(300, maxChars/4)))
		}
		if leadBody != "" {
			if out.Len() > 0 {
				out.WriteString("\n\n")
			}
			out.WriteString("[LEAD]: ")
			out.WriteString(truncateRunes(leadBody, min(600, maxChars/2)))
		}
		bestText := strings.TrimSpace(candidateBodyText(b.BestSeg))
		if bestText != "" {
			if out.Len() > 0 {
				out.WriteString("\n\n")
			}
			out.WriteString("[MATCHED DEEP PASSAGE]: ")
			out.WriteString(bestText)
		}
	} else {
		if explicitTitle != "" {
			out.WriteString("[TITLE]: ")
			out.WriteString(truncateRunes(explicitTitle, min(300, maxChars/4)))
			out.WriteString("\n\n")
		}
		bestText := strings.TrimSpace(candidateBodyText(b.BestSeg))
		out.WriteString("[DEEP BODY EXCERPT — NOT IN ARTICLE LEAD]: ")
		out.WriteString(bestText)
	}
	res := out.String()
	if utf8.RuneCountInString(res) > maxChars {
		return truncateRunes(res, maxChars)
	}
	return res
}

// buildLeadView builds a compact title+lead view for the Recall Band, strictly
// bounded to maxChars runes.
func buildLeadView(b *recordBundle, maxChars int) string {
	if !b.HasBestSeg || maxChars <= 0 {
		return ""
	}
	explicitTitle := strings.TrimSpace(b.Title)
	if explicitTitle == "" {
		explicitTitle = strings.TrimSpace(b.BestSeg.Title)
	}
	var title, body string
	if b.LeadSeg != nil {
		title, body = extractOpeningParts(*b.LeadSeg, explicitTitle)
	} else if isLeadSegment(b.BestSeg) {
		title, body = extractOpeningParts(b.BestSeg, explicitTitle)
	} else if leadText := strings.TrimSpace(b.LeadPassage); leadText != "" {
		title = explicitTitle
		body = stripLeadingTitle(title, leadText)
	} else {
		return buildSurrogate(b, maxChars)
	}
	var out strings.Builder
	if title != "" {
		out.WriteString("[TITLE]: ")
		out.WriteString(truncateRunes(title, min(300, maxChars)))
	}
	if body != "" {
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString("[LEAD]: ")
		out.WriteString(body)
	}
	res := out.String()
	if utf8.RuneCountInString(res) > maxChars {
		return truncateRunes(res, maxChars)
	}
	return res
}

// zScores standardizes values per query: (v - mean) / std (0 when flat).
func zScores(vals []float64) []float64 {
	out := make([]float64, len(vals))
	if len(vals) == 0 {
		return out
	}
	mean := 0.0
	for _, v := range vals {
		mean += v
	}
	mean /= float64(len(vals))
	varSum := 0.0
	for _, v := range vals {
		d := v - mean
		varSum += d * d
	}
	std := math.Sqrt(varSum / float64(len(vals)))
	if std < 1e-9 {
		return out
	}
	for i, v := range vals {
		out[i] = (v - mean) / std
	}
	return out
}

func minMaxNormalize(vals []float64) []float64 {
	out := make([]float64, len(vals))
	if len(vals) == 0 {
		return out
	}
	lo, hi := slices.Min(vals), slices.Max(vals)
	if hi-lo < 1e-12 {
		for i := range out {
			out[i] = 0.5
		}
		return out
	}
	for i, v := range vals {
		out[i] = (v - lo) / (hi - lo)
	}
	return out
}

func dedupByRecord(cands []quivrplugin.Candidate) []quivrplugin.Candidate {
	seen := make(map[string]bool, len(cands))
	out := make([]quivrplugin.Candidate, 0, len(cands))
	sorted := make([]quivrplugin.Candidate, len(cands))
	copy(sorted, cands)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })
	for _, c := range sorted {
		k := recordKey(c)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

func sortBundlesDeterministic(bundles []*recordBundle) {
	sort.SliceStable(bundles, func(i, j int) bool {
		if bundles[i].FinalScore != bundles[j].FinalScore {
			return bundles[i].FinalScore > bundles[j].FinalScore
		}
		return bundles[i].BestSeg.SegmentID < bundles[j].BestSeg.SegmentID
	})
}
