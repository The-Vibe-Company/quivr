package content_test

import (
	"context"
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
