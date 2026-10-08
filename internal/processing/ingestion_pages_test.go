package processing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	"github.com/The-Vibe-Company/quivr/internal/processing"
)

// The external owner supplies known four-passage output. This test owns the
// engine's selection and persistence; hosted.embed owns actual paragraph packing.
type boundedItemOwner struct {
	wholeError            error
	failPage              bool
	wholeCalls, pageCalls int
}

func (*boundedItemOwner) Descriptor() processing.IngestionDescriptor {
	return processing.IngestionDescriptor{Recipe: "plugin:example.paged@1", Producer: "plugin:example.paged@1", Paged: true,
		Spaces: []string{"example.paged.text@1"}, VectorSpaces: map[string]content.VectorSpace{
			"example.paged.text@1": {ID: "example.paged.text@1", Dimensions: 2, Manifest: json.RawMessage(`{"space":"example.paged.text"}`)},
		}}
}

func (p *boundedItemOwner) SegmentAndEmbed(_ context.Context, _, _ string, v content.Version, spaces []string) ([]processing.PluginSegment, error) {
	p.wholeCalls++
	if p.wholeError != nil {
		return nil, p.wholeError
	}
	var out []processing.PluginSegment
	for first := 2; first < len(v.Manifest.Parts); first += 5 {
		var ranges []content.SourceRange
		for _, part := range v.Manifest.Parts[first:min(first+5, len(v.Manifest.Parts))] {
			ranges = append(ranges, content.SourceRange{PartKey: part.Key, End: len([]rune(part.Content.Text))})
		}
		out = append(out, processing.PluginSegment{SegmentInput: content.SegmentInput{PartKey: ranges[0].PartKey, End: ranges[0].End, SourceRanges: ranges, SourceSeparator: "\n\n"}, Vectors: map[string][]float32{spaces[0]: {1, 0}}})
	}
	return out, nil
}

func (p *boundedItemOwner) SegmentAndEmbedPage(_ context.Context, _, _ string, v content.Version, spaces []string, cursor json.RawMessage) (processing.PluginPage, error) {
	p.pageCalls++
	part := 0
	if len(cursor) > 0 {
		if err := json.Unmarshal(cursor, &part); err != nil {
			return processing.PluginPage{}, err
		}
	}
	if p.failPage && part == 1 {
		return processing.PluginPage{}, errors.New("page interrupted")
	}
	source := v.Manifest.Parts[part]
	page := processing.PluginPage{Segments: []processing.PluginSegment{{SegmentInput: content.SegmentInput{PartKey: source.Key, End: len([]rune(source.Content.Text))}, Vectors: map[string][]float32{spaces[0]: {1, 0}}}}}
	if part+1 < len(v.Manifest.Parts) {
		page.Next, _ = json.Marshal(part + 1)
	}
	return page, nil
}

type itemDerivationStore struct {
	content.BaselineRepository
	seg   content.Segmentation
	pages map[int]content.IngestionPage
}

func (s *itemDerivationStore) SaveSegmentation(_ context.Context, _ string, seg content.Segmentation) error {
	s.seg = seg
	return nil
}
func (s *itemDerivationStore) IngestionPage(_ context.Context, _, _, _, _ string, n int) (content.IngestionPage, bool, error) {
	p, ok := s.pages[n]
	return p, ok, nil
}
func (s *itemDerivationStore) SaveIngestionPage(_ context.Context, _, _, _, _ string, n int, p content.IngestionPage) (content.IngestionPage, error) {
	s.pages[n] = p
	return p, nil
}

func TestDerivePacksBoundedItemsAndPagesSizeRefusals(t *testing.T) {
	v := content.Version{ID: "item", RecordID: "record", Manifest: content.Manifest{Parts: []content.Part{
		{Key: "slug", Role: "context", Content: content.Text{Kind: "text", Text: "update"}},
		{Key: "headline", Role: "title", Content: content.Text{Kind: "text", Text: "Library opens"}},
	}}}
	for n := range 20 {
		v.Manifest.Parts = append(v.Manifest.Parts, content.Part{Key: fmt.Sprintf("paragraph-%02d", n), Role: "body", Content: content.Text{Kind: "text", Text: strings.Repeat("word ", 80)}})
	}
	sizeRefusal := func(code string, retryable bool) error {
		return errors.Join(content.Refused("owner refusal"), &plugins.PluginError{Code: code, Retryable: retryable})
	}
	for _, tc := range []struct {
		name         string
		parts        int
		extraBytes   int
		refusal      error
		whole, pages int
		want         error
	}{
		{name: "packed item", whole: 1},
		{name: "too many Parts", parts: 65, pages: 65},
		{name: "too much text", extraBytes: 256 << 10, pages: 22},
		{name: "segmentation limit", refusal: sizeRefusal("segmentation_limit", false), whole: 1, pages: 22},
		{name: "provider input size", refusal: sizeRefusal("input_size", false), whole: 1, pages: 22},
		{name: "resume committed pages", refusal: sizeRefusal("input_size", false), whole: 1, pages: 23},
		{name: "other refusal", refusal: sizeRefusal("invalid_text", false), whole: 1, want: content.ErrIngestionRefused},
		{name: "retryable size error", refusal: sizeRefusal("input_size", true), whole: 1, want: content.ErrIngestionRefused},
		{name: "outage", refusal: errors.New("provider unavailable"), whole: 1},
		{name: "single Part unchanged", parts: 1, pages: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := v
			item.Manifest.Parts = append([]content.Part(nil), v.Manifest.Parts...)
			if tc.parts == 1 {
				item.Manifest.Parts = item.Manifest.Parts[2:3]
			}
			for len(item.Manifest.Parts) < tc.parts {
				n := len(item.Manifest.Parts)
				item.Manifest.Parts = append(item.Manifest.Parts, content.Part{Key: fmt.Sprint(n), Role: "body", Content: content.Text{Kind: "text", Text: "tail"}})
			}
			item.Manifest.Parts[len(item.Manifest.Parts)-1].Content.Text += strings.Repeat("x", tc.extraBytes)
			owner := &boundedItemOwner{wholeError: tc.refusal}
			owner.failPage = tc.name == "resume committed pages"
			baseline := &itemDerivationStore{pages: map[int]content.IngestionPage{}}
			artifacts := &memoryArtifacts{byDerivation: map[string]content.Embedding{}, blobs: map[string][]byte{}}
			d := processing.PluginDeriver{Content: content.Service{Baseline: baseline, Embeddings: artifacts, Blobs: artifacts}, Plugin: owner}
			if owner.failPage {
				if _, _, err := d.Derive(t.Context(), "org", "corpus", item, content.Generation{SpaceID: "example.paged.text@1"}); err == nil {
					t.Fatal("page outage did not interrupt derivation")
				}
				owner.failPage, owner.wholeError = false, nil
			}
			seg, data, err := d.Derive(t.Context(), "org", "corpus", item, content.Generation{SpaceID: "example.paged.text@1"})
			want := tc.want
			if tc.name == "outage" {
				want = tc.refusal
			}
			if !errors.Is(err, want) {
				t.Fatalf("err=%v; want %v", err, want)
			}
			if owner.wholeCalls != tc.whole || owner.pageCalls != tc.pages {
				t.Fatalf("whole=%d pages=%d; want whole=%d pages=%d", owner.wholeCalls, owner.pageCalls, tc.whole, tc.pages)
			}
			if want != nil {
				return
			}
			if len(seg.Segments) != len(data) {
				t.Fatalf("segments=%d vectors=%d", len(seg.Segments), len(data))
			}
			if tc.pages == 0 {
				if len(seg.Segments) != 4 {
					t.Fatalf("got %d passages; want four", len(seg.Segments))
				}
				for n, s := range seg.Segments {
					ranges := s.Derivation.SourceRanges
					if len(ranges) != 5 || ranges[0].PartKey != fmt.Sprintf("paragraph-%02d", n*5) || ranges[4].PartKey != fmt.Sprintf("paragraph-%02d", n*5+4) {
						t.Fatalf("passage %d sources=%+v", n, ranges)
					}
				}
			} else {
				if len(seg.Segments) != len(item.Manifest.Parts) {
					t.Fatalf("incomplete paged item: %d passages for %d Parts", len(seg.Segments), len(item.Manifest.Parts))
				}
				for n, s := range seg.Segments {
					part := item.Manifest.Parts[n]
					if s.PartKey != part.Key || s.Start != 0 || s.End != len([]rune(part.Content.Text)) {
						t.Fatalf("lost source: %+v", s)
					}
				}
			}
		})
	}
}
