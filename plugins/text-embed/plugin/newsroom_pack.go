package plugin

import (
	"context"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// Generic wire dateline: uppercase city/date + agency parenthetical + dash, e.g. "PARIS (Agency) - ", "NEW YORK, 12 MAI (Wire) — "
	reWireDateline = regexp.MustCompile(`^[A-Z0-9\x{00C0}-\x{00D6}\x{00D8}-\x{00DE}][A-Z0-9\x{00C0}-\x{00D6}\x{00D8}-\x{00DE}\s,'-]{1,35}\s*\([^)]{2,25}\)\s*[-–—]\s*`)
	// Generic trailing wire desk/initials signature at end of text on its own line: e.g. "\nab/cd" or "\nab/cd/ef"
	reTrailingCredit = regexp.MustCompile(`(?:\r?\n)[ \t]*[a-z]{2,4}/[a-z]{2,4}(?:/[a-z]{2,4})?[ \t]*$`)
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
// and CMS/Markdown formatting noise from body text sent to the embedding model
// while leaving canonical stored source offsets untouched.
func normalizeWireText(raw string) string {
	return normalizeEditorialText(raw, true)
}

func normalizeEditorialText(raw string, stripDateline bool) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return raw
	}
	if stripDateline {
		s = reWireDateline.ReplaceAllString(s, "")
	}
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
	// Snap back to last whitespace before maxRunes; fall back to maxRunes if
	// no whitespace appears in the tail window.
	cut := maxRunes
	for cut > 120 && !unicode.IsSpace(runes[cut-1]) {
		cut--
	}
	if cut <= 120 {
		cut = maxRunes
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
			t = normalizeEditorialText(t, false)
		}
		if b != "" {
			b = normalizeWireText(b)
		}
		if lead != "" {
			lead = normalizeWireText(lead)
		}
	}
	inlineTitle := c.TitleSource == "inline"
	switch c.ContextualAnchor {
	case "title_in_text":
		if !inlineTitle && t != "" && b != "" {
			b = t + sourceSeparator + b
		}
	case "title_lead_text":
		if !isLeadChunk && lead != "" && b != "" {
			if !inlineTitle && t != "" {
				b = t + " — " + lead + sourceSeparator + b
			} else {
				b = lead + sourceSeparator + b
			}
		} else if !inlineTitle && t != "" && b != "" {
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
			suffix := trailingParagraphSuffixFromEncoding(u, e, overlapTokens)
			if suffix != nil {
				selected = []paragraph{*suffix}
			}
		}
		break
	}
	return selected, nil
}

// trailingParagraphSuffixFromEncoding slices the last <= overlapTokens runes of p
// using an existing token encoding, snapping forward to a sentence or word boundary.
func trailingParagraphSuffixFromEncoding(p paragraph, e tokenEncoding, overlapTokens int) *paragraph {
	if e.Tokens <= overlapTokens {
		cp := p
		return &cp
	}
	if e.Tokens < 2 || len(e.Offsets) != e.Tokens {
		return nil
	}
	startTok := e.Tokens - overlapTokens
	if startTok <= 0 || startTok >= e.Tokens {
		return nil
	}
	runeStart := e.Offsets[startTok][0]
	runes := []rune(p.text)
	if runeStart <= 0 || runeStart >= len(runes) {
		return nil
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
		return nil
	}
	tail := string(runes[snapped:])
	if strings.TrimSpace(tail) == "" || utf8.RuneCountInString(tail) == 0 {
		return nil
	}
	out := paragraph{
		key:   p.key,
		start: p.start + snapped,
		end:   p.end,
		text:  tail,
		run:   p.run,
	}
	return &out
}
