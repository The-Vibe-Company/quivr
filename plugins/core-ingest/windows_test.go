package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode"
)

// words stands in for the pinned tokenizer, which needs Python and a
// prepared tokenizer.json (parity_test.go runs against it): one token per
// run of non-space characters, with code point offsets, and two more for the
// special tokens of a model input.
type words struct{}

func (words) Encode(_ context.Context, inputs []TokenInput) ([]Encoding, error) {
	out := make([]Encoding, len(inputs))
	for i, in := range inputs {
		e := Encoding{Offsets: [][2]int{}}
		start := -1
		runes := []rune(in.Text)
		for j := 0; j <= len(runes); j++ {
			if j < len(runes) && !unicode.IsSpace(runes[j]) {
				if start < 0 {
					start = j
				}
				continue
			}
			if start >= 0 {
				e.Offsets = append(e.Offsets, [2]int{start, j})
				start = -1
			}
		}
		e.Tokens = len(e.Offsets)
		if in.Special {
			e.Tokens += 2
			e.Offsets = nil
		}
		out[i] = e
	}
	return out, nil
}

// A Version whose only indexed text is its title, like a feed item with a
// title and a link but no description, gets one segment on the title Part:
// found by keyword through its excerpt and by meaning through its model input.
func TestTitleOnlyVersionGetsOneSegmentOnItsTitle(t *testing.T) {
	title := "  Harbour closed after the storm "
	got, err := TokenWindows{Tokenizer: words{}}.Process(context.Background(), []Part{
		{Key: "title", Role: "title", Text: title},
		{Key: "link", Role: "link", Text: "https://example.org/news/1"},
	})
	if err != nil {
		t.Fatalf("title-only Version refused: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d segments, want one on the title: %+v", len(got), got)
	}
	w := got[0]
	if w.PartKey != "title" || w.Start != 0 || w.End != len([]rune(title)) {
		t.Errorf("segment %s [%d,%d), want the whole title Part [0,%d)", w.PartKey, w.Start, w.End, len([]rune(title)))
	}
	d := w.Derivation
	if d.ModelInput != "passage: Harbour closed after the storm" || d.ModelInputSHA256 != hash([]byte(d.ModelInput)) {
		t.Errorf("model input %q (sha %s)", d.ModelInput, d.ModelInputSHA256)
	}
	if d.NormalizedSHA256 != hash([]byte(title)) || d.TitleFullSHA256 != hash([]byte(title)) || d.TitleUsedSHA256 != hash([]byte(strings.TrimSpace(title))) {
		t.Errorf("derivation hashes %+v", d)
	}
	if d.TokenStart != 0 || d.TokenEnd != 5 || d.TitleTokens != 5 || d.ModelTokens != 8 || d.UTF8End != len(title) {
		t.Errorf("derivation counts %+v", d)
	}
}

// A blank body is like an empty one: with a title, the Version keeps the
// segment the engine always gave a title over an empty body.
func TestBlankBodyWithATitleIsNotRefused(t *testing.T) {
	got, err := TokenWindows{Tokenizer: words{}}.Process(context.Background(), []Part{
		{Key: "title", Role: "title", Text: "Harbour closed"},
		{Key: "body", Role: "body", Text: " \n\t "},
	})
	if err != nil || len(got) != 1 || got[0].PartKey != "body" || got[0].Start != 0 || got[0].End != 0 || got[0].Derivation.ModelInput != "passage: Harbour closed" {
		t.Fatalf("got %+v, %v; want one title-only segment at the start of the body", got, err)
	}
}

// Each refusal says why, in the code and message the Version's diagnostic
// shows: nothing to index, invalid text, or the limit that was exceeded.
func TestRefusalsNameTheirReason(t *testing.T) {
	for _, c := range []struct {
		name, code, message string
		parts               []Part
	}{
		{"only other roles", "no_indexable_text", "no title or body text", []Part{{Key: "caption", Role: "caption", Text: "A caption"}}},
		{"blank title", "no_indexable_text", "no title or body text", []Part{{Key: "title", Role: "title", Text: "  "}}},
		{"blank body", "no_indexable_text", "no title or body text", []Part{{Key: "body", Role: "body", Text: " \n "}}},
		{"NUL", "invalid_text", "NUL", []Part{{Key: "body", Role: "body", Text: "a\x00b"}}},
		{"two titles", "segmentation_limit", "more than one title Part", []Part{{Key: "a", Role: "title", Text: "One"}, {Key: "b", Role: "title", Text: "Two"}}},
		{"source bytes", "segmentation_limit", "at most 262144", []Part{{Key: "body", Role: "body", Text: strings.Repeat("x", Parameters.MaxSourceBytes+1)}}},
	} {
		_, err := TokenWindows{Tokenizer: words{}}.Process(context.Background(), c.parts)
		var r *Refusal
		if !errors.As(err, &r) || r.Code != c.code || !strings.Contains(r.Message, c.message) {
			t.Errorf("%s: %v; want %s naming %q", c.name, err, c.code, c.message)
		}
	}
}
