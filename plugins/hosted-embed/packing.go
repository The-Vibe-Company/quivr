package main

import (
	"context"
	"math"
	"strings"
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

func (c configuration) gemmaTemplate() bool {
	return c.DocumentTemplate == "gemma" || c.DocumentTemplate == "auto" && strings.Contains(strings.ToLower(c.Model), "embeddinggemma")
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
	if c.gemmaTemplate() {
		if title == "" {
			title = "none"
		}
		return "title: " + title + " | text: " + body
	}
	prefix := c.DocumentPrefix
	// Title context is opt-in to generic templates; preserve their provider format.
	if title != "" {
		return prefix + title + sourceSeparator + body
	}
	return prefix + body
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
	// Body budget is separate from the model's full templated-input window.
	fits := func(group []paragraph) (bool, int, error) {
		text := paragraphText(group)
		e, err := encodeOne(ctx, t, modelBody(text), false)
		if err != nil {
			return false, 0, err
		}
		full, err := encodeOne(ctx, t, c.documentInput(title, modelBody(text)), true)
		if err != nil {
			return false, 0, err
		}
		bounded := utf8.RuneCountInString(text) <= 16384
		for _, r := range paragraphRanges(group) {
			bounded = bounded && r.End-r.Start <= 4096
		}
		return e.Tokens <= c.BodyTokens && full.Tokens <= c.MaxTokens && bounded, e.Tokens, nil
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
		titleOnly = c.TitleSource != "none"
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
				ok, _, err := fits([]paragraph{raw})
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
				// Only an oversized paragraph is split. Find the longest safe token
				// boundary that fits both body and full-model budgets, then prefer an
				// existing sentence/line break in that window.
				e, err := encodeOne(ctx, t, raw.text, false)
				if err != nil {
					return nil, nil, err
				}
				limit := min(e.Tokens, c.BodyTokens)
				cut := 0
				for k := limit; k > 0; k-- {
					candidate := e.Offsets[k-1][1]
					if k < e.Tokens && candidate > e.Offsets[k][0] {
						continue
					}
					if candidate <= 0 || candidate >= utf8.RuneCountInString(raw.text) {
						continue
					}
					piece := raw
					piece.end = raw.start + candidate
					piece.text = string([]rune(raw.text)[:candidate])
					if ok, _, err := fits([]paragraph{piece}); err != nil {
						return nil, nil, err
					} else if ok {
						cut = candidate
						break
					}
				}
				if cut == 0 {
					return nil, nil, quivrplugin.TerminalIngestError("segmentation_limit", "title/template leaves no safe body token boundary")
				}
				local := []rune(raw.text)
				snapped := snap(local, 0, cut)
				if snapped > 0 {
					candidate := raw
					candidate.end = raw.start + snapped
					candidate.text = string(local[:snapped])
					if ok, _, err := fits([]paragraph{candidate}); err != nil {
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
			candidate := append(append([]paragraph(nil), groups[last]...), unit)
			ok, _, err := fits(candidate)
			if err != nil {
				return nil, nil, err
			}
			if ok && groups[last][len(groups[last])-1].run == unit.run {
				groups[last] = candidate
				continue
			}
		}
		groups = append(groups, []paragraph{unit})
	}
	if c.RebalanceTail && len(groups) > 1 {
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
	segments := make([]quivrplugin.Segment, len(groups))
	inputs := make([]string, len(groups))
	for n, group := range groups {
		ranges := paragraphRanges(group)
		body := paragraphText(group)
		input := c.documentInput(title, modelBody(body))
		e, err := encodeOne(ctx, t, input, true)
		if err != nil {
			return nil, nil, err
		}
		segments[n] = quivrplugin.Segment{PartKey: ranges[0].PartKey, Start: ranges[0].Start, End: ranges[0].End, SourceRanges: ranges, SourceSeparator: sourceSeparator, Vectors: map[string][]float32{}, Provenance: map[string]any{"model": c.Model, "input_tokens": e.Tokens, "token_estimate": c.Tokenizer == nil, "packing": "paragraphs"}}
		inputs[n] = input
	}
	return segments, inputs, nil
}
