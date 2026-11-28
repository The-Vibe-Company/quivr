package content_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/The-Vibe-Company/quivr/internal/content"
)

// savedSegmentations records what SaveSegmentation forwarded to storage.
type savedSegmentations struct {
	content.BaselineRepository
	saved int
}

func (s *savedSegmentations) SaveSegmentation(context.Context, string, content.Segmentation) error {
	s.saved++
	return nil
}

// multibyteText cycles 1-, 2-, 3- and 4-byte runes and a combining mark so
// rune and byte offsets diverge throughout the Part.
func multibyteText(runes int) string {
	alphabet := []rune("aé€😀́ Zß日🎉\n")
	var b strings.Builder
	for i := range runes {
		b.WriteRune(alphabet[i%len(alphabet)])
	}
	return b.String()
}

// validSegment builds the Segment the engine accepts for runes [start,end) of
// the text Part key, computing every derived field from first principles.
func validSegment(org, segmentationID, key, text string, ordinal, start, end int) content.Segment {
	runes := []rune(text)
	canonical := string(runes)
	input := "passage: " + string(runes[start:end])
	p := content.Segment{
		PartKey: key, Start: start, End: end, Text: string(runes[start:end]),
		Derivation: content.SegmentDerivation{
			Ordinal: ordinal, UTF8Start: len(string(runes[:start])), UTF8End: len(string(runes[:end])),
			NormalizedSHA256: content.Hash([]byte(canonical)), ModelInput: input, ModelInputSHA256: content.Hash([]byte(input)),
		},
	}
	p.ID = content.SegmentID(org, segmentationID, p)
	return p
}

func segmentationOver(org string, v content.Version, key, text string, segments int) content.Segmentation {
	result := content.Segmentation{ID: content.StableID("segmentation", org, v.ID, "bench"), VersionID: v.ID, Recipe: "bench"}
	n := utf8.RuneCountInString(text)
	for i := range segments {
		start, end := i*n/segments, (i+1)*n/segments
		result.Segments = append(result.Segments, validSegment(org, result.ID, key, text, i, start, end))
	}
	return result
}

func BenchmarkSaveSegmentationValidate(b *testing.B) {
	const org = "bench-org"
	text := multibyteText(100_000)
	v := content.Version{ID: "version", Manifest: content.Manifest{Kind: "manifest", Parts: []content.Part{{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: text}}}}}
	for _, segments := range []int{50, 500, 5000} {
		result := segmentationOver(org, v, "body", text, segments)
		b.Run("seg="+strconv.Itoa(segments), func(b *testing.B) {
			repo := &savedSegmentations{}
			svc := content.Service{Baseline: repo}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := svc.SaveSegmentation(ctx, org, v, result); err != nil {
					b.Fatal(err)
				}
			}
			if repo.saved != b.N {
				b.Fatalf("saved %d of %d valid segmentations", repo.saved, b.N)
			}
		})
	}
}

const (
	eqOrg   = "org"
	blobSHA = "f2ca1bb6c7e907d06dafe4687e579fce76b37e4e93b7605022da52e6ccc26fd2"
	// Each part exercises a way rune and byte coordinates diverge.
	bodyText    = "Ça va? 😀👍🏽 é 日本語 🇫🇷 end"
	invalidText = "ok\xffbad\xc3(\x80z"
	// CESU-8 encoded surrogates are invalid UTF-8: one U+FFFD per byte.
	surrogateText = "x\xed\xa0\x80\xed\xb0\x80y"
)

var eqVersion = content.Version{ID: "version", Manifest: content.Manifest{Kind: "manifest", Parts: []content.Part{
	{Key: "title", Role: "title", Content: content.Text{Kind: "text", Text: "Títle 🎉"}},
	{Key: "body", Role: "body", Content: content.Text{Kind: "text", Text: bodyText}},
	{Key: "second", Role: "body", Content: content.Text{Kind: "text", Text: "second part ✓ ok"}},
	{Key: "bad", Role: "body", Content: content.Text{Kind: "text", Text: invalidText}},
	{Key: "surr", Role: "body", Content: content.Text{Kind: "text", Text: surrogateText}},
	{Key: "empty", Role: "body", Content: content.Text{Kind: "text", Text: ""}},
	{Key: "long", Role: "body", Content: content.Text{Kind: "text", Text: multibyteText(300)}},
	{Key: "img", Role: "figure", Content: content.Text{Kind: "blob", BlobID: "blob", MediaType: "image/png", BlobSHA256: blobSHA}},
}}}

func eqText(key string) string {
	for _, p := range eqVersion.Manifest.Parts {
		if p.Key == key {
			return p.Content.Text
		}
	}
	return ""
}

func eqSegmentationID() string { return content.StableID("segmentation", eqOrg, eqVersion.ID, "eq") }

// seg is the accepted Segment over runes [start,end) of a text Part.
func seg(key string, start, end int) content.Segment {
	return validSegment(eqOrg, eqSegmentationID(), key, eqText(key), 0, start, end)
}

func reid(p content.Segment) content.Segment {
	p.ID = content.SegmentID(eqOrg, eqSegmentationID(), p)
	return p
}

// edit changes one field of an accepted Segment and keeps its ID consistent,
// so the verdict depends on the edited field alone.
func edit(p content.Segment, change func(*content.Segment)) content.Segment {
	change(&p)
	return reid(p)
}

func titled(p content.Segment) content.Segment {
	return edit(p, func(p *content.Segment) { p.TitleKey, p.Title = "title", eqText("title") })
}

func blobSeg() content.Segment {
	p := content.Segment{PartKey: "img", Derivation: content.SegmentDerivation{
		Region:           &content.ImageRegion{CoordinateSystem: "normalized_bbox", Width: 0.5, Height: 0.5},
		NormalizedSHA256: blobSHA, ModelInput: "figure", ModelInputSHA256: content.Hash([]byte("figure")),
	}}
	return reid(p)
}

// packed joins exact source slices like a plugin's packed passage would.
func packed(sep string, ranges ...content.SourceRange) content.Segment {
	p := seg(ranges[0].PartKey, ranges[0].Start, ranges[0].End)
	texts := make([]string, len(ranges))
	for i, r := range ranges {
		if runes := []rune(eqText(r.PartKey)); 0 <= r.Start && r.Start <= r.End && r.End <= len(runes) {
			texts[i] = string(runes[r.Start:r.End])
		}
	}
	p.Text = strings.Join(texts, sep)
	p.Derivation.SourceRanges, p.Derivation.SourceSeparator = ranges, sep
	return reid(p)
}

func TestSaveSegmentationVerdictsOverRuneAndByteCoordinates(t *testing.T) {
	bodyRunes := utf8.RuneCountInString(bodyText)
	rng := func(key string, start, end int) content.SourceRange {
		return content.SourceRange{PartKey: key, Start: start, End: end}
	}
	// Ordered one-rune ranges, valid except for how many there are.
	ordered := func(n int) []content.SourceRange {
		ranges := make([]content.SourceRange, n)
		for i := range ranges {
			ranges[i] = rng("long", i, i+1)
		}
		return ranges
	}

	cases := []struct {
		name     string
		segments []content.Segment
		result   func(*content.Segmentation)
		accept   bool
	}{
		{"whole multibyte part", []content.Segment{seg("body", 0, bodyRunes)}, nil, true},
		{"ascii prefix", []content.Segment{seg("body", 0, 3)}, nil, true},
		{"between emoji and skin tone modifier", []content.Segment{seg("body", 7, 9)}, nil, true},
		{"starting at a combining mark", []content.Segment{seg("body", 12, 14)}, nil, true},
		{"one regional indicator of a flag", []content.Segment{seg("body", 18, 19)}, nil, true},
		{"last rune", []content.Segment{seg("body", bodyRunes-1, bodyRunes)}, nil, true},
		{"end past the part", []content.Segment{edit(seg("body", 0, bodyRunes), func(p *content.Segment) { p.End++ })}, nil, false},
		{"byte length used as end", []content.Segment{edit(seg("body", 0, bodyRunes), func(p *content.Segment) { p.End = len(bodyText) })}, nil, false},
		{"negative start", []content.Segment{edit(seg("body", 0, 3), func(p *content.Segment) { p.Start = -1 })}, nil, false},
		{"inverted range", []content.Segment{edit(seg("body", 2, 5), func(p *content.Segment) { p.Start, p.End = 5, 2 })}, nil, false},
		{"empty range without title", []content.Segment{seg("body", 4, 4)}, nil, false},
		{"empty range with title", []content.Segment{titled(seg("body", 4, 4))}, nil, true},
		{"title matches", []content.Segment{titled(seg("body", 0, 5))}, nil, true},
		{"title differs", []content.Segment{edit(titled(seg("body", 0, 5)), func(p *content.Segment) { p.Title = "Title 🎉" })}, nil, false},
		{"title key unknown", []content.Segment{edit(titled(seg("body", 0, 5)), func(p *content.Segment) { p.TitleKey = "nope" })}, nil, false},
		{"title key names a blob part", []content.Segment{edit(seg("body", 0, 5), func(p *content.Segment) { p.TitleKey, p.Title = "img", "" })}, nil, false},
		{"title from an invalid UTF-8 part", []content.Segment{edit(seg("body", 0, 5), func(p *content.Segment) { p.TitleKey, p.Title = "bad", string([]rune(invalidText)) })}, nil, true},
		{"raw invalid UTF-8 title", []content.Segment{edit(seg("body", 0, 5), func(p *content.Segment) { p.TitleKey, p.Title = "bad", invalidText })}, nil, false},
		{"utf8 start off by one", []content.Segment{edit(seg("body", 7, 9), func(p *content.Segment) { p.Derivation.UTF8Start++ })}, nil, false},
		{"utf8 end off by one", []content.Segment{edit(seg("body", 7, 9), func(p *content.Segment) { p.Derivation.UTF8End-- })}, nil, false},
		{"rune offsets as utf8 offsets", []content.Segment{edit(seg("body", 7, 9), func(p *content.Segment) { p.Derivation.UTF8Start, p.Derivation.UTF8End = 7, 9 })}, nil, false},
		{"normalized hash of another part", []content.Segment{edit(seg("body", 0, 3), func(p *content.Segment) { p.Derivation.NormalizedSHA256 = content.Hash([]byte(eqText("second"))) })}, nil, false},
		{"model input hash differs", []content.Segment{edit(seg("body", 0, 3), func(p *content.Segment) { p.Derivation.ModelInput += "!" })}, nil, false},
		{"512 model tokens", []content.Segment{edit(seg("body", 0, 3), func(p *content.Segment) { p.Derivation.ModelTokens = 512 })}, nil, true},
		{"513 model tokens", []content.Segment{edit(seg("body", 0, 3), func(p *content.Segment) { p.Derivation.ModelTokens = 513 })}, nil, false},
		{"text differs", []content.Segment{edit(seg("body", 0, 3), func(p *content.Segment) { p.Text = "Ca " })}, nil, false},
		{"precomposed instead of combining mark", []content.Segment{edit(seg("body", 11, 13), func(p *content.Segment) { p.Text = "é" })}, nil, false},
		{"segment id differs", []content.Segment{func() content.Segment { p := seg("body", 0, 3); p.ID += "x"; return p }()}, nil, false},
		{"unknown part", []content.Segment{edit(seg("body", 0, 3), func(p *content.Segment) { p.PartKey = "missing" })}, nil, false},
		{"region on a text segment", []content.Segment{edit(seg("body", 0, 3), func(p *content.Segment) { p.Derivation.Region = blobSeg().Derivation.Region })}, nil, false},
		{"invalid UTF-8 part in replacement coordinates", []content.Segment{seg("bad", 0, utf8.RuneCountInString(invalidText))}, nil, true},
		{"invalid UTF-8 slice after a bad byte", []content.Segment{seg("bad", 2, 6)}, nil, true},
		{"invalid UTF-8 raw-byte hash", []content.Segment{edit(seg("bad", 0, 2), func(p *content.Segment) { p.Derivation.NormalizedSHA256 = content.Hash([]byte(invalidText)) })}, nil, false},
		{"invalid UTF-8 raw-byte text", []content.Segment{edit(seg("bad", 2, 3), func(p *content.Segment) { p.Text = "\xff" })}, nil, false},
		{"invalid UTF-8 raw-byte offsets", []content.Segment{edit(seg("bad", 3, 5), func(p *content.Segment) { p.Derivation.UTF8Start, p.Derivation.UTF8End = 3, 5 })}, nil, false},
		{"surrogate bytes as replacement runes", []content.Segment{seg("surr", 0, utf8.RuneCountInString(surrogateText))}, nil, true},
		{"one surrogate byte", []content.Segment{seg("surr", 2, 3)}, nil, true},
		{"surrogate raw-byte hash", []content.Segment{edit(seg("surr", 1, 4), func(p *content.Segment) { p.Derivation.NormalizedSHA256 = content.Hash([]byte(surrogateText)) })}, nil, false},
		{"surrogate decoded as one rune", []content.Segment{edit(seg("surr", 1, 4), func(p *content.Segment) { p.Text = "\U00010000" })}, nil, false},
		{"empty part with title", []content.Segment{titled(seg("empty", 0, 0))}, nil, true},
		{"empty part without title", []content.Segment{seg("empty", 0, 0)}, nil, false},
		{"empty part past its end", []content.Segment{edit(titled(seg("empty", 0, 0)), func(p *content.Segment) { p.End = 1 })}, nil, false},
		{"blob part", []content.Segment{blobSeg()}, nil, true},
		{"blob part with title", []content.Segment{titled(blobSeg())}, nil, true},
		{"blob part with wrong title", []content.Segment{edit(titled(blobSeg()), func(p *content.Segment) { p.Title = "x" })}, nil, false},
		{"blob part with offsets", []content.Segment{edit(blobSeg(), func(p *content.Segment) { p.End = 1 })}, nil, false},
		{"blob part with text", []content.Segment{edit(blobSeg(), func(p *content.Segment) { p.Text = "x" })}, nil, false},
		{"blob part with utf8 offsets", []content.Segment{edit(blobSeg(), func(p *content.Segment) { p.Derivation.UTF8End = 1 })}, nil, false},
		{"blob part with source ranges", []content.Segment{edit(blobSeg(), func(p *content.Segment) { p.Derivation.SourceRanges = []content.SourceRange{rng("body", 0, 1)} })}, nil, false},
		{"blob part without region", []content.Segment{edit(blobSeg(), func(p *content.Segment) { p.Derivation.Region = nil })}, nil, false},
		{"blob part with another hash", []content.Segment{edit(blobSeg(), func(p *content.Segment) { p.Derivation.NormalizedSHA256 = content.Hash(nil) })}, nil, false},
		{"source ranges across parts", []content.Segment{packed("\n\n", rng("body", 0, 9), rng("second", 0, 6))}, nil, true},
		{"source ranges in replacement coordinates", []content.Segment{packed(" | ", rng("body", 7, 9), rng("bad", 1, 4), rng("surr", 1, 4))}, nil, true},
		{"source ranges with emoji separator", []content.Segment{packed("🎉", rng("body", 0, 3), rng("body", 5, 9))}, nil, true},
		{"source ranges out of reading order", []content.Segment{packed("\n", rng("second", 0, 6), rng("body", 0, 3))}, nil, false},
		{"source ranges overlapping in one part", []content.Segment{packed("\n", rng("body", 0, 5), rng("body", 3, 7))}, nil, false},
		{"source range empty", []content.Segment{packed("\n", rng("body", 0, 3), rng("second", 2, 2))}, nil, false},
		{"source range past its part", []content.Segment{packed("\n", rng("body", 0, 3), rng("second", 0, 99))}, nil, false},
		{"source range on a blob part", []content.Segment{packed("\n", rng("body", 0, 3), rng("img", 0, 1))}, nil, false},
		{"first source range is not the segment", []content.Segment{edit(packed("\n", rng("body", 0, 3), rng("second", 0, 6)), func(p *content.Segment) { p.End = 4 })}, nil, false},
		{"source separator of 17 runes", []content.Segment{packed(strings.Repeat("é", 17), rng("body", 0, 3), rng("second", 0, 6))}, nil, false},
		{"source separator with NUL", []content.Segment{packed("\x00", rng("body", 0, 3), rng("second", 0, 6))}, nil, false},
		{"source separator with invalid UTF-8", []content.Segment{packed("\xff", rng("body", 0, 3), rng("second", 0, 6))}, nil, false},
		{"source text joined without separator", []content.Segment{edit(packed("\n", rng("body", 0, 3), rng("second", 0, 6)), func(p *content.Segment) { p.Text = strings.ReplaceAll(p.Text, "\n", "") })}, nil, false},
		{"256 source ranges", []content.Segment{packed(" ", ordered(256)...)}, nil, true},
		{"257 source ranges", []content.Segment{packed(" ", ordered(257)...)}, nil, false},
		{"many valid segments", []content.Segment{seg("body", 0, 5), seg("bad", 0, 3), seg("surr", 2, 5), blobSeg(), packed("\n", rng("body", 5, 9), rng("second", 0, 6))}, nil, true},
		{"one invalid segment among many", []content.Segment{seg("body", 0, 5), seg("body", 5, 9), edit(seg("body", 9, 12), func(p *content.Segment) { p.Derivation.UTF8End++ })}, nil, false},
		{"no segments", nil, nil, false},
		{"another version", []content.Segment{seg("body", 0, 3)}, func(r *content.Segmentation) { r.VersionID = "other" }, false},
		{"no recipe", []content.Segment{seg("body", 0, 3)}, func(r *content.Segmentation) { r.Recipe = "" }, false},
		{"segmentation id differs", []content.Segment{seg("body", 0, 3)}, func(r *content.Segmentation) { r.ID += "x" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := content.Segmentation{ID: eqSegmentationID(), VersionID: eqVersion.ID, Recipe: "eq", Segments: tc.segments}
			if tc.result != nil {
				tc.result(&result)
			}
			repo := &savedSegmentations{}
			err := content.Service{Baseline: repo}.SaveSegmentation(context.Background(), eqOrg, eqVersion, result)
			if tc.accept && (err != nil || repo.saved != 1) {
				t.Fatalf("valid segmentation refused: %v", err)
			}
			if !tc.accept && (!errors.Is(err, content.ErrInvalid) || repo.saved != 0) {
				t.Fatalf("invalid segmentation accepted: %v", err)
			}
		})
	}
}
