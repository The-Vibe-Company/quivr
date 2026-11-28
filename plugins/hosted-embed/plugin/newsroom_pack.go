package plugin

import (
	"context"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// Generic wire dateline: e.g. "PARIS (Agency) - ", "NEW YORK, 12 mai (Wire) — "
	reWireDateline = regexp.MustCompile(`^[A-ZÀ-Ÿ0-9][A-ZÀ-Ÿ0-9\s,'-]{1,35}\s*\([^)]{2,25}\)\s*[-–—]\s*`)
	// Generic trailing wire desk/initials signature on its own line: e.g. "ab/cd" or "ab/cd/ef"
	reTrailingCredit = regexp.MustCompile(`(?m)^[ \t]*[a-z]{2,4}/[a-z]{2,4}(?:/[a-z]{2,4})?[ \t]*$`)
	// Generic CMS/Markdown syntax artifacts
	reMarkdownHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reMarkdownBlockquote = regexp.MustCompile(`(?m)^>\s+`)
	reMarkdownLink       = regexp.MustCompile(`\[([^\]]+)\]\(https?://[^)\s]+\)`)
	reBareURL            = regexp.MustCompile(`https?://\S+`)
	reMarkdownEmphasis   = regexp.MustCompile(`\*{1,3}([^*\n]+)\*{1,3}`)
	reMultiSpaces        = regexp.MustCompile(`[ \t]{2,}`)
	reMultiNewlines      = regexp.MustCompile(`\n{3,}`)
)

// normalizeWireText removes generic wire datelines, trailing desk signatures,
// and CMS/Markdown formatting noise from text sent to the embedding model while
// leaving canonical stored source offsets untouched.
func normalizeWireText(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return raw
	}
	s = reWireDateline.ReplaceAllString(s, "")
	s = reMarkdownLink.ReplaceAllString(s, "$1")
	s = reBareURL.ReplaceAllString(s, "")
	s = reMarkdownHeading.ReplaceAllString(s, "")
	s = reMarkdownBlockquote.ReplaceAllString(s, "")
	s = reMarkdownEmphasis.ReplaceAllString(s, "$1")
	s = reTrailingCredit.ReplaceAllString(s, "")
	// Clean up stray empty parentheses left after bare URL removal (e.g. "()").
	s = strings.ReplaceAll(s, "()", "")
	s = reMultiSpaces.ReplaceAllString(s, " ")
	s = reMultiNewlines.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	if s == "" {
		return raw
	}
	return s
}

// extractLeadSentence returns the opening sentence (or first paragraph capped
// at ~280 runes) of a document body for zero-LLM contextual chunk anchoring.
func extractLeadSentence(body string) string {
	s := strings.TrimSpace(body)
	if s == "" {
		return ""
	}
	if idx := strings.Index(s, "\n\n"); idx > 0 {
		s = strings.TrimSpace(s[:idx])
	}
	runes := []rune(s)
	maxRunes := 280
	// Find the first sentence terminator followed by whitespace after >= 40 runes.
	for i := 40; i < len(runes) && i < maxRunes; i++ {
		if strings.ContainsRune(".!?。！？", runes[i]) && (i+1 == len(runes) || unicode.IsSpace(runes[i+1])) {
			return strings.TrimSpace(string(runes[:i+1]))
		}
	}
	if len(runes) <= maxRunes {
		return s
	}
	// Snap back to last whitespace before maxRunes.
	cut := maxRunes
	for cut > 120 && !unicode.IsSpace(runes[cut-1]) {
		cut--
	}
	return strings.TrimSpace(string(runes[:cut]))
}

// buildAnchoredInput formats the embedding model input for one passage,
// applying optional wire-text normalization and zero-LLM contextual anchoring.
func (c configuration) buildAnchoredInput(title, leadSentence, body string, isLeadChunk bool) string {
	t := title
	b := body
	lead := leadSentence
	if c.NormalizeWireText {
		if t != "" {
			t = normalizeWireText(t)
		}
		if b != "" {
			b = normalizeWireText(b)
		}
		if lead != "" {
			lead = normalizeWireText(lead)
		}
	}
	switch c.ContextualAnchor {
	case "title_in_text":
		if t != "" && b != "" {
			b = t + sourceSeparator + b
		}
	case "title_lead_text":
		if !isLeadChunk && t != "" && lead != "" && b != "" {
			b = t + " — " + lead + sourceSeparator + b
		} else if t != "" && b != "" {
			b = t + sourceSeparator + b
		}
	case "title_lead_slot":
		if !isLeadChunk && t != "" && lead != "" {
			t = t + " — " + lead
		} else if !isLeadChunk && t == "" && lead != "" {
			t = lead
		}
	}
	return c.documentInput(t, b)
}

// trailingOverlapUnits selects trailing paragraph units (or a snapped suffix of
// the final paragraph) from prevGroup totaling at most overlapTokens, preserving
// exact rune offsets in the source Part.
func trailingOverlapUnits(ctx context.Context, t tokenCounter, prevGroup []paragraph, overlapTokens int) ([]paragraph, error) {
	if overlapTokens <= 0 || len(prevGroup) == 0 {
		return nil, nil
	}
	lastRun := prevGroup[len(prevGroup)-1].run
	var selected []paragraph
	for i := len(prevGroup) - 1; i >= 0; i-- {
		u := prevGroup[i]
		if u.run != lastRun {
			break
		}
		candidate := append([]paragraph{u}, selected...)
		e, err := encodeOne(ctx, t, paragraphText(candidate), false)
		if err != nil {
			return nil, err
		}
		if e.Tokens <= overlapTokens {
			selected = candidate
			continue
		}
		// If nothing has been selected yet and the final paragraph alone exceeds
		// overlapTokens, extract a snapped trailing suffix of <= overlapTokens.
		if len(selected) == 0 {
			suffix, err := trailingParagraphSuffix(ctx, t, u, overlapTokens)
			if err != nil {
				return nil, err
			}
			if suffix != nil {
				selected = []paragraph{*suffix}
			}
		}
		break
	}
	return selected, nil
}

// trailingParagraphSuffix slices the last <= overlapTokens runes of p, snapping
// forward to a sentence or whitespace boundary so the overlap starts cleanly.
func trailingParagraphSuffix(ctx context.Context, t tokenCounter, p paragraph, overlapTokens int) (*paragraph, error) {
	e, err := encodeOne(ctx, t, p.text, false)
	if err != nil {
		return nil, err
	}
	if e.Tokens <= overlapTokens {
		cp := p
		return &cp, nil
	}
	if e.Tokens < 2 || len(e.Offsets) != e.Tokens {
		return nil, nil
	}
	startTok := e.Tokens - overlapTokens
	if startTok <= 0 || startTok >= e.Tokens {
		return nil, nil
	}
	runeStart := e.Offsets[startTok][0]
	runes := []rune(p.text)
	if runeStart <= 0 || runeStart >= len(runes) {
		return nil, nil
	}
	// Snap forward up to half the suffix length to the first sentence boundary,
	// falling back to the first word boundary so overlap stays close to target.
	maxSnap := runeStart + (len(runes)-runeStart)/2
	snapped := runeStart
	firstWord := 0
	for k := runeStart; k < maxSnap; k++ {
		if k > 0 && unicode.IsSpace(runes[k-1]) && !unicode.IsSpace(runes[k]) {
			if firstWord == 0 {
				firstWord = k
			}
			if k >= 2 && strings.ContainsRune(".!?。！？", runes[k-2]) {
				snapped = k
				break
			}
		}
	}
	if snapped == runeStart && firstWord > 0 {
		snapped = firstWord
	}
	if snapped <= 0 || snapped >= len(runes) {
		return nil, nil
	}
	tail := string(runes[snapped:])
	if strings.TrimSpace(tail) == "" || utf8.RuneCountInString(tail) == 0 {
		return nil, nil
	}
	out := paragraph{
		key:   p.key,
		start: p.start + snapped,
		end:   p.end,
		text:  tail,
		run:   p.run,
	}
	return &out, nil
}
