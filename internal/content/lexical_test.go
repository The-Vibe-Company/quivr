package content_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

func TestMergeLexicalTextDeduplicatesBoundsAndStaysIdempotent(t *testing.T) {
	base := "Solar panel output increased at the harbour."
	additions := []string{
		"solar panel",            // already present as contiguous tokens -> skipped
		"photovoltaic array",     // new -> appended
		"Photovoltaic   Array\t", // duplicate with different case/whitespace -> skipped
		"car",                    // must NOT falsely match "carbon" (not in base) -> appended
		"carbon",                 // distinct token -> appended
		"!!!",                    // punctuation only -> skipped
		"bad\x00nul\xffutf8 ok",  // NUL and invalid UTF-8 stripped -> appended cleanly
		"how do solar panels work?",
	}
	got := content.MergeLexicalText(base, additions, 256)
	want := strings.Join([]string{
		base,
		"photovoltaic array",
		"car",
		"carbon",
		"badnulutf8 ok",
		"how do solar panels work?",
	}, "\n")
	if got != want {
		t.Fatalf("MergeLexicalText =\n%q\nwant:\n%q", got, want)
	}
	if !content.ValidLexicalText(got, 256) {
		t.Fatalf("merged lexical text is not valid: %q", got)
	}
	// Retrying the exact same merge against the already-enriched text must be a no-op.
	if retry := content.MergeLexicalText(got, additions, 256); retry != got {
		t.Fatalf("MergeLexicalText not idempotent:\nfirst: %q\nretry: %q", got, retry)
	}

	// Multi-byte UTF-8 truncation respects rune budget and stays valid UTF-8 and idempotent.
	bounded := content.MergeLexicalText("éàü", []string{"longue phrase qui dépasse la limite", "court"}, 12)
	if !utf8.ValidString(bounded) || utf8.RuneCountInString(bounded) > 12 {
		t.Fatalf("bounded merge violated UTF-8 or rune limit: %q (%d runes)", bounded, utf8.RuneCountInString(bounded))
	}
	if bounded != "éàü\ncourt" {
		t.Fatalf("bounded merge = %q, want %q", bounded, "éàü\ncourt")
	}
	if retry := content.MergeLexicalText(bounded, []string{"longue phrase qui dépasse la limite", "court"}, 12); retry != bounded {
		t.Fatalf("bounded merge not idempotent: %q vs %q", bounded, retry)
	}

	// Empty base appends additions cleanly without a leading newline.
	if empty := content.MergeLexicalText("", []string{"first term", "second term"}, 64); empty != "first term\nsecond term" {
		t.Fatalf("empty base merge = %q", empty)
	}
}

type fakeLexicalStore struct {
	content.BaselineRepository
	calls int
}

func (f *fakeLexicalStore) UpdateSegmentLexicalText(_ context.Context, _ string, seg content.Segmentation, updates []content.SegmentLexicalUpdate) (content.Segmentation, error) {
	f.calls++
	updated, _, err := content.ApplySegmentLexicalUpdates(seg, updates)
	return updated, err
}

func TestServiceUpdateSegmentLexicalTextPreservesIdentityAndSkipsNoOp(t *testing.T) {
	seg := content.Segmentation{
		ID:        "seg-1",
		VersionID: "ver-1",
		Recipe:    "plugin:example@1",
		Segments: []content.Segment{
			{
				ID:      "p-1",
				PartKey: "body",
				Text:    "canonical body text",
				Start:   0,
				End:     19,
				Derivation: content.SegmentDerivation{
					Ordinal:     0,
					LexicalText: "canonical body text",
				},
			},
		},
	}
	store := &fakeLexicalStore{}
	svc := content.Service{Baseline: store}

	// Unchanged lexical text must not hit canonical storage.
	same, err := svc.UpdateSegmentLexicalText(t.Context(), "org-1", seg, []content.SegmentLexicalUpdate{
		{SegmentID: "p-1", LexicalText: "canonical body text"},
	})
	if err != nil || store.calls != 0 || same.Segments[0].Derivation.LexicalText != "canonical body text" {
		t.Fatalf("no-op update: calls=%d err=%v seg=%+v", store.calls, err, same)
	}

	// Real update persists only Derivation.LexicalText, leaving ID, Text, offsets untouched.
	updated, err := svc.UpdateSegmentLexicalText(t.Context(), "org-1", seg, []content.SegmentLexicalUpdate{
		{SegmentID: "p-1", LexicalText: "canonical body text\nexpanded synonym"},
	})
	if err != nil {
		t.Fatalf("UpdateSegmentLexicalText failed: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
	if updated.Segments[0].ID != seg.Segments[0].ID || updated.Segments[0].Text != seg.Segments[0].Text || updated.Segments[0].Start != 0 || updated.Segments[0].End != 19 {
		t.Fatalf("segment identity or source text mutated: %+v", updated.Segments[0])
	}
	if seg.Segments[0].Derivation.LexicalText != "canonical body text" {
		t.Fatal("input segmentation slice was mutated in place")
	}
	if content.SegmentationDigest(updated) == content.SegmentationDigest(seg) {
		t.Fatal("segmentation digest did not change after lexical update")
	}

	// Invalid updates are rejected before touching storage.
	for _, bad := range [][]content.SegmentLexicalUpdate{
		{{SegmentID: "unknown", LexicalText: "ok"}},
		{{SegmentID: "p-1", LexicalText: "a"}, {SegmentID: "p-1", LexicalText: "b"}},
		{{SegmentID: "p-1", LexicalText: "bad\x00nul"}},
		{{SegmentID: "p-1", LexicalText: strings.Repeat("a", content.MaxLexicalTextRunes+1)}},
	} {
		if _, err := svc.UpdateSegmentLexicalText(t.Context(), "org-1", seg, bad); !errors.Is(err, content.ErrInvalid) {
			t.Fatalf("bad update %+v: err=%v, want ErrInvalid", bad, err)
		}
	}
	if store.calls != 1 {
		t.Fatalf("invalid updates called store: calls=%d, want 1", store.calls)
	}
}
