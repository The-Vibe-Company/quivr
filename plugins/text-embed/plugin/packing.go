package plugin

import (
	"context"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/sdks/go/quivrplugin"
)

const sourceSeparator = "\n\n"

type paragraph struct {
	key        string
	start, end int
	text       string
	run        int
}

func (c configuration) documentTitle(parts []quivrplugin.IngestPart, includeHeadline bool) (string, error) {
	title := ""
	titles := 0
	byKey := map[string]string{}
	for _, p := range parts {
		byKey[p.Key] = p.Text
		if p.Role == "title" {
			titles++
			if includeHeadline {
				title = strings.TrimSpace(p.Text)
			}
		}
	}
	if titles > 1 {
		return "", quivrplugin.TerminalIngestError("segmentation_limit", "more than one title")
	}
	if c.TitleSource == "none" {
		return "", nil
	}
	for _, key := range c.TitleContextParts {
		text, ok := byKey[key]
		if !ok {
			return "", quivrplugin.TerminalIngestError("invalid_title_source", "configured title context Part is missing")
		}
		text = strings.TrimSpace(text)
		if text != "" {
			if title != "" {
				title += " — "
			}
			title += text
		}
	}
	return title, nil
}
func (c configuration) documentInput(title, body string) string {
	if c.TitleSource == "inline" && title != "" {
		body = title + sourceSeparator + body
		title = ""
	}
	if title == "" {
		title = "none"
	}
	return strings.NewReplacer("{prefix}", c.DocumentPrefix, "{title}", title, "{text}", body).Replace(c.DocumentTemplate)
}
func encodeOne(ctx context.Context, t tokenCounter, text string, special bool) (tokenEncoding, error) {
	out, err := t.Encode(ctx, []tokenInput{{Text: text, Special: special}})
	if err != nil || len(out) != 1 {
		return tokenEncoding{}, quivrplugin.RetryableIngestError("tokenizer_unavailable", "configured model tokenizer unavailable")
	}
	e := out[0]
	if e.Tokens < 0 {
		return e, quivrplugin.RetryableIngestError("tokenizer_unavailable", "invalid token count")
	}
	if !special {
		previous := 0
		n := utf8.RuneCountInString(text)
		if len(e.Offsets) != e.Tokens {
			return e, quivrplugin.RetryableIngestError("tokenizer_unavailable", "invalid tokenizer offsets")
		}
		for _, offset := range e.Offsets {
			if offset[0] < previous || offset[1] < offset[0] || offset[1] > n {
				return e, quivrplugin.RetryableIngestError("tokenizer_unavailable", "invalid tokenizer offsets")
			}
			previous = offset[0]
		}
	}
	return e, nil
}
func paragraphText(group []paragraph) string {
	var b strings.Builder
	for i, p := range group {
		if i > 0 && (p.key != group[i-1].key || p.start != group[i-1].end) {
			b.WriteString(sourceSeparator)
		}
		b.WriteString(p.text)
	}
	return b.String()
}
func paragraphRanges(group []paragraph) []quivrplugin.SourceRange {
	var out []quivrplugin.SourceRange
	for _, p := range group {
		if len(out) > 0 && out[len(out)-1].PartKey == p.key && out[len(out)-1].End == p.start {
			out[len(out)-1].End = p.end
		} else {
			out = append(out, quivrplugin.SourceRange{PartKey: p.key, Start: p.start, End: p.end})
		}
	}
	return out
}
func (c configuration) packedSegments(ctx context.Context, parts []quivrplugin.IngestPart, t tokenCounter) ([]quivrplugin.Segment, []string, error) {
	if len(parts) > 64 {
		return nil, nil, quivrplugin.TerminalIngestError("segmentation_limit", "more than 64 Parts")
	}
	total := 0
	for _, p := range parts {
		total += len(p.Text)
		if !utf8.ValidString(p.Text) || strings.ContainsRune(p.Text, 0) {
			return nil, nil, quivrplugin.TerminalIngestError("invalid_text", "text must be UTF-8 without NUL")
		}
	}
	if total > 256<<10 {
		return nil, nil, quivrplugin.TerminalIngestError("segmentation_limit", "more than 256 KiB of text")
	}
	titleOnly := false
	titleOnlyContext := false
	modelBody := func(body string) string {
		if titleOnly {
			return ""
		}
		return body
	}
	title, err := c.documentTitle(parts, true)
	if err != nil {
		return nil, nil, err
	}
	var titlePart quivrplugin.IngestPart
	titlePartIdx := -1
	firstBodyIdx := -1
	firstBodyText := ""
	for idx, part := range parts {
		if part.Role == "title" && strings.TrimSpace(part.Text) != "" && titlePartIdx < 0 {
			titlePart = part
			titlePartIdx = idx
		}
		if part.Role == "body" && strings.TrimSpace(part.Text) != "" && firstBodyIdx < 0 {
			firstBodyIdx = idx
			firstBodyText = part.Text
		}
	}
	leadSentence := ""
	if c.ContextualAnchor == "title_lead_text" || c.ContextualAnchor == "title_lead_slot" {
		leadSource := firstBodyText
		if c.NormalizeWireText {
			leadSource = normalizeWireText(leadSource)
		}
		leadSentence = extractLeadSentence(leadSource)
	}
	inputForBody := func(body string, isLeadChunk bool) string {
		body = modelBody(body)
		if titleOnlyContext && title != "" {
			body = title + sourceSeparator + body
		}
		if !titleOnly && (c.ContextualAnchor != "" || c.NormalizeWireText) {
			return c.buildAnchoredInput(title, leadSentence, body, isLeadChunk)
		}
		return c.documentInput(title, body)
	}
	// Body budget is separate from the model's full templated-input window.
	fitsBudget := func(group []paragraph, budget int, isLeadChunk bool) (bool, int, error) {
		text := paragraphText(group)
		e, err := encodeOne(ctx, t, modelBody(text), false)
		if err != nil {
			return false, 0, err
		}
		full, err := encodeOne(ctx, t, inputForBody(text, isLeadChunk), true)
		if err != nil {
			return false, 0, err
		}
		bounded := utf8.RuneCountInString(text) <= 16384
		for _, r := range paragraphRanges(group) {
			bounded = bounded && r.End-r.Start <= 4096
		}
		return e.Tokens <= budget && full.Tokens <= c.MaxTokens && bounded, e.Tokens, nil
	}
	fits := func(group []paragraph) (bool, int, error) {
		return fitsBudget(group, c.BodyTokens, false)
	}
	units := []paragraph{}
	run := 0
	bodyParts := parts
	hasBody := false
	for _, part := range parts {
		if part.Role == "body" && strings.TrimSpace(part.Text) != "" {
			hasBody = true
			break
		}
	}
	if !hasBody {
		titleOnly = c.TitleSource == "title" && strings.Contains(c.DocumentTemplate, "{title}")
		if !titleOnly {
			// The headline is already the source passage. Preserve selected
			// context even when the template has no separate title field.
			titleOnlyContext = c.TitleSource == "title"
			title, err = c.documentTitle(parts, false)
			if err != nil {
				return nil, nil, err
			}
		}
		bodyParts = nil
		for _, part := range parts {
			if part.Role == "title" && strings.TrimSpace(part.Text) != "" {
				part.Role = "body"
				bodyParts = []quivrplugin.IngestPart{part}
				break
			}
		}
	}
	for _, part := range bodyParts {
		if part.Role != "body" || strings.TrimSpace(part.Text) == "" {
			run++
			continue
		}
		runes := []rune(part.Text)
		// Keep delimiters in the source range; merging adjacent ranges from the
		// same Part avoids inventing an extra separator inside a paragraph stream.
		for start := 0; start < len(runes); {
			end := len(runes)
			for n := start + 1; n < len(runes); n++ {
				if runes[n-1] == '\n' && (runes[n] == '\n' || runes[n] == '\r' && n+1 < len(runes) && runes[n+1] == '\n') {
					end = n + 1
					if runes[n] == '\r' {
						end++
					}
					for end < len(runes) {
						if runes[end] == '\n' {
							end++
						} else if runes[end] == '\r' && end+1 < len(runes) && runes[end+1] == '\n' {
							end += 2
						} else {
							break
						}
					}
					break
				}
			}
			raw := paragraph{key: part.Key, start: start, end: end, text: string(runes[start:end]), run: run}
			for raw.start < raw.end {
				isFirstUnit := len(units) == 0 && !titleOnly
				unitBudget := c.BodyTokens
				if isFirstUnit && c.LeadMaxTokens > 0 {
					unitBudget = c.LeadMaxTokens
				} else if isFirstUnit && c.DualLeadSegment {
					unitBudget = min(180, c.BodyTokens)
				}
				fitsUnit := func(ps []paragraph) (bool, int, error) {
					if isFirstUnit {
						return fitsBudget(ps, unitBudget, true)
					}
					return fits(ps)
				}
				ok, _, err := fitsUnit([]paragraph{raw})
				if err != nil {
					return nil, nil, err
				}
				if ok {
					units = append(units, raw)
					break
				}
				if titleOnly {
					return nil, nil, quivrplugin.TerminalIngestError("segmentation_limit", "title-only input exceeds model or excerpt limit")
				}
				// Only an oversized paragraph is split. Binary search the longest
				// safe token boundary that fits both body and full-model budgets,
				// then prefer an existing sentence/line break in that window.
				e, err := encodeOne(ctx, t, raw.text, false)
				if err != nil {
					return nil, nil, err
				}
				local := []rune(raw.text)
				limit := min(e.Tokens, unitBudget)
				validCuts := make([]int, 0, limit)
				for k := 1; k <= limit; k++ {
					candidate := e.Offsets[k-1][1]
					if k < e.Tokens && candidate > e.Offsets[k][0] {
						continue
					}
					if candidate <= 0 || candidate >= len(local) {
						continue
					}
					if len(validCuts) == 0 || validCuts[len(validCuts)-1] != candidate {
						validCuts = append(validCuts, candidate)
					}
				}
				cut := 0
				lo, hi := 0, len(validCuts)-1
				for lo <= hi {
					mid := lo + (hi-lo)/2
					cand := validCuts[mid]
					piece := raw
					piece.end = raw.start + cand
					piece.text = string(local[:cand])
					ok, _, err := fitsUnit([]paragraph{piece})
					if err != nil {
						return nil, nil, err
					}
					if ok {
						cut = cand
						lo = mid + 1
					} else {
						hi = mid - 1
					}
				}
				if cut == 0 {
					return nil, nil, quivrplugin.TerminalIngestError("segmentation_limit", "title/template leaves no safe body token boundary")
				}
				snapped := snap(local, 0, cut)
				if snapped > 0 {
					candidate := raw
					candidate.end = raw.start + snapped
					candidate.text = string(local[:snapped])
					if ok, _, err := fitsUnit([]paragraph{candidate}); err != nil {
						return nil, nil, err
					} else if ok {
						cut = snapped
					}
				}
				piece := raw
				piece.end = raw.start + cut
				piece.text = string(local[:cut])
				units = append(units, piece)
				raw.start = piece.end
				raw.text = string(local[cut:])
			}
			start = end
		}
	}
	if len(units) == 0 {
		return nil, nil, quivrplugin.TerminalIngestError("no_indexable_text", "no title or body text")
	}
	groups := [][]paragraph{}
	for _, unit := range units {
		if len(groups) > 0 {
			last := len(groups) - 1
			budget := c.BodyTokens
			isLead := last == 0
			if isLead && c.LeadMaxTokens > 0 {
				budget = c.LeadMaxTokens
			}
			candidate := append(append([]paragraph(nil), groups[last]...), unit)
			ok, _, err := fitsBudget(candidate, budget, isLead)
			if err != nil {
				return nil, nil, err
			}
			if ok && groups[last][len(groups[last])-1].run == unit.run {
				groups[last] = candidate
				continue
			}
			// Starting a new group: optionally prepend trailing overlap from the
			// previous group while keeping canonical source offsets exact.
			if c.OverlapTokens > 0 && groups[last][len(groups[last])-1].run == unit.run {
				seed, err := trailingOverlapUnits(ctx, t, groups[last], c.OverlapTokens)
				if err != nil {
					return nil, nil, err
				}
				if len(seed) > 0 {
					withSeed := append(seed, unit)
					if okSeed, _, err := fitsBudget(withSeed, c.BodyTokens, false); err != nil {
						return nil, nil, err
					} else if okSeed {
						groups = append(groups, withSeed)
						continue
					}
				}
			}
		}
		groups = append(groups, []paragraph{unit})
	}
	if c.RebalanceTail && c.OverlapTokens == 0 && len(groups) > 1 && (c.LeadMaxTokens == 0 || len(groups) > 2) {
		last := len(groups) - 1
		left, right := groups[last-1], groups[last]
		_, rightTokens, err := fits(right)
		if err != nil {
			return nil, nil, err
		}
		if float64(rightTokens) < float64(c.BodyTokens)*c.TailMinFraction && left[len(left)-1].run == right[0].run {
			_, leftTokens, err := fits(left)
			if err != nil {
				return nil, nil, err
			}
			best := math.Abs(float64(leftTokens - rightTokens))
			bestCut := len(left)
			for cut := len(left) - 1; cut > 0; cut-- {
				a, b := left[:cut], append(append([]paragraph(nil), left[cut:]...), right...)
				oka, na, err := fits(a)
				if err != nil {
					return nil, nil, err
				}
				okb, nb, err := fits(b)
				if err != nil {
					return nil, nil, err
				}
				delta := math.Abs(float64(na - nb))
				if oka && okb && delta < best {
					best = delta
					bestCut = cut
				}
			}
			if bestCut < len(left) {
				groups[last-1] = left[:bestCut]
				groups[last] = append(append([]paragraph(nil), left[bestCut:]...), right...)
			}
		}
	}
	canPrependTitle := c.TitleSource != "none" && !titleOnly && titlePartIdx >= 0 && firstBodyIdx >= 0 && titlePartIdx < firstBodyIdx
	titleRuneLen := utf8.RuneCountInString(titlePart.Text)
	sepRuneLen := utf8.RuneCountInString(sourceSeparator)
	segments := make([]quivrplugin.Segment, 0, len(groups)+1)
	inputs := make([]string, 0, len(groups)+1)
	emittedDualLead := false

	if c.DualLeadSegment && canPrependTitle && titleRuneLen <= 4096 && len(groups) >= 2 {
		leadBudget := c.LeadMaxTokens
		if leadBudget <= 0 {
			leadBudget = min(180, c.BodyTokens)
		}
		leadGroup := []paragraph{units[0]}
		for i := 1; i < len(units); i++ {
			if units[i].run != units[0].run {
				break
			}
			cand := append(append([]paragraph(nil), leadGroup...), units[i])
			ok, _, err := fitsBudget(cand, leadBudget, true)
			if err != nil {
				return nil, nil, err
			}
			if !ok {
				break
			}
			leadGroup = cand
		}
		leadBody := paragraphText(leadGroup)
		if titleRuneLen+sepRuneLen+utf8.RuneCountInString(leadBody) <= 16384 {
			leadRanges := append([]quivrplugin.SourceRange{{PartKey: titlePart.Key, Start: 0, End: titleRuneLen}}, paragraphRanges(leadGroup)...)
			leadInput := inputForBody(leadBody, true)
			e, err := encodeOne(ctx, t, leadInput, true)
			if err != nil {
				return nil, nil, err
			}
			segments = append(segments, quivrplugin.Segment{
				PartKey:         leadRanges[0].PartKey,
				Start:           leadRanges[0].Start,
				End:             leadRanges[0].End,
				SourceRanges:    leadRanges,
				SourceSeparator: sourceSeparator,
				Vectors:         map[string][]float32{},
				Provenance:      map[string]any{"model": c.Model, "input_tokens": e.Tokens, "token_estimate": c.Tokenizer == nil, "packing": "lead_summary"},
			})
			inputs = append(inputs, leadInput)
			emittedDualLead = true
		}
	}

	for n, group := range groups {
		ranges := paragraphRanges(group)
		body := paragraphText(group)
		if n == 0 && c.IncludeTitleInLead && !emittedDualLead && canPrependTitle && titleRuneLen <= 4096 &&
			titleRuneLen+sepRuneLen+utf8.RuneCountInString(body) <= 16384 {
			ranges = append([]quivrplugin.SourceRange{{PartKey: titlePart.Key, Start: 0, End: titleRuneLen}}, ranges...)
		}
		input := inputForBody(body, n == 0)
		e, err := encodeOne(ctx, t, input, true)
		if err != nil {
			return nil, nil, err
		}
		segments = append(segments, quivrplugin.Segment{PartKey: ranges[0].PartKey, Start: ranges[0].Start, End: ranges[0].End, SourceRanges: ranges, SourceSeparator: sourceSeparator, Vectors: map[string][]float32{}, Provenance: map[string]any{"model": c.Model, "input_tokens": e.Tokens, "token_estimate": c.Tokenizer == nil, "packing": "paragraphs"}})
		inputs = append(inputs, input)
	}
	return segments, inputs, nil
}

// Prefer the latest paragraph, then line, then sentence end in the latter
// half of a window. Hard cuts still preserve every source code point.
func snap(runes []rune, start, end int) int {
	floor := start + (end-start)/2
	for k := end; k > floor; k-- {
		if k > start+1 && runes[k-1] == '\n' && runes[k-2] == '\n' {
			return k
		}
	}
	for k := end; k > floor; k-- {
		if runes[k-1] == '\n' {
			return k
		}
	}
	for k := end; k > floor; k-- {
		if k > start+1 && unicode.IsSpace(runes[k-1]) && strings.ContainsRune(".!?。！？", runes[k-2]) {
			return k
		}
	}
	return end
}
