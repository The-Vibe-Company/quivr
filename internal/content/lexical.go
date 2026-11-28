package content

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLexicalTextRunes bounds SegmentDerivation.LexicalText across baseline
// ingestion and async Phase-2 lexical enrichment.
const MaxLexicalTextRunes = 16384

// SegmentLexicalUpdate carries an updated LexicalText value for an existing
// Segment without altering canonical Part offsets, Segment.Text, or vectors.
type SegmentLexicalUpdate struct {
	SegmentID   string
	LexicalText string
}

// LexicalTextStore persists Phase-2 lexical enrichment updates to canonical
// segment derivations without altering segment identity, offsets, or vectors.
type LexicalTextStore interface {
	UpdateSegmentLexicalText(ctx context.Context, org string, seg Segmentation, updates []SegmentLexicalUpdate) (Segmentation, error)
}

// ValidLexicalText reports whether s is valid UTF-8, free of NUL code points,
// and within maxRunes (clamped to MaxLexicalTextRunes when <= 0 or larger).
func ValidLexicalText(s string, maxRunes int) bool {
	limit := effectiveLexicalMaxRunes(maxRunes)
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= limit
}

func effectiveLexicalMaxRunes(maxRunes int) int {
	if maxRunes <= 0 || maxRunes > MaxLexicalTextRunes {
		return MaxLexicalTextRunes
	}
	return maxRunes
}

// LexicalTokens extracts lowercased Unicode word tokens (letters, numbers, and
// combining marks) from s for whole-word and phrase boundary matching.
func LexicalTokens(s string) []string {
	var tokens []string
	start := -1
	for i, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			tokens = append(tokens, strings.ToLower(s[start:i]))
			start = -1
		}
	}
	if start >= 0 {
		tokens = append(tokens, strings.ToLower(s[start:]))
	}
	return tokens
}

// ContainsTokenSequence reports whether needle appears as a contiguous
// subsequence inside haystack. An empty needle never matches.
func ContainsTokenSequence(haystack, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	limit := len(haystack) - len(needle)
	for i := 0; i <= limit; i++ {
		if haystack[i] != needle[0] {
			continue
		}
		match := true
		for j := 1; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// sanitizeLexicalBase strips invalid UTF-8 and NULs, trims outer whitespace,
// and truncates on a UTF-8 rune boundary if longer than limit.
func sanitizeLexicalBase(s string, limit int) (string, int) {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
	s = strings.TrimSpace(s)
	runes := 0
	for i := range s {
		if runes == limit {
			return strings.TrimSpace(s[:i]), utf8.RuneCountInString(strings.TrimSpace(s[:i]))
		}
		runes++
	}
	return s, runes
}

func sanitizeLexicalAddition(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
	return strings.Join(strings.Fields(s), " ")
}

// MergeLexicalText appends expansion terms or queries onto base cleanly:
// valid UTF-8, NUL-free, deduplicated on whole-token boundaries (preventing
// partial-word false matches), bounded to maxRunes (at most MaxLexicalTextRunes),
// and idempotent across retries.
func MergeLexicalText(base string, additions []string, maxRunes int) string {
	limit := effectiveLexicalMaxRunes(maxRunes)
	cleanBase, usedRunes := sanitizeLexicalBase(base, limit)
	if len(additions) == 0 || usedRunes >= limit {
		return cleanBase
	}
	var out strings.Builder
	out.WriteString(cleanBase)
	presentTokens := LexicalTokens(cleanBase)
	seenKeys := map[string]struct{}{}
	for _, line := range strings.Split(cleanBase, "\n") {
		if norm := sanitizeLexicalAddition(line); norm != "" {
			seenKeys[strings.ToLower(norm)] = struct{}{}
		}
	}
	for _, raw := range additions {
		if usedRunes >= limit {
			break
		}
		cleaned := sanitizeLexicalAddition(raw)
		if cleaned == "" {
			continue
		}
		tokens := LexicalTokens(cleaned)
		if len(tokens) == 0 {
			continue
		}
		key := strings.ToLower(cleaned)
		if _, dup := seenKeys[key]; dup {
			continue
		}
		if ContainsTokenSequence(presentTokens, tokens) {
			seenKeys[key] = struct{}{}
			continue
		}
		addRunes := utf8.RuneCountInString(cleaned)
		sepRunes := 0
		if usedRunes > 0 {
			sepRunes = 1
		}
		if usedRunes+sepRunes+addRunes > limit {
			continue
		}
		if sepRunes > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(cleaned)
		usedRunes += sepRunes + addRunes
		seenKeys[key] = struct{}{}
		presentTokens = append(presentTokens, tokens...)
	}
	return out.String()
}

// ApplySegmentLexicalUpdates validates updates and returns a copy of seg whose
// matching segments have Derivation.LexicalText updated. Segment identity,
// canonical offsets, Segment.Text, and provenance remain untouched.
func ApplySegmentLexicalUpdates(seg Segmentation, updates []SegmentLexicalUpdate) (Segmentation, bool, error) {
	if seg.ID == "" || seg.VersionID == "" || len(seg.Segments) == 0 {
		return Segmentation{}, false, ErrInvalid
	}
	if len(updates) == 0 {
		return seg, false, nil
	}
	indexByID := make(map[string]int, len(seg.Segments))
	for i, p := range seg.Segments {
		indexByID[p.ID] = i
	}
	seen := make(map[string]struct{}, len(updates))
	cloned := seg
	cloned.Segments = append([]Segment(nil), seg.Segments...)
	changed := false
	for _, u := range updates {
		if u.SegmentID == "" || !ValidLexicalText(u.LexicalText, MaxLexicalTextRunes) {
			return Segmentation{}, false, ErrInvalid
		}
		if _, dup := seen[u.SegmentID]; dup {
			return Segmentation{}, false, ErrInvalid
		}
		seen[u.SegmentID] = struct{}{}
		idx, ok := indexByID[u.SegmentID]
		if !ok {
			return Segmentation{}, false, ErrInvalid
		}
		if cloned.Segments[idx].Derivation.LexicalText != u.LexicalText {
			cloned.Segments[idx].Derivation.LexicalText = u.LexicalText
			changed = true
		}
	}
	if !changed {
		return seg, false, nil
	}
	return cloned, true, nil
}

// UpdateSegmentLexicalText persists Phase-2 lexical updates in canonical
// storage when any segment's LexicalText actually changes.
func (s Service) UpdateSegmentLexicalText(ctx context.Context, org string, seg Segmentation, updates []SegmentLexicalUpdate) (Segmentation, error) {
	if org == "" {
		return seg, ErrInvalid
	}
	updated, changed, err := ApplySegmentLexicalUpdates(seg, updates)
	if err != nil || !changed {
		return updated, err
	}
	store, ok := s.Baseline.(LexicalTextStore)
	if !ok {
		return seg, ErrInvalid
	}
	return store.UpdateSegmentLexicalText(ctx, org, seg, updates)
}
