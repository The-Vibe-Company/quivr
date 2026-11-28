package content_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/The-Vibe-Company/quivr/internal/content"
	"github.com/The-Vibe-Company/quivr/internal/corpus"
)

// bigBlobText builds a deterministic ~size-byte canonical Part of prose. The
// unicode variant is mostly ASCII with a 2-, 3- or 4-byte code point every few
// words, as accented or mixed-script text is, so rune and byte offsets
// diverge throughout the blob.
func bigBlobText(blob, size int, unicode bool) string {
	words := []string{"canonical", "immutable", "hydration", "passage", "segment", "retrieval", "search", "engine"}
	accented := []string{"naïve", "第二段", "🌌", "façade"}
	var b strings.Builder
	b.Grow(size + 32)
	for i := 0; b.Len() < size; i++ {
		if unicode && i%8 == 3 {
			b.WriteString(accented[(i/8+blob)%len(accented)])
		} else {
			b.WriteString(words[(i*7+blob)%len(words)])
		}
		b.WriteByte(' ')
	}
	return b.String()
}

// BenchmarkHydrateBigBlobs hydrates a 50-candidate page cut from ten ~200 KB
// cached Parts, five 1000 code-point excerpts per Part spread through it, so
// warm hydration cost is dominated by locating code points in large blobs.
func BenchmarkHydrateBigBlobs(b *testing.B) {
	const numBlobs, blobBytes, perBlob, excerptRunes = 10, 200 << 10, 5, 1000
	for _, variant := range []struct {
		name    string
		unicode bool
	}{{"ascii", false}, {"unicode", true}} {
		b.Run(variant.name, func(b *testing.B) {
			blobs := &fixtureBlobStore{data: map[string][]byte{}}
			repo := &fixtureHydrationRepo{located: map[string]content.Located{}, withdrawn: map[string]bool{}, deniedOrg: map[string]bool{}}
			var candidates []content.Candidate
			for n := range numBlobs {
				text := bigBlobText(n, blobBytes, variant.unicode)
				runes := []rune(text)
				blob := content.Blob{Key: fmt.Sprintf("org/sha256/big-%d", n), SHA256: content.Hash([]byte(text)), Size: int64(len(text))}
				blobs.data[blob.Key] = []byte(text)
				for k := range perBlob {
					start := k * (len(runes) - excerptRunes) / (perBlob - 1)
					end := start + excerptRunes
					id := fmt.Sprintf("big-%d-%d", n, k)
					candidates = append(candidates, content.Candidate{SegmentID: id, GenerationID: "gen-1"})
					repo.located[id] = content.Located{
						Hydrated: content.Hydrated{
							RecordID:   fmt.Sprintf("rec-%d", n),
							VersionID:  fmt.Sprintf("ver-%d", n),
							TextSHA256: content.Hash([]byte(string(runes[start:end]))),
							Segment:    content.Segment{ID: id, PartKey: "body", Start: start, End: end},
						},
						Blob: blob,
					}
				}
			}
			scope := corpus.Scope{Organization: "bench-org", Actions: []string{"content:read", "search:query"}, Corpora: []string{"*"}}
			svc := content.Service{Baseline: repo, Blobs: blobs, BlobCache: content.NewHydrationBlobCache(content.DefaultHydrationBlobCacheBytes)}
			ctx := context.Background()
			if _, err := svc.Hydrate(ctx, scope, candidates); err != nil {
				b.Fatal(err)
			}
			readsAfterWarm := blobs.reads.Load()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, err := svc.Hydrate(ctx, scope, candidates)
				if err != nil || len(got) != len(candidates) {
					b.Fatalf("hydrate failed: %d, %v", len(got), err)
				}
			}
			if blobs.reads.Load() != readsAfterWarm {
				b.Fatalf("warm benchmark triggered %d blob reads", blobs.reads.Load()-readsAfterWarm)
			}
		})
	}
}
