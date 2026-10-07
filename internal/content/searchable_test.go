package content_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// withdrawnDuringRead never finds the stale candidate and finds the others,
// then, on the recheck, no longer finds those listed in withdrawn, as when
// their Record is withdrawn while the blob is read.
type withdrawnDuringRead struct {
	content.BaselineRepository
	text      string
	withdrawn map[string]bool
	lookups   int
}

func (b *withdrawnDuringRead) Hydrate(_ context.Context, _ corpus.Scope, cs []content.Candidate) (map[int]content.Located, error) {
	b.lookups++
	out := map[int]content.Located{}
	for i, c := range cs {
		if c.SegmentID == "stale" || b.lookups > 1 && b.withdrawn[c.SegmentID] {
			continue
		}
		start := map[string]int{"a": 0, "b": 4, "c": 8}[c.SegmentID]
		h := content.Hydrated{Segment: content.Segment{ID: c.SegmentID, Start: start, End: start + 3}, TextSHA256: content.Hash([]byte(string([]rune(b.text)[start : start+3])))}
		out[i] = content.Located{Hydrated: h, Blob: content.Blob{Key: "part"}}
	}
	return out, nil
}

type countedBlob struct {
	content.Blobs
	text  string
	reads *atomic.Int64
}

func (b countedBlob) Read(context.Context, content.Blob) ([]byte, error) {
	b.reads.Add(1)
	return []byte(b.text), nil
}

// A batch reads a blob its candidates share once, and a candidate withdrawn
// while the bytes were read is dropped at the recheck, the others kept at
// their positions.
func TestHydrationRechecksTheBatchAfterReadingIt(t *testing.T) {
	const text = "één dos tri"
	baseline := &withdrawnDuringRead{text: text, withdrawn: map[string]bool{"b": true}}
	reads := &atomic.Int64{}
	s := content.Service{Baseline: baseline, Blobs: countedBlob{text: text, reads: reads}}
	scope := corpus.Scope{Organization: "org", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}
	got, err := s.Hydrate(context.Background(), scope, []content.Candidate{{SegmentID: "stale"}, {SegmentID: "a"}, {SegmentID: "b"}, {SegmentID: "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Segment.Text != "één" || got[3].Segment.Text != "tri" {
		texts := map[int]string{}
		for i, h := range got {
			texts[i] = h.Segment.ID + ":" + h.Segment.Text
		}
		t.Fatalf("hydrated %v; want a:één at 1 and c:tri at 3, stale and b dropped", texts)
	}
	if baseline.lookups != 2 || reads.Load() != 1 {
		t.Fatalf("%d lookups and %d blob reads; want one lookup, one read of the shared blob, one recheck", baseline.lookups, reads.Load())
	}
}

// Packed passages retain independent Unicode source ranges and never pretend
// the joined excerpt belongs to only the first Part.
func TestPackedPassageUsesExactCanonicalRanges(t *testing.T) {
	v := content.Version{ID: "v", Manifest: content.Manifest{Parts: []content.Part{
		{Key: "a", Role: "body", Content: content.Text{Kind: "text", Text: "één 🌌"}},
		{Key: "b", Role: "body", Content: content.Text{Kind: "text", Text: "第二段"}},
	}}}
	input := content.SegmentInput{PartKey: "a", End: 5, SourceRanges: []content.SourceRange{{PartKey: "a", End: 5}, {PartKey: "b", End: 3}}, SourceSeparator: "\n\n"}
	seg, err := content.PluginSegmentation("org", v, "plugin:p@1", nil, []content.SegmentInput{input})
	if err != nil {
		t.Fatal(err)
	}
	if seg.Segments[0].Text != "één 🌌\n\n第二段" {
		t.Fatalf("packed text = %q", seg.Segments[0].Text)
	}
	changed := input
	changed.SourceSeparator = " "
	other, err := content.PluginSegmentation("org", v, "plugin:p@1", nil, []content.SegmentInput{changed})
	if err != nil || other.Segments[0].ID == seg.Segments[0].ID {
		t.Fatalf("separator must change immutable identity: %v", err)
	}
	input.SourceRanges[1].End = 4
	if _, err = content.PluginSegmentation("org", v, "plugin:p@1", nil, []content.SegmentInput{input}); err == nil {
		t.Fatal("accepted range beyond canonical Part")
	}
}
