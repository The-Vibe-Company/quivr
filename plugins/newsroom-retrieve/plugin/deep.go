package plugin

import (
	"math"
	"strings"
	"unicode/utf8"
)

// Deep-tier document views, multi-passage scoring, and score calibration.

// recordZones returns the title, the opening (offset-0) segment text, and the
// best-matching non-opening passage (empty if none was retrieved).
func recordZones(b *recordBundle) (title, opening, body string) {
	title, _, _, _, _ = extractZoneTexts(b, 1)
	if b.LeadSeg != nil {
		opening = strings.TrimSpace(candidateBodyText(*b.LeadSeg))
	} else if b.HasBestSeg && isLeadSegment(b.BestSeg) {
		opening = strings.TrimSpace(candidateBodyText(b.BestSeg))
	}
	switch {
	case b.HasBestSeg && !isLeadSegment(b.BestSeg):
		body = strings.TrimSpace(candidateBodyText(b.BestSeg))
	case b.DeepBestSeg != nil:
		body = strings.TrimSpace(candidateBodyText(*b.DeepBestSeg))
	case b.SecondSeg != nil && !isLeadSegment(*b.SecondSeg):
		body = strings.TrimSpace(candidateBodyText(*b.SecondSeg))
	}
	if body != "" && opening != "" && body == opening {
		body = ""
	}
	return title, opening, body
}

// writeTitle writes the title zone (at most 300 runes) and returns the
// remaining character budget; an empty title consumes nothing.
func writeTitle(out *strings.Builder, title string, budget int) int {
	titleBudget := min(budget, 300)
	return budget - (titleBudget - writeZone(out, "[TITLE]: ", title, titleBudget))
}

func writeZone(out *strings.Builder, label, text string, budget int) int {
	text = strings.TrimSpace(text)
	if text == "" || budget <= 0 {
		return budget
	}
	if out.Len() > 0 {
		out.WriteString("\n\n")
	}
	t := truncateRunes(text, budget)
	out.WriteString(label)
	out.WriteString(t)
	return budget - utf8.RuneCountInString(t)
}

// buildRichSurrogate builds an inverted-pyramid surrogate with explicit zones:
// [TITLE] + [LEAD] (opening segment, up to leadChars) + [BEST BODY PASSAGE]
// (best non-opening passage), filling the rest of maxChars. Records whose
// opening segment was not retrieved are marked so the reranker can discount
// buried mentions.
func buildRichSurrogate(b *recordBundle, maxChars, leadChars int) string {
	if !b.HasBestSeg {
		return ""
	}
	title, opening, body := recordZones(b)
	var out strings.Builder
	budget := maxChars
	if title != "" && !(opening != "" && strings.HasPrefix(opening, title)) {
		budget = writeTitle(&out, title, budget)
	}
	if opening != "" {
		leadBudget := leadChars
		if body == "" {
			leadBudget = budget
		}
		leadBudget = min(budget, leadBudget)
		budget -= leadBudget - writeZone(&out, "[LEAD]: ", opening, leadBudget)
		writeZone(&out, "[BEST BODY PASSAGE]: ", body, budget)
	} else {
		writeZone(&out, "[DEEP BODY EXCERPT — NOT IN ARTICLE LEAD]: ", body, budget)
	}
	return out.String()
}

// buildLeadView returns the title + opening-segment view used by multi-passage
// scoring (falls back to the rich surrogate when no opening was retrieved).
func buildLeadView(b *recordBundle, maxChars int) string {
	title, opening, _ := recordZones(b)
	if opening == "" {
		return buildRichSurrogate(b, maxChars, maxChars)
	}
	var out strings.Builder
	budget := maxChars
	if title != "" && !strings.HasPrefix(opening, title) {
		budget = writeTitle(&out, title, budget)
	}
	writeZone(&out, "[LEAD]: ", opening, budget)
	return out.String()
}

// buildBodyView returns the title + best non-opening passage view, if any.
func buildBodyView(b *recordBundle, maxChars int) (string, bool) {
	title, opening, body := recordZones(b)
	if body == "" || opening == "" {
		return "", false
	}
	var out strings.Builder
	budget := writeTitle(&out, title, maxChars)
	writeZone(&out, "[BODY PASSAGE]: ", body, budget)
	return out.String(), true
}

// combineViews merges lead-view and body-view relevance (same score space).
func combineViews(lead, body float64, mode string, bodyWeight float64) float64 {
	switch mode {
	case "max":
		return math.Max(lead, body)
	case "lead_gated":
		// The body view can only lift a record, never sink a strong lead.
		return lead + bodyWeight*math.Max(0, body-lead)
	case "maxmin":
		hi, lo := math.Max(lead, body), math.Min(lead, body)
		return (1-bodyWeight)*hi + bodyWeight*lo
	default: // blend
		return (1-bodyWeight)*lead + bodyWeight*body
	}
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
		varSum += (v - mean) * (v - mean)
	}
	std := math.Sqrt(varSum / float64(len(vals)))
	if std == 0 {
		return out
	}
	for i, v := range vals {
		out[i] = (v - mean) / std
	}
	return out
}

// surrogateFor dispatches the configured surrogate representation.
func surrogateFor(b *recordBundle, cfg configuration) string {
	if cfg.SurrogateMode == "rich" {
		return buildRichSurrogate(b, cfg.RerankMaxDocChars, cfg.RerankLeadChars)
	}
	return buildSurrogate(b, cfg.SurrogateMode, cfg.RerankMaxDocChars)
}
